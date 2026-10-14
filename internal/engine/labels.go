package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/store"
)

const labelTimeout = 30 * time.Second

// labelJob is GitHub work the label gate needs: add the label, or check the
// PR directly before acting on what a poll showed (search results can lag
// behind label changes, in both directions).
type labelJob struct {
	prID  int64
	pr    github.PR
	label string
	check bool   // read the PR, then queue it or record an opt-out
	view  string // the view and query that produced the job; checkPR
	query string // queues only if the view still matches
}

// gateLabel applies the review-label rules to one PR from a poll. It reports
// whether the PR may be queued now, plus any GitHub work for the label
// batches. When a label applies, nothing is queued here: a check job reads
// the PR first.
func (e *Engine) gateLabel(prID int64, pr github.PR, label string) (bool, *labelJob) {
	if label == "" {
		return true, nil
	}
	if pr.HasLabel(label) {
		// Record it even if a person added it, so removing it later is an
		// opt-out rather than a reason to add it again.
		if err := e.d.Store.MarkLabeled(prID, label); err != nil {
			return false, nil // fail closed: skip the PR this poll
		}
	}
	state, err := e.d.Store.LabelState(prID, label)
	if err != nil {
		return false, nil
	}
	check := &labelJob{prID: prID, pr: pr, label: label, check: true}
	if !pr.HasLabel(label) {
		switch state {
		case store.LabelOptedOut:
			return false, nil
		case store.LabelAdded:
			return false, check // gone, or search is behind
		default:
			return false, &labelJob{prID: prID, pr: pr, label: label}
		}
	}
	if state == store.LabelAdded {
		reviewed, err := e.d.Store.Reviewed(prID, pr.HeadSHA)
		if err != nil || reviewed {
			return false, nil // nothing to queue for this commit
		}
	}
	return false, check // a new commit to review, or an opt-out to clear
}

func splitLabelJobs(jobs []labelJob) (adds, checks []labelJob) {
	for _, j := range jobs {
		if j.check {
			checks = append(checks, j)
		} else {
			adds = append(adds, j)
		}
	}
	return adds, checks
}

// startLabelBatch runs jobs off the Run loop, one batch per mutex at a time.
// It must be called from the Run loop (wg.Add before shutdown's wg.Wait).
func (e *Engine) startLabelBatch(ctx context.Context, mu *sync.Mutex, jobs []labelJob) {
	if len(jobs) == 0 || !mu.TryLock() {
		return
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer mu.Unlock()
		e.labelPRs(ctx, jobs)
	}()
}

// labelPRs runs the GitHub side of the label gate.
func (e *Engine) labelPRs(ctx context.Context, jobs []labelJob) {
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		jctx, cancel := context.WithTimeout(ctx, labelTimeout)
		if j.check {
			e.checkPR(ctx, jctx, j)
		} else {
			e.addLabel(ctx, jctx, j)
		}
		cancel()
	}
}

func (e *Engine) addLabel(ctx, jctx context.Context, j labelJob) {
	// The job may be stale (a person removed the label meanwhile, recorded as
	// an opt-out): only add the label for a PR still in the never-labeled state.
	if st, err := e.d.Store.LabelState(j.prID, j.label); err != nil || st != store.LabelNever {
		return
	}
	if e.d.AddLabel == nil {
		e.labelProblem(j.prID, "label failed", "Ghost cannot add labels in this mode; not reviewing until the label is added")
		return
	}
	if err := e.d.AddLabel(jctx, j.pr.Repo, j.pr.Number, j.label); err != nil {
		if ctx.Err() != nil {
			return // shutting down, not a labeling failure
		}
		e.labelProblem(j.prID, "label failed", err.Error()+"; not reviewing until the label is added (retried every poll)")
		return
	}
	e.mu.Lock()
	delete(e.labelErr, j.prID)
	e.mu.Unlock()
	if err := e.d.Store.MarkLabeled(j.prID, j.label); err != nil {
		return // the next poll sees the label on the PR and records it
	}
	when := "reviews on the next poll"
	if reviewed, err := e.d.Store.Reviewed(j.prID, j.pr.HeadSHA); err == nil && reviewed {
		when = "this commit was already queued or reviewed, so Ghost reviews the next new commit"
	}
	e.audit(j.prID, 0, "labeled", fmt.Sprintf("added %s; %s (remove the label to opt out)", j.label, when))
}

// checkPR reads the PR directly before acting on a poll: search results can
// lag behind label changes. If the label is gone, that's an opt-out; if it's
// there, any opt-out is cleared and the current commit is queued, as long as
// the view still auto-reviews with this label. If the read fails, nothing
// changes and a later poll tries again.
func (e *Engine) checkPR(ctx, jctx context.Context, j labelJob) {
	d, err := e.d.GetPR(jctx, j.pr.Repo, j.pr.Number)
	if err != nil {
		if ctx.Err() == nil { // not just shutting down
			e.labelProblem(j.prID, "label check failed", "could not read the PR to check for "+j.label+": "+err.Error()+"; retried every poll")
		}
		return
	}
	e.mu.Lock()
	delete(e.labelErr, j.prID)
	e.mu.Unlock()
	if !d.HasLabel(j.label) {
		if changed, err := e.d.Store.MarkOptedOut(j.prID, j.label); err == nil && changed {
			e.audit(j.prID, 0, "opted out", fmt.Sprintf("%s was removed; not reviewing this PR until it is added back", j.label))
		}
		return
	}
	if err := e.d.Store.MarkLabeled(j.prID, j.label); err != nil {
		return
	}
	changed, err := e.d.Store.ClearOptOut(j.prID, j.label)
	if err != nil {
		return
	}
	if changed {
		e.audit(j.prID, 0, "label re-added", j.label+" is back; reviews resume")
	}
	if d.IsDraft || d.HeadSHA == "" {
		return
	}
	// The view may have changed while the read was in flight: queue only if
	// it still auto-reviews these PRs with this label.
	v, ok := e.view(j.view)
	if !ok || !v.AutoReview || (j.query != "" && j.query != v.Query) || !strings.EqualFold(e.cfg.LabelFor(v), j.label) {
		return
	}
	agentName := v.Agent
	if agentName == "" {
		agentName = e.cfg.DefaultAgent
	}
	if e.enqueueAudited(j.prID, d.HeadSHA, agentName,
		false, fmt.Sprintf("view %q, agent %s, commit %.7s", v.Name, agentName, d.HeadSHA)) {
		e.poke()
		e.notify()
	}
}

// labelProblem audits a label add or check failure once per distinct
// message per PR, so a PR that fails every poll doesn't flood its history.
func (e *Engine) labelProblem(prID int64, kind, msg string) {
	e.mu.Lock()
	same := e.labelErr[prID] == msg
	e.labelErr[prID] = msg
	e.mu.Unlock()
	if !same {
		e.audit(prID, 0, kind, msg)
	}
}
