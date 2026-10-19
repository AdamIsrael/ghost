package github

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
)

const samlMsg = "Access to the requested organization (acme) is blocked by SAML single sign-on. Your session or token does not satisfy the organization's or enterprise's conditional access policies. Confirm the policy requirements are met, then try again."

func emptySearch(rest *fakeREST) *Client {
	c := New(&fakeGQL{fixture: "testdata/search_empty.json"})
	if rest != nil {
		c.WithREST(rest)
	}
	return c
}

func searchErr422(msgs ...string) *api.HTTPError {
	h := &api.HTTPError{StatusCode: 422, Message: "Validation Failed"}
	for _, m := range msgs {
		h.Errors = append(h.Errors, api.HTTPErrorItem{Message: m, Resource: "Search", Field: "q", Code: "invalid"})
	}
	return h
}

func TestEmptySearchExplainsSAMLBlock(t *testing.T) {
	rest := &fakeREST{errs: []error{searchErr422(samlMsg)}}
	prs, total, err := emptySearch(rest).SearchPRs(context.Background(), "is:open is:pr org:acme", 50)
	if prs == nil || len(prs) != 0 || total != 0 {
		t.Fatalf("prs %v total %d: want a non-nil empty list (a warning, not a failed poll)", prs, total)
	}
	var be *SearchBlockedError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want *SearchBlockedError", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "blocked by SAML single sign-on") || !strings.Contains(msg, "gh auth refresh") {
		t.Errorf("message = %q", msg)
	}
	if len(rest.calls) != 1 || rest.calls[0].method != "GET" {
		t.Fatalf("calls = %+v", rest.calls)
	}
	u, _ := url.Parse(rest.calls[0].path)
	if u.Path != "search/issues" || u.Query().Get("q") != "is:open is:pr org:acme" || u.Query().Get("per_page") != "1" {
		t.Errorf("probe = %s", rest.calls[0].path)
	}
}

func TestEmptySearchExplainsNoAccess(t *testing.T) {
	msg := "The listed users and repositories cannot be searched either because the resources do not exist or you do not have permission to view them."
	_, _, err := emptySearch(&fakeREST{errs: []error{searchErr422(msg)}}).SearchPRs(context.Background(), "is:pr org:ghost-town", 50)
	var be *SearchBlockedError
	if !errors.As(err, &be) || !strings.Contains(err.Error(), "cannot be searched") || strings.Contains(err.Error(), "gh auth refresh") {
		t.Errorf("err = %v", err)
	}
}

func TestEmptySearchWithoutProblemIsQuiet(t *testing.T) {
	for name, rest := range map[string]*fakeREST{
		"really empty":      {getBody: `{"total_count":0,"items":[]}`},
		"probe 500":         {errs: []error{&api.HTTPError{StatusCode: 500}}},
		"probe network err": {errs: []error{errors.New("dial tcp: timeout")}},
		"422 without items": {errs: []error{searchErr422()}},
	} {
		prs, _, err := emptySearch(rest).SearchPRs(context.Background(), "is:pr org:acme", 50)
		if err != nil || prs == nil || len(prs) != 0 {
			t.Errorf("%s: prs %v err %v", name, prs, err)
		}
	}
	if _, _, err := emptySearch(nil).SearchPRs(context.Background(), "q", 50); err != nil {
		t.Errorf("no REST client: %v", err)
	}
}

func TestNonEmptySearchDoesNotProbe(t *testing.T) {
	rest := &fakeREST{}
	New(&fakeGQL{fixture: "testdata/search.json"}).WithREST(rest).SearchPRs(context.Background(), "q", 50)
	if len(rest.calls) != 0 {
		t.Errorf("probed a search that had results: %+v", rest.calls)
	}
}
