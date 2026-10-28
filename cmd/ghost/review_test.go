package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/store"
	"github.com/AdamIsrael/ghost/internal/tui"
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
		"unknown agent, no mode": {"review", "o/r#7", "--agent", "nope"},
		"no pr":                  {"review", "--dry-run"},
		"bad pr":                 {"review", "not-a-pr", "--dry-run"},
		"unknown agent":          {"review", "o/r#7", "--agent", "nope", "--dry-run"},
		"both modes":             {"review", "o/r#7", "--dry-run", "--show-prompt"},
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

func TestReviewUsesCanonicalRepoName(t *testing.T) {
	h, d := newReviewHarness(t, "")
	var checkedOut string
	d.getPR = func(_ context.Context, repo string, n int) (github.PRDetail, error) {
		return github.PRDetail{PR: github.PR{Repo: "cli/cli", Number: n, Title: "T", BaseRef: "main", HeadSHA: "abc"}}, nil
	}
	d.checkout = func(_ context.Context, repo string, n int, base, sha string) (workspace.Workspace, func(), error) {
		checkedOut = repo
		return workspace.Workspace{Dir: "/wt"}, func() {}, nil
	}
	var out, errb bytes.Buffer
	if code := run([]string{"--config", h.cfg, "review", "CLI/CLI#7", "--dry-run"}, &out, &errb, d); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if checkedOut != "cli/cli" {
		t.Errorf("checkout used %q; want GitHub's canonical name so case variants share one clone", checkedOut)
	}
}

func TestReviewOpensTheTUIOnThePR(t *testing.T) {
	h, d := newReviewHarness(t, "")
	db := filepath.Join(t.TempDir(), "ghost.db")
	var seen tui.Model
	d.runTUI = func(m tea.Model) error {
		seen = m.(tui.Model)
		return nil
	}
	var out, errb bytes.Buffer
	if code := run([]string{"--config", h.cfg, "--db", db, "review", "https://github.com/o/my-repo/pull/7"}, &out, &errb, d); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if v, ok := seen.CurrentView(); !ok || v.Name != "my-repo#7" || v.PR != "o/my-repo#7" || v.Source != "session" {
		t.Errorf("active view = %+v (ok %v), want the one-off my-repo#7 tab", v, ok)
	}
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r, _ := st.LatestReview("o/my-repo", 7)
	if r == nil || r.Agent != "claude" {
		t.Fatalf("review = %+v, want one queued with the default agent", r)
	}
	evs, _ := st.Audit("o/my-repo", 7, 10)
	if len(evs) == 0 || evs[0].Kind != "queued" || !strings.Contains(evs[0].Message, "review requested") {
		t.Errorf("audit = %+v", evs)
	}
}

func TestReviewWhileGhostRunsSaysWhatToDo(t *testing.T) {
	h, d := newReviewHarness(t, "")
	db := filepath.Join(t.TempDir(), "ghost.db")
	held, err := store.Open(db) // another Ghost has the database
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	var out, errb bytes.Buffer
	if code := run([]string{"--config", h.cfg, "--db", db, "review", "o/r#7"}, &out, &errb, d); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if msg := errb.String(); !strings.Contains(msg, "already open") || !strings.Contains(msg, "press r") {
		t.Errorf("stderr = %q", msg)
	}
}

func TestBadPRFailsBeforeTheEngineRuns(t *testing.T) {
	h, d := newReviewHarness(t, "")
	db := filepath.Join(t.TempDir(), "ghost.db")
	// An earlier session left a review queued.
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	prID, _ := st.UpsertPR(github.PR{Repo: "o/other", Number: 1, HeadSHA: "abc"})
	st.EnqueueReview(prID, "abc", "claude", false)
	st.Close()

	d.getPR = func(context.Context, string, int) (github.PRDetail, error) {
		return github.PRDetail{}, errors.New("o/r#9999 not found")
	}
	var out, errb bytes.Buffer
	if code := run([]string{"--config", h.cfg, "--db", db, "review", "o/r#9999"}, &out, &errb, d); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if h.checkedOut {
		t.Error("a bad PR must fail before the engine picks up queued work")
	}
	st, _ = store.Open(db)
	defer st.Close()
	if r, _ := st.LatestReview("o/other", 1); r == nil || r.State != store.Queued {
		t.Errorf("the earlier queued review was touched: %+v", r)
	}
}

func TestOneOffTabNameIsUnique(t *testing.T) {
	views := []config.View{{Name: "my-repo#7"}, {Name: "my-repo#7 (review)"}}
	v := oneOffView(github.PR{Repo: "O/my-repo"}, &oneOff{repo: "o/my-repo", number: 7}, views)
	if v.Name != "my-repo#7 (review) (review)" || v.PR != "O/my-repo#7" || v.Source != "session" {
		t.Errorf("view = %+v", v)
	}
}

func TestSavedViewCantTakeTheSessionTabsName(t *testing.T) {
	views := []config.View{
		{Name: "my-repo#7", PR: "o/my-repo#7", Source: "session"},
		{Name: "my-repo#7", Query: "is:pr", Source: "views.toml"},
	}
	if err := checkSessionNames(views); err == nil || !strings.Contains(err.Error(), "ghost review tab") {
		t.Errorf("err = %v", err)
	}
	if err := checkSessionNames(views[:1]); err != nil {
		t.Errorf("no clash: %v", err)
	}
}
