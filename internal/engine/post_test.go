package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"

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
	p := h.posts[0]
	if p.CommitID != "sha1" || p.Event != "REQUEST_CHANGES" || !strings.HasPrefix(p.Body, "Needs a nil check.") || !strings.Contains(p.Body, "Automated review by Ghost") || !strings.Contains(p.Body, "<!-- ghost-review-id:") {
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
	if h.posts[0].Event != "COMMENT" {
		t.Errorf("approve verdict must post as COMMENT until M5, got %s", h.posts[0].Event)
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
