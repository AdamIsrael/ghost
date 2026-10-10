package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/poller"
	"github.com/AdamIsrael/ghost/internal/preflight"
)

func okRunner(_ context.Context, name string, _ ...string) (string, error) {
	return name + " 1.0", nil
}

type fakeSearcher struct {
	once   sync.Once
	called chan string
}

func (f *fakeSearcher) SearchPRs(_ context.Context, q string) ([]github.PR, error) {
	f.once.Do(func() { f.called <- q })
	return nil, nil
}

// testDeps never touches the network or a terminal. runTUI records that it
// ran and, if a searcher is set, waits for the poller's first query.
func testDeps(runner preflight.Runner, s *fakeSearcher) (deps, *bool) {
	ran := new(bool)
	return deps{
		runner: runner,
		newSearcher: func() (poller.Searcher, error) {
			if s == nil {
				return nil, errors.New("gh not authenticated")
			}
			return s, nil
		},
		runTUI: func(m tea.Model) error {
			*ran = true
			m.View()
			if s != nil {
				select {
				case <-s.called:
				case <-time.After(2 * time.Second):
					return errors.New("poller never queried GitHub")
				}
			}
			return nil
		},
		openURL: func(string) error { return nil },
	}, ran
}

func depsFor(runner preflight.Runner) deps { d, _ := testDeps(runner, nil); return d }

func TestInitThenStart(t *testing.T) {
	dir := t.TempDir()
	cfg, db := filepath.Join(dir, "cfg", "config.toml"), filepath.Join(dir, "data", "ghost.db")
	var out, errb bytes.Buffer

	if code := run([]string{"--config", cfg, "--db", db, "init"}, &out, &errb, depsFor(okRunner)); code != 0 {
		t.Fatalf("init exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "created "+cfg) {
		t.Errorf("init output = %q", out.String())
	}

	s := &fakeSearcher{called: make(chan string, 1)}
	d, ran := testDeps(okRunner, s)
	if code := run([]string{"--config", cfg, "--db", db}, &out, &errb, d); code != 0 {
		t.Fatalf("start exit %d: %s", code, errb.String())
	}
	if !*ran {
		t.Error("TUI was not started")
	}
	if _, err := os.Stat(db); err != nil {
		t.Errorf("database not created: %v", err)
	}
}

func TestStartWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := run([]string{"--config", filepath.Join(dir, "none.toml"), "--db", filepath.Join(dir, "g.db")}, &out, &errb, depsFor(okRunner))
	if code != 1 || !strings.Contains(errb.String(), "ghost init") {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	var out, errb bytes.Buffer
	run([]string{"--config", cfg, "init"}, &out, &errb, depsFor(okRunner))

	out.Reset()
	if code := run([]string{"--config", cfg, "--check"}, &out, &errb, depsFor(okRunner)); code != 0 {
		t.Fatalf("check exit %d: %s", code, out.String())
	}

	failGh := func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "gh" && len(args) > 0 && args[0] == "auth" {
			return "", errors.New("exit status 1")
		}
		return okRunner(ctx, name, args...)
	}
	out.Reset()
	if code := run([]string{"--config", cfg, "--check"}, &out, &errb, depsFor(failGh)); code != 1 {
		t.Fatalf("check with unauthenticated gh should exit 1")
	}
	if !strings.Contains(out.String(), "gh auth login") {
		t.Errorf("check output missing fix hint: %q", out.String())
	}
}

func TestInitHonorsFlagsAfterSubcommand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := filepath.Join(t.TempDir(), "x", "config.toml")
	var out, errb bytes.Buffer
	if code := run([]string{"init", "--config", cfg}, &out, &errb, depsFor(okRunner)); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Fatalf("config not written to --config path: %v", err)
	}
}

func TestInitRejectsBadArgs(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.toml")
	for _, args := range [][]string{
		{"--config", cfg, "init", "junk"},
		{"--config", cfg, "--check", "init"},
		{"--config", cfg, "init", "--check"},
	} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb, depsFor(okRunner)); code != 2 {
			t.Errorf("run(%q) exit %d, want 2", args, code)
		}
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Error("rejected invocation still wrote a config")
	}
}

func TestFlagPathsExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out, errb bytes.Buffer
	if code := run([]string{"--config", "~/c/config.toml", "init"}, &out, &errb, depsFor(okRunner)); code != 0 {
		t.Fatalf("init exit %d: %s", code, errb.String())
	}
	if code := run([]string{"--config", "~/c/config.toml", "--db", "~/d/ghost.db"}, &out, &errb, depsFor(okRunner)); code != 0 {
		t.Fatalf("start exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(home, "d", "ghost.db")); err != nil {
		t.Fatalf("db not created under HOME: %v", err)
	}
}

func TestStartWithoutGitHub(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	var out, errb bytes.Buffer
	run([]string{"--config", cfg, "init"}, &out, &errb, depsFor(okRunner))
	d, ran := testDeps(okRunner, nil)
	if code := run([]string{"--config", cfg, "--db", filepath.Join(dir, "g.db")}, &out, &errb, d); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !*ran {
		t.Error("TUI should still open when GitHub is unavailable")
	}
}
