package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/AdamIsrael/ghost/internal/run"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, errOut, err := run.Exec(context.Background(), dir, "", "git", args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, errOut)
	}
	return strings.TrimSpace(string(out))
}

// remote builds a repo with main and a PR head exposed as refs/pull/7/head.
func remote(t *testing.T) (dir, headSHA string) {
	t.Helper()
	dir = t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "t")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	git(t, dir, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644)
	git(t, dir, "commit", "-q", "-am", "change")
	headSHA = git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "update-ref", "refs/pull/7/head", headSHA)
	git(t, dir, "checkout", "-q", "main")
	return dir, headSHA
}

// fakeGh turns `gh repo clone o/r DEST -- ARGS...` into a local git clone.
func fakeGh(remoteDir string, clones *int32) run.Func {
	return func(ctx context.Context, dir, stdin, name string, args ...string) ([]byte, []byte, error) {
		if name == "gh" {
			atomic.AddInt32(clones, 1)
			dest, extra := args[3], args[5:]
			return run.Exec(ctx, dir, stdin, "git", append(append([]string{"clone", "-q"}, extra...), remoteDir, dest)...)
		}
		return run.Exec(ctx, dir, stdin, name, args...)
	}
}

func TestCheckoutAtHeadWithBaseRef(t *testing.T) {
	rdir, head := remote(t)
	var clones int32
	m := New(t.TempDir(), fakeGh(rdir, &clones))
	ws, cleanup, err := m.Checkout(context.Background(), "o/r", 7, "main", head)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	dir := ws.Dir
	if got := git(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD = %s, want %s", got, head)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(data) != "two\n" {
		t.Errorf("a.txt = %q", data)
	}
	if got := git(t, dir, "diff", "--name-only", "origin/main...HEAD"); got != "a.txt" {
		t.Errorf("diff against origin/main = %q", got)
	}
}

func TestSecondCheckoutReusesClone(t *testing.T) {
	rdir, head := remote(t)
	var clones int32
	m := New(t.TempDir(), fakeGh(rdir, &clones))
	for i := 0; i < 2; i++ {
		_, cleanup, err := m.Checkout(context.Background(), "o/r", 7, "main", head)
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
	}
	if clones != 1 {
		t.Errorf("cloned %d times, want 1", clones)
	}
}

func TestCleanupRemovesWorktree(t *testing.T) {
	rdir, head := remote(t)
	var clones int32
	root := t.TempDir()
	m := New(root, fakeGh(rdir, &clones))
	ws, cleanup, err := m.Checkout(context.Background(), "o/r", 7, "main", head)
	if err != nil {
		t.Fatal(err)
	}
	dir := ws.Dir
	cleanup()
	cleanup() // idempotent
	for _, p := range []string{dir, ws.ContextDir} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists: %v", p, err)
		}
	}
	if list := git(t, filepath.Join(root, "repos", "o", "r.git"), "worktree", "list"); strings.Contains(list, dir) {
		t.Errorf("worktree still registered:\n%s", list)
	}
}

func TestConcurrentCheckoutsSameRepo(t *testing.T) {
	rdir, head := remote(t)
	var clones int32
	m := New(t.TempDir(), fakeGh(rdir, &clones))
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, cleanup, err := m.Checkout(context.Background(), "o/r", 7, "main", head)
			if err != nil {
				errs <- err
				return
			}
			cleanup()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if clones != 1 {
		t.Errorf("cloned %d times, want 1", clones)
	}
}

func TestCheckoutUnknownSHAFails(t *testing.T) {
	rdir, _ := remote(t)
	var clones int32
	m := New(t.TempDir(), fakeGh(rdir, &clones))
	_, cleanup, err := m.Checkout(context.Background(), "o/r", 7, "main", strings.Repeat("0", 40))
	cleanup()
	if err == nil {
		t.Fatal("want error for SHA not in the PR")
	}
}

func TestCheckoutRejectsBadRepo(t *testing.T) {
	m := New(t.TempDir(), run.Exec)
	for _, repo := range []string{"noslash", "../x/y", "o/.."} {
		if _, cleanup, err := m.Checkout(context.Background(), repo, 1, "main", "abc"); err == nil {
			cleanup()
			t.Errorf("repo %q should be rejected", repo)
		}
	}
}

func TestCheckoutWritesReviewContext(t *testing.T) {
	rdir, head := remote(t)
	var clones int32
	m := New(t.TempDir(), fakeGh(rdir, &clones))
	ws, cleanup, err := m.Checkout(context.Background(), "o/r", 7, "main", head)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if rel, err := filepath.Rel(ws.Dir, ws.ContextDir); err == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("context dir %s must be outside the worktree %s (a PR could plant files there)", ws.ContextDir, ws.Dir)
	}
	if filepath.Dir(ws.DiffPath) != ws.ContextDir || filepath.Dir(ws.CommitsPath) != ws.ContextDir {
		t.Errorf("context files not in context dir: %+v", ws)
	}
	diff, _ := os.ReadFile(ws.DiffPath)
	if !strings.Contains(string(diff), "-one") || !strings.Contains(string(diff), "+two") {
		t.Errorf("diff.patch = %q", diff)
	}
	commits, _ := os.ReadFile(ws.CommitsPath)
	if !strings.Contains(string(commits), "change") || strings.Contains(string(commits), "base") {
		t.Errorf("commits.txt should list only PR commits: %q", commits)
	}
}

func TestReviewContextDisablesDiffDrivers(t *testing.T) {
	rdir, head := remote(t)
	var clones int32
	var diffArgs []string
	inner := fakeGh(rdir, &clones)
	rec := func(ctx context.Context, dir, stdin, name string, args ...string) ([]byte, []byte, error) {
		if name == "git" && len(args) > 0 && args[0] == "diff" {
			diffArgs = args
		}
		return inner(ctx, dir, stdin, name, args...)
	}
	_, cleanup, err := New(t.TempDir(), rec).Checkout(context.Background(), "o/r", 7, "main", head)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	joined := strings.Join(diffArgs, " ")
	if !strings.Contains(joined, "--no-ext-diff") || !strings.Contains(joined, "--no-textconv") {
		t.Errorf("git diff must disable external diff drivers and textconv: %v", diffArgs)
	}
}

func TestConcurrentCheckoutsAcrossManagers(t *testing.T) {
	// Two Managers share nothing in memory, like two ghost processes.
	rdir, head := remote(t)
	var clones int32
	root := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		m := New(root, fakeGh(rdir, &clones))
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, cleanup, err := m.Checkout(context.Background(), "o/r", 7, "main", head)
			if err != nil {
				errs <- err
				return
			}
			cleanup()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if clones != 1 {
		t.Errorf("cloned %d times across managers, want 1", clones)
	}
	if _, err := os.Stat(filepath.Join(root, "repos", "o", "r.lock")); err != nil {
		t.Errorf("lock file missing: %v", err)
	}
}
