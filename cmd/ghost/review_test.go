package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/workspace"
)

type fakeReviewer struct {
	got agent.ReviewRequest
	res agent.ReviewResult
	err error
}

func (f *fakeReviewer) Name() string { return "fake" }
func (f *fakeReviewer) Review(_ context.Context, req agent.ReviewRequest) (agent.ReviewResult, error) {
	f.got = req
	return f.res, f.err
}

type reviewHarness struct {
	cfg        string
	rev        *fakeReviewer
	checkedOut bool
	cleaned    bool
	agentName  string
}

func newReviewHarness(t *testing.T, extraConfig string) (*reviewHarness, deps) {
	t.Helper()
	dir := t.TempDir()
	h := &reviewHarness{cfg: filepath.Join(dir, "config.toml"), rev: &fakeReviewer{res: agent.ReviewResult{
		Verdict: agent.RequestChanges, Summary: "Needs a nil check.", CostUSD: 0.42,
		Comments: []agent.InlineComment{{Path: "parser.go", Line: 12, Body: "nil deref", Severity: "blocking"}},
	}}}
	var out, errb bytes.Buffer
	run([]string{"--config", h.cfg, "init"}, &out, &errb, depsFor(okRunner))
	if extraConfig != "" {
		appendFile(t, h.cfg, extraConfig)
	}
	d := depsFor(okRunner)
	d.getPR = func(_ context.Context, repo string, n int) (github.PRDetail, error) {
		return github.PRDetail{PR: github.PR{Repo: repo, Number: n, Title: "Fix parser", BaseRef: "main", HeadSHA: "deadbeef"}, Files: []string{"parser.go"}}, nil
	}
	d.checkout = func(_ context.Context, repo string, n int, base, sha string) (workspace.Workspace, func(), error) {
		h.checkedOut = true
		return workspace.Workspace{Dir: "/tmp/wt", ContextDir: "/tmp/ctx", DiffPath: "/tmp/ctx/diff.patch", CommitsPath: "/tmp/ctx/commits.txt"},
			func() { h.cleaned = true }, nil
	}
	d.newReviewer = func(name string, a config.Agent) (agent.Reviewer, error) {
		h.agentName = name
		return h.rev, nil
	}
	return h, d
}

func TestReviewDryRun(t *testing.T) {
	h, d := newReviewHarness(t, "")
	var out, errb bytes.Buffer
	code := run([]string{"--config", h.cfg, "review", "https://github.com/o/r/pull/7", "--dry-run"}, &out, &errb, d)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"o/r#7", "Fix parser", "request_changes", "Needs a nil check.", "parser.go:12", "[blocking]", "nil deref", "$0.42", "dry run"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if !h.checkedOut || !h.cleaned {
		t.Error("worktree not checked out and cleaned up")
	}
	if h.rev.got.Dir != "/tmp/wt" || h.rev.got.ContextDir != "/tmp/ctx" || h.rev.got.MaxCostUSD != 1.5 {
		t.Errorf("request = %+v", h.rev.got)
	}
	if !strings.Contains(h.rev.got.Prompt, "o/r#7") || !strings.Contains(h.rev.got.Prompt, "/tmp/ctx/diff.patch") || !strings.HasSuffix(h.rev.got.Prompt, agent.OutputContract) {
		t.Errorf("prompt not rendered with contract:\n%s", h.rev.got.Prompt)
	}
	if h.agentName != "claude" {
		t.Errorf("agent = %q, want default", h.agentName)
	}
}

func TestReviewShowPromptSpendsNothing(t *testing.T) {
	h, d := newReviewHarness(t, "")
	var out, errb bytes.Buffer
	if code := run([]string{"--config", h.cfg, "review", "o/r#7", "--show-prompt"}, &out, &errb, d); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "o/r#7") || !strings.Contains(out.String(), agent.OutputContract) {
		t.Errorf("prompt not printed:\n%s", out.String())
	}
	if h.checkedOut || h.agentName != "" {
		t.Error("--show-prompt must not check out or run the agent")
	}
}

func TestReviewUsesNamedAgentPrompt(t *testing.T) {
	h, d := newReviewHarness(t, "\n[agents.sec]\nkind = \"claude\"\nprompt = \"SECURITY ONLY for {{.Repo}}\"\n")
	var out, errb bytes.Buffer
	if code := run([]string{"--config", h.cfg, "review", "o/r#7", "--agent", "sec", "--dry-run"}, &out, &errb, d); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if h.agentName != "sec" || !strings.HasPrefix(h.rev.got.Prompt, "SECURITY ONLY for o/r") {
		t.Errorf("agent %q prompt %q", h.agentName, h.rev.got.Prompt)
	}
}

func TestReviewCleansUpOnAgentError(t *testing.T) {
	h, d := newReviewHarness(t, "")
	h.rev.err = errors.New("claude error_max_budget_usd: Reached maximum budget")
	h.rev.res = agent.ReviewResult{CostUSD: 1.51}
	var out, errb bytes.Buffer
	if code := run([]string{"--config", h.cfg, "review", "o/r#7", "--dry-run"}, &out, &errb, d); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !h.cleaned {
		t.Error("worktree not cleaned up after agent error")
	}
	if !strings.Contains(errb.String(), "maximum budget") || !strings.Contains(errb.String(), "$1.51") {
		t.Errorf("stderr should name the error and the cost spent: %q", errb.String())
	}
}

func TestReviewArgErrors(t *testing.T) {
	h, d := newReviewHarness(t, "")
	for name, args := range map[string][]string{
		"no mode":       {"review", "o/r#7"},
		"no pr":         {"review", "--dry-run"},
		"bad pr":        {"review", "not-a-pr", "--dry-run"},
		"unknown agent": {"review", "o/r#7", "--agent", "nope", "--dry-run"},
		"both modes":    {"review", "o/r#7", "--dry-run", "--show-prompt"},
	} {
		var out, errb bytes.Buffer
		if code := run(append([]string{"--config", h.cfg}, args...), &out, &errb, d); code == 0 {
			t.Errorf("%s: exit 0, want failure", name)
		}
	}
	if h.checkedOut {
		t.Error("bad args should fail before checkout")
	}
}

func TestReviewOutputIsSanitized(t *testing.T) {
	h, d := newReviewHarness(t, "")
	h.rev.res = agent.ReviewResult{Verdict: agent.Comment, Summary: "ok\r\x1b]52;c;ZXZpbA==\x07", CostUSD: 0.1,
		Comments: []agent.InlineComment{{Path: "a\x1b[8m.go", Line: 1, Body: "b\x9b", Severity: "nit"}}}
	var out, errb bytes.Buffer
	if code := run([]string{"--config", h.cfg, "review", "o/r#7", "--dry-run"}, &out, &errb, d); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if strings.ContainsAny(out.String(), "\r\x1b\x07") || strings.Contains(out.String(), "\u009b") {
		t.Errorf("control characters reached the terminal: %q", out.String())
	}
}
