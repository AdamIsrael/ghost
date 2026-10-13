package engine

import (
	"errors"
	"testing"

	"github.com/AdamIsrael/ghost/internal/config"
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

// settle sends a labeled PR 99 and waits for it to queue: every poll sent
// before it has then been through enqueue.
func (h *harness) settle(t *testing.T) {
	t.Helper()
	h.poll("Team", labeledPR(99, "s99", "ai-review"))
	waitFor(t, "sentinel queued", func() bool { return h.state(99) != "" })
}

// waitLabelBatch waits until GetPR for PR n has been called at least once
// (so a labeling batch holds labelMu) and that batch has finished.
func (h *harness) waitLabelBatch(t *testing.T, n int) {
	t.Helper()
	waitFor(t, "confirm attempted", func() bool { return h.getPRCallCount(n) >= 1 })
	waitFor(t, "label batch done", func() bool {
		if h.e.labelMu.TryLock() {
			h.e.labelMu.Unlock()
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
	h.poll("Team", labeledPR(1, "sha1", "ai-review"))
	h.waitAudit(t, 1, "label re-added")
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
}

func TestStaleSearchIsNotOptOut(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "labeled")
	// Search hasn't caught up: no label in the result, but GitHub has it.
	h.poll("Team", ghPR(1, "sha1"))
	h.waitLabelBatch(t, 1)
	h.poll("Team", labeledPR(1, "sha1", "ai-review"))
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	if h.auditCount(1, "opted out") != 0 {
		t.Error("stale search result was treated as an opt-out")
	}
}

func TestHumanLabelRemovedIsOptOut(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", labeledPR(1, "sha1", "ai-review")) // a person added it
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	h.poll("Team", ghPR(1, "sha2")) // and removed it (GetPR reports no labels)
	h.waitAudit(t, 1, "opted out")
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
	h.waitLabelBatch(t, 1)
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
