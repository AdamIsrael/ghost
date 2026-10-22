package engine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/poller"
	"github.com/AdamIsrael/ghost/internal/store"
)

func labelHarness(t *testing.T, mutate func(*config.Config)) *harness {
	return newHarness(t, func(c *config.Config) {
		c.ReviewLabel = "ai-review"
		if mutate != nil {
			mutate(c)
		}
	})
}

func strPtr(s string) *string { return &s }

// settle sends a labeled PR 99 until it queues: every poll sent before it
// has then been through enqueue.
func (h *harness) settle(t *testing.T) {
	t.Helper()
	h.setLabels(99, "ai-review")
	// Re-poll: a check batch still running from an earlier poll would drop
	// the sentinel's check.
	waitFor(t, "sentinel queued", func() bool {
		h.poll("Team", labeledPR(99, "s99", "ai-review"))
		return h.state(99) != ""
	})
}

// pollUntil re-sends a poll of prs until cond holds. A check batch still
// finishing from an earlier poll makes the engine skip this poll's check (by
// design: the next poll redoes it), so a single poll can be lost.
func (h *harness) pollUntil(t *testing.T, what string, cond func() bool, prs ...github.PR) {
	t.Helper()
	waitFor(t, what, func() bool {
		h.poll("Team", prs...)
		return cond()
	})
}

// waitCheckBatch waits until GetPR for PR n has been called at least once
// (so a check batch holds checkMu) and that batch has finished.
func (h *harness) waitCheckBatch(t *testing.T, n int) {
	t.Helper()
	waitFor(t, "check attempted", func() bool { return h.getPRCallCount(n) >= 1 })
	waitFor(t, "check batch done", func() bool {
		if h.e.checkMu.TryLock() {
			h.e.checkMu.Unlock()
			return true
		}
		return false
	})
}

func TestLabelOffQueuesWithoutLabel(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	if h.labelCallCount() != 0 {
		t.Error("no label configured: AddLabel must not be called")
	}
}

func TestLabeledPRIsQueued(t *testing.T) {
	h := labelHarness(t, nil)
	h.setLabels(1, "AI-Review")
	h.start()
	h.poll("Team", labeledPR(1, "sha1", "AI-Review"))
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	if h.labelCallCount() != 0 {
		t.Error("label already present: AddLabel must not be called")
	}
}

func TestUnlabeledPRIsLabeledThenQueuedNextPoll(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "labeled")
	if h.state(1) != "" {
		t.Fatalf("queued in the labeling round: %s", h.state(1))
	}
	if got := h.labelsOf(1); len(got) != 1 || got[0] != "ai-review" {
		t.Fatalf("labels = %v", got)
	}
	h.poll("Team", labeledPR(1, "sha1", "ai-review"))
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
}

func TestRemovedLabelOptsOutOnce(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "labeled")
	h.setLabels(1) // someone removes it
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "opted out")
	calls := h.getPRCallCount(1)
	h.poll("Team", ghPR(1, "sha2")) // a new commit doesn't undo the opt-out
	h.settle(t)
	if h.state(1) != "" || h.auditCount(1, "opted out") != 1 || h.labelCallCount() != 1 || h.getPRCallCount(1) != calls {
		t.Errorf("state %q opt-outs %d label calls %d getPR %d→%d",
			h.state(1), h.auditCount(1, "opted out"), h.labelCallCount(), calls, h.getPRCallCount(1))
	}
}

func TestReaddedLabelResumes(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "labeled")
	h.setLabels(1)
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "opted out")
	h.setLabels(1, "ai-review")
	h.pollUntil(t, "label re-added", func() bool { return h.auditCount(1, "label re-added") > 0 }, labeledPR(1, "sha1", "ai-review"))
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
}

func TestStaleSearchIsNotOptOut(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "labeled")
	// Search hasn't caught up: no label in the result, but GitHub has it.
	h.poll("Team", ghPR(1, "sha1"))
	h.waitCheckBatch(t, 1)
	h.poll("Team", labeledPR(1, "sha1", "ai-review"))
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	if h.auditCount(1, "opted out") != 0 {
		t.Error("stale search result was treated as an opt-out")
	}
}

func TestHumanLabelRemovedIsOptOut(t *testing.T) {
	h := labelHarness(t, nil)
	h.setLabels(1, "ai-review") // a person added it
	h.start()
	h.poll("Team", labeledPR(1, "sha1", "ai-review"))
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	h.setLabels(1) // and removed it
	h.pollUntil(t, "opted out", func() bool { return h.auditCount(1, "opted out") > 0 }, ghPR(1, "sha2"))
	if h.labelCallCount() != 0 {
		t.Error("Ghost re-added a label a person removed")
	}
}

func TestConfirmFailureDoesNotOptOut(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "labeled")
	h.setLabels(1)
	h.mu.Lock()
	h.getPRErr = errors.New("network down")
	h.mu.Unlock()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitCheckBatch(t, 1)
	if st, _ := h.st.LabelState(prIDOf(t, h, 1), "ai-review"); st != store.LabelAdded {
		t.Fatalf("label state = %v after a failed confirm", st)
	}
	if n := h.auditCount(1, "opted out"); n != 0 {
		t.Fatalf("opted out %d times after a failed confirm", n)
	}
	h.mu.Lock()
	h.getPRErr = nil
	h.mu.Unlock()
	waitFor(t, "opted out on retry", func() bool {
		h.poll("Team", ghPR(1, "sha1"))
		return h.auditCount(1, "opted out") == 1
	})
}

func TestLabelFailureRetriedAndAuditedOnce(t *testing.T) {
	h := labelHarness(t, nil)
	h.addLabelErr = errors.New("HTTP 403: Resource not accessible")
	h.start()
	waitFor(t, "three attempts", func() bool {
		h.poll("Team", ghPR(1, "sha1"))
		return h.labelCallCount() >= 3
	})
	h.waitAudit(t, 1, "label failed")
	if n := h.auditCount(1, "label failed"); n != 1 || h.state(1) != "" {
		t.Fatalf("label failed audits %d, state %q", n, h.state(1))
	}
	h.mu.Lock()
	h.addLabelErr = nil
	h.mu.Unlock()
	waitFor(t, "labeled after recovery", func() bool {
		h.poll("Team", ghPR(1, "sha1"))
		return h.auditCount(1, "labeled") == 1
	})
}

func TestDraftIsNotLabeled(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	d := ghPR(1, "sha1")
	d.IsDraft = true
	h.poll("Team", d)
	h.settle(t)
	if h.labelCallCount() != 0 || h.state(1) != "" {
		t.Errorf("draft: label calls %d state %q", h.labelCallCount(), h.state(1))
	}
}

func TestRereviewBypassesLabel(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	if err := h.e.Rereview(ghPR(1, "sha1")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	if h.labelCallCount() != 0 {
		t.Error("re-review must not add the label")
	}
}

func TestViewLabelOverride(t *testing.T) {
	h := labelHarness(t, func(c *config.Config) { c.Views[0].Label = strPtr("needs-ai") })
	h.start()
	h.poll("Team", labeledPR(1, "sha1", "ai-review")) // the global label doesn't count here
	h.waitAudit(t, 1, "labeled")
	if got := h.labelsOf(1); len(got) != 1 || got[0] != "needs-ai" {
		t.Errorf("labels = %v", got)
	}
}

func TestViewLabelOff(t *testing.T) {
	h := labelHarness(t, func(c *config.Config) { c.Views[0].Label = strPtr("") })
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	if h.labelCallCount() != 0 {
		t.Error(`label = "" on the view: AddLabel must not be called`)
	}
}

func TestSlowLabelingDoesNotBlockPolls(t *testing.T) {
	h := labelHarness(t, nil)
	block := make(chan struct{})
	h.addLabelBlock = block
	defer close(block)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "labeling started", func() bool { return h.labelCallCount() == 1 })
	h.setLabels(2, "ai-review")
	h.poll("Team", labeledPR(2, "sha2", "ai-review"))
	waitFor(t, "other PR queued while labeling is stuck", func() bool { return h.state(2) != "" })
}

func prIDOf(t *testing.T, h *harness, n int) int64 {
	t.Helper()
	id, err := h.st.UpsertPR(ghPR(n, h.heads[n]))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestGateLabelFailsClosedOnStoreError(t *testing.T) {
	h := labelHarness(t, nil)
	if err := h.st.Close(); err != nil {
		t.Fatal(err)
	}
	ok, job := h.e.gateLabel(1, labeledPR(1, "sha1", "ai-review"), "ai-review")
	if ok || job != nil {
		t.Fatalf("gateLabel = %v, %v with a failing store; want skip", ok, job)
	}
}

func TestSearchShowsRemovedLabelNoReview(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "labeled")
	h.setLabels(1) // removed on GitHub, but search still shows it
	h.poll("Team", labeledPR(1, "sha1", "ai-review"))
	h.waitAudit(t, 1, "opted out")
	h.settle(t)
	if h.state(1) != "" {
		t.Errorf("reviewed a PR whose label was removed: state %q", h.state(1))
	}
}

func TestCheckFailureDoesNotQueue(t *testing.T) {
	h := labelHarness(t, nil)
	h.setLabels(1, "ai-review")
	h.getPRErr = errors.New("network down")
	h.start()
	h.poll("Team", labeledPR(1, "sha1", "ai-review"))
	h.waitCheckBatch(t, 1)
	if h.state(1) != "" || h.auditCount(1, "opted out") != 0 {
		t.Fatalf("after a failed check: state %q, opt-outs %d", h.state(1), h.auditCount(1, "opted out"))
	}
	waitFor(t, "more failed checks", func() bool {
		h.poll("Team", labeledPR(1, "sha1", "ai-review"))
		return h.getPRCallCount(1) >= 3
	})
	h.waitCheckBatch(t, 1)
	if n := h.auditCount(1, "label check failed"); n != 1 {
		t.Fatalf("label check failed audited %d times, want once", n)
	}
	h.mu.Lock()
	h.getPRErr = nil
	h.mu.Unlock()
	waitFor(t, "queued on retry", func() bool {
		h.poll("Team", labeledPR(1, "sha1", "ai-review"))
		return h.state(1) != ""
	})
}

func TestStaleReaddIsNotTrusted(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "labeled")
	h.setLabels(1)
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "opted out")
	calls := h.getPRCallCount(1)
	// Search claims the label is back; GitHub says it isn't.
	h.pollUntil(t, "check ran", func() bool { return h.getPRCallCount(1) > calls }, labeledPR(1, "sha1", "ai-review"))
	h.waitCheckBatch(t, 1)
	if st, _ := h.st.LabelState(prIDOf(t, h, 1), "ai-review"); st != store.LabelOptedOut || h.auditCount(1, "label re-added") != 0 || h.state(1) != "" {
		t.Fatalf("stale re-add trusted: state %v, re-added %d, review %q", st, h.auditCount(1, "label re-added"), h.state(1))
	}
}

func TestReviewedCommitNeedsNoCheck(t *testing.T) {
	h := labelHarness(t, nil)
	h.setLabels(1, "ai-review")
	h.start()
	h.poll("Team", labeledPR(1, "sha1", "ai-review"))
	waitFor(t, "review finished", func() bool { return h.state(1) == store.Ready })
	calls := h.getPRCallCount(1)
	h.poll("Team", labeledPR(1, "sha1", "ai-review"))
	h.settle(t)
	if got := h.getPRCallCount(1); got != calls {
		t.Errorf("GetPR calls for an already-reviewed commit: %d → %d", calls, got)
	}
}

func TestLabeledAuditWording(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	if err := h.e.Rereview(ghPR(1, "sha1")); err != nil { // commit already reviewed
		t.Fatal(err)
	}
	h.poll("Team", ghPR(1, "sha1"), ghPR(2, "sha2"))
	h.waitAudit(t, 1, "labeled")
	h.waitAudit(t, 2, "labeled")
	msg := func(n int) string {
		for _, ev := range h.e.PRState("o/r", n).Audit {
			if ev.Kind == "labeled" {
				return ev.Message
			}
		}
		return ""
	}
	if m := msg(1); !strings.Contains(m, "next new commit") {
		t.Errorf("reviewed commit: %q", m)
	}
	if m := msg(2); !strings.Contains(m, "next poll") {
		t.Errorf("unreviewed commit: %q", m)
	}
}

func TestCheckDoesNotQueueForChangedView(t *testing.T) {
	for name, views := range map[string][]config.View{
		"auto-review off": {{Name: "Team", Query: "q"}, {Name: "Watch", Query: "w"}},
		"query edited":    {{Name: "Team", Query: "q2", AutoReview: true}, {Name: "Watch", Query: "w"}},
		"view deleted":    {{Name: "Watch", Query: "w"}},
		"label changed":   {{Name: "Team", Query: "q", AutoReview: true, Label: strPtr("other")}, {Name: "Watch", Query: "w"}},
	} {
		t.Run(name, func(t *testing.T) {
			h := labelHarness(t, nil)
			h.setLabels(1, "ai-review")
			h.heads[1] = "sha1"
			block := make(chan struct{})
			h.getPRBlock = block
			h.start()
			h.e.HandlePoll(poller.Result{View: "Team", Query: "q", PRs: []github.PR{labeledPR(1, "sha1", "ai-review")}, At: time.Now()})
			waitFor(t, "check started", func() bool { return h.getPRCallCount(1) == 1 })
			h.e.SetViews(views)
			close(block)
			h.waitCheckBatch(t, 1)
			if h.state(1) != "" {
				t.Errorf("queued a review after the view changed: %q", h.state(1))
			}
		})
	}
}
