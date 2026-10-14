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
	limits  []int
	fail    map[string]error
	total   map[string]int // total matches reported per query (default 1)
}

func (f *fakeSearcher) SearchPRs(_ context.Context, q string, limit int) ([]github.PR, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	f.limits = append(f.limits, limit)
	if err := f.fail[q]; err != nil {
		return nil, 0, err
	}
	total, ok := f.total[q]
	if !ok {
		total = 1
	}
	return []github.PR{{Repo: "o/r", Number: len(f.queries), Title: q}}, total, nil
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
	p := New(&fakeSearcher{}, views, time.Hour, nil)
	go p.Run(ctx)
	a, b := next(t, p), next(t, p)
	if a.View != "A" || b.View != "B" {
		t.Fatalf("views = %s,%s", a.View, b.View)
	}
	if len(a.PRs) != 1 || a.PRs[0].Title != "qa" || a.Err != nil || a.At.IsZero() || a.Query != "qa" {
		t.Errorf("result A = %+v", a)
	}
}

func TestPollNowTriggersAnotherRound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := New(&fakeSearcher{}, views, time.Hour, nil)
	go p.Run(ctx)
	next(t, p)
	next(t, p)
	p.PollNow()
	if r := next(t, p); r.View != "A" {
		t.Fatalf("after PollNow got %+v", r)
	}
}

func TestPollNowNeverBlocks(t *testing.T) {
	p := New(&fakeSearcher{}, views, time.Hour, nil)
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
	p := New(&fakeSearcher{fail: map[string]error{"qa": errors.New("rate limited")}}, views, time.Hour, nil)
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
	p := New(&fakeSearcher{}, views, time.Hour, nil)
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

func (partialSearcher) SearchPRs(context.Context, string, int) ([]github.PR, int, error) {
	return []github.PR{{Repo: "o/r", Number: 1}}, 2, errors.New("1 PRs hidden: SAML")
}

func TestRunPassesPartialResultsThrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := New(partialSearcher{}, views[:1], time.Hour, nil)
	go p.Run(ctx)
	r := next(t, p)
	if len(r.PRs) != 1 || r.Err == nil {
		t.Fatalf("want PRs and warning together, got %+v", r)
	}
}

func TestPartialResultIsNotAFailedRound(t *testing.T) {
	p := New(partialSearcher{}, views[:1], time.Hour, nil)
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
	p := New(&fakeSearcher{}, nil, time.Hour, nil) // starts with no views
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

func TestPollUsesEachViewsLimitAndMarksTruncated(t *testing.T) {
	f := &fakeSearcher{total: map[string]int{"qa": 31, "qb": 80}}
	vs := []config.View{{Name: "A", Query: "qa"}, {Name: "B", Query: "qb", MaxPRs: 80}}
	p := New(f, vs, time.Hour, (&config.Config{MaxPRsPerView: 30}).PRLimitFor)
	go p.pollAll(context.Background())
	a, b := next(t, p), next(t, p)
	f.mu.Lock()
	limits := append([]int(nil), f.limits...)
	f.mu.Unlock()
	if len(limits) != 2 || limits[0] != 30 || limits[1] != 80 {
		t.Errorf("limits = %v, want [30 80]", limits)
	}
	if !a.Truncated || b.Truncated {
		t.Errorf("truncated: A=%v (31 > 30) B=%v (80 of 80)", a.Truncated, b.Truncated)
	}
}

func TestNilLimitForUsesDefault(t *testing.T) {
	f := &fakeSearcher{}
	p := New(f, views[:1], time.Hour, nil)
	go p.pollAll(context.Background())
	next(t, p)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.limits) != 1 || f.limits[0] != config.DefaultMaxPRs {
		t.Errorf("limits = %v", f.limits)
	}
}
