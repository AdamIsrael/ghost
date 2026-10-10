// Package engine runs reviews: it queues PRs from poll results, runs them
// with a bounded worker pool under the daily budget, and records everything.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/poller"
	"github.com/AdamIsrael/ghost/internal/secrets"
	"github.com/AdamIsrael/ghost/internal/store"
	"github.com/AdamIsrael/ghost/internal/workspace"
)

type Deps struct {
	Store       *store.Store
	GetPR       func(ctx context.Context, repo string, number int) (github.PRDetail, error)
	Checkout    func(ctx context.Context, repo string, number int, baseRef, headSHA string) (workspace.Workspace, func(), error)
	NewReviewer func(name string, a config.Agent) (agent.Reviewer, error)
	PostReview  func(ctx context.Context, repo string, number int, p github.ReviewPost) (github.PostResult, error)
	HasReview   func(ctx context.Context, repo string, number int, marker string) (bool, error)
	Now         func() time.Time
}

type Snapshot struct {
	SpendTodayUSD float64
	MaxDailyUSD   float64
	Running       int
	Queued        int
	Paused        bool
	BudgetPaused  bool // reviews are queued but the daily budget is reached
}

type PRState struct {
	Audit    []store.AuditEvent
	Latest   *store.Review
	Comments []agent.InlineComment // Latest's comments, decoded
}

// tick re-checks the queue (budget may have reset at midnight).
const tick = 30 * time.Second

type Engine struct {
	cfg     config.Config
	d       Deps
	polls   chan poller.Result
	wake    chan struct{}
	changes chan struct{}

	rereviewMu sync.Mutex // serializes Rereview's check-then-enqueue

	mu           sync.Mutex
	paused       bool
	budgetPaused bool
	running      int
}

func New(cfg config.Config, d Deps) *Engine {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Engine{
		cfg:     cfg,
		d:       d,
		polls:   make(chan poller.Result, 16),
		wake:    make(chan struct{}, 1),
		changes: make(chan struct{}, 1),
	}
}

// Changes signals (coalesced) whenever engine state may have changed.
func (e *Engine) Changes() <-chan struct{} { return e.changes }

func (e *Engine) notify() {
	select {
	case e.changes <- struct{}{}:
	default:
	}
}

func (e *Engine) poke() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// HandlePoll hands a poll result to the engine without blocking. If the
// engine is busy the result is dropped; the next poll carries the same PRs.
func (e *Engine) HandlePoll(r poller.Result) {
	select {
	case e.polls <- r:
	default:
	}
}

// Run settles reviews a previous process left in flight, then processes
// poll results and runs reviews until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	if rs, err := e.d.Store.FailInterrupted(e.cfg.MaxCostPerReviewUSD); err == nil {
		for _, r := range rs {
			if r.State == store.Failed {
				e.audit(r.PRID, r.ID, "interrupted", fmt.Sprintf("ghost exited while this review was running; charged $%.2f (cost unknown). Press r to retry", r.CostUSD))
			} else {
				e.audit(r.PRID, r.ID, "post unknown", "ghost exited while posting; P checks GitHub before posting again")
			}
		}
	}
	for i := 0; i < e.cfg.MaxConcurrentReviews; i++ {
		go e.worker(ctx)
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	e.poke()
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-e.polls:
			e.enqueue(r)
		case <-t.C:
			e.poke()
			e.notify()
		}
	}
}

func (e *Engine) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
		}
		for ctx.Err() == nil {
			r := e.claim()
			if r == nil {
				break
			}
			e.poke() // let another worker claim the next one
			e.process(ctx, r)
		}
	}
}

func (e *Engine) view(name string) (config.View, bool) {
	for _, v := range e.cfg.Views {
		if v.Name == name {
			return v, true
		}
	}
	return config.View{}, false
}

func (e *Engine) enqueue(res poller.Result) {
	v, ok := e.view(res.View)
	if !ok || !v.AutoReview || res.PRs == nil {
		return
	}
	agentName := v.Agent
	if agentName == "" {
		agentName = e.cfg.DefaultAgent
	}
	created := false
	for _, pr := range res.PRs {
		if pr.IsDraft || pr.HeadSHA == "" {
			continue
		}
		prID, err := e.d.Store.UpsertPR(pr)
		if err != nil {
			continue
		}
		if e.enqueueAudited(prID, pr.HeadSHA, agentName, false,
			fmt.Sprintf("view %q, agent %s, commit %.7s", v.Name, agentName, pr.HeadSHA)) {
			created = true
		}
	}
	if created {
		e.poke()
		e.notify()
	}
}

// enqueueAudited inserts a review and its "queued" audit row under e.mu,
// which claim also holds, so no worker can start it (and audit "started")
// before "queued" is recorded.
func (e *Engine) enqueueAudited(prID int64, headSHA, agentName string, force bool, msg string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	id, ok, err := e.d.Store.EnqueueReview(prID, headSHA, agentName, force)
	if err != nil || !ok {
		return false
	}
	e.audit(prID, id, "queued", msg)
	return true
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// claim takes the next queued review if the engine isn't paused and the
// budget allows one more concurrent review at the per-review cap.
func (e *Engine) claim() *store.Review {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.paused {
		return nil
	}
	spend, err := e.d.Store.SpendSince(startOfDay(e.d.Now()))
	if err != nil {
		return nil
	}
	if spend+float64(e.running+1)*e.cfg.MaxCostPerReviewUSD > e.cfg.MaxDailyCostUSD {
		queued, _ := e.d.Store.CountState(store.Queued)
		e.budgetPaused = queued > 0
		return nil
	}
	r, err := e.d.Store.ClaimNext()
	if err != nil {
		// Transient store error: try again soon rather than at the next tick.
		time.AfterFunc(time.Second, e.poke)
		return nil
	}
	if r == nil {
		return nil
	}
	e.budgetPaused = false
	e.running++
	return r
}

func (e *Engine) process(ctx context.Context, r *store.Review) {
	defer func() {
		e.mu.Lock()
		e.running--
		e.mu.Unlock()
		e.notify()
		e.poke()
	}()
	e.audit(r.PRID, r.ID, "started", "agent "+r.Agent)
	e.notify()
	if err := e.review(ctx, r); err != nil {
		if ctx.Err() != nil {
			// Shutting down mid-run: the agent was killed without a cost
			// report, so charge the cap and don't retry automatically.
			r.State, r.Error = store.Failed, "interrupted: ghost exited while it was running"
			r.CostUSD = max(r.CostUSD, e.cfg.MaxCostPerReviewUSD)
			e.save(r)
			e.audit(r.PRID, r.ID, "interrupted", fmt.Sprintf("charged $%.2f (cost unknown). Press r to retry", r.CostUSD))
			return
		}
		r.State, r.Error = store.Failed, err.Error()
		e.save(r)
		e.audit(r.PRID, r.ID, "failed", fmt.Sprintf("%v (spent $%.2f)", err, r.CostUSD))
	}
}

func (e *Engine) review(ctx context.Context, r *store.Review) error {
	a, ok := e.cfg.Agents[r.Agent]
	if !ok {
		return fmt.Errorf("agent %q is no longer configured", r.Agent)
	}
	pr, err := e.d.GetPR(ctx, r.Repo, r.Number)
	if err != nil {
		return err
	}
	if pr.HeadSHA != r.HeadSHA {
		return fmt.Errorf("superseded: PR head moved to %.7s", pr.HeadSHA)
	}
	rendered, err := RenderPrompt(&e.cfg, r.Agent, pr)
	if err != nil {
		return err
	}
	reviewer, err := e.d.NewReviewer(r.Agent, a)
	if err != nil {
		return err
	}
	ws, cleanup, err := e.d.Checkout(ctx, r.Repo, r.Number, pr.BaseRef, pr.HeadSHA)
	if err != nil {
		return err
	}
	defer cleanup()

	r.Prompt = agent.BuildPrompt(rendered, ws.DiffPath, ws.CommitsPath)
	res, err := reviewer.Review(ctx, agent.ReviewRequest{
		Dir: ws.Dir, ContextDir: ws.ContextDir, Prompt: r.Prompt, MaxCostUSD: e.cfg.MaxCostPerReviewUSD,
	})
	r.CostUSD = res.CostUSD
	if err != nil {
		return err
	}
	comments, err := json.Marshal(res.Comments)
	if err != nil {
		return err
	}
	r.Verdict, r.Summary, r.Comments = string(res.Verdict), res.Summary, string(comments)

	if found := secrets.Scan(reviewText(r.Summary, res.Comments)); len(found) > 0 {
		r.State, r.Error = store.Blocked, "possible secret in review: "+secrets.Kinds(found)
		e.save(r)
		e.audit(r.PRID, r.ID, "blocked", r.Error+"; it will not be posted")
		return nil
	}
	r.State = store.Ready
	e.save(r)
	e.audit(r.PRID, r.ID, "reviewed", fmt.Sprintf("%s, %d comments, $%.2f", res.Verdict, len(res.Comments), res.CostUSD))
	if e.cfg.PostMode == "auto" {
		_ = e.post(ctx, r) // a failed post leaves it ready; audited by post
		return nil
	}
	e.audit(r.PRID, r.ID, "ready to post", "press P to post it")
	return nil
}

func reviewText(summary string, comments []agent.InlineComment) string {
	var b strings.Builder
	b.WriteString(summary)
	for _, c := range comments {
		b.WriteString("\n" + c.Path + "\n" + c.Body)
	}
	return b.String()
}

func (e *Engine) save(r *store.Review) { _ = e.d.Store.SaveReview(r) }

func (e *Engine) audit(prID, reviewID int64, kind, msg string) {
	_ = e.d.Store.AddAudit(prID, reviewID, kind, msg)
	e.notify()
}

// TogglePause stops or resumes starting new reviews and returns the new state.
func (e *Engine) TogglePause() bool {
	e.mu.Lock()
	e.paused = !e.paused
	paused := e.paused
	e.mu.Unlock()
	if !paused {
		e.poke()
	}
	e.notify()
	return paused
}

func (e *Engine) Snapshot() Snapshot {
	spend, _ := e.d.Store.SpendSince(startOfDay(e.d.Now()))
	queued, _ := e.d.Store.CountState(store.Queued)
	e.mu.Lock()
	defer e.mu.Unlock()
	return Snapshot{
		SpendTodayUSD: spend, MaxDailyUSD: e.cfg.MaxDailyCostUSD,
		Running: e.running, Queued: queued, Paused: e.paused, BudgetPaused: e.budgetPaused,
	}
}

// PRState returns a PR's recent audit trail and latest review.
func (e *Engine) PRState(repo string, number int) PRState {
	var st PRState
	st.Audit, _ = e.d.Store.Audit(repo, number, 20)
	st.Latest, _ = e.d.Store.LatestReview(repo, number)
	if st.Latest != nil {
		_ = json.Unmarshal([]byte(st.Latest.Comments), &st.Comments)
	}
	return st
}
