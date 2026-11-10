package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/AdamIsrael/ghost/internal/store"
)

// manualHarness has PR 7 at sha7, never reviewed.
func manualHarness(t *testing.T) *harness {
	h := newHarness(t, nil)
	h.heads[7] = "sha7"
	return h
}

func (h *harness) manualState(t *testing.T, n int, sha string) string {
	t.Helper()
	st, err := h.st.ManualApproval(prIDOf(t, h, n), sha)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestApproveByHandUnreviewedPR(t *testing.T) {
	h := manualHarness(t)
	h.mu.Lock()
	h.ci[7] = "FAILURE" // the operator decides; the auto-approve rules don't apply
	h.mu.Unlock()
	if err := h.e.ApproveByHand(context.Background(), "o/r", 7, "sha7"); err != nil {
		t.Fatal(err)
	}
	if h.approveCount() != 1 || h.approvals[0] != manualApprovalMarker("sha7") {
		t.Fatalf("approvals = %q", h.approvals)
	}
	if st := h.manualState(t, 7, "sha7"); st != store.ApprovalApproved {
		t.Errorf("state = %q", st)
	}
	audit := h.e.PRState("o/r", 7).Audit
	if kinds(audit) != "approved" || audit[0].Message != "by you at sha7" {
		t.Errorf("audit = %+v", audit)
	}
}

func TestApproveByHandStopsWhenHeadMoved(t *testing.T) {
	h := manualHarness(t)
	err := h.e.ApproveByHand(context.Background(), "o/r", 7, "old")
	if err == nil || !strings.Contains(err.Error(), "new commits") || h.approveCount() != 0 {
		t.Fatalf("err %v, approvals %d", err, h.approveCount())
	}
}

func TestApproveByHandOncePerCommit(t *testing.T) {
	h := manualHarness(t)
	if err := h.e.ApproveByHand(context.Background(), "o/r", 7, "sha7"); err != nil {
		t.Fatal(err)
	}
	err := h.e.ApproveByHand(context.Background(), "o/r", 7, "sha7")
	if err == nil || !strings.Contains(err.Error(), "already approved") || h.approveCount() != 1 {
		t.Fatalf("err %v, approvals %d", err, h.approveCount())
	}
	// A new commit can be approved.
	h.mu.Lock()
	h.heads[7] = "sha8"
	h.mu.Unlock()
	if err := h.e.ApproveByHand(context.Background(), "o/r", 7, "sha8"); err != nil || h.approveCount() != 2 {
		t.Fatalf("err %v, approvals %d", err, h.approveCount())
	}
}

func TestApproveByHandTwiceConcurrentlyApprovesOnce(t *testing.T) {
	h := manualHarness(t)
	release := make(chan struct{})
	h.approveFn = func() error { <-release; return nil }
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = h.e.ApproveByHand(context.Background(), "o/r", 7, "sha7") }(i)
	}
	waitFor(t, "one approval in flight", func() bool { return h.approveCount() == 1 })
	close(release)
	wg.Wait()
	if h.approveCount() != 1 || (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("approvals %d errs %v", h.approveCount(), errs)
	}
}

func TestApproveByHandRefusedCanRetry(t *testing.T) {
	h := manualHarness(t)
	h.approveFn = func() error {
		return &api.HTTPError{StatusCode: 422, Message: "Can not approve your own pull request"}
	}
	if err := h.e.ApproveByHand(context.Background(), "o/r", 7, "sha7"); err == nil {
		t.Fatal("want error")
	}
	if st := h.manualState(t, 7, "sha7"); st != store.ApprovalFailed {
		t.Fatalf("state = %q", st)
	}
	h.mu.Lock()
	h.approveFn = nil
	h.mu.Unlock()
	if err := h.e.ApproveByHand(context.Background(), "o/r", 7, "sha7"); err != nil {
		t.Fatal(err)
	}
	if st := h.manualState(t, 7, "sha7"); st != store.ApprovalApproved || h.approveCount() != 2 {
		t.Fatalf("state %q approvals %d", st, h.approveCount())
	}
}

func TestApproveByHandUnknownChecksGitHub(t *testing.T) {
	h := manualHarness(t)
	h.approveFn = func() error { return errors.New("context deadline exceeded") }
	h.e.ApproveByHand(context.Background(), "o/r", 7, "sha7")
	if st := h.manualState(t, 7, "sha7"); st != store.ApprovalUnknown {
		t.Fatalf("state = %q", st)
	}
	h.mu.Lock()
	h.onGH, h.approveFn = true, nil
	h.mu.Unlock()
	if err := h.e.ApproveByHand(context.Background(), "o/r", 7, "sha7"); err != nil {
		t.Fatal(err)
	}
	if st := h.manualState(t, 7, "sha7"); st != store.ApprovalApproved || h.approveCount() != 1 {
		t.Fatalf("state %q approvals %d", st, h.approveCount())
	}
}

// After a manual approval, a review of the same commit can't approve it again.
func TestApproveByHandBlocksAutoApproval(t *testing.T) {
	h := approvingHarness(t, nil)
	h.heads[1] = "sha1"
	h.postedReview(t)
	if h.approval(1) != store.ApprovalPending {
		t.Fatalf("approval = %q", h.approval(1))
	}
	if err := h.e.ApproveByHand(context.Background(), "o/r", 1, "sha1"); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Approve(context.Background(), "o/r", 1); err == nil || h.approveCount() != 1 {
		t.Fatalf("err %v approvals %d", err, h.approveCount())
	}
}

// A commit a review already got approved is not approved again by hand.
func TestApproveByHandAfterAutoApproval(t *testing.T) {
	h := approvingHarness(t, nil)
	h.heads[1] = "sha1"
	h.postedReview(t)
	if err := h.e.Approve(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
	err := h.e.ApproveByHand(context.Background(), "o/r", 1, "sha1")
	if err == nil || !strings.Contains(err.Error(), "already approved") || h.approveCount() != 1 {
		t.Fatalf("err %v approvals %d", err, h.approveCount())
	}
}
