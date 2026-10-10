package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adamisrael/ghost/internal/preflight"
)

func okRunner(_ context.Context, name string, _ ...string) (string, error) {
	return name + " 1.0", nil
}

func TestInitThenStart(t *testing.T) {
	dir := t.TempDir()
	cfg, db := filepath.Join(dir, "cfg", "config.toml"), filepath.Join(dir, "data", "ghost.db")
	var out, errb bytes.Buffer

	if code := run([]string{"--config", cfg, "--db", db, "init"}, &out, &errb, okRunner); code != 0 {
		t.Fatalf("init exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "created "+cfg) {
		t.Errorf("init output = %q", out.String())
	}

	out.Reset()
	if code := run([]string{"--config", cfg, "--db", db}, &out, &errb, okRunner); code != 0 {
		t.Fatalf("start exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "config OK") {
		t.Errorf("start output = %q", out.String())
	}
	if _, err := os.Stat(db); err != nil {
		t.Errorf("database not created: %v", err)
	}
}

func TestStartWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := run([]string{"--config", filepath.Join(dir, "none.toml"), "--db", filepath.Join(dir, "g.db")}, &out, &errb, okRunner)
	if code != 1 || !strings.Contains(errb.String(), "ghost init") {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	var out, errb bytes.Buffer
	run([]string{"--config", cfg, "init"}, &out, &errb, okRunner)

	out.Reset()
	if code := run([]string{"--config", cfg, "--check"}, &out, &errb, okRunner); code != 0 {
		t.Fatalf("check exit %d: %s", code, out.String())
	}

	failGh := func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "gh" && len(args) > 0 && args[0] == "auth" {
			return "", errors.New("exit status 1")
		}
		return okRunner(ctx, name, args...)
	}
	out.Reset()
	if code := run([]string{"--config", cfg, "--check"}, &out, &errb, preflight.Runner(failGh)); code != 1 {
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
	if code := run([]string{"init", "--config", cfg}, &out, &errb, okRunner); code != 0 {
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
		if code := run(args, &out, &errb, okRunner); code != 2 {
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
	if code := run([]string{"--config", "~/c/config.toml", "init"}, &out, &errb, okRunner); code != 0 {
		t.Fatalf("init exit %d: %s", code, errb.String())
	}
	if code := run([]string{"--config", "~/c/config.toml", "--db", "~/d/ghost.db"}, &out, &errb, okRunner); code != 0 {
		t.Fatalf("start exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(home, "d", "ghost.db")); err != nil {
		t.Fatalf("db not created under HOME: %v", err)
	}
}
