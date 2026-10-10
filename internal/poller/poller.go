// Package poller runs each view's GitHub query on an interval.
package poller

import (
	"context"
	"time"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
)

type Searcher interface {
	SearchPRs(ctx context.Context, query string) ([]github.PR, error)
}

// Result is one view's poll outcome. On error PRs is nil and the caller keeps
// whatever it showed before.
type Result struct {
	View string
	PRs  []github.PR
	Err  error
	At   time.Time
}

type Poller struct {
	searcher Searcher
	views    []config.View
	interval time.Duration
	results  chan Result
	trigger  chan struct{}
}

func New(s Searcher, views []config.View, interval time.Duration) *Poller {
	return &Poller{
		searcher: s,
		views:    views,
		interval: interval,
		results:  make(chan Result, len(views)),
		trigger:  make(chan struct{}, 1),
	}
}

func (p *Poller) Results() <-chan Result { return p.results }

// PollNow asks Run to start a new round immediately. It never blocks.
func (p *Poller) PollNow() {
	select {
	case p.trigger <- struct{}{}:
	default:
	}
}

// Run polls every view once, then again after each interval (backed off while
// rounds fail) or when PollNow is called, until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	failures := 0
	for {
		if p.pollAll(ctx) {
			failures++
		} else {
			failures = 0
		}
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(Backoff(p.interval, failures))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-p.trigger:
			timer.Stop()
		}
	}
}

// pollAll queries each view in order and reports whether any query failed.
func (p *Poller) pollAll(ctx context.Context) (failed bool) {
	for _, v := range p.views {
		prs, err := p.searcher.SearchPRs(ctx, v.Query)
		if ctx.Err() != nil {
			return failed
		}
		if err != nil {
			failed = true
		}
		select {
		case p.results <- Result{View: v.Name, PRs: prs, Err: err, At: time.Now()}:
		case <-ctx.Done():
			return failed
		}
	}
	return failed
}

// Backoff returns base × 2^failures, capped at 8× base.
func Backoff(base time.Duration, failures int) time.Duration {
	if failures > 3 {
		failures = 3
	}
	return base << failures
}
