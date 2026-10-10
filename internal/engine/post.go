package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/secrets"
	"github.com/AdamIsrael/ghost/internal/store"
)

// PostPending posts a PR's finished review (state ready), or retries one
// whose earlier post may not have reached GitHub (state post-unknown).
func (e *Engine) PostPending(ctx context.Context, repo string, number int) error {
	ctx, done, err := e.op(ctx)
	if err != nil {
		return err
	}
	defer done()
	r, err := e.d.Store.LatestReview(repo, number)
	if err != nil {
		return err
	}
	if r == nil || (r.State != store.Ready && r.State != store.PostUnknown) {
		return errors.New("no review is waiting to be posted")
	}
	return e.post(ctx, r)
}

func marker(id int64) string { return fmt.Sprintf("<!-- ghost-review-id:%d -->", id) }

// post publishes r. It first claims the review (ready/post-unknown → posting)
// so concurrent callers can't post it twice. A definite 4xx rejection returns
// it to ready; any other failure leaves it post-unknown, and the next attempt
// checks GitHub for its marker before posting again.
func (e *Engine) post(ctx context.Context, r *store.Review) error {
	from := r.State
	ok, err := e.d.Store.TransitionState(r.ID, from, store.Posting)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("this review is already being posted")
	}
	r.State = store.Posting
	e.notify()

	if from == store.PostUnknown && e.d.HasReview != nil {
		found, err := e.d.HasReview(ctx, r.Repo, r.Number, marker(r.ID))
		if err != nil {
			r.State = store.PostUnknown
			e.save(r)
			return err
		}
		if found {
			r.State = store.Posted
			e.save(r)
			e.audit(r.PRID, r.ID, "posted", "found on GitHub from the earlier attempt; not posted again")
			e.evaluateApproval(ctx, r, nil)
			return nil
		}
	}

	var comments []agent.InlineComment
	if err := json.Unmarshal([]byte(r.Comments), &comments); err != nil {
		r.State = from
		e.save(r)
		return err
	}
	// Re-check: the stored text is what gets posted.
	if found := secrets.Scan(reviewText(r.Summary, comments)); len(found) > 0 {
		r.State, r.Error = store.Blocked, "possible secret in review: "+secrets.Kinds(found)
		e.save(r)
		e.audit(r.PRID, r.ID, "blocked", r.Error+"; it will not be posted")
		return errors.New(r.Error)
	}
	footer, err := e.message(e.cfg.ReviewFooter, r)
	if err != nil {
		r.State = from
		e.save(r)
		return fmt.Errorf("review_footer: %w", err)
	}
	body := r.Summary
	if footer != "" {
		body += "\n\n" + footer
	}
	p := github.ReviewPost{CommitID: r.HeadSHA, Event: "COMMENT", Body: body + "\n" + marker(r.ID)}
	if r.Verdict == string(agent.RequestChanges) {
		p.Event = "REQUEST_CHANGES"
	}
	for _, c := range comments {
		label := "**Nit:** "
		if c.Severity == "blocking" {
			label = "**Blocking:** "
		}
		p.Comments = append(p.Comments, github.ReviewComment{Path: c.Path, Line: c.Line, Body: label + c.Body})
	}
	res, err := e.d.PostReview(ctx, r.Repo, r.Number, p)
	if err != nil {
		if github.IsClientError(err) {
			r.State = store.Ready
			e.save(r)
			e.audit(r.PRID, r.ID, "post failed", err.Error())
		} else {
			r.State = store.PostUnknown
			e.save(r)
			e.audit(r.PRID, r.ID, "post failed", err.Error()+"; it may have reached GitHub, so P checks before posting again")
		}
		return err
	}
	r.State = store.Posted
	e.save(r)
	msg := fmt.Sprintf("%s with %d inline comments", p.Event, len(p.Comments))
	if res.Folded {
		msg += "; comments folded into the body (GitHub rejected their positions)"
	}
	if res.Downgraded {
		msg += "; posted as a comment (GitHub rejected the review event)"
	}
	e.audit(r.PRID, r.ID, "posted", msg)
	e.evaluateApproval(ctx, r, nil)
	return nil
}

// Rereview queues a fresh review of pr's head, using the PR's last agent (or
// the default), unless one is already queued or running.
func (e *Engine) Rereview(pr github.PR) error {
	e.rereviewMu.Lock()
	defer e.rereviewMu.Unlock()
	latest, err := e.d.Store.LatestReview(pr.Repo, pr.Number)
	if err != nil {
		return err
	}
	if latest != nil && (latest.State == store.Queued || latest.State == store.Running) {
		return errors.New("a review of this PR is already queued or running")
	}
	agentName := e.cfg.DefaultAgent
	if latest != nil {
		if _, ok := e.cfg.Agents[latest.Agent]; ok {
			agentName = latest.Agent
		}
	}
	prID, err := e.d.Store.UpsertPR(pr)
	if err != nil {
		return err
	}
	if !e.enqueueAudited(prID, pr.HeadSHA, agentName, true,
		fmt.Sprintf("re-review requested, agent %s, commit %.7s", agentName, pr.HeadSHA)) {
		return errors.New("could not queue the re-review")
	}
	e.poke()
	return nil
}

// message renders an operator message template (review_footer or
// approval_message) for r. An empty template renders as "".
func (e *Engine) message(text string, r *store.Review) (string, error) {
	return config.RenderMessage(text, config.MessageData{
		Agent: r.Agent, Model: e.cfg.Agents[r.Agent].Model,
		Repo: r.Repo, Number: r.Number, HeadSHA: fmt.Sprintf("%.7s", r.HeadSHA),
	})
}
