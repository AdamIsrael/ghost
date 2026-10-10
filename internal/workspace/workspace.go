// Package workspace checks out pull requests into isolated git worktrees.
package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/AdamIsrael/ghost/internal/run"
)

// Manager keeps one blobless bare clone per repo under root/repos and adds a
// detached worktree per review under root/worktrees.
type Manager struct {
	root string
	run  run.Func

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func New(root string, r run.Func) *Manager {
	return &Manager{root: root, run: r, locks: map[string]*sync.Mutex{}}
}

func (m *Manager) repoLock(repo string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[repo]
	if !ok {
		l = &sync.Mutex{}
		m.locks[repo] = l
	}
	return l
}

// lockRepo serializes work on one repo's bare clone across goroutines (mutex)
// and across ghost processes (flock on <root>/repos/<repo>.lock).
func (m *Manager) lockRepo(repo string) (func(), error) {
	mu := m.repoLock(repo)
	mu.Lock()
	path := filepath.Join(m.root, "repos", repo+".lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		mu.Unlock()
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		mu.Unlock()
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		mu.Unlock()
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		mu.Unlock()
	}, nil
}

func (m *Manager) git(ctx context.Context, dir string, args ...string) error {
	_, errOut, err := m.run(ctx, dir, "", "git", args...)
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(errOut)))
	}
	return nil
}

func validRepo(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	bad := func(s string) bool { return s == "" || s == "." || s == ".." || strings.ContainsAny(s, `/\`) }
	return ok && !bad(owner) && !bad(name)
}

// Workspace is one review's checkout.
type Workspace struct {
	Dir         string // worktree at the PR head
	ContextDir  string // Ghost-written review materials; outside Dir so PR content can't touch it
	DiffPath    string // full diff against origin/<base>
	CommitsPath string // commits in the PR
}

// Checkout fetches PR number's head and baseRef into the repo's bare clone,
// adds a detached worktree at headSHA, and writes the diff and commit list
// into a separate context dir. cleanup removes both; it is always non-nil and
// idempotent.
func (m *Manager) Checkout(ctx context.Context, repo string, number int, baseRef, headSHA string) (Workspace, func(), error) {
	noop := func() {}
	if !validRepo(repo) {
		return Workspace{}, noop, fmt.Errorf("invalid repo %q", repo)
	}
	bare := filepath.Join(m.root, "repos", repo+".git")

	unlock, err := m.lockRepo(repo)
	if err != nil {
		return Workspace{}, noop, err
	}
	err = m.prepare(ctx, repo, bare, number, baseRef)
	var ws Workspace
	if err == nil {
		ws.Dir, err = m.addWorktree(ctx, bare, headSHA)
	}
	unlock()
	if err != nil {
		return Workspace{}, noop, err
	}

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			if ws.ContextDir != "" {
				os.RemoveAll(ws.ContextDir)
			}
			if unlock, err := m.lockRepo(repo); err == nil {
				defer unlock()
			}
			if m.git(context.Background(), bare, "worktree", "remove", "--force", ws.Dir) != nil {
				os.RemoveAll(ws.Dir)
				m.git(context.Background(), bare, "worktree", "prune")
			}
		})
	}
	if err := m.writeContext(ctx, &ws, baseRef); err != nil {
		cleanup()
		return Workspace{}, noop, err
	}
	return ws, cleanup, nil
}

// writeContext stores the PR's diff and commit list for an agent that cannot
// run commands. External diff drivers and textconv are disabled so the
// user's git config can't run programs on PR content.
func (m *Manager) writeContext(ctx context.Context, ws *Workspace, baseRef string) error {
	dir, err := os.MkdirTemp(filepath.Join(m.root, "worktrees"), "context-")
	if err != nil {
		return err
	}
	ws.ContextDir = dir
	ws.DiffPath = filepath.Join(dir, "diff.patch")
	ws.CommitsPath = filepath.Join(dir, "commits.txt")
	base := "origin/" + baseRef
	outputs := []struct {
		path string
		args []string
	}{
		{ws.DiffPath, []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", base + "...HEAD"}},
		{ws.CommitsPath, []string{"log", "--no-color", "--format=commit %H%nAuthor: %an%n%n%B", base + "..HEAD"}},
	}
	for _, o := range outputs {
		out, errOut, err := m.run(ctx, ws.Dir, "", "git", o.args...)
		if err != nil {
			return fmt.Errorf("git %s: %w: %s", o.args[0], err, strings.TrimSpace(string(errOut)))
		}
		if err := os.WriteFile(o.path, out, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) prepare(ctx context.Context, repo, bare string, number int, baseRef string) error {
	if _, err := os.Stat(bare); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(bare), 0o700); err != nil {
			return err
		}
		if _, errOut, err := m.run(ctx, "", "", "gh", "repo", "clone", repo, bare, "--", "--bare", "--filter=blob:none"); err != nil {
			os.RemoveAll(bare)
			return fmt.Errorf("clone %s: %w: %s", repo, err, strings.TrimSpace(string(errOut)))
		}
	}
	return m.git(ctx, bare, "fetch", "--quiet", "--force", "origin",
		fmt.Sprintf("+refs/pull/%d/head:refs/ghost/pr/%d", number, number),
		fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", baseRef, baseRef))
}

func (m *Manager) addWorktree(ctx context.Context, bare, headSHA string) (string, error) {
	parent := filepath.Join(m.root, "worktrees")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(parent, "review-")
	if err != nil {
		return "", err
	}
	if err := m.git(ctx, bare, "cat-file", "-e", headSHA+"^{commit}"); err != nil {
		os.RemoveAll(dir)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("PR head %.7s is no longer on the PR (new commits were pushed?); run the review again", headSHA)
	}
	if err := m.git(ctx, bare, "worktree", "add", "--quiet", "--detach", dir, headSHA); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}
