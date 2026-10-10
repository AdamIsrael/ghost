package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/policy"
	"github.com/AdamIsrael/ghost/internal/poller"
	"github.com/AdamIsrael/ghost/internal/store"
)

func approvalMarker(id int64) string { return fmt.Sprintf("<!-- ghost-approval-id:%d -->", id) }

func (e *Engine) decide(r *store.Review, pr github.PR) policy.Decision {
	var comments []agent.InlineComment
	_ = json.Unmarshal([]byte(r.Comments), &comments)
	return policy.Evaluate(e.cfg.AutoApprove, policy.Input{PR: pr, Verdict: r.Verdict, Comments: comments})
}

// evaluateApproval sets a posted review's approval state from the rules. pr
// is the PR's latest known state; nil fetches it.
func (e *Engine) evaluateApproval(ctx context.Context, r *store.Review, pr *github.PR) {
	if !e.cfg.AutoApprove.Enabled || r.State != store.Posted {
		return
	}
	if pr == nil {
		d, err := e.d.GetPR(ctx, r.Repo, r.Number)
		if err != nil {
			e.setApproval(r, store.ApprovalWaiting, "could not read the PR's status: "+err.Error())
			return
		}
		pr = &d.PR
	}
	if pr.HeadSHA != r.HeadSHA {
		e.setApproval(r, store.ApprovalNo, fmt.Sprintf("superseded: PR head moved to %.7s", pr.HeadSHA))
		return
	}
	dec := e.decide(r, *pr)
	switch {
	case dec.Waiting:
		e.setApproval(r, store.ApprovalWaiting, dec.Summary())
	case !dec.Eligible:
		e.setApproval(r, store.ApprovalNo, dec.Summary())
	case e.cfg.AutoApprove.RequireConfirm:
		e.setApproval(r, store.ApprovalPending, "meets the auto-approve rules; press a to approve")
	default:
		e.setApproval(r, store.ApprovalPending, "meets the auto-approve rules")
		_ = e.approve(ctx, r) // outcome is audited
	}
}

// setApproval saves and audits an approval state change (repeats are silent).
func (e *Engine) setApproval(r *store.Review, state, note string) {
	if r.Approval == state && r.ApprovalNote == note {
		return
	}
	r.Approval, r.ApprovalNote = state, note
	e.save(r)
	kind := map[string]string{
		store.ApprovalWaiting: "approval waiting", store.ApprovalPending: "approval pending",
		store.ApprovalNo: "not approved", store.ApprovalApproved: "approved",
		store.ApprovalFailed: "approval failed", store.ApprovalUnknown: "approval failed",
	}[state]
	if kind != "" {
		e.audit(r.PRID, r.ID, kind, note)
	}
}

// recheckApprovals re-evaluates waiting approvals for PRs in a poll result.
func (e *Engine) recheckApprovals(ctx context.Context, res poller.Result) {
	if !e.cfg.AutoApprove.Enabled || res.PRs == nil {
		return
	}
	waiting, err := e.d.Store.WaitingApprovals()
	if err != nil || len(waiting) == 0 {
		return
	}
	for i := range waiting {
		r := &waiting[i]
		for _, pr := range res.PRs {
			if pr.Repo == r.Repo && pr.Number == r.Number {
				pr := pr
				e.evaluateApproval(ctx, r, &pr)
			}
		}
	}
}

// Approve approves a PR whose review is pending approval, or retries one
// whose earlier approval may not have reached GitHub.
func (e *Engine) Approve(ctx context.Context, repo string, number int) error {
	r, err := e.d.Store.LatestReview(repo, number)
	if err != nil {
		return err
	}
	if r == nil || (r.Approval != store.ApprovalPending && r.Approval != store.ApprovalUnknown) {
		return errors.New("nothing is waiting for approval")
	}
	return e.approve(ctx, r)
}

// approve claims the review's approval (pending/unknown → approving), checks
// the rules against the PR as it is now, and posts the approval.
func (e *Engine) approve(ctx context.Context, r *store.Review) error {
	from := r.Approval
	ok, err := e.d.Store.TransitionApproval(r.ID, from, store.ApprovalApproving)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("this PR is already being approved")
	}
	r.Approval = store.ApprovalApproving
	e.notify()
	marker := approvalMarker(r.ID)

	if from == store.ApprovalUnknown && e.d.HasReview != nil {
		found, err := e.d.HasReview(ctx, r.Repo, r.Number, marker)
		if err != nil {
			e.setApproval(r, store.ApprovalUnknown, "could not check GitHub: "+err.Error())
			return err
		}
		if found {
			e.setApproval(r, store.ApprovalApproved, "found on GitHub from the earlier attempt; not approved again")
			return nil
		}
	}

	d, err := e.d.GetPR(ctx, r.Repo, r.Number)
	if err != nil {
		r.Approval = from // release the claim; nothing was sent
		e.save(r)
		return err
	}
	if d.HeadSHA != r.HeadSHA {
		e.setApproval(r, store.ApprovalNo, fmt.Sprintf("superseded: PR head moved to %.7s", d.HeadSHA))
		return errors.New(r.ApprovalNote)
	}
	if dec := e.decide(r, d.PR); !dec.Eligible {
		state := store.ApprovalNo
		if dec.Waiting {
			state = store.ApprovalWaiting
		}
		e.setApproval(r, state, dec.Summary())
		return fmt.Errorf("no longer meets the auto-approve rules: %s", dec.Summary())
	}

	body := fmt.Sprintf("Approved by Ghost: the review found no blocking issues and the PR meets the auto-approve rules.\n\n<sub>agent `%s` · commit %.7s</sub>\n%s",
		r.Agent, r.HeadSHA, marker)
	if err := e.d.Approve(ctx, r.Repo, r.Number, r.HeadSHA, body); err != nil {
		if github.IsClientError(err) {
			e.setApproval(r, store.ApprovalFailed, err.Error())
		} else {
			e.setApproval(r, store.ApprovalUnknown, err.Error()+"; it may have reached GitHub, so a checks before approving again")
		}
		return err
	}
	e.setApproval(r, store.ApprovalApproved, "APPROVE posted")
	return nil
}
