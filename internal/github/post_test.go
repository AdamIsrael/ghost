package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
)

type restCall struct {
	method, path string
	body         ReviewPost
}

type fakeREST struct {
	calls   []restCall
	errs    []error // returned in order; nil when exhausted
	getBody string  // JSON returned for GET requests
}

func (f *fakeREST) DoWithContext(_ context.Context, method, path string, body io.Reader, resp interface{}) error {
	var p ReviewPost
	if body != nil {
		data, _ := io.ReadAll(body)
		json.Unmarshal(data, &p)
	}
	f.calls = append(f.calls, restCall{method, path, p})
	if method == "GET" && resp != nil {
		json.Unmarshal([]byte(f.getBody), resp)
	}
	if len(f.errs) == 0 {
		return nil
	}
	err := f.errs[0]
	f.errs = f.errs[1:]
	return err
}

var err422 = &api.HTTPError{StatusCode: 422, Message: "Unprocessable Entity"}

func samplePost() ReviewPost {
	return ReviewPost{CommitID: "abc", Event: "REQUEST_CHANGES", Body: "Summary",
		Comments: []ReviewComment{{Path: "a.go", Line: 3, Body: "**Blocking:** nil deref"}}}
}

func TestPostReview(t *testing.T) {
	f := &fakeREST{}
	res, err := New(nil).WithREST(f).PostReview(context.Background(), "o/r", 7, samplePost())
	if err != nil || res.Folded || res.Downgraded {
		t.Fatalf("res %+v err %v", res, err)
	}
	if len(f.calls) != 1 || f.calls[0].method != "POST" || f.calls[0].path != "repos/o/r/pulls/7/reviews" {
		t.Fatalf("calls = %+v", f.calls)
	}
	got := f.calls[0].body
	if got.CommitID != "abc" || got.Event != "REQUEST_CHANGES" || len(got.Comments) != 1 || got.Comments[0].Side != "RIGHT" {
		t.Errorf("body = %+v", got)
	}
}

func TestPostReviewFallbackChain(t *testing.T) {
	f := &fakeREST{errs: []error{err422, err422}}
	res, err := New(nil).WithREST(f).PostReview(context.Background(), "o/r", 7, samplePost())
	if err != nil || !res.Folded || !res.Downgraded || len(f.calls) != 3 {
		t.Fatalf("res %+v err %v calls %d", res, err, len(f.calls))
	}
	folded := f.calls[1].body
	if len(folded.Comments) != 0 || !strings.Contains(folded.Body, "`a.go:3`") || !strings.Contains(folded.Body, "nil deref") || folded.Event != "REQUEST_CHANGES" {
		t.Errorf("folded attempt = %+v", folded)
	}
	if last := f.calls[2].body; last.Event != "COMMENT" || len(last.Comments) != 0 {
		t.Errorf("downgraded attempt = %+v", last)
	}
}

func TestPostReviewFoldOnlySucceeds(t *testing.T) {
	f := &fakeREST{errs: []error{err422}}
	res, err := New(nil).WithREST(f).PostReview(context.Background(), "o/r", 7, samplePost())
	if err != nil || !res.Folded || res.Downgraded || len(f.calls) != 2 {
		t.Fatalf("res %+v err %v calls %d", res, err, len(f.calls))
	}
}

func TestPostReviewNoRetryOnOtherErrors(t *testing.T) {
	f := &fakeREST{errs: []error{&api.HTTPError{StatusCode: 500}}}
	if _, err := New(nil).WithREST(f).PostReview(context.Background(), "o/r", 7, samplePost()); err == nil || len(f.calls) != 1 {
		t.Fatalf("err %v calls %d", err, len(f.calls))
	}
	f = &fakeREST{errs: []error{errors.New("network down")}}
	if _, err := New(nil).WithREST(f).PostReview(context.Background(), "o/r", 7, samplePost()); err == nil || len(f.calls) != 1 {
		t.Fatalf("err %v calls %d", err, len(f.calls))
	}
}

func TestPostReviewGivesUpAfterChain(t *testing.T) {
	p := samplePost()
	p.Event = "COMMENT"
	f := &fakeREST{errs: []error{err422, err422}}
	if _, err := New(nil).WithREST(f).PostReview(context.Background(), "o/r", 7, p); err == nil || len(f.calls) != 2 {
		t.Fatalf("COMMENT reviews have nothing to downgrade: err %v calls %d", err, len(f.calls))
	}
}

func TestPostReviewOwnPRDowngradesAndKeepsInlineComments(t *testing.T) {
	own := &api.HTTPError{StatusCode: 422, Message: "Unprocessable Entity",
		Errors: []api.HTTPErrorItem{{Message: "Review Can not request changes on your own pull request"}}}
	f := &fakeREST{errs: []error{own}}
	res, err := New(nil).WithREST(f).PostReview(context.Background(), "o/r", 7, samplePost())
	if err != nil || !res.Downgraded || res.Folded || len(f.calls) != 2 {
		t.Fatalf("res %+v err %v calls %d", res, err, len(f.calls))
	}
	if second := f.calls[1].body; second.Event != "COMMENT" || len(second.Comments) != 1 {
		t.Errorf("own-PR retry should keep inline comments as a COMMENT review: %+v", second)
	}
}

func TestPostReviewOwnPRMessageInMessageField(t *testing.T) {
	own := &api.HTTPError{StatusCode: 422, Message: "Unprocessable Entity\nCan not approve your own pull request"}
	f := &fakeREST{errs: []error{own}}
	res, err := New(nil).WithREST(f).PostReview(context.Background(), "o/r", 7, samplePost())
	if err != nil || !res.Downgraded || res.Folded {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestHasReview(t *testing.T) {
	f := &fakeREST{getBody: `[{"body":"other"},{"body":"Summary\n<!-- ghost-review-id:42 -->"}]`}
	c := New(nil).WithREST(f)
	found, err := c.HasReview(context.Background(), "o/r", 7, "<!-- ghost-review-id:42 -->")
	if err != nil || !found {
		t.Fatalf("found %v err %v", found, err)
	}
	if f.calls[0].method != "GET" || f.calls[0].path != "repos/o/r/pulls/7/reviews?per_page=100" {
		t.Errorf("call = %+v", f.calls[0])
	}
	if found, _ := c.HasReview(context.Background(), "o/r", 7, "<!-- ghost-review-id:7 -->"); found {
		t.Error("unexpected match")
	}
}

func TestIsClientError(t *testing.T) {
	if !IsClientError(fmt.Errorf("post review: %w", &api.HTTPError{StatusCode: 422})) {
		t.Error("422 is a client error")
	}
	for _, err := range []error{&api.HTTPError{StatusCode: 502}, errors.New("timeout"), context.DeadlineExceeded} {
		if IsClientError(err) {
			t.Errorf("%v must not count as a definite client error", err)
		}
	}
}
