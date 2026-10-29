package engine

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/store"
)

func TestPostPending(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "ready", func() bool { return h.state(1) == store.Ready })
	if err := h.e.PostPending(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
	if h.state(1) != store.Posted || h.postCount() != 1 {
		t.Fatalf("state %s posts %d", h.state(1), h.postCount())
	}
	p := h.postAt(0)
	if p.CommitID != "sha1" || p.Event != "REQUEST_CHANGES" || !strings.HasPrefix(p.Body, "Needs a nil check.") || p.Body != "Needs a nil check.\n"+marker(1) {
		t.Errorf("post = %+v", p)
	}
	if len(p.Comments) != 1 || p.Comments[0].Path != "a.go" || p.Comments[0].Line != 3 || p.Comments[0].Body != "**Blocking:** nil deref" {
		t.Errorf("comments = %+v", p.Comments)
	}
	if got := kinds(h.e.PRState("o/r", 1).Audit); !strings.HasSuffix(got, ",posted") {
		t.Errorf("audit = %s", got)
	}
}

func TestPostPendingRequiresReadyReview(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.e.PostPending(context.Background(), "o/r", 1); err == nil {
		t.Error("want error when nothing is pending")
	}
}

func TestPostFailureKeepsReady(t *testing.T) {
	h := newHarness(t, nil)
	h.postFn = func(github.ReviewPost) (github.PostResult, error) {
		return github.PostResult{}, &api.HTTPError{StatusCode: 422, Message: "Validation Failed"}
	}
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "ready", func() bool { return h.state(1) == store.Ready })
	if err := h.e.PostPending(context.Background(), "o/r", 1); err == nil {
		t.Fatal("want post error")
	}
	if h.state(1) != store.Ready || !strings.Contains(kinds(h.e.PRState("o/r", 1).Audit), "post failed") {
		t.Fatalf("state %s", h.state(1))
	}
	h.mu.Lock()
	h.postFn = nil
	h.mu.Unlock()
	if err := h.e.PostPending(context.Background(), "o/r", 1); err != nil || h.state(1) != store.Posted {
		t.Fatalf("retry: %v state %s", err, h.state(1))
	}
}

func TestPostAuditMentionsFallbacks(t *testing.T) {
	h := newHarness(t, nil)
	h.postFn = func(github.ReviewPost) (github.PostResult, error) {
		return github.PostResult{Folded: true, Downgraded: true}, nil
	}
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "ready", func() bool { return h.state(1) == store.Ready })
	h.e.PostPending(context.Background(), "o/r", 1)
	evs := h.e.PRState("o/r", 1).Audit
	msg := evs[len(evs)-1].Message
	if !strings.Contains(msg, "folded") || !strings.Contains(msg, "as a comment") {
		t.Errorf("posted audit = %q", msg)
	}
}

func TestAutoModePostsImmediately(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.PostMode = "auto" })
	h.rev.res.Verdict = "approve"
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "posted", func() bool { return h.state(1) == store.Posted })
	if h.postAt(0).Event != "COMMENT" {
		t.Errorf("approve verdict must post as COMMENT until M5, got %s", h.postAt(0).Event)
	}
}

func TestAutoModeNeverPostsBlocked(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.PostMode = "auto" })
	h.rev.res.Comments[0].Body = "key AKIA" + strings.Repeat("Q", 16)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "blocked", func() bool { return h.state(1) == store.Blocked })
	if h.postCount() != 0 {
		t.Fatal("blocked review was posted")
	}
	if err := h.e.PostPending(context.Background(), "o/r", 1); err == nil || h.postCount() != 0 {
		t.Fatal("blocked review must not be postable")
	}
}

func TestRereview(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "ready", func() bool { return h.state(1) == store.Ready })
	h.rev.release = make(chan struct{})
	if err := h.e.Rereview(ghPR(1, "sha1")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second review running", func() bool { return h.rev.count() == 2 })
	if err := h.e.Rereview(ghPR(1, "sha1")); err == nil {
		t.Error("re-review while one is running should fail")
	}
	close(h.rev.release)
	waitFor(t, "second review ready", func() bool { return h.state(1) == store.Ready && h.rev.count() == 2 })
	found := false
	for _, ev := range h.e.PRState("o/r", 1).Audit {
		found = found || strings.Contains(ev.Message, "re-review requested")
	}
	if !found {
		t.Error("audit missing re-review")
	}
}

func TestPostPendingTwiceConcurrentlyPostsOnce(t *testing.T) {
	h := newHarness(t, nil)
	release := make(chan struct{})
	h.postFn = func(github.ReviewPost) (github.PostResult, error) { <-release; return github.PostResult{}, nil }
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = h.e.PostPending(context.Background(), "o/r", 1) }(i)
	}
	waitFor(t, "one post in flight", func() bool { return h.postCount() == 1 })
	close(release)
	wg.Wait()
	if h.postCount() != 1 || (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("posts %d errs %v", h.postCount(), errs)
	}
	if h.state(1) != store.Posted {
		t.Errorf("state = %s", h.state(1))
	}
}

func TestUncertainPostChecksGitHubBeforeRetry(t *testing.T) {
	h := newHarness(t, nil)
	h.postFn = func(github.ReviewPost) (github.PostResult, error) {
		return github.PostResult{}, errors.New("context deadline exceeded")
	}
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	if err := h.e.PostPending(context.Background(), "o/r", 1); err == nil {
		t.Fatal("want error")
	}
	if h.state(1) != store.PostUnknown {
		t.Fatalf("transport error should leave the post unknown, got %s", h.state(1))
	}
	h.mu.Lock()
	h.onGH = true // it did reach GitHub
	h.postFn = nil
	h.mu.Unlock()
	if err := h.e.PostPending(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
	if h.state(1) != store.Posted || h.postCount() != 1 {
		t.Fatalf("must not re-post a review GitHub already has: state %s posts %d", h.state(1), h.postCount())
	}
}

func TestUncertainPostRetriesWhenNotOnGitHub(t *testing.T) {
	h := newHarness(t, nil)
	h.postFn = func(github.ReviewPost) (github.PostResult, error) {
		return github.PostResult{}, errors.New("connection reset")
	}
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	h.e.PostPending(context.Background(), "o/r", 1)
	h.mu.Lock()
	h.postFn = nil
	h.mu.Unlock()
	if err := h.e.PostPending(context.Background(), "o/r", 1); err != nil || h.state(1) != store.Posted || h.postCount() != 2 {
		t.Fatalf("err %v state %s posts %d", err, h.state(1), h.postCount())
	}
}

func TestRereviewTwiceConcurrentlyQueuesOnce(t *testing.T) {
	h := newHarness(t, nil)
	h.e.TogglePause() // nothing runs; reviews stay queued
	h.start()
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = h.e.Rereview(ghPR(1, "sha1")) }(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	if q, _ := h.st.CountState(store.Queued); ok != 1 || q != 1 {
		t.Fatalf("successful rereviews %d, queued %d; want 1 and 1", ok, q)
	}
}

func TestPostCustomFooter(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.ReviewFooter = "<sub>{{.Agent}} ({{.Model}}) · {{.Repo}}#{{.Number}} · {{.HeadSHA}}</sub>"
		c.Agents["claude"] = config.Agent{Kind: "claude", Model: "m1", Prompt: "x"}
	})
	h.heads[1] = "0123456789"
	h.start()
	h.poll("Team", ghPR(1, "0123456789"))
	waitFor(t, "ready", func() bool { return h.state(1) == store.Ready })
	if err := h.e.PostPending(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
	want := "Needs a nil check.\n\n<sub>claude (m1) · o/r#1 · 0123456</sub>\n" + marker(1)
	if got := h.postAt(0).Body; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

const aGoDiff = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,4 +1,5 @@
 package a
+// new
 func A() {
 }
 
`

func TestCommentsOutsideDiffAreListedInBody(t *testing.T) {
	h := newHarness(t, nil)
	h.diff = aGoDiff
	h.rev.res.Comments = []agent.InlineComment{
		{Path: "a.go", Line: 3, Body: "in the diff", Severity: "nit"},
		{Path: "a.go", Line: 50, Body: "outside it", Severity: "blocking"},
	}
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	if err := h.e.PostPending(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
	p := h.postAt(0)
	if len(p.Comments) != 1 || p.Comments[0].Line != 3 || len(p.Unattached) != 1 || p.Unattached[0].Line != 50 {
		t.Fatalf("inline %+v unattached %+v", p.Comments, p.Unattached)
	}
	evs := h.e.PRState("o/r", 1).Audit
	if msg := evs[len(evs)-1].Message; !strings.Contains(msg, "1 inline comments") || !strings.Contains(msg, "1 outside the diff") {
		t.Errorf("posted audit = %q", msg)
	}
}

func TestAgentCannotForceOutsideDiff(t *testing.T) {
	h := newHarness(t, nil)
	h.diff = aGoDiff
	h.rev.res.Comments = []agent.InlineComment{{Path: "a.go", Line: 3, Body: "x", Severity: "nit", OutsideDiff: true}}
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	h.e.PostPending(context.Background(), "o/r", 1)
	if p := h.postAt(0); len(p.Comments) != 1 || len(p.Unattached) != 0 {
		t.Errorf("Ghost's own diff check must decide: inline %d unattached %d", len(p.Comments), len(p.Unattached))
	}
}

func TestUnreadableDiffKeepsCommentsInline(t *testing.T) {
	h := newHarness(t, nil) // h.diff == "": the diff can't be read
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	h.e.PostPending(context.Background(), "o/r", 1)
	if p := h.postAt(0); len(p.Comments) != 1 || len(p.Unattached) != 0 {
		t.Errorf("without a diff, comments stay inline (GitHub decides): inline %d unattached %d", len(p.Comments), len(p.Unattached))
	}
}

func TestFoldReasonIsAudited(t *testing.T) {
	h := newHarness(t, nil)
	h.postFn = func(github.ReviewPost) (github.PostResult, error) {
		return github.PostResult{Folded: true, FoldReason: "Pull request review thread line must be part of the diff"}, nil
	}
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	h.e.PostPending(context.Background(), "o/r", 1)
	evs := h.e.PRState("o/r", 1).Audit
	if msg := evs[len(evs)-1].Message; !strings.Contains(msg, "GitHub: Pull request review thread line must be part of the diff") {
		t.Errorf("posted audit = %q", msg)
	}
}

func transientHarness(t *testing.T, fails int) *harness {
	h := newHarness(t, nil)
	h.e.postRetryDelays = []time.Duration{0, 0}
	n := 0
	h.postFn = func(github.ReviewPost) (github.PostResult, error) {
		n++
		if n <= fails {
			return github.PostResult{}, &api.HTTPError{StatusCode: 502, Message: "Bad Gateway"}
		}
		return github.PostResult{}, nil
	}
	return h
}

func TestTransientPostIsRetried(t *testing.T) {
	h := transientHarness(t, 1)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	if err := h.e.PostPending(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
	if h.state(1) != store.Posted || h.postCount() != 2 || h.hasRevCallCount() != 1 {
		t.Errorf("state %s posts %d marker checks %d", h.state(1), h.postCount(), h.hasRevCallCount())
	}
}

func TestTransientPostThatReachedGitHubIsNotReposted(t *testing.T) {
	h := transientHarness(t, 1)
	h.onGH = true // the 502 hid a review GitHub did create
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	if err := h.e.PostPending(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
	if h.state(1) != store.Posted || h.postCount() != 1 {
		t.Errorf("state %s posts %d; must not post twice", h.state(1), h.postCount())
	}
}

func TestTransientPostGivesUp(t *testing.T) {
	h := transientHarness(t, 100)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	if err := h.e.PostPending(context.Background(), "o/r", 1); err == nil {
		t.Fatal("want an error after the retries")
	}
	if h.state(1) != store.PostUnknown || h.postCount() != 3 {
		t.Errorf("state %s posts %d; want post-unknown after 3 attempts", h.state(1), h.postCount())
	}
}

func TestClientErrorIsNotRetried(t *testing.T) {
	h := newHarness(t, nil)
	h.e.postRetryDelays = []time.Duration{0, 0}
	h.postFn = func(github.ReviewPost) (github.PostResult, error) {
		return github.PostResult{}, &api.HTTPError{StatusCode: 422, Message: "Validation Failed"}
	}
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	h.e.PostPending(context.Background(), "o/r", 1)
	if h.postCount() != 1 || h.state(1) != store.Ready {
		t.Errorf("posts %d state %s", h.postCount(), h.state(1))
	}
}

func TestTransientPostFoundOnGitHubSaysSo(t *testing.T) {
	h := transientHarness(t, 1)
	h.onGH = true
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	h.e.PostPending(context.Background(), "o/r", 1)
	evs := h.e.PRState("o/r", 1).Audit
	if msg := evs[len(evs)-1].Message; !strings.Contains(msg, "found on GitHub after a failed response") {
		t.Errorf("posted audit = %q", msg)
	}
}

func TestDiffIsReadBeforeTheAgentRuns(t *testing.T) {
	h := newHarness(t, nil)
	h.diff = aGoDiff
	h.rev.res.Comments = []agent.InlineComment{{Path: "a.go", Line: 3, Body: "x", Severity: "nit"}}
	h.rev.during = func() { // the agent (somehow) empties the diff file
		h.mu.Lock()
		p := h.diffPath
		h.mu.Unlock()
		os.WriteFile(p, nil, 0o600)
	}
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	h.e.PostPending(context.Background(), "o/r", 1)
	if p := h.postAt(0); len(p.Comments) != 1 || len(p.Unattached) != 0 {
		t.Errorf("the diff read after the agent ran: inline %d unattached %d", len(p.Comments), len(p.Unattached))
	}
}

func TestReviewNowUsesTheGivenAgent(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Agents["sec"] = config.Agent{Kind: "claude", Prompt: "Review {{.Repo}}"}
	})
	h.start()
	if err := h.e.ReviewNow(ghPR(1, "sha1"), "sec"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	r, _ := h.st.LatestReview("o/r", 1)
	if r.Agent != "sec" {
		t.Errorf("agent = %q, want sec", r.Agent)
	}
	h.waitAudit(t, 1, "queued")
	if a := h.e.PRState("o/r", 1).Audit; !strings.Contains(a[0].Message, "review requested") {
		t.Errorf("queued audit = %q", a[0].Message)
	}
}

func TestReviewNowDefaultsToTheDefaultAgent(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	if err := h.e.ReviewNow(ghPR(1, "sha1"), ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	if r, _ := h.st.LatestReview("o/r", 1); r.Agent != "claude" {
		t.Errorf("agent = %q, want the default (claude)", r.Agent)
	}
}

func TestReviewNowRejectsUnknownAgent(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.e.ReviewNow(ghPR(1, "sha1"), "nope"); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("err = %v", err)
	}
}

// GitHub #9: a queued review says why it hasn't started.
func TestQueuedReviewSaysWhyItWaits(t *testing.T) {
	waiting := func(h *harness, n int) string { return h.e.PRState("o/r", n).Waiting }

	t.Run("paused", func(t *testing.T) {
		h := newHarness(t, nil)
		h.start()
		h.e.TogglePause()
		h.poll("Watch", ghPR(1, "sha1"))
		if err := h.e.Rereview(ghPR(1, "sha1")); err != nil {
			t.Fatal(err)
		}
		if w := waiting(h, 1); !strings.Contains(w, "paused") || !strings.Contains(w, "press p") {
			t.Errorf("waiting = %q, want the pause and how to resume", w)
		}
		h.e.TogglePause()
		waitFor(t, "ready", func() bool { return h.state(1) == store.Ready })
		if w := waiting(h, 1); w != "" {
			t.Errorf("finished review still waiting: %q", w)
		}
	})

	t.Run("budget", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) { c.MaxDailyCostUSD, c.MaxCostPerReviewUSD = 1, 1.5 })
		h.start()
		if err := h.e.Rereview(ghPR(1, "sha1")); err != nil {
			t.Fatal(err)
		}
		if w := waiting(h, 1); !strings.Contains(w, "budget") || !strings.Contains(w, "$1.00") {
			t.Errorf("waiting = %q, want the budget", w)
		}
	})

	t.Run("slots", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) { c.MaxConcurrentReviews = 1 })
		h.rev.release = make(chan struct{})
		h.start()
		h.poll("Watch", ghPR(1, "sha1"), ghPR(2, "sha2"))
		if err := h.e.Rereview(ghPR(1, "sha1")); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "first review running", func() bool { return h.rev.count() == 1 })
		if err := h.e.Rereview(ghPR(2, "sha2")); err != nil {
			t.Fatal(err)
		}
		if w := waiting(h, 2); !strings.Contains(w, "max_concurrent_reviews") {
			t.Errorf("waiting = %q, want the concurrency limit", w)
		}
		close(h.rev.release)
		waitFor(t, "both ready", func() bool { return h.state(1) == store.Ready && h.state(2) == store.Ready })
	})

	t.Run("free", func(t *testing.T) {
		h := newHarness(t, nil) // not started, so nothing claims it
		if err := h.e.Rereview(ghPR(1, "sha1")); err != nil {
			t.Fatal(err)
		}
		if w := waiting(h, 1); w != "" {
			t.Errorf("waiting = %q, want none when a worker can take it", w)
		}
	})
}
