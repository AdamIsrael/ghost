package github

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

func retryClient(f *fakeGQL) *Client {
	c := New(f)
	c.retryDelays = []time.Duration{0, 0}
	return c
}

var err502 = &api.HTTPError{StatusCode: 502, Message: "Bad Gateway"}

func TestSearchRetriesTransientErrors(t *testing.T) {
	for name, first := range map[string]error{
		"502":     err502,
		"503":     &api.HTTPError{StatusCode: 503},
		"network": &url.Error{Op: "Post", URL: "https://api.github.com/graphql", Err: errors.New("connection reset by peer")},
	} {
		f := &fakeGQL{fixture: "testdata/search.json", errs: []error{first}}
		prs, _, err := retryClient(f).SearchPRs(context.Background(), "q", 50)
		if err != nil || len(prs) != 2 || f.calls != 2 {
			t.Errorf("%s: prs %d err %v calls %d; want success on the retry", name, len(prs), err, f.calls)
		}
	}
}

func TestSearchGivesUpAfterThreeAttempts(t *testing.T) {
	f := &fakeGQL{fixture: "testdata/search.json", errs: []error{err502, err502, err502, err502}}
	_, _, err := retryClient(f).SearchPRs(context.Background(), "q", 50)
	if err == nil || f.calls != 3 {
		t.Errorf("err %v calls %d; want an error after 3 attempts", err, f.calls)
	}
}

func TestSearchDoesNotRetryPermanentErrors(t *testing.T) {
	for name, e := range map[string]error{
		"401":           &api.HTTPError{StatusCode: 401},
		"403 rate":      &api.HTTPError{StatusCode: 403},
		"422":           &api.HTTPError{StatusCode: 422},
		"429":           &api.HTTPError{StatusCode: 429},
		"unknown error": errors.New("something odd"),
		"graphql error": &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Message: "Field 'x' doesn't exist"}}},
	} {
		f := &fakeGQL{fixture: "testdata/search.json", errs: []error{e}}
		if _, _, err := retryClient(f).SearchPRs(context.Background(), "q", 50); err == nil || f.calls != 1 {
			t.Errorf("%s: err %v calls %d; want one attempt", name, err, f.calls)
		}
	}
}

func TestSearchDoesNotRetryWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeGQL{fixture: "testdata/search.json", errs: []error{err502}}
	if _, _, err := retryClient(f).SearchPRs(ctx, "q", 50); err == nil || f.calls != 1 {
		t.Errorf("err %v calls %d", err, f.calls)
	}
}

func TestSearchRetryWaitStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeGQL{fixture: "testdata/search.json", errs: []error{err502}}
	c := New(f)
	c.retryDelays = []time.Duration{time.Hour}
	time.AfterFunc(20*time.Millisecond, cancel)
	start := time.Now()
	if _, _, err := c.SearchPRs(ctx, "q", 50); err == nil || time.Since(start) > 2*time.Second {
		t.Errorf("err %v after %v; want a prompt error on cancel", err, time.Since(start))
	}
}
