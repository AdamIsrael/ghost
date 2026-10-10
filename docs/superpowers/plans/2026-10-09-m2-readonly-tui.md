# M2 Read-only TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Running `ghost` opens a full-screen TUI that polls GitHub for every configured view, lists matching PRs with their derived status, shows details for the selected PR, and shows a status bar. No reviews yet (M4); no view editing yet (M6).

**Architecture:** Three new leaf packages plus the TUI. `status` derives a PR status from GitHub fields (pure). `github` wraps the go-gh GraphQL client behind a one-method interface and parses search results. `poller` runs each view's query on an interval (with backoff on failure) and emits results on a channel. `tui` is a Bubble Tea v2 model that consumes poller results as messages; rendering lives in pure functions so it can be tested as strings. `cmd/ghost` wires them through an injectable `deps` struct so tests never touch the network or a terminal.

**Tech Stack:** Go 1.27, `charm.land/bubbletea/v2` v2.1.0, `charm.land/lipgloss/v2` v2.0.6, `github.com/charmbracelet/x/ansi`, `github.com/cli/go-gh/v2` v2.16.2. Stdlib `testing` only.

**Spec:** `docs/superpowers/specs/2026-10-09-ghost-design.md`

**Bead:** `ghost-qz7.2`

## Global Constraints

- Module path: `github.com/AdamIsrael/ghost`
- Bubble Tea v2 import paths are `charm.land/bubbletea/v2` and `charm.land/lipgloss/v2` (NOT `github.com/charmbracelet/...`). `View()` returns `tea.View`; build it with `tea.NewView(s)` and set `v.AltScreen = true`. Keys arrive as `tea.KeyPressMsg`; match with `msg.String()` (printable keys return their text, e.g. `"j"`, `"R"`; others return keystrokes, e.g. `"right"`, `"ctrl+c"`).
- PR statuses (spec): Draft, Changes requested, Waiting on CI, CI failing, Approved, Ready. Precedence: Draft > Changes requested > CI failing > Waiting on CI > Approved > Ready. No CI checks counts as green.
- TUI never calls GitHub directly; it receives poller results as messages (spec: "TUI never calls GitHub or the agent directly").
- Keys in M2: `←/→`, `h/l`, `tab/shift+tab` switch view; `↑/↓`, `j/k` move; `o`/`enter` open PR in browser; `R` poll now; `q`/`ctrl+c` quit. (`r`, `a`, `p` are reserved for M4/M5.)
- Search returns at most 50 PRs per view (`github.SearchLimit`).
- Poll backoff on failure: `interval × 2^failures`, capped at 8×.
- **Git:** user authorized one commit per task on branch `m2-tui`; ask before merging or pushing.

## Review Focus

1. A view query matching issues, not just PRs (user omits `is:pr`) → issue nodes are dropped, no zero-value rows (Task 2 `TestSearchPRsSkipsNonPRNodes`).
2. PR authored by a deleted account (`author: null`) → shows `ghost`, no panic (Task 2 `TestSearchPRsParsesFields`).
3. Config with zero views → TUI renders a "no views" hint, no divide-by-zero on view switching (Task 5 `TestNoViews`).
4. GitHub unavailable at startup (gh not authenticated) → TUI still opens, list shows why polling is off (Task 6 `TestStartWithoutGitHub`).
5. Terminal narrower than the two-pane layout → no panic, list still shown (Task 5 `TestNarrowTerminal`).

---

### File Structure

```
internal/status/status.go          Status type, Derive, String, Icon
internal/status/status_test.go
internal/github/github.go          PR, Client, New, NewDefault, SearchPRs
internal/github/github_test.go
internal/github/testdata/search.json
internal/poller/poller.go          Searcher, Result, Poller, Backoff
internal/poller/poller_test.go
internal/tui/render.go             pure render helpers
internal/tui/render_test.go
internal/tui/model.go              Options, Model, Init/Update/View
internal/tui/model_test.go
cmd/ghost/main.go                  deps injection, launches TUI
cmd/ghost/main_test.go
```

---

### Task 1: PR status derivation

**Files:**
- Create: `internal/status/status.go`, `internal/status/status_test.go`

**Interfaces:**
- Produces: `type Status int` with constants `Ready, Draft, WaitingOnCI, CIFailing, ChangesRequested, Approved`; `func Derive(isDraft bool, reviewDecision, ciState string) Status`; `func (s Status) String() string`; `func (s Status) Icon() string`.

- [ ] **Step 1: Write the failing test** — `internal/status/status_test.go`

```go
package status

import "testing"

func TestDerive(t *testing.T) {
	cases := []struct {
		name     string
		draft    bool
		decision string
		ci       string
		want     Status
	}{
		{"draft wins over everything", true, "APPROVED", "FAILURE", Draft},
		{"changes requested beats CI", false, "CHANGES_REQUESTED", "FAILURE", ChangesRequested},
		{"ci failure", false, "", "FAILURE", CIFailing},
		{"ci error", false, "REVIEW_REQUIRED", "ERROR", CIFailing},
		{"ci pending", false, "", "PENDING", WaitingOnCI},
		{"ci expected", false, "", "EXPECTED", WaitingOnCI},
		{"approved but ci pending waits", false, "APPROVED", "PENDING", WaitingOnCI},
		{"approved and green", false, "APPROVED", "SUCCESS", Approved},
		{"approved, no checks", false, "APPROVED", "", Approved},
		{"ready: green, awaiting review", false, "REVIEW_REQUIRED", "SUCCESS", Ready},
		{"ready: no checks, no decision", false, "", "", Ready},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Derive(tc.draft, tc.decision, tc.ci); got != tc.want {
				t.Errorf("Derive = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStringAndIcon(t *testing.T) {
	for _, s := range []Status{Ready, Draft, WaitingOnCI, CIFailing, ChangesRequested, Approved} {
		if s.String() == "" || s.String() == "unknown" || s.Icon() == "" {
			t.Errorf("status %d missing name or icon", s)
		}
	}
	if ChangesRequested.String() != "Changes requested" || WaitingOnCI.String() != "Waiting on CI" {
		t.Error("unexpected status names")
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/status/` → FAIL `undefined: Derive`

- [ ] **Step 3: Implement** — `internal/status/status.go`

```go
// Package status derives a pull request's display status from GitHub fields.
package status

type Status int

const (
	Ready Status = iota
	Draft
	WaitingOnCI
	CIFailing
	ChangesRequested
	Approved
)

// Derive maps GitHub's isDraft, reviewDecision and statusCheckRollup.state to
// a Status. Precedence: draft, changes requested, CI failing, CI pending,
// approved, ready. A PR with no checks counts as green.
func Derive(isDraft bool, reviewDecision, ciState string) Status {
	switch {
	case isDraft:
		return Draft
	case reviewDecision == "CHANGES_REQUESTED":
		return ChangesRequested
	case ciState == "FAILURE" || ciState == "ERROR":
		return CIFailing
	case ciState == "PENDING" || ciState == "EXPECTED":
		return WaitingOnCI
	case reviewDecision == "APPROVED":
		return Approved
	default:
		return Ready
	}
}

var info = map[Status]struct{ name, icon string }{
	Ready:            {"Ready", "●"},
	Draft:            {"Draft", "✎"},
	WaitingOnCI:      {"Waiting on CI", "◐"},
	CIFailing:        {"CI failing", "✗"},
	ChangesRequested: {"Changes requested", "↺"},
	Approved:         {"Approved", "✓"},
}

func (s Status) String() string {
	if i, ok := info[s]; ok {
		return i.name
	}
	return "unknown"
}

func (s Status) Icon() string {
	if i, ok := info[s]; ok {
		return i.icon
	}
	return "?"
}
```

- [ ] **Step 4: Run, verify pass** — `go test ./internal/status/` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/status
git commit -m "Add PR status derivation"
```

---

### Task 2: GitHub search client

**Files:**
- Create: `internal/github/github.go`, `internal/github/github_test.go`, `internal/github/testdata/search.json`

**Interfaces:**
- Consumes: `status.Derive`, `status.Status` (Task 1).
- Produces:
  - `type PR struct { Repo string; Number int; Title, URL, Author, Body, BaseRef, HeadRef, HeadSHA string; IsDraft bool; ReviewDecision, CIState string; Additions, Deletions, ChangedFiles int; UpdatedAt time.Time }`
  - `func (p PR) Status() status.Status`
  - `type GraphQL interface { DoWithContext(ctx context.Context, query string, variables map[string]interface{}, response interface{}) error }`
  - `func New(gql GraphQL) *Client`, `func NewDefault() (*Client, error)`
  - `func (c *Client) SearchPRs(ctx context.Context, query string) ([]PR, error)`
  - `const SearchLimit = 50`

- [ ] **Step 1: Add dependency** — `go get github.com/cli/go-gh/v2@v2.16.2`

- [ ] **Step 2: Write the fixture** — `internal/github/testdata/search.json` (the `data` object go-gh unmarshals into the response)

```json
{
  "search": {
    "nodes": [
      {
        "number": 42,
        "title": "Add retries to fetcher",
        "url": "https://github.com/o/r/pull/42",
        "body": "Retries transient errors.",
        "isDraft": false,
        "updatedAt": "2026-10-09T10:00:00Z",
        "additions": 120,
        "deletions": 30,
        "changedFiles": 4,
        "baseRefName": "main",
        "headRefName": "retries",
        "headRefOid": "abc1234def5678",
        "reviewDecision": "REVIEW_REQUIRED",
        "author": {"login": "octocat"},
        "repository": {"nameWithOwner": "o/r"},
        "commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "PENDING"}}}]}
      },
      {},
      {
        "number": 7,
        "title": "WIP: rewrite",
        "url": "https://github.com/o/r/pull/7",
        "body": "",
        "isDraft": true,
        "updatedAt": "2026-10-08T09:00:00Z",
        "additions": 1,
        "deletions": 0,
        "changedFiles": 1,
        "baseRefName": "main",
        "headRefName": "rewrite",
        "headRefOid": "fff0000",
        "reviewDecision": null,
        "author": null,
        "repository": {"nameWithOwner": "o/r"},
        "commits": {"nodes": [{"commit": {"statusCheckRollup": null}}]}
      }
    ]
  }
}
```

- [ ] **Step 3: Write the failing tests** — `internal/github/github_test.go`

```go
package github

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AdamIsrael/ghost/internal/status"
)

type fakeGQL struct {
	fixture string
	err     error
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
	return json.Unmarshal(data, resp)
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
```

- [ ] **Step 4: Run, verify fail** — `go test ./internal/github/` → FAIL `undefined: New`

- [ ] **Step 5: Implement** — `internal/github/github.go`

```go
// Package github queries GitHub through the user's gh authentication.
package github

import (
	"context"
	"fmt"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/AdamIsrael/ghost/internal/status"
)

// SearchLimit is the maximum number of PRs returned per view query.
const SearchLimit = 50

type PR struct {
	Repo           string
	Number         int
	Title          string
	URL            string
	Author         string
	Body           string
	BaseRef        string
	HeadRef        string
	HeadSHA        string
	IsDraft        bool
	ReviewDecision string
	CIState        string
	Additions      int
	Deletions      int
	ChangedFiles   int
	UpdatedAt      time.Time
}

func (p PR) Status() status.Status { return status.Derive(p.IsDraft, p.ReviewDecision, p.CIState) }

// GraphQL is the subset of go-gh's GraphQLClient that Client uses.
type GraphQL interface {
	DoWithContext(ctx context.Context, query string, variables map[string]interface{}, response interface{}) error
}

type Client struct{ gql GraphQL }

func New(gql GraphQL) *Client { return &Client{gql: gql} }

// NewDefault builds a Client from the user's gh auth configuration.
func NewDefault() (*Client, error) {
	gql, err := api.DefaultGraphQLClient()
	if err != nil {
		return nil, err
	}
	return New(gql), nil
}

const searchQuery = `query($q: String!, $first: Int!) {
  search(query: $q, type: ISSUE, first: $first) {
    nodes {
      ... on PullRequest {
        number title url body isDraft updatedAt additions deletions changedFiles
        baseRefName headRefName headRefOid reviewDecision
        author { login }
        repository { nameWithOwner }
        commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
      }
    }
  }
}`

type searchResponse struct {
	Search struct {
		Nodes []prNode `json:"nodes"`
	} `json:"search"`
}

type prNode struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	URL            string    `json:"url"`
	Body           string    `json:"body"`
	IsDraft        bool      `json:"isDraft"`
	UpdatedAt      time.Time `json:"updatedAt"`
	Additions      int       `json:"additions"`
	Deletions      int       `json:"deletions"`
	ChangedFiles   int       `json:"changedFiles"`
	BaseRefName    string    `json:"baseRefName"`
	HeadRefName    string    `json:"headRefName"`
	HeadRefOid     string    `json:"headRefOid"`
	ReviewDecision string    `json:"reviewDecision"`
	Author         *struct {
		Login string `json:"login"`
	} `json:"author"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// SearchPRs runs a GitHub search query and returns the matching pull requests.
// Non-PR results (issues) are dropped.
func (c *Client) SearchPRs(ctx context.Context, query string) ([]PR, error) {
	var resp searchResponse
	vars := map[string]interface{}{"q": query, "first": SearchLimit}
	if err := c.gql.DoWithContext(ctx, searchQuery, vars, &resp); err != nil {
		return nil, fmt.Errorf("search %q: %w", query, err)
	}
	prs := make([]PR, 0, len(resp.Search.Nodes))
	for _, n := range resp.Search.Nodes {
		if n.Number == 0 {
			continue
		}
		pr := PR{
			Repo: n.Repository.NameWithOwner, Number: n.Number, Title: n.Title, URL: n.URL,
			Author: "ghost", Body: n.Body, BaseRef: n.BaseRefName, HeadRef: n.HeadRefName,
			HeadSHA: n.HeadRefOid, IsDraft: n.IsDraft, ReviewDecision: n.ReviewDecision,
			Additions: n.Additions, Deletions: n.Deletions, ChangedFiles: n.ChangedFiles,
			UpdatedAt: n.UpdatedAt,
		}
		if n.Author != nil {
			pr.Author = n.Author.Login
		}
		if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
			pr.CIState = n.Commits.Nodes[0].Commit.StatusCheckRollup.State
		}
		prs = append(prs, pr)
	}
	return prs, nil
}
```

(`"ghost"` is GitHub's own placeholder login for deleted accounts.)

- [ ] **Step 6: Run, verify pass** — `go mod tidy && go test ./internal/github/` → PASS

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/github
git commit -m "Add GitHub PR search client"
```

---

### Task 3: Poller

**Files:**
- Create: `internal/poller/poller.go`, `internal/poller/poller_test.go`

**Interfaces:**
- Consumes: `github.PR` (Task 2), `config.View` (M1).
- Produces:
  - `type Searcher interface { SearchPRs(ctx context.Context, query string) ([]github.PR, error) }` (`*github.Client` satisfies it)
  - `type Result struct { View string; PRs []github.PR; Err error; At time.Time }`
  - `func New(s Searcher, views []config.View, interval time.Duration) *Poller`
  - `func (p *Poller) Results() <-chan Result`, `func (p *Poller) PollNow()`, `func (p *Poller) Run(ctx context.Context)`
  - `func Backoff(base time.Duration, failures int) time.Duration`

- [ ] **Step 1: Write the failing tests** — `internal/poller/poller_test.go`

```go
package poller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
)

type fakeSearcher struct {
	mu      sync.Mutex
	queries []string
	fail    map[string]error
}

func (f *fakeSearcher) SearchPRs(_ context.Context, q string) ([]github.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	if err := f.fail[q]; err != nil {
		return nil, err
	}
	return []github.PR{{Repo: "o/r", Number: len(f.queries), Title: q}}, nil
}

var views = []config.View{{Name: "A", Query: "qa"}, {Name: "B", Query: "qb"}}

func next(t *testing.T, p *Poller) Result {
	t.Helper()
	select {
	case r := <-p.Results():
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for poll result")
		return Result{}
	}
}

func TestRunPollsEveryViewImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := New(&fakeSearcher{}, views, time.Hour)
	go p.Run(ctx)
	a, b := next(t, p), next(t, p)
	if a.View != "A" || b.View != "B" {
		t.Fatalf("views = %s,%s", a.View, b.View)
	}
	if len(a.PRs) != 1 || a.PRs[0].Title != "qa" || a.Err != nil || a.At.IsZero() {
		t.Errorf("result A = %+v", a)
	}
}

func TestPollNowTriggersAnotherRound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := New(&fakeSearcher{}, views, time.Hour)
	go p.Run(ctx)
	next(t, p)
	next(t, p)
	p.PollNow()
	if r := next(t, p); r.View != "A" {
		t.Fatalf("after PollNow got %+v", r)
	}
}

func TestPollNowNeverBlocks(t *testing.T) {
	p := New(&fakeSearcher{}, views, time.Hour)
	done := make(chan struct{})
	go func() {
		p.PollNow()
		p.PollNow()
		p.PollNow()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("PollNow blocked while Run was not running")
	}
}

func TestRunReportsErrorsPerView(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := New(&fakeSearcher{fail: map[string]error{"qa": errors.New("rate limited")}}, views, time.Hour)
	go p.Run(ctx)
	a, b := next(t, p), next(t, p)
	if a.Err == nil || a.PRs != nil {
		t.Errorf("A should carry the error: %+v", a)
	}
	if b.Err != nil || len(b.PRs) != 1 {
		t.Errorf("B should succeed: %+v", b)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := New(&fakeSearcher{}, views, time.Hour)
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	next(t, p)
	next(t, p)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestBackoff(t *testing.T) {
	base := time.Minute
	for failures, want := range []time.Duration{base, 2 * base, 4 * base, 8 * base, 8 * base, 8 * base} {
		if got := Backoff(base, failures); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", failures, got, want)
		}
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/poller/` → FAIL `undefined: New`

- [ ] **Step 3: Implement** — `internal/poller/poller.go`

```go
// Package poller runs each view's GitHub query on an interval.
package poller

import (
	"context"
	"time"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
)

type Searcher interface {
	SearchPRs(ctx context.Context, query string) ([]github.PR, error)
}

// Result is one view's poll outcome. On error PRs is nil and the caller keeps
// whatever it showed before.
type Result struct {
	View string
	PRs  []github.PR
	Err  error
	At   time.Time
}

type Poller struct {
	searcher Searcher
	views    []config.View
	interval time.Duration
	results  chan Result
	trigger  chan struct{}
}

func New(s Searcher, views []config.View, interval time.Duration) *Poller {
	return &Poller{
		searcher: s,
		views:    views,
		interval: interval,
		results:  make(chan Result, len(views)),
		trigger:  make(chan struct{}, 1),
	}
}

func (p *Poller) Results() <-chan Result { return p.results }

// PollNow asks Run to start a new round immediately. It never blocks.
func (p *Poller) PollNow() {
	select {
	case p.trigger <- struct{}{}:
	default:
	}
}

// Run polls every view once, then again after each interval (backed off while
// rounds fail) or when PollNow is called, until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	failures := 0
	for {
		if p.pollAll(ctx) {
			failures++
		} else {
			failures = 0
		}
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(Backoff(p.interval, failures))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-p.trigger:
			timer.Stop()
		}
	}
}

// pollAll queries each view in order and reports whether any query failed.
func (p *Poller) pollAll(ctx context.Context) (failed bool) {
	for _, v := range p.views {
		prs, err := p.searcher.SearchPRs(ctx, v.Query)
		if ctx.Err() != nil {
			return failed
		}
		if err != nil {
			failed = true
		}
		select {
		case p.results <- Result{View: v.Name, PRs: prs, Err: err, At: time.Now()}:
		case <-ctx.Done():
			return failed
		}
	}
	return failed
}

// Backoff returns base × 2^failures, capped at 8× base.
func Backoff(base time.Duration, failures int) time.Duration {
	if failures > 3 {
		failures = 3
	}
	return base << failures
}
```

- [ ] **Step 4: Run, verify pass** — `go test -race ./internal/poller/` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/poller
git commit -m "Add view poller with backoff"
```

---

### Task 4: TUI render helpers

**Files:**
- Create: `internal/tui/render.go`, `internal/tui/render_test.go`

**Interfaces:**
- Consumes: `config.View`, `github.PR`, `preflight.Result`.
- Produces (package-private, used by Task 5):
  - `func fit(s string, w int) string` — truncate with `…` and pad to exactly `w` cells
  - `func renderFilterBar(views []config.View, active int, counts map[string]int, width int) string`
  - `func renderList(prs []github.PR, cursor, width, height int, empty string) string`
  - `func renderDetail(pr *github.PR, width, height int) string`
  - `type statusInfo struct { SpendUSD, MaxDailyUSD float64; LastPoll time.Time; Err string; Checks []preflight.Result }`
  - `func renderStatusBar(si statusInfo, width int) string`

- [ ] **Step 1: Add dependencies**

```bash
go get charm.land/bubbletea/v2@v2.1.0 charm.land/lipgloss/v2@v2.0.6 github.com/charmbracelet/x/ansi
```

- [ ] **Step 2: Write the failing tests** — `internal/tui/render_test.go`

```go
package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/preflight"
)

func samplePR(n int, title string) github.PR {
	return github.PR{
		Repo: "o/r", Number: n, Title: title, URL: "https://github.com/o/r/pull/1",
		Author: "octocat", Body: "Body text here.", BaseRef: "main", HeadRef: "feat",
		HeadSHA: "abc1234def", CIState: "SUCCESS", ReviewDecision: "REVIEW_REQUIRED",
		Additions: 10, Deletions: 2, ChangedFiles: 3,
	}
}

func lines(s string) []string { return strings.Split(ansi.Strip(s), "\n") }

func TestFitTruncatesAndPads(t *testing.T) {
	if got := fit("hello", 8); got != "hello   " {
		t.Errorf("pad: %q", got)
	}
	if got := fit("hello world", 6); lipgloss.Width(got) != 6 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncate: %q", got)
	}
	if got := fit("x", 0); got != "" {
		t.Errorf("zero width: %q", got)
	}
}

func TestRenderFilterBar(t *testing.T) {
	views := []config.View{{Name: "Team"}, {Name: "Mine"}}
	out := ansi.Strip(renderFilterBar(views, 1, map[string]int{"Team": 3}, 80))
	if !strings.Contains(out, "Team (3)") || !strings.Contains(out, "Mine (…)") {
		t.Errorf("filter bar = %q", out)
	}
	if lipgloss.Width(out) != 80 {
		t.Errorf("width = %d", lipgloss.Width(out))
	}
}

func TestRenderListScrollsToCursor(t *testing.T) {
	prs := []github.PR{samplePR(1, "one"), samplePR(2, "two"), samplePR(3, "three"), samplePR(4, "four")}
	ls := lines(renderList(prs, 3, 50, 2, ""))
	if len(ls) != 2 || !strings.Contains(ls[0], "three") || !strings.Contains(ls[1], "four") {
		t.Errorf("list = %q", ls)
	}
	if !strings.Contains(ls[1], "o/r#4") || !strings.Contains(ls[1], "Ready") {
		t.Errorf("row missing repo/number/status: %q", ls[1])
	}
}

func TestRenderListEmpty(t *testing.T) {
	ls := lines(renderList(nil, 0, 30, 3, "No pull requests"))
	if len(ls) != 3 || !strings.Contains(ls[0], "No pull requests") {
		t.Errorf("empty list = %q", ls)
	}
}

func TestRenderDetail(t *testing.T) {
	pr := samplePR(42, "Add retries")
	out := ansi.Strip(renderDetail(&pr, 60, 20))
	for _, want := range []string{"o/r#42", "Add retries", "@octocat", "+10 −2", "3 files", "Ready", "passing", "review required", "feat → main", "abc1234", "Audit trail", "Body text here."} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q:\n%s", want, out)
		}
	}
	if n := len(lines(renderDetail(&pr, 60, 5))); n != 5 {
		t.Errorf("detail not clipped to height: %d lines", n)
	}
	if !strings.Contains(ansi.Strip(renderDetail(nil, 40, 3)), "No pull request selected") {
		t.Error("nil PR message missing")
	}
}

func TestRenderStatusBar(t *testing.T) {
	si := statusInfo{
		SpendUSD: 0, MaxDailyUSD: 10, LastPoll: time.Date(2026, 10, 9, 10, 4, 0, 0, time.Local),
		Checks: []preflight.Result{{Name: "gh", OK: true}, {Name: "claude", OK: false}},
		Err:    "Team: rate limited",
	}
	out := ansi.Strip(renderStatusBar(si, 200))
	for _, want := range []string{"Spend today $0.00 / $10.00", "Last poll 10:04", "gh ✓", "claude ✗", "Team: rate limited"} {
		if !strings.Contains(out, want) {
			t.Errorf("status bar missing %q: %q", want, out)
		}
	}
	if !strings.Contains(ansi.Strip(renderStatusBar(statusInfo{MaxDailyUSD: 10}, 100)), "Last poll —") {
		t.Error("zero LastPoll should render as —")
	}
}
```

- [ ] **Step 3: Run, verify fail** — `go test ./internal/tui/` → FAIL `undefined: fit`

- [ ] **Step 4: Implement** — `internal/tui/render.go`

```go
package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/preflight"
)

var (
	activeTab = lipgloss.NewStyle().Bold(true).Reverse(true)
	dim       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	bold      = lipgloss.NewStyle().Bold(true)
	selected  = lipgloss.NewStyle().Reverse(true)
	errText   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

// fit truncates s with "…" and pads it to exactly w terminal cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// block fits each line to width and clips or pads to exactly height lines.
func block(ls []string, width, height int) string {
	out := make([]string, height)
	for i := range out {
		line := ""
		if i < len(ls) {
			line = ls[i]
		}
		out[i] = fit(line, width)
	}
	return strings.Join(out, "\n")
}

func renderFilterBar(views []config.View, active int, counts map[string]int, width int) string {
	parts := []string{"Views:"}
	for i, v := range views {
		count := "…"
		if n, ok := counts[v.Name]; ok {
			count = fmt.Sprint(n)
		}
		label := fmt.Sprintf(" %s (%s) ", v.Name, count)
		if i == active {
			label = activeTab.Render(label)
		}
		parts = append(parts, label)
	}
	return fit(strings.Join(parts, " ")+"  "+dim.Render("←/→ switch · o open · R refresh · q quit"), width)
}

func renderRow(pr github.PR, width int, sel bool) string {
	st := pr.Status()
	line := fit(fmt.Sprintf("%s %-17s %s#%d %s", st.Icon(), st, pr.Repo, pr.Number, pr.Title), width)
	if sel {
		return selected.Render(line)
	}
	return line
}

func renderList(prs []github.PR, cursor, width, height int, empty string) string {
	if len(prs) == 0 {
		return block([]string{dim.Render(empty)}, width, height)
	}
	start := 0
	if cursor >= height {
		start = cursor - height + 1
	}
	var ls []string
	for i := start; i < len(prs) && i < start+height; i++ {
		ls = append(ls, renderRow(prs[i], width, i == cursor))
	}
	return block(ls, width, height)
}

func ciLabel(state string) string {
	switch state {
	case "":
		return "none"
	case "SUCCESS":
		return "✓ passing"
	case "PENDING", "EXPECTED":
		return "◐ pending"
	case "FAILURE", "ERROR":
		return "✗ failing"
	}
	return strings.ToLower(state)
}

func reviewLabel(decision string) string {
	if decision == "" {
		return "none"
	}
	return strings.ToLower(strings.ReplaceAll(decision, "_", " "))
}

func renderDetail(pr *github.PR, width, height int) string {
	if pr == nil {
		return block([]string{dim.Render("No pull request selected")}, width, height)
	}
	st := pr.Status()
	sha := pr.HeadSHA
	if len(sha) > 7 {
		sha = sha[:7]
	}
	ls := []string{
		bold.Render(fmt.Sprintf("%s#%d  %s", pr.Repo, pr.Number, pr.Title)),
		fmt.Sprintf("by @%s  +%d −%d  %d files", pr.Author, pr.Additions, pr.Deletions, pr.ChangedFiles),
		fmt.Sprintf("Status: %s %s   CI: %s   Review: %s", st.Icon(), st, ciLabel(pr.CIState), reviewLabel(pr.ReviewDecision)),
		fmt.Sprintf("%s → %s  (%s)", pr.HeadRef, pr.BaseRef, sha),
		dim.Render(pr.URL),
		"",
		bold.Render("── Audit trail ──"),
		dim.Render("No activity yet. Automated reviews arrive in a later milestone."),
		"",
		bold.Render("── Description ──"),
	}
	body := strings.TrimSpace(pr.Body)
	if body == "" {
		ls = append(ls, dim.Render("(no description)"))
	} else {
		ls = append(ls, strings.Split(ansi.Wordwrap(body, width, " "), "\n")...)
	}
	return block(ls, width, height)
}

type statusInfo struct {
	SpendUSD    float64
	MaxDailyUSD float64
	LastPoll    time.Time
	Err         string
	Checks      []preflight.Result
}

func renderStatusBar(si statusInfo, width int) string {
	last := "—"
	if !si.LastPoll.IsZero() {
		last = si.LastPoll.Format("15:04")
	}
	parts := []string{
		fmt.Sprintf("Spend today $%.2f / $%.2f", si.SpendUSD, si.MaxDailyUSD),
		"Reviews 0 running · 0 queued",
		"Last poll " + last,
	}
	var checks []string
	for _, c := range si.Checks {
		mark := "✓"
		if !c.OK {
			mark = "✗"
		}
		checks = append(checks, c.Name+" "+mark)
	}
	if len(checks) > 0 {
		parts = append(parts, strings.Join(checks, " "))
	}
	line := strings.Join(parts, " · ")
	if si.Err != "" {
		line += "  " + errText.Render("⚠ "+si.Err)
	}
	return fit(line, width)
}
```

- [ ] **Step 5: Run, verify pass** — `go mod tidy && go test ./internal/tui/` → PASS

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/tui/render.go internal/tui/render_test.go
git commit -m "Add TUI render helpers"
```

---

### Task 5: TUI model

**Files:**
- Create: `internal/tui/model.go`, `internal/tui/model_test.go`

**Interfaces:**
- Consumes: render helpers (Task 4), `poller.Result` (Task 3), `config.View`, `preflight.Result`.
- Produces:
  - `type Options struct { Views []config.View; MaxDailyCostUSD float64; Checks []preflight.Result; Results <-chan poller.Result; PollNow func(); PollDisabled string; OpenURL func(string) error }`
  - `func New(o Options) Model`; `Model` implements `tea.Model` (`Init() tea.Cmd`, `Update(tea.Msg) (tea.Model, tea.Cmd)`, `View() tea.View`)

- [ ] **Step 1: Write the failing tests** — `internal/tui/model_test.go`

```go
package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/poller"
)

var twoViews = []config.View{{Name: "Team", Query: "qa"}, {Name: "Mine", Query: "qb"}}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func send(t *testing.T, m tea.Model, msgs ...tea.Msg) tea.Model {
	t.Helper()
	for _, msg := range msgs {
		m, _ = m.Update(msg)
	}
	return m
}

func screen(m tea.Model) string { return ansi.Strip(m.View().Content) }

func result(view string, prs ...github.PR) resultMsg {
	return resultMsg(poller.Result{View: view, PRs: prs, At: time.Now()})
}

func TestShowsPolledPRsAndDetail(t *testing.T) {
	m := send(t, New(Options{Views: twoViews, MaxDailyCostUSD: 10}),
		tea.WindowSizeMsg{Width: 120, Height: 20},
		result("Team", samplePR(1, "First PR"), samplePR(2, "Second PR")),
	)
	out := screen(m)
	for _, want := range []string{"Team (2)", "Mine (…)", "First PR", "Second PR", "o/r#1  First PR", "Spend today $0.00 / $10.00"} {
		if !strings.Contains(out, want) {
			t.Errorf("screen missing %q:\n%s", want, out)
		}
	}
	m = send(t, m, key("j"))
	if !strings.Contains(screen(m), "o/r#2  Second PR") {
		t.Errorf("j did not move selection:\n%s", screen(m))
	}
	m = send(t, m, key("j"), key("k"))
	if !strings.Contains(screen(m), "o/r#1  First PR") {
		t.Error("k did not move selection up / j moved past end")
	}
}

func TestSwitchViews(t *testing.T) {
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 100, Height: 15}, result("Team", samplePR(1, "First PR")))
	m = send(t, m, key("right"))
	if out := screen(m); !strings.Contains(out, "Loading…") || strings.Contains(out, "First PR") {
		t.Errorf("right should show Mine (loading):\n%s", out)
	}
	m = send(t, m, key("right"))
	if !strings.Contains(screen(m), "First PR") {
		t.Error("right should wrap back to Team")
	}
	m = send(t, m, key("left"))
	if !strings.Contains(screen(m), "Loading…") {
		t.Error("left should wrap to Mine")
	}
}

func TestEmptyViewAndErrors(t *testing.T) {
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 120, Height: 15}, result("Team"))
	if !strings.Contains(screen(m), "No pull requests match this view") {
		t.Errorf("empty message missing:\n%s", screen(m))
	}
	m = send(t, m, resultMsg(poller.Result{View: "Team", Err: errors.New("rate limited"), At: time.Now()}))
	if !strings.Contains(screen(m), "Team: rate limited") {
		t.Errorf("error not shown:\n%s", screen(m))
	}
	m = send(t, m, result("Team", samplePR(1, "Back")))
	if strings.Contains(screen(m), "rate limited") {
		t.Error("error should clear after the view succeeds")
	}
}

func TestErrorKeepsPreviousPRs(t *testing.T) {
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 120, Height: 15},
		result("Team", samplePR(1, "Keep me")),
		resultMsg(poller.Result{View: "Team", Err: errors.New("boom"), At: time.Now()}))
	if !strings.Contains(screen(m), "Keep me") {
		t.Error("a failed poll should not erase the last good results")
	}
}

func TestCursorClampedWhenListShrinks(t *testing.T) {
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 120, Height: 15},
		result("Team", samplePR(1, "a"), samplePR(2, "b"), samplePR(3, "c")), key("j"), key("j"),
		result("Team", samplePR(9, "only")))
	if !strings.Contains(screen(m), "o/r#9  only") {
		t.Errorf("cursor not clamped:\n%s", screen(m))
	}
}

func TestOpenSelectedPR(t *testing.T) {
	var opened string
	m := send(t, New(Options{Views: twoViews, OpenURL: func(u string) error { opened = u; return nil }}),
		tea.WindowSizeMsg{Width: 120, Height: 15}, result("Team", samplePR(1, "x")))
	_, cmd := m.Update(key("o"))
	if cmd == nil {
		t.Fatal("o returned no command")
	}
	cmd()
	if opened != "https://github.com/o/r/pull/1" {
		t.Errorf("opened %q", opened)
	}
	_, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter returned no command")
	}
}

func TestRefreshAndQuit(t *testing.T) {
	polled := 0
	m := New(Options{Views: twoViews, PollNow: func() { polled++ }})
	m2, _ := m.Update(key("R"))
	if polled != 1 {
		t.Errorf("R called PollNow %d times", polled)
	}
	_, cmd := m2.Update(key("q"))
	if cmd == nil {
		t.Fatal("q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q should quit")
	}
}

func TestInitWaitsForResults(t *testing.T) {
	ch := make(chan poller.Result, 1)
	ch <- poller.Result{View: "Team", At: time.Now()}
	cmd := New(Options{Views: twoViews, Results: ch}).Init()
	if cmd == nil {
		t.Fatal("Init should wait on the results channel")
	}
	if msg, ok := cmd().(resultMsg); !ok || msg.View != "Team" {
		t.Errorf("Init cmd returned %#v", msg)
	}
	if New(Options{Views: twoViews}).Init() != nil {
		t.Error("Init with no results channel should return nil")
	}
}

func TestPollDisabledMessage(t *testing.T) {
	m := send(t, New(Options{Views: twoViews, PollDisabled: "GitHub unavailable: not logged in"}), tea.WindowSizeMsg{Width: 120, Height: 10})
	if !strings.Contains(screen(m), "GitHub unavailable: not logged in") {
		t.Errorf("disabled reason missing:\n%s", screen(m))
	}
}

func TestNoViews(t *testing.T) {
	m := send(t, New(Options{}), tea.WindowSizeMsg{Width: 100, Height: 10}, key("right"), key("left"), key("j"), key("o"))
	if !strings.Contains(screen(m), "No views configured") {
		t.Errorf("no-views hint missing:\n%s", screen(m))
	}
}

func TestNarrowTerminal(t *testing.T) {
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 40, Height: 4}, result("Team", samplePR(1, "Tiny")))
	out := screen(m)
	if !strings.Contains(out, "Tiny") {
		t.Errorf("list missing in narrow layout:\n%s", out)
	}
	m = send(t, m, tea.WindowSizeMsg{Width: 5, Height: 1})
	screen(m) // must not panic
}

func TestUsesAltScreen(t *testing.T) {
	if !New(Options{}).View().AltScreen {
		t.Error("View should request the alternate screen")
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/tui/` → FAIL `undefined: New`, `undefined: resultMsg`

- [ ] **Step 3: Implement** — `internal/tui/model.go`

```go
// Package tui renders Ghost's terminal interface.
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/poller"
	"github.com/AdamIsrael/ghost/internal/preflight"
)

type Options struct {
	Views           []config.View
	MaxDailyCostUSD float64
	Checks          []preflight.Result
	Results         <-chan poller.Result // nil when polling is disabled
	PollNow         func()
	PollDisabled    string // why polling is off; shown in empty lists
	OpenURL         func(string) error
}

type Model struct {
	opts     Options
	active   int
	cursor   int
	prs      map[string][]github.PR
	errs     map[string]error
	lastPoll time.Time
	flash    string
	width    int
	height   int
}

type resultMsg poller.Result

type openedMsg struct{ err error }

func New(o Options) Model {
	return Model{opts: o, prs: map[string][]github.PR{}, errs: map[string]error{}}
}

func (m Model) Init() tea.Cmd { return waitForResult(m.opts.Results) }

func waitForResult(ch <-chan poller.Result) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return nil
		}
		return resultMsg(r)
	}
}

func (m Model) currentView() (config.View, bool) {
	if len(m.opts.Views) == 0 {
		return config.View{}, false
	}
	return m.opts.Views[m.active], true
}

func (m Model) currentPRs() []github.PR {
	v, ok := m.currentView()
	if !ok {
		return nil
	}
	return m.prs[v.Name]
}

func (m Model) selectedPR() *github.PR {
	prs := m.currentPRs()
	if m.cursor < 0 || m.cursor >= len(prs) {
		return nil
	}
	return &prs[m.cursor]
}

func (m *Model) clampCursor() {
	if n := len(m.currentPRs()); m.cursor >= n {
		m.cursor = max(n-1, 0)
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case resultMsg:
		m.lastPoll = msg.At
		if msg.Err != nil {
			m.errs[msg.View] = msg.Err
		} else {
			delete(m.errs, msg.View)
			// Non-nil marks the view as polled, so an empty result reads
			// "no PRs match" instead of "Loading…".
			prs := msg.PRs
			if prs == nil {
				prs = []github.PR{}
			}
			m.prs[msg.View] = prs
		}
		m.clampCursor()
		return m, waitForResult(m.opts.Results)
	case openedMsg:
		m.flash = ""
		if msg.err != nil {
			m.flash = "open failed: " + msg.err.Error()
		}
	case tea.KeyPressMsg:
		return m.handleKey(msg.String())
	}
	return m, nil
}

func (m Model) handleKey(k string) (tea.Model, tea.Cmd) {
	n := len(m.opts.Views)
	switch k {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "right", "l", "tab":
		if n > 0 {
			m.active, m.cursor = (m.active+1)%n, 0
		}
	case "left", "h", "shift+tab":
		if n > 0 {
			m.active, m.cursor = (m.active-1+n)%n, 0
		}
	case "down", "j":
		if m.cursor < len(m.currentPRs())-1 {
			m.cursor++
		}
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "R":
		if m.opts.PollNow != nil {
			m.opts.PollNow()
		}
	case "o", "enter":
		pr := m.selectedPR()
		if pr == nil || m.opts.OpenURL == nil {
			return m, nil
		}
		url, open := pr.URL, m.opts.OpenURL
		return m, func() tea.Msg { return openedMsg{err: open(url)} }
	}
	return m, nil
}

func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m Model) emptyMessage() string {
	v, ok := m.currentView()
	switch {
	case !ok:
		return "No views configured. Add a [[view]] to config.toml."
	case m.opts.PollDisabled != "":
		return m.opts.PollDisabled
	case m.prs[v.Name] == nil:
		return "Loading…"
	default:
		return "No pull requests match this view."
	}
}

func (m Model) statusErr() string {
	if m.flash != "" {
		return m.flash
	}
	for _, v := range m.opts.Views {
		if err := m.errs[v.Name]; err != nil {
			return fmt.Sprintf("%s: %v", v.Name, err)
		}
	}
	return ""
}

const minDetailWidth = 30

func (m Model) render() string {
	if m.width <= 0 || m.height <= 0 {
		return "Loading…"
	}
	counts := map[string]int{}
	for name, prs := range m.prs {
		counts[name] = len(prs)
	}
	top := renderFilterBar(m.opts.Views, m.active, counts, m.width)
	bottom := renderStatusBar(statusInfo{
		MaxDailyUSD: m.opts.MaxDailyCostUSD,
		LastPoll:    m.lastPoll,
		Err:         m.statusErr(),
		Checks:      m.opts.Checks,
	}, m.width)
	bodyH := m.height - 2
	if bodyH < 1 {
		return top
	}

	listW := max(m.width*2/5, 20)
	detailW := m.width - listW - 1
	if detailW < minDetailWidth {
		list := renderList(m.currentPRs(), m.cursor, m.width, bodyH, m.emptyMessage())
		return lipgloss.JoinVertical(lipgloss.Left, top, list, bottom)
	}
	list := renderList(m.currentPRs(), m.cursor, listW, bodyH, m.emptyMessage())
	sep := strings.TrimSuffix(strings.Repeat("│\n", bodyH), "\n")
	detail := renderDetail(m.selectedPR(), detailW, bodyH)
	body := lipgloss.JoinHorizontal(lipgloss.Top, list, dim.Render(sep), detail)
	return lipgloss.JoinVertical(lipgloss.Left, top, body, bottom)
}
```

- [ ] **Step 4: Run, verify pass** — `go test ./internal/tui/` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/model_test.go
git commit -m "Add read-only TUI model"
```

---

### Task 6: Wire the TUI into `ghost`

**Files:**
- Modify: `cmd/ghost/main.go`, `cmd/ghost/main_test.go`

**Interfaces:**
- Consumes: `tui.New/Options`, `poller.New/Searcher`, `github.NewDefault`, `preflight.Run`, M1 `config`, `store`, `paths`.
- Produces: `type deps struct { runner preflight.Runner; newSearcher func() (poller.Searcher, error); runTUI func(tea.Model) error; openURL func(string) error }`, `func defaultDeps() deps`, `func run(args []string, stdout, stderr io.Writer, d deps) int`.

- [ ] **Step 1: Update tests** — in `cmd/ghost/main_test.go`:

1. Add imports `"sync"`, `"time"`, `tea "charm.land/bubbletea/v2"`, `"github.com/AdamIsrael/ghost/internal/github"`, `"github.com/AdamIsrael/ghost/internal/poller"`.
2. Add helpers after `okRunner`:

```go
type fakeSearcher struct {
	once   sync.Once
	called chan string
}

func (f *fakeSearcher) SearchPRs(_ context.Context, q string) ([]github.PR, error) {
	f.once.Do(func() { f.called <- q })
	return nil, nil
}

// testDeps never touches the network or a terminal. runTUI records that it
// ran and, if a searcher is set, waits for the poller's first query.
func testDeps(runner preflight.Runner, s *fakeSearcher) (deps, *bool) {
	ran := new(bool)
	return deps{
		runner: runner,
		newSearcher: func() (poller.Searcher, error) {
			if s == nil {
				return nil, errors.New("gh not authenticated")
			}
			return s, nil
		},
		runTUI: func(m tea.Model) error {
			*ran = true
			m.View()
			if s != nil {
				select {
				case <-s.called:
				case <-time.After(2 * time.Second):
					return errors.New("poller never queried GitHub")
				}
			}
			return nil
		},
		openURL: func(string) error { return nil },
	}, ran
}

func depsFor(runner preflight.Runner) deps { d, _ := testDeps(runner, nil); return d }
```

3. Replace every existing `run(..., okRunner)` call with `run(..., depsFor(okRunner))`, and `run(..., preflight.Runner(failGh))` with `run(..., depsFor(failGh))`.
4. Replace the second half of `TestInitThenStart` (from `out.Reset()` to the end) with:

```go
	s := &fakeSearcher{called: make(chan string, 1)}
	d, ran := testDeps(okRunner, s)
	if code := run([]string{"--config", cfg, "--db", db}, &out, &errb, d); code != 0 {
		t.Fatalf("start exit %d: %s", code, errb.String())
	}
	if !*ran {
		t.Error("TUI was not started")
	}
	if _, err := os.Stat(db); err != nil {
		t.Errorf("database not created: %v", err)
	}
```

5. Add:

```go
func TestStartWithoutGitHub(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	var out, errb bytes.Buffer
	run([]string{"--config", cfg, "init"}, &out, &errb, depsFor(okRunner))
	d, ran := testDeps(okRunner, nil)
	if code := run([]string{"--config", cfg, "--db", filepath.Join(dir, "g.db")}, &out, &errb, d); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !*ran {
		t.Error("TUI should still open when GitHub is unavailable")
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./cmd/ghost/` → FAIL (`undefined: deps`, `cannot use depsFor(...)`)

- [ ] **Step 3: Implement** — in `cmd/ghost/main.go`:

1. Imports become:

```go
import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/cli/go-gh/v2/pkg/browser"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/paths"
	"github.com/AdamIsrael/ghost/internal/poller"
	"github.com/AdamIsrael/ghost/internal/preflight"
	"github.com/AdamIsrael/ghost/internal/store"
	"github.com/AdamIsrael/ghost/internal/tui"
)
```

2. Replace `main` and the `run` signature:

```go
// deps are the side-effecting collaborators run needs; tests replace them.
type deps struct {
	runner      preflight.Runner
	newSearcher func() (poller.Searcher, error)
	runTUI      func(tea.Model) error
	openURL     func(string) error
}

func defaultDeps() deps {
	return deps{
		runner:      preflight.ExecRunner,
		newSearcher: func() (poller.Searcher, error) { return github.NewDefault() },
		runTUI: func(m tea.Model) error {
			_, err := tea.NewProgram(m).Run()
			return err
		},
		// Discard browser launcher output so it can't corrupt the TUI.
		openURL: browser.New("", io.Discard, io.Discard).Browse,
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, defaultDeps()))
}

func run(args []string, stdout, stderr io.Writer, d deps) int {
```

3. In the `switch`, change `runCheck(*cfgPath, stdout, runner)` to `runCheck(*cfgPath, stdout, d.runner)`.
4. Replace the tail of `run` (from `defer st.Close()` through `return 0`) with:

```go
	defer st.Close()

	opts := tui.Options{
		Views:           cfg.Views,
		MaxDailyCostUSD: cfg.MaxDailyCostUSD,
		Checks:          preflight.Run(context.Background(), d.runner),
		OpenURL:         d.openURL,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if s, err := d.newSearcher(); err != nil {
		opts.PollDisabled = "GitHub unavailable: " + err.Error()
	} else if len(cfg.Views) > 0 {
		p := poller.New(s, cfg.Views, cfg.PollInterval.Duration)
		go p.Run(ctx)
		opts.Results, opts.PollNow = p.Results(), p.PollNow
	}
	if err := d.runTUI(tui.New(opts)); err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	return 0
}
```

`cfg` is now used, so the old `fmt.Fprintf(stdout, "ghost: config OK ...")` line is gone.

- [ ] **Step 4: Run, verify pass** — `go mod tidy && go vet ./... && go test -race ./...` → all PASS

- [ ] **Step 5: Manual end-to-end** in tmux, with a throwaway config pointed at a busy public repo (read-only query using your gh auth):

```bash
go build -o bin/ghost ./cmd/ghost
T=$(mktemp -d)
./bin/ghost --config $T/config.toml init
cat >> $T/config.toml <<'EOF'

[[view]]
name = "cli/cli open"
query = "repo:cli/cli is:open is:pr"
EOF
tmux new-session -d -s ghost-m2 -x 140 -y 30 "./bin/ghost --config $T/config.toml --db $T/ghost.db"
sleep 6 && tmux send-keys -t ghost-m2 Right && sleep 1 && tmux capture-pane -p -t ghost-m2
tmux send-keys -t ghost-m2 j && sleep 1 && tmux capture-pane -p -t ghost-m2 | head -12
tmux send-keys -t ghost-m2 q && sleep 1; tmux kill-session -t ghost-m2 2>/dev/null; true
```

Expected: filter bar shows `Review requested (N)` and `cli/cli open (M)` (M up to 50); after `Right`, the list shows cli/cli PRs with status icons and the detail pane shows the first PR; `j` moves the selection; status bar shows `Last poll HH:MM` and `git ✓ gh ✓ claude ✓`; `q` exits cleanly.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum cmd/ghost
git commit -m "Launch read-only TUI from ghost"
```
