package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/poller"
	"github.com/AdamIsrael/ghost/internal/store"
)

// approvable: auto-approve on, agent approves with only a nit.
func approvable(mutate func(*config.Config)) func(*config.Config) {
	return func(c *config.Config) {
		c.AutoApprove = config.AutoApprove{Enabled: true, RequireConfirm: true, RequireCIGreen: true,
			NoBlockingComments: true, NoChangesRequested: true, MaxChangedLines: 400}
		if mutate != nil {
			mutate(c)
		}
	}
}

func approvingHarness(t *testing.T, mutate func(*config.Config)) *harness {
	h := newHarness(t, approvable(mutate))
	h.rev.res = agent.ReviewResult{Verdict: agent.Approve, Summary: "Looks good.", CostUSD: 0.1,
		Comments: []agent.InlineComment{{Path: "a.go", Line: 1, Body: "typo", Severity: "nit"}}}
	return h
}

// postedReview runs PR 1 through review and posting.
func (h *harness) postedReview(t *testing.T) {
	t.Helper()
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	if err := h.e.PostPending(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalPendingThenApprove(t *testing.T) {
	h := approvingHarness(t, nil)
	h.postedReview(t)
	if h.approval(1) != store.ApprovalPending || h.approveCount() != 0 {
		t.Fatalf("approval %q calls %d", h.approval(1), h.approveCount())
	}
	if err := h.e.Approve(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
	if h.approval(1) != store.ApprovalApproved || h.approveCount() != 1 {
		t.Fatalf("approval %q calls %d", h.approval(1), h.approveCount())
	}
	if body := h.approvals[0]; !strings.Contains(body, "Approved by Ghost") || !strings.Contains(body, "<!-- ghost-approval-id:") {
		t.Errorf("body = %q", body)
	}
	if got := kinds(h.e.PRState("o/r", 1).Audit); !strings.HasSuffix(got, "approval pending,approved") {
		t.Errorf("audit = %s", got)
	}
}

func TestAutoApproveWithoutConfirm(t *testing.T) {
	h := approvingHarness(t, func(c *config.Config) { c.PostMode = "auto"; c.AutoApprove.RequireConfirm = false })
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "approved", func() bool { return h.approval(1) == store.ApprovalApproved })
	if h.postCount() != 1 || h.approveCount() != 1 {
		t.Fatalf("posts %d approvals %d", h.postCount(), h.approveCount())
	}
}

func TestApprovalWaitsForCIThenApprovesOnPoll(t *testing.T) {
	h := approvingHarness(t, func(c *config.Config) { c.AutoApprove.RequireConfirm = false })
	h.ci[1] = "PENDING"
	h.postedReview(t)
	if h.approval(1) != store.ApprovalWaiting {
		t.Fatalf("approval = %q", h.approval(1))
	}
	if r, _ := h.st.LatestReview("o/r", 1); !strings.Contains(r.ApprovalNote, "CI is pending") {
		t.Errorf("note = %q", r.ApprovalNote)
	}
	h.mu.Lock()
	h.ci[1] = "SUCCESS"
	h.mu.Unlock()
	green := ghPR(1, "sha1")
	green.CIState = "SUCCESS"
	h.e.HandlePoll(poller.Result{View: "Watch", PRs: []github.PR{green}, At: time.Now()})
	waitFor(t, "approved after CI", func() bool { return h.approval(1) == store.ApprovalApproved })
}

func TestNotEligibleWithBlockingComment(t *testing.T) {
	h := approvingHarness(t, nil)
	h.rev.res.Comments[0].Severity = "blocking"
	h.postedReview(t)
	r, _ := h.st.LatestReview("o/r", 1)
	if r.Approval != store.ApprovalNo || !strings.Contains(r.ApprovalNote, "blocking") || h.approveCount() != 0 {
		t.Fatalf("review %+v approvals %d", r, h.approveCount())
	}
	if err := h.e.Approve(context.Background(), "o/r", 1); err == nil {
		t.Error("ineligible PR must not be approvable with a")
	}
}

func TestRequestChangesVerdictNeverApproved(t *testing.T) {
	h := approvingHarness(t, func(c *config.Config) { c.AutoApprove.RequireConfirm = false })
	h.rev.res.Verdict = agent.RequestChanges
	h.postedReview(t)
	if h.approval(1) != store.ApprovalNo || h.approveCount() != 0 {
		t.Fatalf("approval %q calls %d", h.approval(1), h.approveCount())
	}
}

func TestApproveRechecksAtApproveTime(t *testing.T) {
	h := approvingHarness(t, nil)
	h.postedReview(t)
	h.mu.Lock()
	h.ci[1] = "FAILURE"
	h.mu.Unlock()
	if err := h.e.Approve(context.Background(), "o/r", 1); err == nil {
		t.Fatal("want error: CI turned red")
	}
	if h.approval(1) != store.ApprovalWaiting || h.approveCount() != 0 {
		t.Fatalf("approval %q calls %d", h.approval(1), h.approveCount())
	}
}

func TestApproveSupersededHead(t *testing.T) {
	h := approvingHarness(t, nil)
	h.postedReview(t)
	h.mu.Lock()
	h.heads[1] = "sha2"
	h.mu.Unlock()
	if err := h.e.Approve(context.Background(), "o/r", 1); err == nil {
		t.Fatal("want error: head moved")
	}
	if h.approval(1) != store.ApprovalNo || h.approveCount() != 0 {
		t.Fatalf("approval %q calls %d", h.approval(1), h.approveCount())
	}
}

func TestApproveTwiceConcurrentlyApprovesOnce(t *testing.T) {
	h := approvingHarness(t, nil)
	release := make(chan struct{})
	h.approveFn = func() error { <-release; return nil }
	h.postedReview(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = h.e.Approve(context.Background(), "o/r", 1) }(i)
	}
	waitFor(t, "one approval in flight", func() bool { return h.approveCount() == 1 })
	close(release)
	wg.Wait()
	if h.approveCount() != 1 || (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("approvals %d errs %v", h.approveCount(), errs)
	}
}

func TestApproveOwnPRFailsCleanly(t *testing.T) {
	h := approvingHarness(t, nil)
	h.approveFn = func() error {
		return &api.HTTPError{StatusCode: 422, Message: "Can not approve your own pull request"}
	}
	h.postedReview(t)
	if err := h.e.Approve(context.Background(), "o/r", 1); err == nil {
		t.Fatal("want error")
	}
	r, _ := h.st.LatestReview("o/r", 1)
	if r.Approval != store.ApprovalFailed || !strings.Contains(r.ApprovalNote, "own pull request") {
		t.Fatalf("review = %+v", r)
	}
	if h.postCount() != 1 {
		t.Error("a failed approval must not post anything else")
	}
}

func TestUncertainApprovalChecksGitHub(t *testing.T) {
	h := approvingHarness(t, nil)
	h.approveFn = func() error { return errors.New("context deadline exceeded") }
	h.postedReview(t)
	h.e.Approve(context.Background(), "o/r", 1)
	if h.approval(1) != store.ApprovalUnknown {
		t.Fatalf("approval = %q", h.approval(1))
	}
	h.mu.Lock()
	h.onGH, h.approveFn = true, nil
	h.mu.Unlock()
	if err := h.e.Approve(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
	if h.approval(1) != store.ApprovalApproved || h.approveCount() != 1 {
		t.Fatalf("approval %q calls %d", h.approval(1), h.approveCount())
	}
}

func TestApprovalDisabledDoesNothing(t *testing.T) {
	h := newHarness(t, nil) // auto_approve disabled by default
	h.rev.res.Verdict = agent.Approve
	h.rev.res.Comments = nil
	h.postedReview(t)
	if h.approval(1) != "" || strings.Contains(kinds(h.e.PRState("o/r", 1).Audit), "approv") {
		t.Fatalf("approval %q audit %s", h.approval(1), kinds(h.e.PRState("o/r", 1).Audit))
	}
}

func TestRereviewSupersedesOlderApproval(t *testing.T) {
	h := approvingHarness(t, func(c *config.Config) { c.AutoApprove.RequireConfirm = false })
	h.ci[1] = "PENDING"
	h.postedReview(t) // review #1: approve, waiting on CI
	if h.approval(1) != store.ApprovalWaiting {
		t.Fatalf("approval = %q", h.approval(1))
	}
	h.rev.mu.Lock()
	h.rev.res.Verdict = agent.RequestChanges
	h.rev.mu.Unlock()
	if err := h.e.Rereview(ghPR(1, "sha1")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second review", func() bool { return h.rev.count() == 2 })
	h.mu.Lock()
	h.ci[1] = "SUCCESS"
	h.mu.Unlock()
	green := ghPR(1, "sha1")
	green.CIState = "SUCCESS"
	h.e.HandlePoll(poller.Result{View: "Watch", PRs: []github.PR{green}, At: time.Now()})
	time.Sleep(100 * time.Millisecond)
	if h.approveCount() != 0 {
		t.Fatal("an older review approved the PR after a newer review replaced it")
	}
	found := false
	for _, ev := range h.e.PRState("o/r", 1).Audit {
		found = found || strings.Contains(ev.Message, "superseded")
	}
	if !found {
		t.Error("audit should say the older approval was superseded")
	}
}

func TestSameCommitNotApprovedTwice(t *testing.T) {
	h := approvingHarness(t, nil)
	h.postedReview(t)
	if err := h.e.Approve(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Rereview(ghPR(1, "sha1")); err != nil {
		t.Fatal(err)
	}
	h.waitAudit(t, 1, "ready to post")
	waitFor(t, "second review ready", func() bool { return h.state(1) == store.Ready })
	if err := h.e.PostPending(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
	if h.approval(1) == store.ApprovalPending {
		t.Fatal("a commit Ghost already approved must not become pending again")
	}
	if err := h.e.Approve(context.Background(), "o/r", 1); err == nil || h.approveCount() != 1 {
		t.Fatalf("err %v approvals %d", err, h.approveCount())
	}
}

func TestUnreadableCommentsAreNotEligible(t *testing.T) {
	h := approvingHarness(t, nil)
	d := h.e.decide(&store.Review{Verdict: "approve", Comments: "{not json"}, github.PR{CIState: "SUCCESS"})
	if d.Eligible || d.Waiting {
		t.Fatalf("unreadable comments must block approval: %+v", d)
	}
}

func greenPoll(h *harness, n int) {
	pr := ghPR(n, "sha1")
	pr.CIState = "SUCCESS"
	h.e.HandlePoll(poller.Result{View: "Watch", PRs: []github.PR{pr}, At: time.Now()})
}

func TestAutoModeRetriesUnknownApprovalOnPoll(t *testing.T) {
	h := approvingHarness(t, func(c *config.Config) { c.AutoApprove.RequireConfirm = false })
	h.approveFn = func() error { return errors.New("connection reset") }
	h.postedReview(t)
	waitFor(t, "unknown", func() bool { return h.approval(1) == store.ApprovalUnknown })
	h.mu.Lock()
	h.approveFn = nil
	h.mu.Unlock()
	greenPoll(h, 1) // too soon: GitHub may not show an in-flight review yet
	time.Sleep(100 * time.Millisecond)
	if h.approveCount() != 1 {
		t.Fatal("unknown approval retried before a poll interval passed")
	}
	h.advance(6 * time.Minute)
	greenPoll(h, 1)
	waitFor(t, "approved on poll", func() bool { return h.approval(1) == store.ApprovalApproved })
	if h.approveCount() != 2 {
		t.Errorf("approvals = %d (marker not on GitHub, so one retry)", h.approveCount())
	}
}

func TestAutoModeGetPRFailureReleasesToWaiting(t *testing.T) {
	h := approvingHarness(t, func(c *config.Config) { c.AutoApprove.RequireConfirm = false })
	h.ci[1] = "PENDING"
	h.postedReview(t)
	h.mu.Lock()
	h.getPRErr = errors.New("HTTP 502")
	h.mu.Unlock()
	greenPoll(h, 1) // eligible on poll data, but approve() can't read the PR
	waitFor(t, "approve attempted", func() bool {
		r, _ := h.st.LatestReview("o/r", 1)
		return strings.Contains(r.ApprovalNote, "502")
	})
	if h.approval(1) != store.ApprovalWaiting {
		t.Fatalf("auto mode must fall back to waiting, got %q", h.approval(1))
	}
	h.mu.Lock()
	h.getPRErr, h.ci[1] = nil, "SUCCESS"
	h.mu.Unlock()
	greenPoll(h, 1)
	waitFor(t, "approved after recovery", func() bool { return h.approval(1) == store.ApprovalApproved })
}

func TestUnknownNotRetriedWithoutHasReview(t *testing.T) {
	h := approvingHarness(t, nil)
	h.approveFn = func() error { return errors.New("connection reset") }
	h.postedReview(t)
	h.e.Approve(context.Background(), "o/r", 1)
	h.e.d.HasReview = nil
	h.mu.Lock()
	h.approveFn = nil
	h.mu.Unlock()
	if err := h.e.Approve(context.Background(), "o/r", 1); err == nil || h.approveCount() != 1 {
		t.Fatalf("err %v approvals %d: an unverifiable retry must not post", err, h.approveCount())
	}
}

func TestRecheckDoesNotBlockPolls(t *testing.T) {
	h := approvingHarness(t, func(c *config.Config) { c.AutoApprove.RequireConfirm = false })
	release := make(chan struct{})
	defer close(release)
	h.ci[1] = "PENDING"
	h.postedReview(t)
	h.mu.Lock()
	h.ci[1] = "SUCCESS"
	h.approveFn = func() error { <-release; return nil }
	h.mu.Unlock()
	greenPoll(h, 1)
	waitFor(t, "approval in flight", func() bool { return h.approveCount() == 1 })
	h.poll("Team", ghPR(2, "sha2")) // must be handled while the approval is stuck
	waitFor(t, "PR 2 queued while approving", func() bool { return h.state(2) != "" })
}

func TestUnknownCheckFailureDoesNotRepeatAudit(t *testing.T) {
	h := approvingHarness(t, nil)
	h.approveFn = func() error { return errors.New("connection reset") }
	h.postedReview(t)
	h.e.Approve(context.Background(), "o/r", 1) // → unknown
	h.mu.Lock()
	h.hasRevErr = errors.New("HTTP 503")
	h.mu.Unlock()
	h.e.Approve(context.Background(), "o/r", 1)
	n := len(h.e.PRState("o/r", 1).Audit)
	h.e.Approve(context.Background(), "o/r", 1)
	if got := len(h.e.PRState("o/r", 1).Audit); got != n {
		t.Errorf("same failure audited again: %d → %d events", n, got)
	}
	if h.approval(1) != store.ApprovalUnknown {
		t.Errorf("approval = %q, want unknown (claim released)", h.approval(1))
	}
}

func TestStaleApprovalWriteIsDropped(t *testing.T) {
	h := approvingHarness(t, nil)
	h.ci[1] = "PENDING"
	h.postedReview(t)
	stale, _ := h.st.LatestReview("o/r", 1) // waiting
	cur, _ := h.st.LatestReview("o/r", 1)
	h.e.setApproval(cur, store.ApprovalWaiting, store.ApprovalNo, "superseded by review #9")
	n := len(h.e.PRState("o/r", 1).Audit)
	h.e.setApproval(stale, store.ApprovalWaiting, store.ApprovalPending, "meets the rules")
	if h.approval(1) != store.ApprovalNo || len(h.e.PRState("o/r", 1).Audit) != n {
		t.Errorf("a stale writer overwrote a newer approval state: %q", h.approval(1))
	}
}

func TestPostFromTUIIsTrackedByEngineShutdown(t *testing.T) {
	h := newHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { h.e.Run(ctx); close(runDone) }()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	started := make(chan struct{})
	h.e.d.PostReview = func(pctx context.Context, repo string, n int, p github.ReviewPost) (github.PostResult, error) {
		close(started)
		<-pctx.Done() // only returns when the engine is shutting down
		time.Sleep(20 * time.Millisecond)
		return github.PostResult{}, pctx.Err()
	}
	postDone := make(chan struct{})
	go func() { h.e.PostPending(context.Background(), "o/r", 1); close(postDone) }()
	<-started
	cancel()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
	select {
	case <-postDone:
	default:
		t.Fatal("Run returned while a TUI-started post was still in flight")
	}
	if h.state(1) != store.PostUnknown {
		t.Errorf("interrupted post should be post-unknown, got %q", h.state(1))
	}
}
