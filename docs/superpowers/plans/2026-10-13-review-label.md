# Review Label Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Before auto-reviewing a PR, Ghost adds a configured label (e.g. `ai-review`) and waits one poll; removing the label opts the PR out.

**Architecture:** `github` learns to read PR labels and add one. `config` gets a global `review_label` and a per-view `label` override (`*string`: nil = inherit, `""` = off). `store` remembers, per PR and label, that the label was on the PR and whether it was later removed. The engine's `enqueue` gates each PR on the label; GitHub calls (adding a label, confirming a removal) run off the engine loop, one batch at a time.

**Tech Stack:** Go 1.27.1, go-gh v2 (GraphQL + REST), BurntSushi/toml, modernc.org/sqlite, Bubble Tea v2 / bubbles textinput.

**Spec:** `docs/superpowers/specs/2026-10-13-review-label-design.md`

## Global Constraints

- Label off (global `review_label` empty and no view override) = exactly today's behavior; no GitHub label calls.
- Label names compare case-insensitively; stored lowercase in `pr_labels`.
- Label validation: no leading/trailing whitespace, ≤ 50 characters, no commas. `""` is allowed (means off).
- Never queue an auto-review for a PR unless the label is currently on it (when a label applies). Manual re-review (`r`, `Engine.Rereview`) bypasses the label.
- Drafts are neither labeled nor reviewed.
- Store writes are single statements (no read-then-write transactions) — see beads memory on SQLite BUSY_SNAPSHOT.
- Adding a label: `POST repos/{repo}/issues/{number}/labels` with body `{"labels":["<label>"]}`; 30s timeout per call.
- Audit kinds (exact strings): `labeled`, `opted out`, `label re-added`, `label failed`.
- Ghost never writes `config.toml`.

## Review Focus

1. **Stale search results.** GitHub search can lag behind label changes, and two views can poll the same PR back to back; a PR Ghost just labeled may come back without the label. That must not count as an opt-out → the engine confirms with `GetPR` (not search) before recording one. Test: `TestStaleSearchIsNotOptOut` (Task 4).
2. **Human-added label later removed.** If a person added `ai-review` before Ghost ever saw the PR and later removes it, that is an opt-out, not a reason to add it again → the has-label path records the label. Test: `TestHumanLabelRemovedIsOptOut` (Task 4).
3. **Case mismatch.** Config says `ai-review`, the PR has `AI-Review` → treated as present. Tests: `TestHasLabelIgnoresCase` (Task 1), `TestLabeledPRIsQueued` (Task 4).
4. **GetPR failure while confirming a removal** → no opt-out recorded; retried next poll. Test: `TestConfirmFailureDoesNotOptOut` (Task 4).
5. **Slow GitHub while labeling** must not stall polls for other PRs. Test: `TestSlowLabelingDoesNotBlockPolls` (Task 4).

---

### Task 1: GitHub — read labels, add a label

**Files:**
- Modify: `internal/github/github.go` (PR struct ~line 21, `searchQuery` ~line 68, `prNode`, `toPR` ~line 170, `prQuery` ~line 193)
- Modify: `internal/github/post.go` (add `AddLabel` after `Approve`)
- Modify: `internal/github/testdata/search.json`, `internal/github/testdata/pr.json`
- Test: `internal/github/github_test.go`, `internal/github/post_test.go`

**Interfaces:**
- Produces: `PR.Labels []string`; `func (p PR) HasLabel(name string) bool`; `func (c *Client) AddLabel(ctx context.Context, repo string, number int, label string) error`.

- [ ] **Step 1: Write the failing tests**

In `internal/github/github_test.go`, `TestSearchPRsParsesFields` compares PRs with `!=`; adding a slice makes `PR` uncomparable. Change that comparison to `!reflect.DeepEqual(got, want)` (add `"reflect"` to imports) and add `Labels: []string{"ai-review", "bug"},` to `want`. Append:

```go
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
```

In `TestGetPR`, also assert `d.Labels` equals `[]string{"ai-review"}` (use `reflect.DeepEqual`).

In `internal/github/testdata/search.json`, add to the **first** PR node (number 42):
```json
"labels": {"nodes": [{"name": "ai-review"}, {"name": "bug"}]},
```
In `internal/github/testdata/pr.json`, add to the pullRequest object:
```json
"labels": {"nodes": [{"name": "ai-review"}]},
```

In `internal/github/post_test.go`, make the fake record the raw request body. Change `restCall` and the append:
```go
type restCall struct {
	method, path string
	body         ReviewPost
	raw          string
}
```
```go
func (f *fakeREST) DoWithContext(_ context.Context, method, path string, body io.Reader, resp interface{}) error {
	var p ReviewPost
	var raw []byte
	if body != nil {
		raw, _ = io.ReadAll(body)
		json.Unmarshal(raw, &p)
	}
	f.calls = append(f.calls, restCall{method: method, path: path, body: p, raw: string(raw)})
	// ... rest unchanged
```
Append:
```go
func TestAddLabel(t *testing.T) {
	f := &fakeREST{}
	if err := New(nil).WithREST(f).AddLabel(context.Background(), "o/r", 7, "ai-review"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0].method != "POST" || f.calls[0].path != "repos/o/r/issues/7/labels" ||
		f.calls[0].raw != `{"labels":["ai-review"]}` {
		t.Errorf("calls = %+v", f.calls)
	}
}

func TestAddLabelError(t *testing.T) {
	f := &fakeREST{errs: []error{&api.HTTPError{StatusCode: 403, Message: "Forbidden"}}}
	err := New(nil).WithREST(f).AddLabel(context.Background(), "o/r", 7, "ai-review")
	if err == nil || !strings.Contains(err.Error(), `add label "ai-review"`) || !IsClientError(err) {
		t.Errorf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/github/`
Expected: compile errors (`HasLabel`, `AddLabel`, `Labels` undefined).

- [ ] **Step 3: Implement**

`internal/github/github.go` — add to `PR` after `UpdatedAt`:
```go
	Labels         []string
```
After the `Status()` method:
```go
// HasLabel reports whether the PR has the named label, ignoring case as
// GitHub does.
func (p PR) HasLabel(name string) bool {
	for _, l := range p.Labels {
		if strings.EqualFold(l, name) {
			return true
		}
	}
	return false
}
```
In both `searchQuery` and `prQuery`, after the `commits(last: 1) …` line add:
```
        labels(first: 50) { nodes { name } }
```
(match each query's indentation). Add to `prNode`:
```go
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
```
In `toPR`, before `return pr`:
```go
	for _, l := range n.Labels.Nodes {
		pr.Labels = append(pr.Labels, l.Name)
	}
```

`internal/github/post.go` — after `Approve`:
```go
// AddLabel adds label to a PR, keeping its other labels. GitHub creates the
// label in the repository if it doesn't exist.
func (c *Client) AddLabel(ctx context.Context, repo string, number int, label string) error {
	path := fmt.Sprintf("repos/%s/issues/%d/labels", repo, number)
	if err := c.post(ctx, path, map[string][]string{"labels": {label}}); err != nil {
		return fmt.Errorf("add label %q: %w", label, err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/github/ && go vet ./...`
Expected: PASS; vet clean (no other code compares `github.PR` with `==`).

- [ ] **Step 5: Commit**

```bash
git add internal/github
git commit -m "github: read PR labels and add a label"
```

---

### Task 2: Config — review_label and per-view label

**Files:**
- Modify: `internal/config/config.go` (Config struct ~line 30, View struct ~line 67)
- Modify: `internal/config/validate.go` (`Validate`, `ValidateViews`)
- Modify: `internal/config/default_config.toml`
- Create: `internal/config/label.go`
- Test: `internal/config/label_test.go`

**Interfaces:**
- Produces: `Config.ReviewLabel string` (`review_label`); `View.Label *string` (`label,omitempty`); `func (c *Config) LabelFor(v View) string`.

- [ ] **Step 1: Write the failing tests**

Create `internal/config/label_test.go`:
```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

func TestLabelFor(t *testing.T) {
	c := Config{ReviewLabel: "ai-review"}
	cases := []struct {
		v    View
		want string
	}{
		{View{}, "ai-review"},
		{View{Label: ptr("")}, ""},
		{View{Label: ptr("needs-ai")}, "needs-ai"},
	}
	for _, tc := range cases {
		if got := c.LabelFor(tc.v); got != tc.want {
			t.Errorf("LabelFor(%v) = %q, want %q", tc.v.Label, got, tc.want)
		}
	}
	if got := (&Config{}).LabelFor(View{}); got != "" {
		t.Errorf("no global label: got %q", got)
	}
}

func TestValidateLabels(t *testing.T) {
	ok := mustParse(t, "review_label = \"ai-review\"\n"+minimal+"[[view]]\nname = \"V\"\nquery = \"q\"\nlabel = \"\"\n")
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []string{" ai", "ai ", "   ", "a,b", strings.Repeat("x", 51)}
	for _, b := range bad {
		cfg := mustParse(t, "review_label = \""+b+"\"\n"+minimal)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "review_label") {
			t.Errorf("review_label %q: err = %v", b, err)
		}
		cfg = mustParse(t, minimal+"[[view]]\nname = \"V\"\nquery = \"q\"\nlabel = \""+b+"\"\n")
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), `view "V": label`) {
			t.Errorf("view label %q: err = %v", b, err)
		}
	}
	if cfg := mustParse(t, "review_label = \""+strings.Repeat("é", 50)+"\"\n"+minimal); cfg.Validate() != nil {
		t.Error("50 characters (not bytes) should be allowed")
	}
}

func TestViewLabelRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	writeFile(t, cfg, minimal)
	views := []View{
		{Name: "Inherit", Query: "q"},
		{Name: "Off", Query: "q", Label: ptr("")},
		{Name: "Own", Query: "q", Label: ptr("needs-ai")},
	}
	if err := SaveViews(ViewsPath(cfg), views); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(ViewsPath(cfg))
	if strings.Count(string(data), "label =") != 2 {
		t.Errorf("views.toml =\n%s", data)
	}
	c, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c.Views[0].Label != nil || c.Views[1].Label == nil || *c.Views[1].Label != "" ||
		c.Views[2].Label == nil || *c.Views[2].Label != "needs-ai" {
		t.Errorf("labels = %v %v %v", c.Views[0].Label, c.Views[1].Label, c.Views[2].Label)
	}
}

func TestDefaultConfigLeavesLabelOff(t *testing.T) {
	if strings.Contains(defaultConfig, "\nreview_label") {
		t.Error("the starter config must not turn the label on")
	}
	if !strings.Contains(defaultConfig, "# review_label = \"ai-review\"") {
		t.Error("the starter config should show review_label commented out")
	}
}
```
Before writing the last test, check the embedded default config's variable name: `grep -n "go:embed default_config.toml" -A 1 internal/config/*.go`. If it isn't `defaultConfig`, use the actual name.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/`
Expected: compile errors (`ReviewLabel`, `Label`, `LabelFor` undefined).

- [ ] **Step 3: Implement**

`internal/config/config.go` — in `Config`, after `ApprovalMessage`:
```go
	ReviewLabel          string           `toml:"review_label"`
```
In `View`, after `Agent`:
```go
	// Label overrides review_label for this view: nil = use review_label,
	// "" = no label, otherwise the label to require.
	Label *string `toml:"label,omitempty"`
```

Create `internal/config/label.go`:
```go
package config

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// LabelFor returns the review label that applies to v: the view's own label
// if set ("" meaning none), otherwise review_label. "" means no label gate.
func (c *Config) LabelFor(v View) string {
	if v.Label != nil {
		return *v.Label
	}
	return c.ReviewLabel
}

// checkLabel enforces GitHub's label limits. "" is valid: it turns the label off.
func checkLabel(s string) error {
	switch {
	case s == "":
		return nil
	case strings.TrimSpace(s) != s:
		return errors.New("must not start or end with spaces")
	case utf8.RuneCountInString(s) > 50:
		return errors.New("must be at most 50 characters")
	case strings.Contains(s, ","):
		return errors.New("must not contain commas")
	}
	return nil
}
```

`internal/config/validate.go` — in `Validate`, after the `review_footer`/`approval_message` loop:
```go
	if err := checkLabel(c.ReviewLabel); err != nil {
		add("review_label %q %v", c.ReviewLabel, err)
	}
```
In `ValidateViews`, inside the loop after the agent check:
```go
		if v.Label != nil {
			if err := checkLabel(*v.Label); err != nil {
				add("%s: label %q %v", label, *v.Label, err)
			}
		}
```
(`label` there is the existing `view "Name"` description variable.)

`internal/config/default_config.toml` — after the `approval_message` example, before `default_agent`:
```toml
# Label PRs before reviewing them. When set, Ghost adds this label to a PR
# and reviews it on the next poll. Removing the label opts the PR out (add
# it back to resume). A view can override it with `label = "..."`, or turn
# it off with `label = ""`. Off when empty.
# review_label = "ai-review"

```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "config: review_label with per-view override"
```

---

### Task 3: Store — remember labels and opt-outs

**Files:**
- Modify: `internal/store/store.go` (append migration 4 to `migrations`)
- Create: `internal/store/labels.go`
- Test: `internal/store/labels_test.go`

**Interfaces:**
- Produces:
  ```go
  type LabelState int
  const (LabelNever LabelState = iota; LabelAdded; LabelOptedOut)
  func (s *Store) LabelState(prID int64, label string) (LabelState, error)
  func (s *Store) MarkLabeled(prID int64, label string) error
  func (s *Store) MarkOptedOut(prID int64, label string) (bool, error)
  func (s *Store) ClearOptOut(prID int64, label string) (bool, error)
  ```

- [ ] **Step 1: Write the failing tests**

Create `internal/store/labels_test.go`:
```go
package store

import (
	"path/filepath"
	"testing"

	"github.com/AdamIsrael/ghost/internal/github"
)

func labelStore(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ghost.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	id, err := s.UpsertPR(github.PR{Repo: "o/r", Number: 1, HeadSHA: "a"})
	if err != nil {
		t.Fatal(err)
	}
	return s, id
}

func TestLabelLifecycle(t *testing.T) {
	s, pr := labelStore(t)
	if st, err := s.LabelState(pr, "ai-review"); err != nil || st != LabelNever {
		t.Fatalf("new: %v %v", st, err)
	}
	if changed, _ := s.MarkOptedOut(pr, "ai-review"); changed {
		t.Error("cannot opt out of a label never recorded")
	}
	for i := 0; i < 2; i++ { // idempotent
		if err := s.MarkLabeled(pr, "AI-Review"); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := s.LabelState(pr, "ai-review"); st != LabelAdded {
		t.Fatalf("after mark (case-insensitive): %v", st)
	}
	if changed, err := s.MarkOptedOut(pr, "ai-review"); err != nil || !changed {
		t.Fatalf("opt out: %v %v", changed, err)
	}
	if changed, _ := s.MarkOptedOut(pr, "ai-review"); changed {
		t.Error("second opt-out should report no change")
	}
	if st, _ := s.LabelState(pr, "ai-review"); st != LabelOptedOut {
		t.Fatalf("after opt-out: %v", st)
	}
	if err := s.MarkLabeled(pr, "ai-review"); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.LabelState(pr, "ai-review"); st != LabelOptedOut {
		t.Error("MarkLabeled must not clear an opt-out")
	}
	if changed, err := s.ClearOptOut(pr, "ai-review"); err != nil || !changed {
		t.Fatalf("clear: %v %v", changed, err)
	}
	if changed, _ := s.ClearOptOut(pr, "ai-review"); changed {
		t.Error("second clear should report no change")
	}
	if st, _ := s.LabelState(pr, "ai-review"); st != LabelAdded {
		t.Fatalf("after clear: %v", st)
	}
	if st, _ := s.LabelState(pr, "other"); st != LabelNever {
		t.Error("labels are tracked separately")
	}
}

func TestMigrationFromVersion3KeepsData(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ghost.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	pr, _ := s.UpsertPR(github.PR{Repo: "o/r", Number: 9, HeadSHA: "a"})
	if _, err := s.db.Exec(`DROP TABLE pr_labels; PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.SchemaVersion(); v != 4 {
		t.Errorf("version = %d, want 4", v)
	}
	if err := s.MarkLabeled(pr, "ai-review"); err != nil {
		t.Fatalf("pr_labels missing after migration: %v", err)
	}
	if id, _ := s.UpsertPR(github.PR{Repo: "o/r", Number: 9, HeadSHA: "a"}); id != pr {
		t.Error("existing PR row was lost")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store/`
Expected: compile errors (`LabelState` etc. undefined).

- [ ] **Step 3: Implement**

`internal/store/store.go` — append a fourth element to `migrations`:
```go
	`CREATE TABLE pr_labels (
		pr_id        INTEGER NOT NULL REFERENCES prs(id),
		label        TEXT    NOT NULL,
		added_at     TEXT    NOT NULL,
		opted_out_at TEXT,
		PRIMARY KEY (pr_id, label)
	);`,
```

Create `internal/store/labels.go`:
```go
package store

import (
	"database/sql"
	"errors"
	"strings"
)

// LabelState is what Ghost knows about a review label on a PR.
type LabelState int

const (
	LabelNever    LabelState = iota // never seen on the PR or added by Ghost
	LabelAdded                      // was on the PR (added by Ghost or seen there)
	LabelOptedOut                   // removed after that: don't review
)

// LabelState reports whether label was ever on the PR, and whether it was
// removed since. Labels compare case-insensitively.
func (s *Store) LabelState(prID int64, label string) (LabelState, error) {
	var opted sql.NullString
	err := s.db.QueryRow(`SELECT opted_out_at FROM pr_labels WHERE pr_id = ? AND label = ?`,
		prID, strings.ToLower(label)).Scan(&opted)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return LabelNever, nil
	case err != nil:
		return LabelNever, err
	case opted.Valid:
		return LabelOptedOut, nil
	}
	return LabelAdded, nil
}

// MarkLabeled records that label is (or was) on the PR. It never clears an
// opt-out; use ClearOptOut for that.
func (s *Store) MarkLabeled(prID int64, label string) error {
	_, err := s.db.Exec(`INSERT INTO pr_labels (pr_id, label, added_at) VALUES (?, ?, ?)
		ON CONFLICT (pr_id, label) DO NOTHING`, prID, strings.ToLower(label), ts(s.now()))
	return err
}

// MarkOptedOut records that a recorded label was removed. It reports whether
// anything changed, so the opt-out is audited once.
func (s *Store) MarkOptedOut(prID int64, label string) (bool, error) {
	res, err := s.db.Exec(`UPDATE pr_labels SET opted_out_at = ?
		WHERE pr_id = ? AND label = ? AND opted_out_at IS NULL`, ts(s.now()), prID, strings.ToLower(label))
	return changed(res, err)
}

// ClearOptOut records that the label was added back. It reports whether
// anything changed.
func (s *Store) ClearOptOut(prID int64, label string) (bool, error) {
	res, err := s.db.Exec(`UPDATE pr_labels SET opted_out_at = NULL
		WHERE pr_id = ? AND label = ? AND opted_out_at IS NOT NULL`, prID, strings.ToLower(label))
	return changed(res, err)
}

func changed(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
```
If `changed` collides with an existing identifier in package `store` (`grep -n "func changed" internal/store/*.go`), name it `oneRow` instead.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/`
Expected: PASS, including `TestMigrationsApplyAndAreIdempotent` (now version 4).

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "store: remember review labels and opt-outs"
```

---

### Task 4: Engine — gate auto-reviews on the label

**Files:**
- Create: `internal/engine/labels.go`
- Modify: `internal/engine/engine.go` (`Deps` ~line 25, `Engine` struct ~line 55, `New`, `Run` poll case ~line 150, `enqueue` ~line 198)
- Test: `internal/engine/labels_test.go`; modify harness in `internal/engine/engine_test.go`

**Interfaces:**
- Consumes: `github.PR.HasLabel`, `Config.LabelFor`, `store.LabelState/MarkLabeled/MarkOptedOut/ClearOptOut` and constants `store.LabelNever/LabelAdded/LabelOptedOut`.
- Produces: `Deps.AddLabel func(ctx context.Context, repo string, number int, label string) error`.

- [ ] **Step 1: Extend the test harness**

In `internal/engine/engine_test.go`, add fields to `harness`:
```go
	labels        map[int][]string // PR number → labels GetPR reports (AddLabel appends)
	labelCalls    int
	getPRCalls    map[int]int // PR number → GetPR calls
	addLabelErr   error
	addLabelBlock chan struct{} // if set, AddLabel waits for it to close
```
In `newHarness`, initialize `labels: map[int][]string{}, getPRCalls: map[int]int{}` in the `&harness{...}` literal. In the `GetPR` fake, increment `h.getPRCalls[n]++` right after `defer h.mu.Unlock()` (before the error check) and add `Labels: append([]string(nil), h.labels[n]...)` to the returned `github.PR`. Add to `Deps`:
```go
		AddLabel: func(_ context.Context, repo string, n int, label string) error {
			h.mu.Lock()
			h.labelCalls++
			err, block := h.addLabelErr, h.addLabelBlock
			h.mu.Unlock()
			if block != nil {
				<-block
			}
			if err != nil {
				return err
			}
			h.mu.Lock()
			h.labels[n] = append(h.labels[n], label)
			h.mu.Unlock()
			return nil
		},
```
Add helpers:
```go
func labeledPR(n int, sha string, labels ...string) github.PR {
	p := ghPR(n, sha)
	p.Labels = labels
	return p
}

func (h *harness) labelCallCount() int { h.mu.Lock(); defer h.mu.Unlock(); return h.labelCalls }
func (h *harness) getPRCallCount(n int) int { h.mu.Lock(); defer h.mu.Unlock(); return h.getPRCalls[n] }
func (h *harness) setLabels(n int, labels ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.labels[n] = labels
}
func (h *harness) labelsOf(n int) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.labels[n]...)
}
func (h *harness) auditCount(n int, kind string) int {
	c := 0
	for _, ev := range h.e.PRState("o/r", n).Audit {
		if ev.Kind == kind {
			c++
		}
	}
	return c
}
```

- [ ] **Step 2: Write the failing tests**

Create `internal/engine/labels_test.go`:
```go
package engine

import (
	"errors"
	"testing"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/store"
)

func labelHarness(t *testing.T, mutate func(*config.Config)) *harness {
	return newHarness(t, func(c *config.Config) {
		c.ReviewLabel = "ai-review"
		if mutate != nil {
			mutate(c)
		}
	})
}

func strPtr(s string) *string { return &s }

// settle sends a labeled PR 99 and waits for it to queue: every poll sent
// before it has then been through enqueue.
func (h *harness) settle(t *testing.T) {
	t.Helper()
	h.poll("Team", labeledPR(99, "s99", "ai-review"))
	waitFor(t, "sentinel queued", func() bool { return h.state(99) != "" })
}

func TestLabelOffQueuesWithoutLabel(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	if h.labelCallCount() != 0 {
		t.Error("no label configured: AddLabel must not be called")
	}
}

func TestLabeledPRIsQueued(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", labeledPR(1, "sha1", "AI-Review"))
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	if h.labelCallCount() != 0 {
		t.Error("label already present: AddLabel must not be called")
	}
}

func TestUnlabeledPRIsLabeledThenQueuedNextPoll(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "labeled")
	if h.state(1) != "" {
		t.Fatalf("queued in the labeling round: %s", h.state(1))
	}
	if got := h.labelsOf(1); len(got) != 1 || got[0] != "ai-review" {
		t.Fatalf("labels = %v", got)
	}
	h.poll("Team", labeledPR(1, "sha1", "ai-review"))
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
}

func TestRemovedLabelOptsOutOnce(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "labeled")
	h.setLabels(1) // someone removes it
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "opted out")
	calls := h.getPRCallCount(1)
	h.poll("Team", ghPR(1, "sha2")) // a new commit doesn't undo the opt-out
	h.settle(t)
	if h.state(1) != "" || h.auditCount(1, "opted out") != 1 || h.labelCallCount() != 1 || h.getPRCallCount(1) != calls {
		t.Errorf("state %q opt-outs %d label calls %d getPR %d→%d",
			h.state(1), h.auditCount(1, "opted out"), h.labelCallCount(), calls, h.getPRCallCount(1))
	}
}

func TestReaddedLabelResumes(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "labeled")
	h.setLabels(1)
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "opted out")
	h.poll("Team", labeledPR(1, "sha1", "ai-review"))
	h.waitAudit(t, 1, "label re-added")
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
}

func TestStaleSearchIsNotOptOut(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "labeled")
	// Search hasn't caught up: no label in the result, but GitHub has it.
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "confirm via GetPR", func() bool { return h.getPRCallCount(1) >= 1 })
	h.poll("Team", labeledPR(1, "sha1", "ai-review"))
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	if h.auditCount(1, "opted out") != 0 {
		t.Error("stale search result was treated as an opt-out")
	}
}

func TestHumanLabelRemovedIsOptOut(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", labeledPR(1, "sha1", "ai-review")) // a person added it
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	h.poll("Team", ghPR(1, "sha2")) // and removed it (GetPR reports no labels)
	h.waitAudit(t, 1, "opted out")
	if h.labelCallCount() != 0 {
		t.Error("Ghost re-added a label a person removed")
	}
}

func TestConfirmFailureDoesNotOptOut(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "labeled")
	h.setLabels(1)
	h.mu.Lock()
	h.getPRErr = errors.New("network down")
	h.mu.Unlock()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "confirm attempted", func() bool { return h.getPRCallCount(1) >= 1 })
	if st, _ := h.st.LabelState(prIDOf(t, h, 1), "ai-review"); st != store.LabelAdded {
		t.Fatalf("label state = %v after a failed confirm", st)
	}
	h.mu.Lock()
	h.getPRErr = nil
	h.mu.Unlock()
	waitFor(t, "opted out on retry", func() bool {
		h.poll("Team", ghPR(1, "sha1"))
		return h.auditCount(1, "opted out") == 1
	})
}

func TestLabelFailureRetriedAndAuditedOnce(t *testing.T) {
	h := labelHarness(t, nil)
	h.addLabelErr = errors.New("HTTP 403: Resource not accessible")
	h.start()
	waitFor(t, "three attempts", func() bool {
		h.poll("Team", ghPR(1, "sha1"))
		return h.labelCallCount() >= 3
	})
	h.waitAudit(t, 1, "label failed")
	if n := h.auditCount(1, "label failed"); n != 1 || h.state(1) != "" {
		t.Fatalf("label failed audits %d, state %q", n, h.state(1))
	}
	h.mu.Lock()
	h.addLabelErr = nil
	h.mu.Unlock()
	waitFor(t, "labeled after recovery", func() bool {
		h.poll("Team", ghPR(1, "sha1"))
		return h.auditCount(1, "labeled") == 1
	})
}

func TestDraftIsNotLabeled(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	d := ghPR(1, "sha1")
	d.IsDraft = true
	h.poll("Team", d)
	h.settle(t)
	if h.labelCallCount() != 0 || h.state(1) != "" {
		t.Errorf("draft: label calls %d state %q", h.labelCallCount(), h.state(1))
	}
}

func TestRereviewBypassesLabel(t *testing.T) {
	h := labelHarness(t, nil)
	h.start()
	if err := h.e.Rereview(ghPR(1, "sha1")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	if h.labelCallCount() != 0 {
		t.Error("re-review must not add the label")
	}
}

func TestViewLabelOverride(t *testing.T) {
	h := labelHarness(t, func(c *config.Config) { c.Views[0].Label = strPtr("needs-ai") })
	h.start()
	h.poll("Team", labeledPR(1, "sha1", "ai-review")) // the global label doesn't count here
	h.waitAudit(t, 1, "labeled")
	if got := h.labelsOf(1); len(got) != 1 || got[0] != "needs-ai" {
		t.Errorf("labels = %v", got)
	}
}

func TestViewLabelOff(t *testing.T) {
	h := labelHarness(t, func(c *config.Config) { c.Views[0].Label = strPtr("") })
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "queued", func() bool { return h.state(1) != "" })
	if h.labelCallCount() != 0 {
		t.Error(`label = "" on the view: AddLabel must not be called`)
	}
}

func TestSlowLabelingDoesNotBlockPolls(t *testing.T) {
	h := labelHarness(t, nil)
	block := make(chan struct{})
	h.addLabelBlock = block
	defer close(block)
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "labeling started", func() bool { return h.labelCallCount() == 1 })
	h.poll("Team", labeledPR(2, "sha2", "ai-review"))
	waitFor(t, "other PR queued while labeling is stuck", func() bool { return h.state(2) != "" })
}

func prIDOf(t *testing.T, h *harness, n int) int64 {
	t.Helper()
	id, err := h.st.UpsertPR(ghPR(n, h.heads[n]))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
```
Note: `settle` uses PR 99 with the label present; with the label gate it queues on its first poll.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/engine/ -run 'Label|Rereview|Draft'`
Expected: compile error (`AddLabel` not a field of `Deps`).

- [ ] **Step 4: Implement**

`internal/engine/engine.go`:
- In `Deps`, after `Approve`:
  ```go
  	AddLabel    func(ctx context.Context, repo string, number int, label string) error
  ```
- In `Engine`, next to `recheckMu`:
  ```go
  	labelMu    sync.Mutex     // one labeling batch at a time, off the Run loop
  ```
  and next to `unknownAt`:
  ```go
  	labelErr  map[int64]string    // PR ID → last "label failed" message audited
  ```
- In `New`, add `labelErr: map[int64]string{},`.
- In `Run`, replace `e.enqueue(r)` with:
  ```go
  			if jobs := e.enqueue(r); len(jobs) > 0 && e.labelMu.TryLock() {
  				// Labeling calls GitHub; keep it off the loop. If a batch is
  				// still running, skip: nothing was recorded, so the next poll
  				// produces the same jobs.
  				e.wg.Add(1)
  				go func() {
  					defer e.wg.Done()
  					defer e.labelMu.Unlock()
  					e.labelPRs(ctx, jobs)
  				}()
  			}
  ```
- Change `enqueue` to return `[]labelJob`: signature `func (e *Engine) enqueue(res poller.Result) []labelJob`; the two early `return`s become `return nil`; declare `var jobs []labelJob` and `label := e.cfg.LabelFor(v)` after `agentName`; after the `UpsertPR` error check insert:
  ```go
  		ok, job := e.gateLabel(prID, pr, label)
  		if job != nil {
  			jobs = append(jobs, *job)
  		}
  		if !ok {
  			continue
  		}
  ```
  and end the function with `return jobs`.

Create `internal/engine/labels.go`:
```go
package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/store"
)

const labelTimeout = 30 * time.Second

// labelJob is GitHub work the label gate needs: add the label, or confirm
// that a label Ghost recorded is really gone before counting an opt-out
// (search results can lag behind label changes).
type labelJob struct {
	prID    int64
	pr      github.PR
	label   string
	confirm bool
}

// gateLabel applies the review-label rules to one PR from a poll. It reports
// whether the PR may be queued now, plus any GitHub work for labelPRs.
func (e *Engine) gateLabel(prID int64, pr github.PR, label string) (bool, *labelJob) {
	if label == "" {
		return true, nil
	}
	if pr.HasLabel(label) {
		// Record it even if a person added it, so removing it later is an
		// opt-out rather than a reason to add it again.
		_ = e.d.Store.MarkLabeled(prID, label)
		if changed, err := e.d.Store.ClearOptOut(prID, label); err == nil && changed {
			e.audit(prID, 0, "label re-added", label+" is back; reviews resume")
		}
		return true, nil
	}
	state, err := e.d.Store.LabelState(prID, label)
	switch {
	case err != nil, state == store.LabelOptedOut:
		return false, nil
	case state == store.LabelAdded:
		return false, &labelJob{prID: prID, pr: pr, label: label, confirm: true}
	default:
		return false, &labelJob{prID: prID, pr: pr, label: label}
	}
}

// labelPRs runs the GitHub side of the label gate. Nothing is queued here:
// a labeled PR is reviewed when a later poll shows the label.
func (e *Engine) labelPRs(ctx context.Context, jobs []labelJob) {
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		jctx, cancel := context.WithTimeout(ctx, labelTimeout)
		if j.confirm {
			e.confirmOptOut(jctx, j)
		} else {
			e.addLabel(jctx, j)
		}
		cancel()
	}
}

func (e *Engine) addLabel(ctx context.Context, j labelJob) {
	if e.d.AddLabel == nil {
		e.labelFailed(j.prID, "Ghost cannot add labels in this mode")
		return
	}
	if err := e.d.AddLabel(ctx, j.pr.Repo, j.pr.Number, j.label); err != nil {
		e.labelFailed(j.prID, err.Error())
		return
	}
	e.mu.Lock()
	delete(e.labelErr, j.prID)
	e.mu.Unlock()
	if err := e.d.Store.MarkLabeled(j.prID, j.label); err != nil {
		return // the next poll sees the label on the PR and records it
	}
	e.audit(j.prID, 0, "labeled", fmt.Sprintf("added %s; reviews on the next poll (remove the label to opt out)", j.label))
}

// confirmOptOut checks GitHub directly before treating a missing label as
// removed: the poll may have come from a search index that hadn't caught up.
func (e *Engine) confirmOptOut(ctx context.Context, j labelJob) {
	d, err := e.d.GetPR(ctx, j.pr.Repo, j.pr.Number)
	if err != nil || d.HasLabel(j.label) {
		return // unknown, or still labeled: decide on a later poll
	}
	if changed, err := e.d.Store.MarkOptedOut(j.prID, j.label); err == nil && changed {
		e.audit(j.prID, 0, "opted out", fmt.Sprintf("%s was removed; not reviewing this PR until it is added back", j.label))
	}
}

// labelFailed audits a labeling error, once per distinct message per PR.
func (e *Engine) labelFailed(prID int64, msg string) {
	e.mu.Lock()
	same := e.labelErr[prID] == msg
	e.labelErr[prID] = msg
	e.mu.Unlock()
	if !same {
		e.audit(prID, 0, "label failed", msg+"; not reviewing until the label is added (retried every poll)")
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/engine/`
Expected: PASS (all engine tests, old and new).

- [ ] **Step 6: Stress**

Run: `go test -count=200 ./internal/engine/ -run 'Label|Rereview|Draft|Opt|Stale|Slow|Confirm'` and `go test -race -count=20 ./internal/engine/`
Expected: PASS both. If anything flakes, find the race (don't add sleeps).

- [ ] **Step 7: Commit**

```bash
git add internal/engine
git commit -m "engine: label PRs before auto-review; removing the label opts out"
```

---

### Task 5: Wiring, TUI form, --check, docs

**Files:**
- Modify: `cmd/ghost/main.go` (`deps` struct ~line 32, default deps ~line 80, engine construction ~line 198, `opts` ~line 187, `--check` ~line 291)
- Modify: `cmd/ghost/main_test.go` (`testDeps` — add an `addLabel` stub; `TestCheck`)
- Modify: `internal/tui/form.go`, `internal/tui/model.go` (Options + two `newViewForm` calls ~lines 364, 375)
- Modify: `internal/tui/form_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `github.Client.AddLabel`, `engine.Deps.AddLabel`, `config.Config.ReviewLabel`, `config.View.Label`.
- Produces: `tui.Options.ReviewLabel string`; `newViewForm(v config.View, editing string, agents []string, globalLabel string) viewForm`.

- [ ] **Step 1: Write the failing tests**

In `internal/tui/form_test.go`, update every existing `newViewForm(...)` call to pass a fourth argument `""`:
```bash
perl -pi -e 's/newViewForm\((.*?), (\[\]string\{[^}]*\})\)/newViewForm($1, $2, "")/g' internal/tui/form_test.go
```
(check with `grep -n newViewForm internal/tui/*_test.go` that every call now has four arguments). Append:
```go
func labelField(f viewForm) viewForm {
	f, _ = formKeys(f, tab, tab, tab, tab) // name → query → auto → agent → label
	return f
}

func TestFormLabelStates(t *testing.T) {
	cases := []struct {
		typed string
		want  *string
	}{
		{"", nil},
		{"-", strPtr("")},
		{" needs-ai ", strPtr("needs-ai")},
	}
	for _, tc := range cases {
		f := labelField(newViewForm(config.View{Name: "V", Query: "q"}, "", nil, "ai-review"))
		f, _ = formKeys(f, typed(tc.typed)...)
		got := f.view().Label
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("typed %q: label = %v, want %v", tc.typed, got, tc.want)
		}
	}
}

func TestFormLabelPrefill(t *testing.T) {
	for _, tc := range []struct {
		in   *string
		show string
	}{{nil, ""}, {strPtr(""), "-"}, {strPtr("needs-ai"), "needs-ai"}} {
		f := newViewForm(config.View{Name: "V", Query: "q", Label: tc.in}, "V", nil, "ai-review")
		if f.label.Value() != tc.show {
			t.Errorf("prefill %v: field = %q, want %q", tc.in, f.label.Value(), tc.show)
		}
		if got := f.view().Label; (got == nil) != (tc.in == nil) || (got != nil && *got != *tc.in) {
			t.Errorf("prefill %v did not round-trip: %v", tc.in, got)
		}
	}
}

func TestFormLabelPlaceholderShowsDefault(t *testing.T) {
	if f := newViewForm(config.View{}, "", nil, "ai-review"); !strings.Contains(f.label.Placeholder, "ai-review") {
		t.Errorf("placeholder = %q", f.label.Placeholder)
	}
	if f := newViewForm(config.View{}, "", nil, ""); !strings.Contains(f.label.Placeholder, "none") {
		t.Errorf("placeholder = %q", f.label.Placeholder)
	}
}

func strPtr(s string) *string { return &s }
```
If `strPtr` already exists in package `tui` tests, drop this definition.

In `cmd/ghost/main_test.go` `TestCheck`, after the first successful `--check`, add:
```go
	if !strings.Contains(out.String(), "label") || !strings.Contains(out.String(), "off") {
		t.Errorf("check should report the review label as off: %q", out.String())
	}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ ./cmd/ghost/`
Expected: compile errors (`newViewForm` arity, `f.label` undefined) and `TestCheck` failing on the label line.

- [ ] **Step 3: Implement the form**

`internal/tui/form.go`:
- Add `fieldLabel` after `fieldAgent` in the field const block (before `fieldCount`).
- Add `label textinput.Model` to `viewForm`.
- `newViewForm(v config.View, editing string, agents []string, globalLabel string) viewForm` — after the query input setup:
  ```go
  	f.label = textinput.New()
  	f.label.CharLimit = 50
  	if globalLabel != "" {
  		f.label.Placeholder = globalLabel + " (default; - for none)"
  	} else {
  		f.label.Placeholder = "none (default)"
  	}
  	switch {
  	case v.Label == nil:
  	case *v.Label == "":
  		f.label.SetValue("-")
  	default:
  		f.label.SetValue(*v.Label)
  	}
  ```
- `setFocus`: add `f.label.Blur()` with the others and `case fieldLabel: f.label.Focus()`.
- `view()`: compute the label and set `Label: lbl` in the returned struct:
  ```go
  	var lbl *string
  	switch s := strings.TrimSpace(f.label.Value()); s {
  	case "":
  	case "-":
  		lbl = new(string)
  	default:
  		lbl = &s
  	}
  ```
- `update`: add `case fieldLabel: f.label, cmd = f.label.Update(msg)` to the focus switch.
- `paste`: add `case fieldLabel: f.label, cmd = f.label.Update(msg)`.
- `render`: `f.label.SetWidth(max(width-20, 10))` with the others, and add the line after Agent:
  ```go
  		marker(fieldLabel) + "Label       " + f.label.View(),
  ```

`internal/tui/model.go`: add to `Options`:
```go
	ReviewLabel     string                    // review_label: the view form's default
```
and pass `m.opts.ReviewLabel` as the fourth argument in both `newViewForm` calls.

- [ ] **Step 4: Implement wiring and --check**

`cmd/ghost/main.go`:
- `deps` struct: add `addLabel func(ctx context.Context, repo string, number int, label string) error`.
- Default deps, after `approve`:
  ```go
  		addLabel: func(ctx context.Context, repo string, number int, label string) error {
  			c, err := github.NewDefault()
  			if err != nil {
  				return err
  			}
  			return c.AddLabel(ctx, repo, number, label)
  		},
  ```
- Engine construction: add `AddLabel: d.addLabel` to `engine.Deps{...}`.
- `opts`: add `ReviewLabel: cfg.ReviewLabel,`.
- `--check`, right after the `✓ config` line:
  ```go
  		if cfg.ReviewLabel == "" {
  			fmt.Fprintf(stdout, "✓ %-7s %s\n", "label", "off (PRs are reviewed without a label)")
  		} else {
  			fmt.Fprintf(stdout, "✓ %-7s %s\n", "label", cfg.ReviewLabel+" (added before review; removing it opts a PR out)")
  		}
  ```
`cmd/ghost/main_test.go` `testDeps`: add `addLabel: func(context.Context, string, int, string) error { return nil },` to the returned `deps`.

- [ ] **Step 5: Docs**

`README.md`:
- In "How it works", after step 2 add:
  `   If \`review_label\` is set, Ghost first adds that label to the PR and reviews it on the next poll; see [Review label](#review-label).`
- In the configuration table after `approval_message`:
  `| \`review_label\` | \`""\` (off) | Label Ghost adds before reviewing; removing it opts a PR out |`
- In the Views example, add `label = "needs-ai"        # optional; overrides review_label, "" = none`.
- New section after "Footer and approval message":
  ~~~markdown
  ### Review label

  ```toml
  review_label = "ai-review"
  ```

  With a label set, Ghost doesn't review a PR the first time it sees it. It adds the label (creating it in the repo if needed) and reviews on the next poll, which gives the author a heads-up. Removing the label opts the PR out: Ghost won't add it again or review that PR, even after new commits, until someone adds the label back. A PR that already has the label is reviewed right away.

  - A view can use a different label (`label = "needs-ai"`) or none (`label = ""`); in the TUI's view form, leave **Label** blank for the default or type `-` for none.
  - Adding labels needs triage or write access to the repo. If it fails, the PR isn't reviewed and the audit trail says why; Ghost retries every poll.
  - Pressing `r` reviews a PR regardless of the label.
  ~~~
- Keys section, view form sentence: add "type `-` in Label for no label".

- [ ] **Step 6: Run everything**

Run:
```bash
test -z "$(gofmt -l .)" && go mod tidy -diff && go vet ./... && go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./... && go test -race ./...
```
Expected: all clean, all PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd internal/tui README.md
git commit -m "Wire the review label: TUI view form, --check, README"
```

---

### Task 6: Live check on the sandbox (operator go-ahead required)

**Files:** none (manual).

- [ ] **Step 1:** Ask the operator before writing to `AdamIsrael/ghost-sandbox`. With approval: build `go build -o bin/ghost ./cmd/ghost`, use a scratch config dir with `review_label = "ghost-test"`, `post_mode = "confirm"`, and a view `repo:AdamIsrael/ghost-sandbox is:open is:pr` with `auto_review = true`.
- [ ] **Step 2:** Start Ghost in tmux; confirm the sandbox PR gets the `ghost-test` label (`gh pr view <n> --repo AdamIsrael/ghost-sandbox --json labels`) and the audit shows `labeled`.
- [ ] **Step 3:** Press `R` (poll now) once the label is visible; confirm the review queues. Quit before it runs if the operator hasn't approved the spend (press `p` to pause first).
- [ ] **Step 4:** Remove the label (`gh pr edit <n> --repo AdamIsrael/ghost-sandbox --remove-label ghost-test`), poll, confirm `opted out` in the audit and that the label is not re-added.
- [ ] **Step 5:** Clean up: delete the `ghost-test` label from the sandbox (`gh label delete ghost-test --repo AdamIsrael/ghost-sandbox --yes`) with the operator's OK.
