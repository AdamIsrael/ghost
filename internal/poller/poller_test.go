package poller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
)

type fakeSearcher struct {
	mu      sync.Mutex
	queries []string
	limits  []int
	fail    map[string]error
	failN   map[string]int // fail this many times with errTransient, then succeed
	total   map[string]int // total matches reported per query (default 1)
}

var errTransient = &api.HTTPError{StatusCode: 502, Message: "Bad Gateway"}

func (f *fakeSearcher) SearchPRs(_ context.Context, q string, limit int) ([]github.PR, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	f.limits = append(f.limits, limit)
	if err := f.fail[q]; err != nil {
		return nil, 0, err
	}
	if f.failN[q] > 0 {
		f.failN[q]--
		return nil, 0, errTransient
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
	if retry, all := p.poll(context.Background(), nil); len(retry) > 0 || all {
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
	go p.poll(context.Background(), nil)
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
	go p.poll(context.Background(), nil)
	next(t, p)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.limits) != 1 || f.limits[0] != config.DefaultMaxPRs {
		t.Errorf("limits = %v", f.limits)
	}
}

// quiet asserts no poll result arrives within d.
func quiet(t *testing.T, p *Poller, d time.Duration) {
	t.Helper()
	select {
	case r := <-p.Results():
		t.Fatalf("unexpected poll of %q (err %v)", r.View, r.Err)
	case <-time.After(d):
	}
}

func TestFailedViewIsRetriedSoonAlone(t *testing.T) {
	f := &fakeSearcher{failN: map[string]int{"qb": 2}}
	p := New(f, views, time.Hour, nil)
	p.retryBase = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	if a := next(t, p); a.View != "A" || a.Err != nil {
		t.Fatalf("A: %+v", a)
	}
	if b := next(t, p); b.View != "B" || b.Err == nil {
		t.Fatalf("B should fail first: %+v", b)
	}
	// Only B is re-polled, quickly, until it recovers.
	if b := next(t, p); b.View != "B" || b.Err == nil {
		t.Fatalf("second try: %+v", b)
	}
	if b := next(t, p); b.View != "B" || b.Err != nil || len(b.PRs) == 0 {
		t.Fatalf("recovery: %+v", b)
	}
	quiet(t, p, 150*time.Millisecond) // healthy views wait for the regular round
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := strings.Join(f.queries, ","); got != "qa,qb,qb,qb" {
		t.Errorf("queries = %s", got)
	}
}

func TestRetryDelayDoubles(t *testing.T) {
	f := &fakeSearcher{failN: map[string]int{"qb": 3}}
	p := New(f, views, time.Hour, nil)
	p.retryBase = 100 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	next(t, p)
	var at []time.Time
	for i := 0; i < 4; i++ {
		next(t, p)
		at = append(at, time.Now())
	}
	if gap1, gap2 := at[1].Sub(at[0]), at[2].Sub(at[1]); gap2 <= gap1*3/2 {
		t.Errorf("retry gaps %v then %v: want the wait to grow", gap1, gap2)
	}
}

func TestAllViewsFailingBacksOffWholeRound(t *testing.T) {
	f := &fakeSearcher{fail: map[string]error{"qa": errTransient, "qb": errTransient}}
	p := New(f, views, time.Hour, nil)
	p.retryBase = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	next(t, p)
	next(t, p)
	quiet(t, p, 150*time.Millisecond) // no per-view retries while GitHub looks down
}

func TestPollNowDuringRetryRunsFullRound(t *testing.T) {
	f := &fakeSearcher{failN: map[string]int{"qb": 100}}
	p := New(f, views, time.Hour, nil)
	p.retryBase = 10 * time.Minute // in a retry wait that won't end on its own
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	next(t, p)
	next(t, p)
	p.PollNow()
	if a := next(t, p); a.View != "A" {
		t.Fatalf("PollNow should poll every view, got %q first", a.View)
	}
	next(t, p)
}

func TestPermanentFailureIsNotRetriedEarly(t *testing.T) {
	bad := &api.HTTPError{StatusCode: 422, Message: "Validation Failed"}
	f := &fakeSearcher{fail: map[string]error{"qb": bad}}
	p := New(f, views, time.Hour, nil)
	p.retryBase = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	next(t, p)
	if b := next(t, p); b.Err == nil {
		t.Fatalf("B should fail: %+v", b)
	}
	quiet(t, p, 150*time.Millisecond) // a bad query won't fix itself in 30s
}

func TestDeletedViewIsNotRetried(t *testing.T) {
	f := &fakeSearcher{failN: map[string]int{"qb": 100}}
	p := New(f, views, time.Hour, nil)
	p.retryBase = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	next(t, p)
	next(t, p)
	p.SetViews(views[:1]) // B deleted while it waits for a retry
	if a := next(t, p); a.View != "A" {
		t.Fatalf("SetViews should poll the remaining views, got %q", a.View)
	}
	quiet(t, p, 250*time.Millisecond)
}

type prSearcher struct {
	fakeSearcher
	gets []string
	err  error
}

func (f *prSearcher) GetPR(_ context.Context, repo string, n int) (github.PRDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets = append(f.gets, fmt.Sprintf("%s#%d", repo, n))
	if f.err != nil {
		return github.PRDetail{}, f.err
	}
	return github.PRDetail{PR: github.PR{Repo: repo, Number: n, Title: "one"}}, nil
}

func TestSinglePRViewIsFetchedNotSearched(t *testing.T) {
	f := &prSearcher{}
	vs := []config.View{{Name: "A", Query: "qa"}, {Name: "my-repo#5", PR: "o/my-repo#5", Source: "session"}}
	p := New(f, vs, time.Hour, nil)
	go p.poll(context.Background(), nil)
	next(t, p)
	r := next(t, p)
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.View != "my-repo#5" || r.Err != nil || len(r.PRs) != 1 || r.PRs[0].Number != 5 || r.PRs[0].Repo != "o/my-repo" {
		t.Errorf("result = %+v", r)
	}
	if len(f.gets) != 1 || f.gets[0] != "o/my-repo#5" || strings.Join(f.queries, ",") != "qa" {
		t.Errorf("gets %v queries %v; the PR view must not be searched", f.gets, f.queries)
	}
}

func TestSinglePRViewFailureIsRetriedLikeOthers(t *testing.T) {
	f := &prSearcher{err: errTransient}
	vs := []config.View{{Name: "A", Query: "qa"}, {Name: "one", PR: "o/r#5", Source: "session"}}
	p := New(f, vs, time.Hour, nil)
	p.retryBase = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	next(t, p)
	if r := next(t, p); r.Err == nil {
		t.Fatalf("want the fetch error: %+v", r)
	}
	if r := next(t, p); r.View != "one" {
		t.Errorf("the failed PR view should be re-polled soon, got %q", r.View)
	}
}
