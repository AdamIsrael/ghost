package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
)

// REST is the subset of go-gh's RESTClient that Client uses.
type REST interface {
	DoWithContext(ctx context.Context, method, path string, body io.Reader, response interface{}) error
}

func (c *Client) WithREST(r REST) *Client {
	c.rest = r
	return c
}

type ReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

type ReviewPost struct {
	CommitID string          `json:"commit_id"`
	Event    string          `json:"event"` // COMMENT, REQUEST_CHANGES or APPROVE
	Body     string          `json:"body"`
	Comments []ReviewComment `json:"comments,omitempty"`
}

// PostResult says which fallbacks PostReview needed.
type PostResult struct {
	Folded     bool // inline comments moved into the body
	Downgraded bool // event changed to COMMENT
}

// PostReview submits a review. GitHub answers 422 both when you request
// changes on (or approve) your own PR and when an inline comment is outside the
// diff. For the own-PR case it retries as a plain COMMENT, keeping the inline
// comments; for other 422s it retries with the comments folded into the body,
// then, if needed, as a COMMENT.
func (c *Client) PostReview(ctx context.Context, repo string, number int, p ReviewPost) (PostResult, error) {
	path := fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, number)
	for i := range p.Comments {
		if p.Comments[i].Side == "" {
			p.Comments[i].Side = "RIGHT"
		}
	}
	var res PostResult
	err := c.post(ctx, path, p)
	if ownPR(err) && p.Event != "COMMENT" {
		p.Event = "COMMENT"
		res.Downgraded = true
		err = c.post(ctx, path, p)
	}
	if unprocessable(err) && len(p.Comments) > 0 {
		p.Body += foldComments(p.Comments)
		p.Comments = nil
		res.Folded = true
		err = c.post(ctx, path, p)
	}
	if unprocessable(err) && p.Event != "COMMENT" {
		p.Event = "COMMENT"
		res.Downgraded = true
		err = c.post(ctx, path, p)
	}
	if err != nil {
		return PostResult{}, fmt.Errorf("post review: %w", err)
	}
	return res, nil
}

func (c *Client) post(ctx context.Context, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.rest.DoWithContext(ctx, "POST", path, bytes.NewReader(data), nil)
}

func unprocessable(err error) bool {
	var h *api.HTTPError
	return errors.As(err, &h) && h.StatusCode == 422
}

// ownPR reports GitHub's refusal to let you approve or request changes on
// your own pull request.
func ownPR(err error) bool {
	var h *api.HTTPError
	if !errors.As(err, &h) || h.StatusCode != 422 {
		return false
	}
	texts := []string{h.Message}
	for _, item := range h.Errors {
		texts = append(texts, item.Message)
	}
	return strings.Contains(strings.ToLower(strings.Join(texts, " ")), "your own pull request")
}

func foldComments(cs []ReviewComment) string {
	var b strings.Builder
	b.WriteString("\n\n**Inline comments** (GitHub could not attach these to the diff):\n")
	for _, c := range cs {
		fmt.Fprintf(&b, "- `%s:%d` %s\n", c.Path, c.Line, strings.ReplaceAll(c.Body, "\n", " "))
	}
	return b.String()
}

// HasReview reports whether any of the PR's reviews contains marker. Ghost
// uses it to avoid re-posting a review or approval whose request timed out,
// so it reads every page (reviews are listed oldest first).
func (c *Client) HasReview(ctx context.Context, repo string, number int, marker string) (bool, error) {
	const perPage = 100
	for page := 1; ; page++ {
		var reviews []struct {
			Body string `json:"body"`
		}
		path := fmt.Sprintf("repos/%s/pulls/%d/reviews?per_page=%d&page=%d", repo, number, perPage, page)
		if err := c.rest.DoWithContext(ctx, "GET", path, nil, &reviews); err != nil {
			return false, fmt.Errorf("list reviews: %w", err)
		}
		for _, r := range reviews {
			if strings.Contains(r.Body, marker) {
				return true, nil
			}
		}
		if len(reviews) < perPage {
			return false, nil
		}
	}
}

// IsClientError reports a definite 4xx rejection: the request did not take
// effect. Anything else (5xx, timeout, network) may have reached GitHub.
func IsClientError(err error) bool {
	var h *api.HTTPError
	return errors.As(err, &h) && h.StatusCode >= 400 && h.StatusCode < 500
}

// Approve submits an APPROVE review on commitID. Unlike PostReview it never
// falls back: if GitHub refuses (for example, your own pull request), the
// approval simply failed.
func (c *Client) Approve(ctx context.Context, repo string, number int, commitID, body string) error {
	path := fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, number)
	err := c.post(ctx, path, ReviewPost{CommitID: commitID, Event: "APPROVE", Body: body})
	if ownPR(err) {
		return fmt.Errorf("approve: GitHub does not allow approving your own pull request: %w", err)
	}
	if err != nil {
		return fmt.Errorf("approve: %w", err)
	}
	return nil
}
