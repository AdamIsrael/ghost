// Package workspace checks out pull requests into isolated git worktrees.
package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

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

// Checkout fetches PR number's head and baseRef into the repo's bare clone and
// adds a detached worktree at headSHA. origin/<baseRef> is available inside it.
// cleanup removes the worktree; it is always non-nil and idempotent.
func (m *Manager) Checkout(ctx context.Context, repo string, number int, baseRef, headSHA string) (string, func(), error) {
	noop := func() {}
	if !validRepo(repo) {
		return "", noop, fmt.Errorf("invalid repo %q", repo)
	}
	bare := filepath.Join(m.root, "repos", repo+".git")

	lock := m.repoLock(repo)
	lock.Lock()
	err := m.prepare(ctx, repo, bare, number, baseRef)
	var dir string
	if err == nil {
		dir, err = m.addWorktree(ctx, bare, headSHA)
	}
	lock.Unlock()
	if err != nil {
		return "", noop, err
	}

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			l := m.repoLock(repo)
			l.Lock()
			defer l.Unlock()
			if m.git(context.Background(), bare, "worktree", "remove", "--force", dir) != nil {
				os.RemoveAll(dir)
				m.git(context.Background(), bare, "worktree", "prune")
			}
		})
	}
	return dir, cleanup, nil
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
	if err := m.git(ctx, bare, "worktree", "add", "--quiet", "--detach", dir, headSHA); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}
