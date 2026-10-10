package github

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/AdamIsrael/ghost/internal/status"
)

type fakeGQL struct {
	fixture string
	err     error
	partial *api.GraphQLError // returned after filling resp, like go-gh does
	query   string
	vars    map[string]interface{}
}

func (f *fakeGQL) DoWithContext(_ context.Context, query string, vars map[string]interface{}, resp interface{}) error {
	f.query, f.vars = query, vars
	if f.err != nil {
		return f.err
	}
	data, err := os.ReadFile(f.fixture)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, resp); err != nil {
		return err
	}
	if f.partial != nil {
		return f.partial
	}
	return nil
}

func TestSearchPRsParsesFields(t *testing.T) {
	f := &fakeGQL{fixture: "testdata/search.json"}
	prs, err := New(f).SearchPRs(context.Background(), "is:open is:pr review-requested:@me")
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 2 {
		t.Fatalf("got %d PRs, want 2", len(prs))
	}
	got := prs[0]
	want := PR{
		Repo: "o/r", Number: 42, Title: "Add retries to fetcher", URL: "https://github.com/o/r/pull/42",
		Author: "octocat", Body: "Retries transient errors.", BaseRef: "main", HeadRef: "retries",
		HeadSHA: "abc1234def5678", ReviewDecision: "REVIEW_REQUIRED", CIState: "PENDING",
		Additions: 120, Deletions: 30, ChangedFiles: 4,
		UpdatedAt: time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC),
	}
	if got != want {
		t.Errorf("PR 0 =\n%+v\nwant\n%+v", got, want)
	}
	if got.Status() != status.WaitingOnCI {
		t.Errorf("status = %v", got.Status())
	}
	draft := prs[1]
	if draft.Author != "ghost" || draft.CIState != "" || draft.ReviewDecision != "" || draft.Status() != status.Draft {
		t.Errorf("draft PR = %+v", draft)
	}
}

func TestSearchPRsSkipsNonPRNodes(t *testing.T) {
	prs, _ := New(&fakeGQL{fixture: "testdata/search.json"}).SearchPRs(context.Background(), "q")
	for _, pr := range prs {
		if pr.Number == 0 {
			t.Fatalf("non-PR node leaked into results: %+v", pr)
		}
	}
}

func TestSearchPRsSendsQueryAndLimit(t *testing.T) {
	f := &fakeGQL{fixture: "testdata/search.json"}
	New(f).SearchPRs(context.Background(), "repo:o/r is:open")
	if f.vars["q"] != "repo:o/r is:open" || f.vars["first"] != SearchLimit {
		t.Errorf("vars = %v", f.vars)
	}
	if !strings.Contains(f.query, "search(query: $q, type: ISSUE, first: $first)") {
		t.Errorf("unexpected query: %s", f.query)
	}
}

func TestSearchPRsWrapsError(t *testing.T) {
	_, err := New(&fakeGQL{err: errors.New("HTTP 401")}).SearchPRs(context.Background(), "q")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "search") {
		t.Fatalf("err = %v", err)
	}
}

func nodeErr(i int, msg string) api.GraphQLErrorItem {
	return api.GraphQLErrorItem{Message: msg, Path: []interface{}{"search", "nodes", float64(i)}}
}

func TestSearchPRsKeepsVisiblePRsWhenSomeAreHidden(t *testing.T) {
	saml := "Resource protected by organization SAML enforcement."
	f := &fakeGQL{fixture: "testdata/search.json", partial: &api.GraphQLError{Errors: []api.GraphQLErrorItem{nodeErr(1, saml), nodeErr(3, saml)}}}
	prs, err := New(f).SearchPRs(context.Background(), "q")
	if len(prs) != 2 {
		t.Fatalf("want the 2 visible PRs, got %d (err %v)", len(prs), err)
	}
	var pe *PartialError
	if !errors.As(err, &pe) || pe.Hidden != 2 || !strings.Contains(err.Error(), "2 PRs hidden") || !strings.Contains(err.Error(), "SAML") {
		t.Fatalf("want PartialError for 2 hidden PRs, got %v", err)
	}
}

func TestSearchPRsFailsOnQueryLevelGraphQLError(t *testing.T) {
	f := &fakeGQL{fixture: "testdata/search.json", partial: &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Message: "Field 'x' doesn't exist", Path: []interface{}{"search"}}}}}
	prs, err := New(f).SearchPRs(context.Background(), "q")
	var pe *PartialError
	if prs != nil || err == nil || errors.As(err, &pe) {
		t.Fatalf("query-level error should fail the search: prs=%v err=%v", prs, err)
	}
}
