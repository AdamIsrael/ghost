package store

import (
	"path/filepath"
	"testing"

	"github.com/AdamIsrael/ghost/internal/github"
)

func labelStore(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ghost.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	id, err := s.UpsertPR(github.PR{Repo: "o/r", Number: 1, HeadSHA: "a"})
	if err != nil {
		t.Fatal(err)
	}
	return s, id
}

func TestLabelLifecycle(t *testing.T) {
	s, pr := labelStore(t)
	if st, err := s.LabelState(pr, "ai-review"); err != nil || st != LabelNever {
		t.Fatalf("new: %v %v", st, err)
	}
	if changed, _ := s.MarkOptedOut(pr, "ai-review"); changed {
		t.Error("cannot opt out of a label never recorded")
	}
	for i := 0; i < 2; i++ { // idempotent
		if err := s.MarkLabeled(pr, "AI-Review"); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := s.LabelState(pr, "ai-review"); st != LabelAdded {
		t.Fatalf("after mark (case-insensitive): %v", st)
	}
	if changed, err := s.MarkOptedOut(pr, "ai-review"); err != nil || !changed {
		t.Fatalf("opt out: %v %v", changed, err)
	}
	if changed, _ := s.MarkOptedOut(pr, "ai-review"); changed {
		t.Error("second opt-out should report no change")
	}
	if st, _ := s.LabelState(pr, "ai-review"); st != LabelOptedOut {
		t.Fatalf("after opt-out: %v", st)
	}
	if err := s.MarkLabeled(pr, "ai-review"); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.LabelState(pr, "ai-review"); st != LabelOptedOut {
		t.Error("MarkLabeled must not clear an opt-out")
	}
	if changed, err := s.ClearOptOut(pr, "ai-review"); err != nil || !changed {
		t.Fatalf("clear: %v %v", changed, err)
	}
	if changed, _ := s.ClearOptOut(pr, "ai-review"); changed {
		t.Error("second clear should report no change")
	}
	if st, _ := s.LabelState(pr, "ai-review"); st != LabelAdded {
		t.Fatalf("after clear: %v", st)
	}
	if st, _ := s.LabelState(pr, "other"); st != LabelNever {
		t.Error("labels are tracked separately")
	}
}

func TestMigrationFromVersion3KeepsData(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ghost.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	pr, _ := s.UpsertPR(github.PR{Repo: "o/r", Number: 9, HeadSHA: "a"})
	if _, err := s.db.Exec(`DROP TABLE pr_labels; PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.SchemaVersion(); v != 4 {
		t.Errorf("version = %d, want 4", v)
	}
	if err := s.MarkLabeled(pr, "ai-review"); err != nil {
		t.Fatalf("pr_labels missing after migration: %v", err)
	}
	if id, _ := s.UpsertPR(github.PR{Repo: "o/r", Number: 9, HeadSHA: "a"}); id != pr {
		t.Error("existing PR row was lost")
	}
}

func TestReviewed(t *testing.T) {
	s, pr := labelStore(t)
	if done, err := s.Reviewed(pr, "a"); err != nil || done {
		t.Fatalf("before any review: %v %v", done, err)
	}
	if _, ok, err := s.EnqueueReview(pr, "a", "claude", false); err != nil || !ok {
		t.Fatalf("enqueue: %v %v", ok, err)
	}
	if done, err := s.Reviewed(pr, "a"); err != nil || !done {
		t.Fatalf("after enqueue: %v %v", done, err)
	}
	if done, _ := s.Reviewed(pr, "b"); done {
		t.Error("another commit is not reviewed")
	}
	if _, ok, _ := s.EnqueueReview(pr, "a", "claude", false); ok {
		t.Error("EnqueueReview must still refuse a reviewed commit")
	}
}

func TestPRRef(t *testing.T) {
	s, pr := labelStore(t)
	if ref, err := s.PRRef(pr); err != nil || ref != "o/r#1" {
		t.Errorf("PRRef = %q, %v", ref, err)
	}
	if _, err := s.PRRef(9999); err == nil {
		t.Error("unknown PR should be an error")
	}
}
