package poller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
)

type fakeSearcher struct {
	mu      sync.Mutex
	queries []string
	fail    map[string]error
}

func (f *fakeSearcher) SearchPRs(_ context.Context, q string) ([]github.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	if err := f.fail[q]; err != nil {
		return nil, err
	}
	return []github.PR{{Repo: "o/r", Number: len(f.queries), Title: q}}, nil
}

var views = []config.View{{Name: "A", Query: "qa"}, {Name: "B", Query: "qb"}}

func next(t *testing.T, p *Poller) Result {
	t.Helper()
	select {
	case r := <-p.Results():
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for poll result")
		return Result{}
	}
}

func TestRunPollsEveryViewImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := New(&fakeSearcher{}, views, time.Hour)
	go p.Run(ctx)
	a, b := next(t, p), next(t, p)
	if a.View != "A" || b.View != "B" {
		t.Fatalf("views = %s,%s", a.View, b.View)
	}
	if len(a.PRs) != 1 || a.PRs[0].Title != "qa" || a.Err != nil || a.At.IsZero() {
		t.Errorf("result A = %+v", a)
	}
}

func TestPollNowTriggersAnotherRound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := New(&fakeSearcher{}, views, time.Hour)
	go p.Run(ctx)
	next(t, p)
	next(t, p)
	p.PollNow()
	if r := next(t, p); r.View != "A" {
		t.Fatalf("after PollNow got %+v", r)
	}
}

func TestPollNowNeverBlocks(t *testing.T) {
	p := New(&fakeSearcher{}, views, time.Hour)
	done := make(chan struct{})
	go func() {
		p.PollNow()
		p.PollNow()
		p.PollNow()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("PollNow blocked while Run was not running")
	}
}

func TestRunReportsErrorsPerView(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := New(&fakeSearcher{fail: map[string]error{"qa": errors.New("rate limited")}}, views, time.Hour)
	go p.Run(ctx)
	a, b := next(t, p), next(t, p)
	if a.Err == nil || a.PRs != nil {
		t.Errorf("A should carry the error: %+v", a)
	}
	if b.Err != nil || len(b.PRs) != 1 {
		t.Errorf("B should succeed: %+v", b)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := New(&fakeSearcher{}, views, time.Hour)
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	next(t, p)
	next(t, p)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestBackoff(t *testing.T) {
	base := time.Minute
	for failures, want := range []time.Duration{base, 2 * base, 4 * base, 8 * base, 8 * base, 8 * base} {
		if got := Backoff(base, failures); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", failures, got, want)
		}
	}
}

type partialSearcher struct{}

func (partialSearcher) SearchPRs(context.Context, string) ([]github.PR, error) {
	return []github.PR{{Repo: "o/r", Number: 1}}, errors.New("1 PRs hidden: SAML")
}

func TestRunPassesPartialResultsThrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := New(partialSearcher{}, views[:1], time.Hour)
	go p.Run(ctx)
	r := next(t, p)
	if len(r.PRs) != 1 || r.Err == nil {
		t.Fatalf("want PRs and warning together, got %+v", r)
	}
}

func TestPartialResultIsNotAFailedRound(t *testing.T) {
	p := New(partialSearcher{}, views[:1], time.Hour)
	go func() {
		for range p.Results() {
		}
	}()
	if p.pollAll(context.Background()) {
		t.Error("a round that returned PRs should not count as failed (would trigger backoff)")
	}
}

func TestSetViewsPollsNewViews(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := New(&fakeSearcher{}, nil, time.Hour) // starts with no views
	go p.Run(ctx)
	p.SetViews([]config.View{{Name: "C", Query: "qc"}})
	if r := next(t, p); r.View != "C" {
		t.Fatalf("after SetViews got %+v", r)
	}
	p.SetViews(views)
	// Rounds already in flight may still report C; the new views must follow.
	r := next(t, p)
	for r.View == "C" {
		r = next(t, p)
	}
	if b := next(t, p); r.View != "A" || b.View != "B" {
		t.Fatalf("views = %s,%s", r.View, b.View)
	}
}
