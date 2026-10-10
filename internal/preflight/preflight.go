// Package preflight verifies the external tools Ghost shells out to.
package preflight

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// Runner executes a command and returns its trimmed combined output.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

func ExecRunner(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

type Result struct {
	Name   string
	OK     bool
	Detail string
	Fix    string
}

const checkTimeout = 10 * time.Second

// Run checks git, gh (installed and authenticated) and claude.
func Run(ctx context.Context, run Runner) []Result {
	return []Result{
		version(ctx, run, "git", "install git: https://git-scm.com/downloads"),
		gh(ctx, run),
		version(ctx, run, "claude", "install Claude Code: https://docs.claude.com/en/docs/claude-code"),
	}
}

func AllOK(rs []Result) bool {
	for _, r := range rs {
		if !r.OK {
			return false
		}
	}
	return true
}

func version(ctx context.Context, run Runner, name, fix string) Result {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	out, err := run(ctx, name, "--version")
	if err != nil {
		detail := err.Error()
		if errors.Is(err, exec.ErrNotFound) {
			detail = "not found on PATH"
		}
		return Result{Name: name, Detail: detail, Fix: fix}
	}
	return Result{Name: name, OK: true, Detail: firstLine(out)}
}

func gh(ctx context.Context, run Runner) Result {
	r := version(ctx, run, "gh", "install gh: https://cli.github.com")
	if !r.OK {
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	if out, err := run(ctx, "gh", "auth", "status"); err != nil {
		detail := "not authenticated"
		if line := strings.TrimSpace(firstLine(out)); line != "" {
			detail = line // gh's own reason: logged out, network, expired token…
		}
		return Result{Name: "gh", Detail: detail, Fix: "run `gh auth status` for details, or `gh auth login`"}
	}
	return r
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
