package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AdamIsrael/ghost/internal/github"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ghost.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func clock(t time.Time) func() time.Time { return func() time.Time { return t } }

func pr(n int, sha string) github.PR {
	return github.PR{Repo: "o/r", Number: n, Title: "PR", Author: "a", URL: "u", HeadSHA: sha}
}

func TestUpsertPRIsIdempotent(t *testing.T) {
	s := openTest(t)
	id1, err := s.UpsertPR(pr(1, "a"))
	if err != nil {
		t.Fatal(err)
	}
	p := pr(1, "b")
	p.Title = "Renamed"
	id2, err := s.UpsertPR(p)
	if err != nil || id1 != id2 {
		t.Fatalf("ids %d/%d err %v", id1, id2, err)
	}
	var title, sha string
	s.db.QueryRow(`SELECT title, head_sha FROM prs WHERE id = ?`, id1).Scan(&title, &sha)
	if title != "Renamed" || sha != "b" {
		t.Errorf("row = %q %q", title, sha)
	}
}

func TestEnqueueDedupesByHeadSHA(t *testing.T) {
	s := openTest(t)
	prID, _ := s.UpsertPR(pr(1, "a"))
	if _, ok, err := s.EnqueueReview(prID, "a", "claude", false); !ok || err != nil {
		t.Fatalf("first enqueue: %v %v", ok, err)
	}
	if _, ok, _ := s.EnqueueReview(prID, "a", "claude", false); ok {
		t.Error("same head enqueued twice")
	}
	if _, ok, _ := s.EnqueueReview(prID, "a", "claude", true); !ok {
		t.Error("force should enqueue")
	}
	if _, ok, _ := s.EnqueueReview(prID, "b", "claude", false); !ok {
		t.Error("new head should enqueue")
	}
}

func TestClaimNextOrderAndState(t *testing.T) {
	s := openTest(t)
	base := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	p1, _ := s.UpsertPR(pr(1, "a"))
	p2, _ := s.UpsertPR(pr(2, "b"))
	s.SetNow(clock(base))
	id1, _, _ := s.EnqueueReview(p1, "a", "claude", false)
	s.SetNow(clock(base.Add(time.Second)))
	id2, _, _ := s.EnqueueReview(p2, "b", "claude", false)

	r, err := s.ClaimNext()
	if err != nil || r == nil || r.ID != id1 || r.State != Running || r.Repo != "o/r" || r.Number != 1 || r.HeadSHA != "a" {
		t.Fatalf("first claim = %+v, %v", r, err)
	}
	if r, _ := s.ClaimNext(); r == nil || r.ID != id2 {
		t.Fatalf("second claim = %+v", r)
	}
	if r, _ := s.ClaimNext(); r != nil {
		t.Fatalf("third claim should be nil, got %+v", r)
	}
	if n, _ := s.CountState(Running); n != 2 {
		t.Errorf("running = %d", n)
	}
}

func TestSaveReviewAndLatest(t *testing.T) {
	s := openTest(t)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	s.SetNow(clock(now))
	prID, _ := s.UpsertPR(pr(1, "a"))
	s.EnqueueReview(prID, "a", "claude", false)
	r, _ := s.ClaimNext()
	r.State, r.Verdict, r.Summary, r.Comments, r.CostUSD, r.Prompt = Ready, "comment", "ok", `[{"path":"a.go","line":1,"body":"x","severity":"nit"}]`, 0.42, "P"
	if err := s.SaveReview(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.LatestReview("o/r", 1)
	if err != nil || got == nil {
		t.Fatalf("latest = %v, %v", got, err)
	}
	if got.State != Ready || got.Summary != "ok" || got.CostUSD != 0.42 || got.Prompt != "P" || !got.FinishedAt.Equal(now) {
		t.Errorf("latest = %+v", got)
	}
	if missing, _ := s.LatestReview("o/r", 99); missing != nil {
		t.Error("unknown PR should return nil")
	}
}

func TestSpendSinceUsesFinishedAt(t *testing.T) {
	s := openTest(t)
	today := time.Date(2026, 10, 9, 0, 0, 0, 0, time.Local)
	prID, _ := s.UpsertPR(pr(1, "a"))
	for i, at := range []time.Time{today.Add(-time.Hour), today.Add(time.Hour)} {
		s.SetNow(clock(at))
		s.EnqueueReview(prID, string(rune('a'+i)), "claude", true)
		r, _ := s.ClaimNext()
		r.State, r.CostUSD = Failed, 1.0+float64(i)
		s.SaveReview(r)
	}
	if got, _ := s.SpendSince(today); got != 2.0 {
		t.Errorf("spend today = %v, want 2.0 (yesterday's 1.0 excluded)", got)
	}
}

func TestFailInterrupted(t *testing.T) {
	s := openTest(t)
	p1, _ := s.UpsertPR(pr(1, "a"))
	p2, _ := s.UpsertPR(pr(2, "b"))
	s.EnqueueReview(p1, "a", "claude", false)
	s.EnqueueReview(p2, "b", "claude", false)
	r1, _ := s.ClaimNext() // left running by a crash
	r2, _ := s.ClaimNext()
	r2.State = Posting // crashed mid-post
	s.SaveReview(r2)
	if rs, err := s.FailInterrupted(1.5); len(rs) != 2 || err != nil {
		t.Fatalf("FailInterrupted = %d, %v", len(rs), err)
	}
	got1, _ := s.LatestReview("o/r", 1)
	if got1.State != Failed || got1.CostUSD != 1.5 || !strings.Contains(got1.Error, "interrupted") || got1.FinishedAt.IsZero() {
		t.Errorf("running review = %+v", got1)
	}
	got2, _ := s.LatestReview("o/r", 2)
	if got2.State != PostUnknown {
		t.Errorf("posting review = %+v", got2)
	}
	if n, _ := s.CountState(Queued); n != 0 {
		t.Errorf("interrupted reviews must not be requeued (queued=%d)", n)
	}
	_ = r1
}

func TestTransitionState(t *testing.T) {
	s := openTest(t)
	prID, _ := s.UpsertPR(pr(1, "a"))
	s.EnqueueReview(prID, "a", "claude", false)
	r, _ := s.ClaimNext()
	if ok, err := s.TransitionState(r.ID, Running, Ready); !ok || err != nil {
		t.Fatalf("first transition: %v %v", ok, err)
	}
	if ok, _ := s.TransitionState(r.ID, Running, Ready); ok {
		t.Error("transition from a state the review is no longer in must fail")
	}
}

func TestOpenRejectsSecondInstance(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ghost.db")
	s1, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second Open = %v, want already-running error", err)
	}
	s1.Close()
	s3, err := Open(p)
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	s3.Close()
}

func TestAuditOrderAndLimit(t *testing.T) {
	s := openTest(t)
	prID, _ := s.UpsertPR(pr(1, "a"))
	id, _, _ := s.EnqueueReview(prID, "a", "claude", false)
	for _, k := range []string{"queued", "started", "reviewed"} {
		if err := s.AddAudit(prID, id, k, k+" msg"); err != nil {
			t.Fatal(err)
		}
	}
	s.AddAudit(prID, 0, "note", "no review")
	ev, err := s.Audit("o/r", 1, 2)
	if err != nil || len(ev) != 2 || ev[0].Kind != "reviewed" || ev[1].Kind != "note" || ev[1].ReviewID != 0 || ev[0].ReviewID != id {
		t.Fatalf("audit = %+v, %v", ev, err)
	}
}

func TestClaimNextConcurrentWithWrites(t *testing.T) {
	s := openTest(t)
	const n = 40
	prID, _ := s.UpsertPR(pr(1, "a"))
	for i := 0; i < n; i++ {
		s.EnqueueReview(prID, "a", "claude", true)
	}
	var mu sync.Mutex
	claimed := map[int64]bool{}
	var errs []error
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				r, err := s.ClaimNext()
				mu.Lock()
				if err != nil {
					errs = append(errs, err)
				} else if r != nil {
					if claimed[r.ID] {
						errs = append(errs, fmt.Errorf("review %d claimed twice", r.ID))
					}
					claimed[r.ID] = true
				}
				done := err == nil && r == nil
				mu.Unlock()
				if done || err != nil {
					return
				}
				s.AddAudit(prID, r.ID, "started", "") // concurrent writes, like the engine
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 || len(claimed) != n {
		t.Fatalf("claimed %d/%d, errors: %v", len(claimed), n, errs)
	}
}

func TestApprovalFieldsRoundTrip(t *testing.T) {
	s := openTest(t)
	prID, _ := s.UpsertPR(pr(1, "a"))
	s.EnqueueReview(prID, "a", "claude", false)
	r, _ := s.ClaimNext()
	r.State, r.Approval, r.ApprovalNote = Posted, ApprovalWaiting, "CI is pending"
	if err := s.SaveReview(r); err != nil {
		t.Fatal(err)
	}
	got, _ := s.LatestReview("o/r", 1)
	if got.Approval != ApprovalWaiting || got.ApprovalNote != "CI is pending" {
		t.Errorf("got %+v", got)
	}
	w, err := s.WaitingApprovals()
	if err != nil || len(w) != 1 || w[0].ID != r.ID || w[0].Repo != "o/r" {
		t.Fatalf("waiting = %+v, %v", w, err)
	}
	if ok, _ := s.TransitionApproval(r.ID, ApprovalWaiting, ApprovalApproving); !ok {
		t.Fatal("transition failed")
	}
	if ok, _ := s.TransitionApproval(r.ID, ApprovalWaiting, ApprovalApproving); ok {
		t.Error("second transition from a stale state must fail")
	}
	if w, _ := s.WaitingApprovals(); len(w) != 0 {
		t.Errorf("no longer waiting, got %+v", w)
	}
}

func TestFailInterruptedSettlesApproving(t *testing.T) {
	s := openTest(t)
	prID, _ := s.UpsertPR(pr(1, "a"))
	s.EnqueueReview(prID, "a", "claude", false)
	r, _ := s.ClaimNext()
	r.State, r.Approval = Posted, ApprovalApproving
	s.SaveReview(r)
	if _, err := s.FailInterrupted(1.5); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LatestReview("o/r", 1); got.Approval != ApprovalUnknown || got.State != Posted {
		t.Errorf("got %+v", got)
	}
}
