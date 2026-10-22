package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/poller"
	"github.com/AdamIsrael/ghost/internal/store"
	"github.com/AdamIsrael/ghost/internal/workspace"
)

type fakeReviewer struct {
	mu      sync.Mutex
	calls   []agent.ReviewRequest
	res     agent.ReviewResult
	err     error
	release chan struct{} // if set, Review blocks until it is closed
}

func (f *fakeReviewer) Name() string { return "fake" }
func (f *fakeReviewer) Review(ctx context.Context, req agent.ReviewRequest) (agent.ReviewResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	release := f.release
	f.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return agent.ReviewResult{}, ctx.Err()
		}
	}
	return f.res, f.err
}
func (f *fakeReviewer) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

type harness struct {
	t      *testing.T
	e      *Engine
	st     *store.Store
	rev    *fakeReviewer
	mu     sync.Mutex
	heads  map[int]string // PR number → current head SHA reported by GetPR
	posts  []github.ReviewPost
	postFn func(github.ReviewPost) (github.PostResult, error)
	onGH   bool // what HasReview reports

	ci        map[int]string // PR number → CI state reported by GetPR (default SUCCESS)
	approvals []string       // bodies passed to Approve
	approveFn func() error
	getPRErr  error
	hasRevErr error
	now       time.Time // engine clock; zero = time.Now
	cancel    context.CancelFunc

	labels        map[int][]string // PR number → labels GetPR reports (AddLabel appends)
	labelCalls    int
	getPRCalls    map[int]int // PR number → GetPR calls
	addLabelErr   error
	addLabelBlock chan struct{} // if set, AddLabel waits for it to close
	getPRBlock    chan struct{} // if set, GetPR waits for it to close
}

func newHarness(t *testing.T, mutate func(*config.Config)) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ghost.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	cfg.DefaultAgent = "claude"
	cfg.Agents = map[string]config.Agent{"claude": {Kind: "claude", Prompt: "Review {{.Repo}}#{{.Number}}"}}
	cfg.Views = []config.View{{Name: "Team", Query: "q", AutoReview: true}, {Name: "Watch", Query: "w"}}
	if mutate != nil {
		mutate(&cfg)
	}
	h := &harness{t: t, st: st, heads: map[int]string{}, ci: map[int]string{}, labels: map[int][]string{}, getPRCalls: map[int]int{}, rev: &fakeReviewer{res: agent.ReviewResult{
		Verdict: agent.RequestChanges, Summary: "Needs a nil check.", CostUSD: 0.4,
		Comments: []agent.InlineComment{{Path: "a.go", Line: 3, Body: "nil deref", Severity: "blocking"}},
	}}}
	h.e = New(cfg, Deps{
		Store: st,
		GetPR: func(_ context.Context, repo string, n int) (github.PRDetail, error) {
			h.mu.Lock()
			h.getPRCalls[n]++
			block := h.getPRBlock
			h.mu.Unlock()
			if block != nil {
				<-block
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.getPRErr != nil {
				return github.PRDetail{}, h.getPRErr
			}
			ci, ok := h.ci[n]
			if !ok {
				ci = "SUCCESS"
			}
			return github.PRDetail{PR: github.PR{Repo: repo, Number: n, State: "OPEN", Title: "T", BaseRef: "main", HeadSHA: h.heads[n], CIState: ci, Additions: 10, Labels: append([]string(nil), h.labels[n]...)}, Files: []string{"a.go"}}, nil
		},
		AddLabel: func(_ context.Context, repo string, n int, label string) error {
			h.mu.Lock()
			h.labelCalls++
			err, block := h.addLabelErr, h.addLabelBlock
			h.mu.Unlock()
			if block != nil {
				<-block
			}
			if err != nil {
				return err
			}
			h.mu.Lock()
			h.labels[n] = append(h.labels[n], label)
			h.mu.Unlock()
			return nil
		},
		Approve: func(_ context.Context, repo string, n int, commitID, body string) error {
			h.mu.Lock()
			h.approvals = append(h.approvals, body)
			fn := h.approveFn
			h.mu.Unlock()
			if fn != nil {
				return fn()
			}
			return nil
		},
		Checkout: func(_ context.Context, repo string, n int, base, sha string) (workspace.Workspace, func(), error) {
			return workspace.Workspace{Dir: "/wt", ContextDir: "/ctx", DiffPath: "/ctx/diff.patch", CommitsPath: "/ctx/commits.txt"}, func() {}, nil
		},
		NewReviewer: func(string, config.Agent) (agent.Reviewer, error) { return h.rev, nil },
		HasReview: func(_ context.Context, repo string, n int, marker string) (bool, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.onGH, h.hasRevErr
		},
		Now: func() time.Time {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.now.IsZero() {
				return time.Now()
			}
			return h.now
		},
		PostReview: func(_ context.Context, repo string, n int, p github.ReviewPost) (github.PostResult, error) {
			h.mu.Lock()
			h.posts = append(h.posts, p)
			fn := h.postFn
			h.mu.Unlock()
			if fn != nil {
				return fn(p)
			}
			return github.PostResult{}, nil
		},
	})
	return h
}

// start runs the engine until the test ends. Cleanup waits for Run to
// return, so nothing writes to the store while it closes and its temp
// directory is removed (cleanups run last-in, first-out).
func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	done := make(chan struct{})
	h.t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			h.t.Error("engine did not stop within 5s of the test ending")
		}
	})
	go func() { h.e.Run(ctx); close(done) }()
}

func (h *harness) poll(view string, prs ...github.PR) {
	h.mu.Lock()
	for _, p := range prs {
		h.heads[p.Number] = p.HeadSHA
	}
	h.mu.Unlock()
	h.e.HandlePoll(poller.Result{View: view, PRs: prs, At: time.Now()})
}

func (h *harness) postCount() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.posts) }

func (h *harness) approveCount() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.approvals) }

func (h *harness) approval(n int) string {
	r, _ := h.st.LatestReview("o/r", n)
	if r == nil {
		return ""
	}
	return r.Approval
}

func ghPR(n int, sha string) github.PR {
	return github.PR{Repo: "o/r", Number: n, State: "OPEN", Title: "T", HeadSHA: sha, BaseRef: "main"}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waitAudit waits for an audit event kind; state is saved before its audit row.
func (h *harness) waitAudit(t *testing.T, n int, kind string) {
	t.Helper()
	waitFor(t, "audit "+kind, func() bool { return strings.Contains(kinds(h.e.PRState("o/r", n).Audit), kind) })
}

func (h *harness) state(n int) string {
	r, _ := h.st.LatestReview("o/r", n)
	if r == nil {
		return ""
	}
	return r.State
}

func kinds(evs []store.AuditEvent) string {
	var ks []string
	for _, e := range evs {
		ks = append(ks, e.Kind)
	}
	return strings.Join(ks, ",")
}

func TestPollQueuesAndReviewsToReady(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")

	st := h.e.PRState("o/r", 1)
	if got := kinds(st.Audit); got != "queued,started,reviewed,ready to post" {
		t.Errorf("audit = %s", got)
	}
	if st.Latest.Verdict != "request_changes" || st.Latest.CostUSD != 0.4 || len(st.Comments) != 1 || st.Comments[0].Path != "a.go" {
		t.Errorf("latest = %+v comments %+v", st.Latest, st.Comments)
	}
	req := h.rev.calls[0]
	if req.Dir != "/wt" || req.ContextDir != "/ctx" || req.MaxCostUSD != 1.5 || !strings.HasPrefix(req.Prompt, "Review o/r#1") || !strings.Contains(req.Prompt, "/ctx/diff.patch") {
		t.Errorf("request = %+v", req)
	}
	if h.postCount() != 0 {
		t.Error("confirm mode must not post")
	}
	if snap := h.e.Snapshot(); snap.SpendTodayUSD != 0.4 || snap.Running != 0 || snap.Queued != 0 || snap.MaxDailyUSD != 10 {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestSameHeadNotReviewedTwice(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "first review", func() bool { return h.state(1) == store.Ready })
	h.poll("Team", ghPR(1, "sha1"))
	time.Sleep(50 * time.Millisecond)
	if h.rev.count() != 1 {
		t.Fatalf("same head reviewed %d times", h.rev.count())
	}
	h.poll("Team", ghPR(1, "sha2"))
	waitFor(t, "second review", func() bool { return h.rev.count() == 2 })
}

func TestDraftsAndNonAutoViewsSkipped(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	draft := ghPR(1, "d")
	draft.IsDraft = true
	h.poll("Team", draft)
	h.poll("Watch", ghPR(2, "w"))
	h.poll("Team", ghPR(3, "ok"))
	waitFor(t, "PR 3 reviewed", func() bool { return h.state(3) == store.Ready })
	if h.state(1) != "" || h.state(2) != "" {
		t.Errorf("draft/non-auto reviewed: %q %q", h.state(1), h.state(2))
	}
}

func TestBudgetGuardStopsNewReviews(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.MaxDailyCostUSD = 2; c.MaxCostPerReviewUSD = 1.5 })
	h.rev.res.CostUSD = 1.0
	h.start()
	h.poll("Team", ghPR(1, "a"), ghPR(2, "b"), ghPR(3, "c"))
	waitFor(t, "one review done", func() bool { return h.e.Snapshot().SpendTodayUSD == 1.0 })
	time.Sleep(50 * time.Millisecond)
	snap := h.e.Snapshot()
	if h.rev.count() != 1 || !snap.BudgetPaused || snap.Queued != 2 {
		t.Fatalf("reviews %d snapshot %+v", h.rev.count(), snap)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.MaxConcurrentReviews = 2 })
	h.rev.release = make(chan struct{})
	h.start()
	h.poll("Team", ghPR(1, "a"), ghPR(2, "b"), ghPR(3, "c"))
	waitFor(t, "two running", func() bool { return h.e.Snapshot().Running == 2 })
	time.Sleep(50 * time.Millisecond)
	if snap := h.e.Snapshot(); snap.Running != 2 || snap.Queued != 1 || h.rev.count() != 2 {
		t.Fatalf("snapshot %+v calls %d", snap, h.rev.count())
	}
	close(h.rev.release)
	waitFor(t, "all done", func() bool { return h.state(3) == store.Ready })
}

func TestFailedReviewRecordsCostAndIsNotRetried(t *testing.T) {
	h := newHarness(t, nil)
	h.rev.err = errors.New("claude error_max_budget_usd: Reached maximum budget")
	h.rev.res = agent.ReviewResult{CostUSD: 1.51}
	h.start()
	h.poll("Team", ghPR(1, "a"))
	h.waitAudit(t, 1, "failed")
	st := h.e.PRState("o/r", 1)
	if st.Latest.CostUSD != 1.51 || !strings.Contains(st.Audit[len(st.Audit)-1].Message, "$1.51") {
		t.Errorf("latest %+v audit %+v", st.Latest, st.Audit)
	}
	h.poll("Team", ghPR(1, "a"))
	time.Sleep(50 * time.Millisecond)
	if h.rev.count() != 1 {
		t.Errorf("failed review retried automatically (%d calls)", h.rev.count())
	}
}

func TestSecretInReviewBlocksPosting(t *testing.T) {
	h := newHarness(t, nil)
	h.rev.res.Summary = "Found token ghp_" + strings.Repeat("a", 36)
	h.start()
	h.poll("Team", ghPR(1, "a"))
	h.waitAudit(t, 1, "blocked")
	st := h.e.PRState("o/r", 1)
	if !strings.Contains(st.Latest.Error, "GitHub token") || !strings.Contains(kinds(st.Audit), "blocked") {
		t.Errorf("latest %+v audit %s", st.Latest, kinds(st.Audit))
	}
}

func TestSupersededHead(t *testing.T) {
	h := newHarness(t, nil)
	h.rev.release = make(chan struct{}) // never used: review must not start
	h.start()
	h.mu.Lock()
	h.heads[1] = "newer"
	h.mu.Unlock()
	h.e.HandlePoll(poller.Result{View: "Team", PRs: []github.PR{ghPR(1, "older")}, At: time.Now()})
	waitFor(t, "failed", func() bool { return h.state(1) == store.Failed })
	if r, _ := h.st.LatestReview("o/r", 1); !strings.Contains(r.Error, "superseded") || h.rev.count() != 0 {
		t.Errorf("review %+v calls %d", r, h.rev.count())
	}
}

func TestInterruptedReviewIsChargedNotRequeued(t *testing.T) {
	h := newHarness(t, nil)
	prID, _ := h.st.UpsertPR(ghPR(1, "a"))
	h.st.EnqueueReview(prID, "a", "claude", false)
	h.st.ClaimNext() // left "running" by a crashed process
	h.heads[1] = "a"
	h.start()
	h.waitAudit(t, 1, "interrupted")
	r, _ := h.st.LatestReview("o/r", 1)
	if r.State != store.Failed || r.CostUSD != 1.5 || h.rev.count() != 0 {
		t.Fatalf("review %+v calls %d", r, h.rev.count())
	}
	if h.e.Snapshot().SpendTodayUSD != 1.5 {
		t.Error("interrupted run must count toward today's spend")
	}
}

func TestShutdownMidReviewChargesCap(t *testing.T) {
	h := newHarness(t, nil)
	h.rev.release = make(chan struct{})
	h.start()
	h.poll("Team", ghPR(1, "a"))
	waitFor(t, "running", func() bool { return h.rev.count() == 1 })
	h.cancel()
	waitFor(t, "failed", func() bool { return h.state(1) == store.Failed })
	r, _ := h.st.LatestReview("o/r", 1)
	if r.CostUSD != 1.5 || !strings.Contains(r.Error, "interrupted") {
		t.Errorf("review = %+v", r)
	}
}

func TestPauseStopsNewReviews(t *testing.T) {
	h := newHarness(t, nil)
	if !h.e.TogglePause() {
		t.Fatal("TogglePause should report paused")
	}
	h.start()
	h.poll("Team", ghPR(1, "a"))
	waitFor(t, "queued", func() bool { return h.e.Snapshot().Queued == 1 })
	time.Sleep(50 * time.Millisecond)
	if h.rev.count() != 0 || !h.e.Snapshot().Paused {
		t.Fatalf("paused engine ran a review")
	}
	h.e.TogglePause()
	waitFor(t, "resumed review", func() bool { return h.state(1) == store.Ready })
}

func TestChangesNotifies(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "a"))
	select {
	case <-h.e.Changes():
	case <-time.After(2 * time.Second):
		t.Fatal("no change notification")
	}
}

func TestBudgetFlagIsLive(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.MaxDailyCostUSD = 2; c.MaxCostPerReviewUSD = 1.5 })
	h.rev.res.CostUSD = 1.0
	h.start()
	h.poll("Team", ghPR(1, "a"), ghPR(2, "b"))
	waitFor(t, "budget reached", func() bool { return h.e.Snapshot().BudgetPaused })
	// The queued review leaves the queue some other way (e.g. a newer process).
	r, _ := h.st.LatestReview("o/r", 2)
	r.State = store.Failed
	h.st.SaveReview(r)
	if h.e.Snapshot().BudgetPaused {
		t.Error("budget flag must clear when nothing is queued")
	}
}

func TestRunWaitsForWorkers(t *testing.T) {
	h := newHarness(t, nil)
	h.rev.release = make(chan struct{}) // blocks until ctx is cancelled
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.e.Run(ctx); close(done) }()
	h.poll("Team", ghPR(1, "a"))
	waitFor(t, "running", func() bool { return h.rev.count() == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
	if h.state(1) != store.Failed {
		t.Errorf("Run returned before the worker recorded the interruption (state %q)", h.state(1))
	}
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.now.IsZero() {
		h.now = time.Now()
	}
	h.now = h.now.Add(d)
}

func TestEngineSetViews(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	h.e.SetViews([]config.View{{Name: "Fresh", Query: "q", AutoReview: true}})
	h.poll("Team", ghPR(1, "a")) // Team no longer exists
	h.poll("Fresh", ghPR(2, "b"))
	waitFor(t, "PR 2 reviewed", func() bool { return h.state(2) == store.Ready })
	if h.state(1) != "" {
		t.Error("a removed view must not auto-review")
	}
}

func TestResultFromOldQueryIgnored(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	h.e.SetViews([]config.View{{Name: "Team", Query: "narrow", AutoReview: true}})
	h.heads[1] = "a"
	h.e.HandlePoll(poller.Result{View: "Team", Query: "q", PRs: []github.PR{ghPR(1, "a")}, At: time.Now()}) // in flight from before the edit
	h.heads[2] = "b"
	h.e.HandlePoll(poller.Result{View: "Team", Query: "narrow", PRs: []github.PR{ghPR(2, "b")}, At: time.Now()})
	waitFor(t, "PR 2 reviewed", func() bool { return h.state(2) == store.Ready })
	if h.state(1) != "" {
		t.Error("a result from the view's old query must not queue paid reviews")
	}
}

func labeledPR(n int, sha string, labels ...string) github.PR {
	p := ghPR(n, sha)
	p.Labels = labels
	return p
}

func (h *harness) labelCallCount() int { h.mu.Lock(); defer h.mu.Unlock(); return h.labelCalls }
func (h *harness) getPRCallCount(n int) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.getPRCalls[n]
}
func (h *harness) setLabels(n int, labels ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.labels[n] = labels
}
func (h *harness) labelsOf(n int) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.labels[n]...)
}
func (h *harness) auditCount(n int, kind string) int {
	c := 0
	for _, ev := range h.e.PRState("o/r", n).Audit {
		if ev.Kind == kind {
			c++
		}
	}
	return c
}
