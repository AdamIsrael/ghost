package store

import (
	"testing"

	"github.com/AdamIsrael/ghost/internal/github"
)

func TestManualApprovalClaim(t *testing.T) {
	s := openTest(t)
	pr, _ := s.UpsertPR(github.PR{Repo: "o/r", Number: 1, HeadSHA: "a"})

	if st, err := s.ManualApproval(pr, "a"); err != nil || st != "" {
		t.Fatalf("ManualApproval = %q, %v; want none", st, err)
	}
	if ok, err := s.ClaimManualApproval(pr, "a", ""); err != nil || !ok {
		t.Fatalf("first claim = %v, %v", ok, err)
	}
	// A second press while the first is in flight can't claim it.
	if ok, _ := s.ClaimManualApproval(pr, "a", ""); ok {
		t.Fatal("second claim succeeded")
	}
	if done, _ := s.HeadApproved(pr, "a", 0); !done {
		t.Error("HeadApproved false while a manual approval is in flight")
	}
	if done, _ := s.HeadApproved(pr, "b", 0); done {
		t.Error("HeadApproved true for another commit")
	}

	// A failed approval can be claimed again; an approved one can't.
	s.SetManualApproval(pr, "a", ApprovalFailed, "403")
	if done, _ := s.HeadApproved(pr, "a", 0); done {
		t.Error("HeadApproved true after the approval failed")
	}
	if ok, _ := s.ClaimManualApproval(pr, "a", ApprovalFailed); !ok {
		t.Fatal("could not retry a failed approval")
	}
	s.SetManualApproval(pr, "a", ApprovalApproved, "")
	if ok, _ := s.ClaimManualApproval(pr, "a", ApprovalFailed); ok {
		t.Fatal("claimed an approved commit")
	}
	if st, _ := s.ManualApproval(pr, "a"); st != ApprovalApproved {
		t.Errorf("state = %q, want approved", st)
	}
}

func TestInterruptedManualApprovalBecomesUnknown(t *testing.T) {
	s := openTest(t)
	pr, _ := s.UpsertPR(github.PR{Repo: "o/r", Number: 1, HeadSHA: "a"})
	s.ClaimManualApproval(pr, "a", "")
	if _, err := s.FailInterrupted(1); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.ManualApproval(pr, "a"); st != ApprovalUnknown {
		t.Errorf("state = %q, want unknown", st)
	}
}
