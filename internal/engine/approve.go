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
	if err := json.Unmarshal([]byte(r.Comments), &comments); err != nil {
		// Fail closed: unreadable comments could hide blocking ones.
		return policy.Decision{Reasons: []string{"review comments are unreadable"}}
	}
	return policy.Evaluate(e.cfg.AutoApprove, policy.Input{PR: pr, Verdict: r.Verdict, Comments: comments})
}

// blockedForApproval returns why r may not lead to an approval regardless of
// the rules: a newer review of the PR exists, or Ghost already approved (or
// may have approved) this commit through another review.
func (e *Engine) blockedForApproval(r *store.Review) string {
	if latest, err := e.d.Store.LatestReview(r.Repo, r.Number); err != nil || latest == nil || latest.ID != r.ID {
		if latest != nil {
			return fmt.Sprintf("superseded by review #%d", latest.ID)
		}
		return "could not confirm this is the latest review"
	}
	if done, err := e.d.Store.HeadApproved(r.PRID, r.HeadSHA, r.ID); err != nil || done {
		return "Ghost already approved this commit"
	}
	return ""
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
	if why := e.blockedForApproval(r); why != "" {
		e.setApproval(r, store.ApprovalNo, why)
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

// setApproval saves an approval state and audits it if it differs from the
// state r held before (repeats are saved but not audited again).
func (e *Engine) setApproval(r *store.Review, state, note string) {
	changed := r.Approval != state || r.ApprovalNote != note
	r.Approval, r.ApprovalNote = state, note
	e.save(r)
	if state == store.ApprovalUnknown {
		e.mu.Lock()
		e.unknownAt[r.ID] = e.d.Now()
		e.mu.Unlock()
	}
	if !changed {
		return
	}
	kind := map[string]string{
		store.ApprovalWaiting: "approval waiting", store.ApprovalPending: "approval pending",
		store.ApprovalNo: "not approved", store.ApprovalApproved: "approved",
		store.ApprovalFailed: "approval failed", store.ApprovalUnknown: "approval failed",
	}[state]
	if kind != "" {
		e.audit(r.PRID, r.ID, kind, note)
	}
}

// recheckApprovals re-evaluates waiting approvals for the PRs in a poll
// result. In auto mode it also retries approvals whose result is unknown
// (approve checks GitHub for the earlier one first).
func (e *Engine) recheckApprovals(ctx context.Context, res poller.Result) {
	if !e.cfg.AutoApprove.Enabled || res.PRs == nil {
		return
	}
	states := []string{store.ApprovalWaiting}
	if !e.cfg.AutoApprove.RequireConfirm {
		states = append(states, store.ApprovalUnknown)
	}
	rs, err := e.d.Store.ApprovalsFor(res.PRs, states...)
	if err != nil {
		return
	}
	for i := range rs {
		r := &rs[i]
		if r.Approval == store.ApprovalUnknown {
			// Give an in-flight approval time to appear on GitHub before
			// checking for it; otherwise a retry could approve twice.
			e.mu.Lock()
			at, seen := e.unknownAt[r.ID]
			e.mu.Unlock()
			if seen && e.d.Now().Sub(at) < e.cfg.PollInterval.Duration {
				continue
			}
			_ = e.approve(ctx, r)
			continue
		}
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
	if why := e.blockedForApproval(r); why != "" {
		e.setApproval(r, store.ApprovalNo, why)
		return errors.New(why)
	}
	from := r.Approval
	ok, err := e.d.Store.TransitionApproval(r.ID, from, store.ApprovalApproving)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("this PR is already being approved")
	}
	// r keeps its pre-claim state in memory so setApproval audits only real
	// changes; the database holds "approving" until the outcome is saved.
	e.notify()
	marker := approvalMarker(r.ID)

	if from == store.ApprovalUnknown && e.d.HasReview == nil {
		r.Approval = from
		e.save(r)
		return errors.New("cannot check GitHub for the earlier approval, so it is not retried")
	}
	if from == store.ApprovalUnknown {
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
		// Nothing was sent. In auto mode fall back to waiting so the next
		// poll tries again; with confirm, hand it back to the operator.
		if !e.cfg.AutoApprove.RequireConfirm && from == store.ApprovalPending {
			e.setApproval(r, store.ApprovalWaiting, "could not read the PR's status: "+err.Error())
			return err
		}
		r.Approval = from
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
