package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/secrets"
	"github.com/AdamIsrael/ghost/internal/store"
)

// PostPending posts a PR's finished review (state ready).
func (e *Engine) PostPending(ctx context.Context, repo string, number int) error {
	r, err := e.d.Store.LatestReview(repo, number)
	if err != nil {
		return err
	}
	if r == nil || r.State != store.Ready {
		return errors.New("no review is waiting to be posted")
	}
	return e.post(ctx, r)
}

func (e *Engine) post(ctx context.Context, r *store.Review) error {
	var comments []agent.InlineComment
	if err := json.Unmarshal([]byte(r.Comments), &comments); err != nil {
		return err
	}
	// Re-check: the stored text is what gets posted.
	if found := secrets.Scan(reviewText(r.Summary, comments)); len(found) > 0 {
		r.State, r.Error = store.Blocked, "possible secret in review: "+secrets.Kinds(found)
		e.save(r)
		e.audit(r.PRID, r.ID, "blocked", r.Error+"; it will not be posted")
		return errors.New(r.Error)
	}
	p := github.ReviewPost{
		CommitID: r.HeadSHA,
		Event:    "COMMENT",
		Body:     fmt.Sprintf("%s\n\n<sub>Automated review by Ghost · agent `%s` · commit %.7s</sub>", r.Summary, r.Agent, r.HeadSHA),
	}
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
		e.audit(r.PRID, r.ID, "post failed", err.Error())
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
	return nil
}

// Rereview queues a fresh review of pr's head, using the PR's last agent (or
// the default), unless one is already queued or running.
func (e *Engine) Rereview(pr github.PR) error {
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
	id, _, err := e.d.Store.EnqueueReview(prID, pr.HeadSHA, agentName, true)
	if err != nil {
		return err
	}
	e.audit(prID, id, "queued", fmt.Sprintf("re-review requested, agent %s, commit %.7s", agentName, pr.HeadSHA))
	e.poke()
	return nil
}
