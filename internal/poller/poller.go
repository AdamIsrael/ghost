// Package poller runs each view's GitHub query on an interval.
package poller

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
)

type Searcher interface {
	// SearchPRs returns up to limit PRs and the total number of matches.
	SearchPRs(ctx context.Context, query string, limit int) ([]github.PR, int, error)
}

// Result is one view's poll outcome. Err with nil PRs is a failed poll and the
// caller keeps whatever it showed before; Err with PRs is a warning.
type Result struct {
	View  string
	Query string // the query that produced this result
	PRs   []github.PR
	Err   error
	At    time.Time
	// Truncated reports that GitHub has more matches than the view's limit.
	Truncated bool
}

// defaultRetryBase is how soon a view that failed while others succeeded is
// polled again; the wait doubles until it recovers or the full round is due.
const defaultRetryBase = 30 * time.Second

type Poller struct {
	searcher  Searcher
	mu        sync.Mutex // guards views
	views     []config.View
	interval  time.Duration
	retryBase time.Duration // first quick re-poll of views that failed alone
	limitFor  func(config.View) int
	results   chan Result
	trigger   chan struct{}
}

// New returns a poller for views. limitFor gives each view's PR limit; nil
// uses config.DefaultMaxPRs for every view.
func New(s Searcher, views []config.View, interval time.Duration, limitFor func(config.View) int) *Poller {
	if limitFor == nil {
		limitFor = func(config.View) int { return config.DefaultMaxPRs }
	}
	return &Poller{
		searcher:  s,
		views:     views,
		interval:  interval,
		retryBase: defaultRetryBase,
		limitFor:  limitFor,
		results:   make(chan Result, 16),
		trigger:   make(chan struct{}, 1),
	}
}

func (p *Poller) Results() <-chan Result { return p.results }

// SetViews replaces the views to poll and starts a new round now.
func (p *Poller) SetViews(views []config.View) {
	p.mu.Lock()
	p.views = append([]config.View(nil), views...)
	p.mu.Unlock()
	p.PollNow()
}

// PollNow asks Run to start a new round immediately. It never blocks.
func (p *Poller) PollNow() {
	select {
	case p.trigger <- struct{}{}:
	default:
	}
}

// Run polls every view once, then again after each interval (backed off while
// every view fails) or when PollNow is called, until ctx is cancelled. Views
// that fail transiently (GitHub 5xx, network) while others succeed are
// re-polled on their own after retryBase, doubling each time, until they
// recover or the next full round is due.
func (p *Poller) Run(ctx context.Context) {
	failures := 0 // consecutive rounds in which every view failed
round:
	for {
		retry, all := p.poll(ctx, nil)
		if all {
			failures++
		} else {
			failures = 0
		}
		if ctx.Err() != nil {
			return
		}
		due := time.Now().Add(Backoff(p.interval, failures))
		retryIn := p.retryBase
		for {
			wait := time.Until(due)
			// While GitHub looks down for every view, back off the whole
			// round instead of retrying views one by one.
			retrying := len(retry) > 0 && !all && retryIn < wait
			if retrying {
				wait = retryIn
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-p.trigger:
				timer.Stop()
				continue round
			case <-timer.C:
			}
			if !retrying {
				continue round
			}
			retry, _ = p.poll(ctx, retry)
			if ctx.Err() != nil {
				return
			}
			retryIn *= 2
		}
	}
}

// poll queries the named views (nil = all, in order) and sends each result.
// It returns the views worth retrying soon (failed transiently) and whether
// every polled view failed outright. A result that still carried PRs (e.g.
// some hidden by SAML) is a warning, not a failure.
func (p *Poller) poll(ctx context.Context, only []string) (retry []string, all bool) {
	p.mu.Lock()
	views := p.views
	p.mu.Unlock()
	want := map[string]bool{}
	for _, n := range only {
		want[n] = true
	}
	polled, failed := 0, 0
	for _, v := range views {
		if only != nil && !want[v.Name] {
			continue // not retrying this one, or it was renamed/deleted
		}
		polled++
		limit := p.limitFor(v)
		var prs []github.PR
		var total int
		var err error
		if v.PR != "" {
			prs, total, err = p.fetchOne(ctx, v.PR)
		} else {
			prs, total, err = p.searcher.SearchPRs(ctx, v.Query, limit)
		}
		if ctx.Err() != nil {
			return retry, false
		}
		if err != nil && prs == nil {
			failed++
			if github.Transient(err) {
				retry = append(retry, v.Name)
			}
		}
		select {
		case p.results <- Result{View: v.Name, Query: v.Query, PRs: prs, Err: err, At: time.Now(), Truncated: total > limit}:
		case <-ctx.Done():
			return retry, false
		}
	}
	return retry, polled > 0 && failed == polled
}

// prGetter is what a single-PR view needs; github.Client provides it.
type prGetter interface {
	GetPR(ctx context.Context, repo string, number int) (github.PRDetail, error)
}

// fetchOne loads the PR of a one-off view ("owner/repo#N") directly.
func (p *Poller) fetchOne(ctx context.Context, ref string) ([]github.PR, int, error) {
	g, ok := p.searcher.(prGetter)
	if !ok {
		return nil, 0, errors.New("this view needs a GitHub client that can fetch a single PR")
	}
	repo, n, err := github.ParsePRRef(ref)
	if err != nil {
		return nil, 0, err
	}
	d, err := g.GetPR(ctx, repo, n)
	if err != nil {
		return nil, 0, err
	}
	return []github.PR{d.PR}, 1, nil
}

// Backoff returns base × 2^failures, capped at 8× base.
func Backoff(base time.Duration, failures int) time.Duration {
	if failures > 3 {
		failures = 3
	}
	return base << failures
}
