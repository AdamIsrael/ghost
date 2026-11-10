package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/engine"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/poller"
	"github.com/AdamIsrael/ghost/internal/store"
)

type fakeEngine struct {
	snap      engine.Snapshot
	state     engine.PRState
	polls     []poller.Result
	rereview  []github.PR
	posted    []string
	postErr   error
	paused    bool
	changes   chan struct{}
	approved  []string
	byHand    []string // repo#n@sha passed to ApproveByHand
	byHandErr error

	snapCalls, stateCalls int
}

func (f *fakeEngine) Approve(_ context.Context, repo string, n int) error {
	f.approved = append(f.approved, repo)
	return nil
}

func (f *fakeEngine) ApproveByHand(_ context.Context, repo string, n int, sha string) error {
	f.byHand = append(f.byHand, fmt.Sprintf("%s#%d@%s", repo, n, sha))
	return f.byHandErr
}

func (f *fakeEngine) HandlePoll(r poller.Result)         { f.polls = append(f.polls, r) }
func (f *fakeEngine) Snapshot() engine.Snapshot          { f.snapCalls++; return f.snap }
func (f *fakeEngine) PRState(string, int) engine.PRState { f.stateCalls++; return f.state }
func (f *fakeEngine) Rereview(pr github.PR) error        { f.rereview = append(f.rereview, pr); return nil }
func (f *fakeEngine) TogglePause() bool                  { f.paused = !f.paused; return f.paused }
func (f *fakeEngine) Changes() <-chan struct{}           { return f.changes }
func (f *fakeEngine) PostPending(_ context.Context, repo string, n int) error {
	f.posted = append(f.posted, repo)
	return f.postErr
}

func withEngine(t *testing.T, fe *fakeEngine) tea.Model {
	t.Helper()
	if fe.changes == nil {
		fe.changes = make(chan struct{}, 1)
	}
	return send(t, New(Options{Views: twoViews, MaxDailyCostUSD: 10, Engine: fe}),
		tea.WindowSizeMsg{Width: 160, Height: 30}, result("Team", samplePR(1, "First")))
}

func runCmd(m tea.Model, cmd tea.Cmd) tea.Model {
	if cmd == nil {
		return m
	}
	m, _ = m.Update(cmd())
	return m
}

func TestPollResultsReachEngine(t *testing.T) {
	fe := &fakeEngine{}
	withEngine(t, fe)
	if len(fe.polls) != 1 || fe.polls[0].View != "Team" {
		t.Errorf("polls = %+v", fe.polls)
	}
}

func TestStatusBarShowsEngineSnapshot(t *testing.T) {
	fe := &fakeEngine{snap: engine.Snapshot{SpendTodayUSD: 3.12, MaxDailyUSD: 10, Running: 1, Queued: 2, BudgetPaused: true}}
	out := screen(withEngine(t, fe))
	for _, want := range []string{"Spend today $3.12 / $10.00", "Reviews 1 running · 2 queued", "budget reached"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	fe.snap = engine.Snapshot{MaxDailyUSD: 10, Paused: true}
	if !strings.Contains(screen(withEngine(t, fe)), "paused") {
		t.Error("paused state not shown")
	}
}

func TestDetailShowsAuditAndPendingReview(t *testing.T) {
	at := time.Date(2026, 10, 9, 10, 2, 0, 0, time.Local)
	fe := &fakeEngine{state: engine.PRState{
		Audit:    []store.AuditEvent{{At: at, Kind: "queued", Message: "view \"Team\""}, {At: at, Kind: "reviewed", Message: "request_changes, 1 comments, $0.40"}},
		Latest:   &store.Review{State: store.Ready, Verdict: "request_changes", Summary: "Needs a nil check.", CostUSD: 0.4},
		Comments: []agent.InlineComment{{Path: "parser.go", Line: 12, Body: "nil deref", Severity: "blocking"}},
	}}
	out := screen(withEngine(t, fe))
	for _, want := range []string{"10:02 queued", "reviewed — request_changes", "Pending review", "press P", "Needs a nil check.", "parser.go:12", "blocking", "nil deref"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "No activity yet") {
		t.Error("placeholder shown alongside real audit")
	}
}

func TestDetailShowsBlockedReview(t *testing.T) {
	fe := &fakeEngine{state: engine.PRState{Latest: &store.Review{State: store.Blocked, Error: "possible secret in review: GitHub token"}}}
	out := screen(withEngine(t, fe))
	if !strings.Contains(out, "Review blocked") || !strings.Contains(out, "GitHub token") || strings.Contains(out, "press P") {
		t.Errorf("blocked review not shown correctly:\n%s", out)
	}
}

func TestKeysCallEngine(t *testing.T) {
	fe := &fakeEngine{}
	m := withEngine(t, fe)
	m, cmd := m.Update(key("r"))
	m = runCmd(m, cmd)
	if len(fe.rereview) != 1 || fe.rereview[0].Number != 1 {
		t.Errorf("r: rereview = %+v", fe.rereview)
	}
	m, cmd = m.Update(key("P"))
	m = runCmd(m, cmd)
	if len(fe.posted) != 1 {
		t.Errorf("P: posted = %v", fe.posted)
	}
	m.Update(key("p"))
	if !fe.paused {
		t.Error("p did not pause")
	}
}

func TestEngineErrorsFlash(t *testing.T) {
	fe := &fakeEngine{postErr: errors.New("no review is waiting to be posted")}
	m := withEngine(t, fe)
	m, cmd := m.Update(key("P"))
	m = runCmd(m, cmd)
	if !strings.Contains(screen(m), "no review is waiting to be posted") {
		t.Errorf("error not flashed:\n%s", screen(m))
	}
}

func TestInitWaitsForEngineChanges(t *testing.T) {
	fe := &fakeEngine{changes: make(chan struct{}, 1)}
	fe.changes <- struct{}{}
	cmd := New(Options{Views: twoViews, Engine: fe}).Init()
	if cmd == nil {
		t.Fatal("Init should wait on engine changes")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		if _, isChange := msg.(changeMsg); !isChange {
			t.Fatalf("Init returned %T", msg)
		}
		return
	}
	found := false
	for _, c := range batch {
		if c == nil {
			continue
		}
		if _, ok := c().(changeMsg); ok {
			found = true
		}
	}
	if !found {
		t.Error("no changeMsg from Init")
	}
}

func TestDetailShowsPostUnknown(t *testing.T) {
	fe := &fakeEngine{state: engine.PRState{Latest: &store.Review{State: store.PostUnknown, Summary: "S"}}}
	if out := screen(withEngine(t, fe)); !strings.Contains(out, "Post result unknown") {
		t.Errorf("post-unknown not shown:\n%s", out)
	}
}

func TestApproveKey(t *testing.T) {
	for _, state := range []string{store.ApprovalPending, store.ApprovalUnknown} {
		fe := &fakeEngine{state: engine.PRState{Latest: &store.Review{State: store.Posted, Approval: state}}}
		m := withEngine(t, fe)
		m, cmd := m.Update(key("a"))
		m = runCmd(m, cmd)
		if len(fe.approved) != 1 || len(fe.byHand) != 0 {
			t.Errorf("%s: approved = %v, by hand = %v", state, fe.approved, fe.byHand)
		}
		if f := m.(Model).flash; f != "approved o/r#1" {
			t.Errorf("%s: flash = %q", state, f)
		}
	}
}

// #22: with no approval waiting, a approves the selected PR by hand after
// a y, whatever its review state.
func TestApproveKeyByHand(t *testing.T) {
	for name, latest := range map[string]*store.Review{
		"unreviewed":   nil,
		"not eligible": {State: store.Posted, Approval: store.ApprovalNo},
		"ready":        {State: store.Ready},
	} {
		fe := &fakeEngine{state: engine.PRState{Latest: latest}}
		m := withEngine(t, fe)
		m, _ = m.Update(key("a"))
		if out := screen(m); !strings.Contains(out, "Approve o/r#1 at abc1234 on GitHub? y/n") {
			t.Errorf("%s: no prompt:\n%s", name, out)
		}
		if len(fe.byHand) != 0 || len(fe.approved) != 0 {
			t.Fatalf("%s: approved before y", name)
		}
		m, cmd := m.Update(key("y"))
		m = runCmd(m, cmd)
		if len(fe.byHand) != 1 || fe.byHand[0] != "o/r#1@abc1234def" || len(fe.approved) != 0 {
			t.Errorf("%s: by hand = %v, approved = %v", name, fe.byHand, fe.approved)
		}
		if f := m.(Model).flash; f != "approved o/r#1 at abc1234" {
			t.Errorf("%s: flash = %q", name, f)
		}
	}
}

func TestApproveByHandCancelled(t *testing.T) {
	fe := &fakeEngine{}
	m := withEngine(t, fe)
	m, _ = m.Update(key("a"))
	m, cmd := m.Update(key("n"))
	m = runCmd(m, cmd)
	if len(fe.byHand) != 0 || strings.Contains(screen(m), "Approve o/r#1") {
		t.Errorf("by hand = %v after n:\n%s", fe.byHand, screen(m))
	}
}

func TestApproveByHandError(t *testing.T) {
	fe := &fakeEngine{byHandErr: errors.New("GitHub does not allow approving your own pull request")}
	m := withEngine(t, fe)
	m, _ = m.Update(key("a"))
	m, cmd := m.Update(key("y"))
	m = runCmd(m, cmd)
	if f := m.(Model).flash; f != "approve: GitHub does not allow approving your own pull request" {
		t.Errorf("flash = %q", f)
	}
}

func TestApprovePromptWarns(t *testing.T) {
	p := samplePR(5, "x")
	p.CIState, p.IsDraft, p.ReviewDecision = "FAILURE", true, "CHANGES_REQUESTED"
	if got, want := approvePrompt(p), "Approve o/r#5 at abc1234 on GitHub? (CI failing, draft, changes requested) y/n"; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
	p.CIState, p.IsDraft, p.ReviewDecision = "PENDING", false, "APPROVED"
	if got := approvePrompt(p); !strings.Contains(got, "(CI pending)") {
		t.Errorf("prompt = %q", got)
	}
}

func TestDetailShowsApprovalState(t *testing.T) {
	for state, want := range map[string]string{
		store.ApprovalPending:  "press a to approve",
		store.ApprovalWaiting:  "Approval waiting: CI is pending",
		store.ApprovalApproved: "Approved",
		store.ApprovalNo:       "Not approved: CI is pending",
		store.ApprovalFailed:   "Approval failed: CI is pending",
		store.ApprovalUnknown:  "press a to check GitHub",
	} {
		fe := &fakeEngine{state: engine.PRState{Latest: &store.Review{State: store.Posted, Approval: state, ApprovalNote: "CI is pending"}}}
		if out := screen(withEngine(t, fe)); !strings.Contains(out, want) {
			t.Errorf("%s: missing %q:\n%s", state, want, out)
		}
	}
}

func TestFlashDoesNotRepeatPrefix(t *testing.T) {
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 160, Height: 10},
		engineDoneMsg{what: "approve", err: errors.New("approve: GitHub does not allow approving your own pull request")})
	if out := screen(m); strings.Contains(out, "approve: approve:") || !strings.Contains(out, "approve: GitHub does not allow") {
		t.Errorf("flash:\n%s", out)
	}
}

func TestRenderDoesNotQueryEngine(t *testing.T) {
	fe := &fakeEngine{}
	m := withEngine(t, fe)
	snaps, states := fe.snapCalls, fe.stateCalls
	for i := 0; i < 5; i++ {
		m.View()
	}
	if fe.snapCalls != snaps || fe.stateCalls != states {
		t.Errorf("View queried the engine: snapshot %d→%d, state %d→%d", snaps, fe.snapCalls, states, fe.stateCalls)
	}
	m.Update(changeMsg{})
	if fe.snapCalls == snaps {
		t.Error("an engine change should refresh the snapshot")
	}
}

func TestCursorMoveRefreshesPRState(t *testing.T) {
	fe := &fakeEngine{changes: make(chan struct{}, 1)}
	m := send(t, New(Options{Views: twoViews, MaxDailyCostUSD: 10, Engine: fe}),
		tea.WindowSizeMsg{Width: 160, Height: 30}, result("Team", samplePR(1, "First"), samplePR(2, "Second")))
	before := fe.stateCalls
	m.Update(key("j"))
	if fe.stateCalls == before {
		t.Error("moving the cursor should load the newly selected PR's state")
	}
}

func TestStaleReviewWarning(t *testing.T) {
	fe := &fakeEngine{state: engine.PRState{Latest: &store.Review{State: store.Ready, HeadSHA: "0000000aaaa", Verdict: "comment", Summary: "S"}}}
	out := screen(withEngine(t, fe)) // samplePR head is abc1234def
	if !strings.Contains(out, "PR has new commits since this review") || !strings.Contains(out, "0000000") || !strings.Contains(out, "abc1234") {
		t.Errorf("stale warning missing:\n%s", out)
	}
	fe.state.Latest.HeadSHA = "abc1234def"
	if out := screen(withEngine(t, fe)); strings.Contains(out, "new commits") {
		t.Error("no warning when the review matches the head")
	}
}

// GitHub #9: r confirms the review was queued, and says why it's waiting.
func TestReviewKeyConfirmsQueued(t *testing.T) {
	fe := &fakeEngine{}
	m := withEngine(t, fe)
	m, cmd := m.Update(key("r"))
	m = runCmd(m, cmd)
	ref := fmt.Sprintf("%s#%d", fe.rereview[0].Repo, fe.rereview[0].Number)
	if out := screen(m); !strings.Contains(out, "review queued for "+ref) || strings.Contains(out, "waiting:") {
		t.Errorf("no plain queued confirmation:\n%s", out)
	}

	fe.state = engine.PRState{Latest: &store.Review{State: store.Queued}, Waiting: "Ghost is paused (press p to resume)"}
	m, cmd = m.Update(key("r"))
	m = runCmd(m, cmd)
	if out := screen(m); !strings.Contains(out, "review queued for "+ref+"; waiting: Ghost is paused") {
		t.Errorf("flash doesn't say why it's waiting:\n%s", out)
	}
}

func TestDetailShowsQueuedAndRunning(t *testing.T) {
	for _, tc := range []struct {
		state engine.PRState
		want  string
	}{
		{engine.PRState{Latest: &store.Review{State: store.Queued}, Waiting: "Ghost is paused (press p to resume)"}, "Queued, waiting: Ghost is paused"},
		{engine.PRState{Latest: &store.Review{State: store.Queued}}, "Queued; starting…"},
		{engine.PRState{Latest: &store.Review{State: store.Running}}, "Reviewing…"},
	} {
		if out := screen(withEngine(t, &fakeEngine{state: tc.state})); !strings.Contains(out, tc.want) {
			t.Errorf("missing %q:\n%s", tc.want, out)
		}
	}
}
