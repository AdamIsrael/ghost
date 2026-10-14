package github

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
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
	prs, total, err := New(f).SearchPRs(context.Background(), "is:open is:pr review-requested:@me", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 2 || total != 120 {
		t.Fatalf("got %d PRs (total %d), want 2 (total 120)", len(prs), total)
	}
	got := prs[0]
	want := PR{
		Repo: "o/r", Number: 42, Title: "Add retries to fetcher", URL: "https://github.com/o/r/pull/42",
		Author: "octocat", Body: "Retries transient errors.", BaseRef: "main", HeadRef: "retries",
		HeadSHA: "abc1234def5678", ReviewDecision: "REVIEW_REQUIRED", CIState: "PENDING", State: "OPEN",
		Additions: 120, Deletions: 30, ChangedFiles: 4,
		UpdatedAt: time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC),
		Labels:    []string{"ai-review", "bug"},
	}
	if !reflect.DeepEqual(got, want) {
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
	prs, _, _ := New(&fakeGQL{fixture: "testdata/search.json"}).SearchPRs(context.Background(), "q", 50)
	for _, pr := range prs {
		if pr.Number == 0 {
			t.Fatalf("non-PR node leaked into results: %+v", pr)
		}
	}
}

func TestSearchPRsSendsQueryAndLimit(t *testing.T) {
	f := &fakeGQL{fixture: "testdata/search.json"}
	New(f).SearchPRs(context.Background(), "repo:o/r is:open", 75)
	if f.vars["q"] != "repo:o/r is:open" || f.vars["first"] != 75 {
		t.Errorf("vars = %v", f.vars)
	}
	if !strings.Contains(f.query, "issueCount") {
		t.Error("query does not ask for the total match count")
	}
	for limit, want := range map[int]int{0: 50, -3: 50, 101: 100, 1: 1, 100: 100} {
		New(f).SearchPRs(context.Background(), "q", limit)
		if f.vars["first"] != want {
			t.Errorf("limit %d sent first=%v, want %d", limit, f.vars["first"], want)
		}
	}
	if !strings.Contains(f.query, "search(query: $q, type: ISSUE, first: $first)") {
		t.Errorf("unexpected query: %s", f.query)
	}
}

func TestSearchPRsWrapsError(t *testing.T) {
	_, _, err := New(&fakeGQL{err: errors.New("HTTP 401")}).SearchPRs(context.Background(), "q", 50)
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
	prs, _, err := New(f).SearchPRs(context.Background(), "q", 50)
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
	prs, _, err := New(f).SearchPRs(context.Background(), "q", 50)
	var pe *PartialError
	if prs != nil || err == nil || errors.As(err, &pe) {
		t.Fatalf("query-level error should fail the search: prs=%v err=%v", prs, err)
	}
}

func TestSearchErrorOmitsQuery(t *testing.T) {
	_, _, err := New(&fakeGQL{err: errors.New("HTTP 502")}).SearchPRs(context.Background(), "repo:secret/long-query is:open", 50)
	if err == nil || strings.Contains(err.Error(), "secret/long-query") || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("error should carry the cause, not the query: %v", err)
	}
}

func TestGetPR(t *testing.T) {
	f := &fakeGQL{fixture: "testdata/pr.json"}
	d, err := New(f).GetPR(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if d.Number != 7 || d.Title != "Fix parser" || d.HeadSHA != "deadbeef00" || d.CIState != "SUCCESS" || d.Author != "octocat" || d.State != "OPEN" {
		t.Errorf("PR = %+v", d.PR)
	}
	if !reflect.DeepEqual(d.Labels, []string{"ai-review"}) {
		t.Errorf("labels = %v", d.Labels)
	}
	if len(d.Files) != 2 || d.Files[0] != "parser.go" {
		t.Errorf("files = %v", d.Files)
	}
	if f.vars["owner"] != "o" || f.vars["name"] != "r" || f.vars["number"] != 7 {
		t.Errorf("vars = %v", f.vars)
	}
}

func TestGetPRNotFound(t *testing.T) {
	f := &fakeGQL{fixture: "testdata/search.json"} // no repository.pullRequest
	if _, err := New(f).GetPR(context.Background(), "o/r", 7); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestParsePRRef(t *testing.T) {
	good := map[string]struct {
		repo string
		n    int
	}{
		"https://github.com/o/r/pull/7":                 {"o/r", 7},
		"https://github.com/o/r/pull/7/files":           {"o/r", 7},
		"https://github.com/o/r/pull/7#discussion_r1":   {"o/r", 7},
		"http://github.com/Org-1/repo.name/pull/12?x=1": {"Org-1/repo.name", 12},
		"https://www.github.com/o/r/pull/9":             {"o/r", 9},
		"o/r#7":                                         {"o/r", 7},
		"  o/r#7  ":                                     {"o/r", 7},
	}
	for in, want := range good {
		repo, n, err := ParsePRRef(in)
		if err != nil || repo != want.repo || n != want.n {
			t.Errorf("ParsePRRef(%q) = %q, %d, %v", in, repo, n, err)
		}
	}
	for _, in := range []string{"", "o/r", "https://github.com/o/r/issues/7", "https://gitlab.com/o/r/pull/7", "o/r#x", "o/r#0", "a/b/c#1"} {
		if _, _, err := ParsePRRef(in); err == nil {
			t.Errorf("ParsePRRef(%q) should fail", in)
		}
	}
}

func TestHasLabelIgnoresCase(t *testing.T) {
	p := PR{Labels: []string{"bug", "AI-Review"}}
	if !p.HasLabel("ai-review") || !p.HasLabel("AI-REVIEW") {
		t.Error("HasLabel should ignore case")
	}
	if p.HasLabel("ai") || (PR{}).HasLabel("ai-review") {
		t.Error("HasLabel matched a label that isn't there")
	}
}

func TestQueriesRequestLabels(t *testing.T) {
	for name, q := range map[string]string{"search": searchQuery, "pr": prQuery} {
		if !strings.Contains(q, "labels(first: 50) { nodes { name } }") {
			t.Errorf("%s query does not fetch labels", name)
		}
	}
}
