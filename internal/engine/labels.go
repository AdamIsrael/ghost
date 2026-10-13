package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/store"
)

const labelTimeout = 30 * time.Second

// labelJob is GitHub work the label gate needs: add the label, or confirm
// that a label Ghost recorded is really gone before counting an opt-out
// (search results can lag behind label changes).
type labelJob struct {
	prID    int64
	pr      github.PR
	label   string
	confirm bool
}

// gateLabel applies the review-label rules to one PR from a poll. It reports
// whether the PR may be queued now, plus any GitHub work for labelPRs.
func (e *Engine) gateLabel(prID int64, pr github.PR, label string) (bool, *labelJob) {
	if label == "" {
		return true, nil
	}
	if pr.HasLabel(label) {
		// Record it even if a person added it, so removing it later is an
		// opt-out rather than a reason to add it again.
		_ = e.d.Store.MarkLabeled(prID, label)
		if changed, err := e.d.Store.ClearOptOut(prID, label); err == nil && changed {
			e.audit(prID, 0, "label re-added", label+" is back; reviews resume")
		}
		return true, nil
	}
	state, err := e.d.Store.LabelState(prID, label)
	switch {
	case err != nil, state == store.LabelOptedOut:
		return false, nil
	case state == store.LabelAdded:
		return false, &labelJob{prID: prID, pr: pr, label: label, confirm: true}
	default:
		return false, &labelJob{prID: prID, pr: pr, label: label}
	}
}

// labelPRs runs the GitHub side of the label gate. Nothing is queued here:
// a labeled PR is reviewed when a later poll shows the label.
func (e *Engine) labelPRs(ctx context.Context, jobs []labelJob) {
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		jctx, cancel := context.WithTimeout(ctx, labelTimeout)
		if j.confirm {
			e.confirmOptOut(jctx, j)
		} else {
			e.addLabel(jctx, j)
		}
		cancel()
	}
}

func (e *Engine) addLabel(ctx context.Context, j labelJob) {
	if e.d.AddLabel == nil {
		e.labelFailed(j.prID, "Ghost cannot add labels in this mode")
		return
	}
	if err := e.d.AddLabel(ctx, j.pr.Repo, j.pr.Number, j.label); err != nil {
		e.labelFailed(j.prID, err.Error())
		return
	}
	e.mu.Lock()
	delete(e.labelErr, j.prID)
	e.mu.Unlock()
	if err := e.d.Store.MarkLabeled(j.prID, j.label); err != nil {
		return // the next poll sees the label on the PR and records it
	}
	e.audit(j.prID, 0, "labeled", fmt.Sprintf("added %s; reviews on the next poll (remove the label to opt out)", j.label))
}

// confirmOptOut checks GitHub directly before treating a missing label as
// removed: the poll may have come from a search index that hadn't caught up.
func (e *Engine) confirmOptOut(ctx context.Context, j labelJob) {
	d, err := e.d.GetPR(ctx, j.pr.Repo, j.pr.Number)
	if err != nil || d.HasLabel(j.label) {
		return // unknown, or still labeled: decide on a later poll
	}
	if changed, err := e.d.Store.MarkOptedOut(j.prID, j.label); err == nil && changed {
		e.audit(j.prID, 0, "opted out", fmt.Sprintf("%s was removed; not reviewing this PR until it is added back", j.label))
	}
}

// labelFailed audits a labeling error, once per distinct message per PR.
func (e *Engine) labelFailed(prID int64, msg string) {
	e.mu.Lock()
	same := e.labelErr[prID] == msg
	e.labelErr[prID] = msg
	e.mu.Unlock()
	if !same {
		e.audit(prID, 0, "label failed", msg+"; not reviewing until the label is added (retried every poll)")
	}
}
