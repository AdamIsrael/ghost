# M5 Approvals Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After Ghost posts a review, it checks the `[auto_approve]` rules. If the PR qualifies, it waits for the operator to press `a` (`require_confirm = true`, default) or approves on its own (`require_confirm = false`). PRs that fail only on CI, draft status, or a human's requested changes wait and are re-checked on every poll. The rules are checked again right before approving.

**Architecture:** A pure `policy` package evaluates the rules. `store` gains approval columns (migration 3). `github.Approve` posts an `APPROVE` review with no fallbacks. The engine evaluates after a successful post and on poll results, and approves through the same claim-then-act pattern as posting (no double approvals, uncertain results checked via a hidden marker). The TUI shows the approval state and adds the `a` key.

**Tech Stack:** Go 1.27, existing deps.

**Spec:** `docs/superpowers/specs/2026-10-09-ghost-design.md`

**Bead:** `ghost-qz7.5`

## Global Constraints

- Module path `github.com/AdamIsrael/ghost`.
- Approval is only considered when `auto_approve.enabled = true`, and only for a review in state `posted`.
- Eligible requires all of: agent verdict `approve`; no `blocking` comments (if `no_blocking_comments`); `additions + deletions ≤ max_changed_lines` (if > 0); CI state `SUCCESS` (if `require_ci_green`). A PR with **no checks** counts as green only if `auto_approve.no_checks_is_green = true` (operator decision 2026-10-10; default `false`); not a draft; `reviewDecision != CHANGES_REQUESTED` (if `no_changes_requested`).
- CI, draft and human-requested-changes failures are **transient** → approval `waiting`, re-checked on polls. The others are **final** → `not eligible`.
- Rules are re-evaluated against a fresh `GetPR` right before approving; the head SHA must still equal the reviewed SHA.
- Approval states: `""` (not considered), `waiting`, `pending` (press `a`), `approving` (in flight), `approved`, `not eligible`, `failed` (definite 4xx, e.g. own PR), `unknown` (may have reached GitHub; `a` checks the marker before retrying). On startup `approving` → `unknown`.
- `github.Approve` never downgrades or folds; GitHub refusing (e.g. "your own pull request") is an error.
- **Git:** one commit per task on branch `m5-approvals`; ask before merging or pushing.

## Review Focus

1. Pressing `a` twice, or `a` racing an automatic approval → one approval (Task 4 `TestApproveTwiceConcurrentlyApprovesOnce`).
2. CI turns red between "pending" and pressing `a` → no approval (Task 4 `TestApproveRechecksAtApproveTime`).
3. PR head moves after review → never approve the old commit (Task 4 `TestApproveSupersededHead`).
4. Own PR → clean "failed" state, no COMMENT posted in its place (Task 3 `TestApproveOwnPRIsAnError`, Task 4 `TestApproveOwnPRFailsCleanly`).
5. Auto-approve disabled (the default) → no approval state, no audit noise (Task 4 `TestApprovalDisabledDoesNothing`).

---

### File Structure

```
internal/policy/policy.go           Input, Decision, Evaluate
internal/policy/policy_test.go
internal/config/validate.go         max_changed_lines ≥ 0
internal/store/store.go             migration 3
internal/store/reviews.go           approval fields, TransitionApproval, WaitingApprovals, FailInterrupted
internal/store/reviews_test.go
internal/github/post.go             Approve
internal/github/post_test.go
internal/engine/approve.go          evaluateApproval, recheckApprovals, Approve, approve
internal/engine/engine.go           Deps.Approve; Run passes ctx; recheck on polls
internal/engine/post.go             evaluate after posting
internal/engine/engine_test.go      harness: CI state per PR, Approve fake
internal/engine/approve_test.go
internal/tui/model.go, render.go    Engine.Approve, `a` key, approval line
internal/tui/engine_test.go
cmd/ghost/main.go                   deps.approve
```

---

### Task 1: Approval policy

**Files:**
- Create: `internal/policy/policy.go`, `internal/policy/policy_test.go`
- Modify: `internal/config/config.go`, `internal/config/default_config.toml`, `internal/config/validate.go`, `internal/config/validate_test.go`

**Interfaces:**
- Produces: `config.AutoApprove.NoChecksIsGreen bool` (`toml:"no_checks_is_green"`, default false); `type Input struct { PR github.PR; Verdict string; Comments []agent.InlineComment }`; `type Decision struct { Eligible, Waiting bool; Reasons []string }`; `func Evaluate(c config.AutoApprove, in Input) Decision`; `func (d Decision) Summary() string`.

- [ ] **Step 1: Write failing tests**

`internal/policy/policy_test.go`:

```go
package policy

import (
	"strings"
	"testing"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
)

func rules() config.AutoApprove {
	return config.AutoApprove{Enabled: true, RequireConfirm: true, RequireCIGreen: true,
		NoBlockingComments: true, NoChangesRequested: true, MaxChangedLines: 100}
}

func good() Input {
	return Input{
		PR:       github.PR{CIState: "SUCCESS", ReviewDecision: "REVIEW_REQUIRED", Additions: 30, Deletions: 10},
		Verdict:  "approve",
		Comments: []agent.InlineComment{{Severity: "nit"}},
	}
}

func TestEligible(t *testing.T) {
	d := Evaluate(rules(), good())
	if !d.Eligible || d.Waiting || len(d.Reasons) != 0 {
		t.Fatalf("decision = %+v", d)
	}
}

func TestFinalReasons(t *testing.T) {
	cases := map[string]func(*config.AutoApprove, *Input){
		"disabled":        func(c *config.AutoApprove, _ *Input) { c.Enabled = false },
		"verdict comment": func(_ *config.AutoApprove, in *Input) { in.Verdict = "comment" },
		"blocking":        func(_ *config.AutoApprove, in *Input) { in.Comments = []agent.InlineComment{{Severity: "blocking"}} },
		"too large":       func(_ *config.AutoApprove, in *Input) { in.PR.Additions = 95 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c, in := rules(), good()
			mutate(&c, &in)
			d := Evaluate(c, in)
			if d.Eligible || d.Waiting || len(d.Reasons) == 0 {
				t.Fatalf("decision = %+v", d)
			}
		})
	}
}

func TestTransientReasonsWait(t *testing.T) {
	cases := map[string]func(*Input){
		"ci pending":        func(in *Input) { in.PR.CIState = "PENDING" },
		"ci failing":        func(in *Input) { in.PR.CIState = "FAILURE" },
		"no checks":         func(in *Input) { in.PR.CIState = "" },
		"draft":             func(in *Input) { in.PR.IsDraft = true },
		"changes requested": func(in *Input) { in.PR.ReviewDecision = "CHANGES_REQUESTED" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := good()
			mutate(&in)
			d := Evaluate(rules(), in)
			if d.Eligible || !d.Waiting || len(d.Reasons) != 1 {
				t.Fatalf("decision = %+v", d)
			}
		})
	}
}

func TestFinalBeatsTransient(t *testing.T) {
	in := good()
	in.Verdict = "request_changes"
	in.PR.CIState = "PENDING"
	if d := Evaluate(rules(), in); d.Waiting || len(d.Reasons) != 2 {
		t.Fatalf("a final reason must stop waiting: %+v", d)
	}
}

func TestRulesCanBeTurnedOff(t *testing.T) {
	c := rules()
	c.RequireCIGreen, c.NoBlockingComments, c.NoChangesRequested, c.MaxChangedLines = false, false, false, 0
	in := good()
	in.PR.CIState, in.PR.ReviewDecision, in.PR.Additions = "FAILURE", "CHANGES_REQUESTED", 10000
	in.Comments = []agent.InlineComment{{Severity: "blocking"}}
	if d := Evaluate(c, in); !d.Eligible {
		t.Fatalf("disabled rules still applied: %+v", d)
	}
}

func TestNoChecksIsGreenOption(t *testing.T) {
	c := rules()
	in := good()
	in.PR.CIState = ""
	if d := Evaluate(c, in); d.Eligible {
		t.Fatal("no checks must not be green by default")
	}
	c.NoChecksIsGreen = true
	if d := Evaluate(c, in); !d.Eligible {
		t.Fatalf("no_checks_is_green = true should allow it: %+v", d)
	}
	in.PR.CIState = "PENDING"
	if d := Evaluate(c, in); d.Eligible {
		t.Fatal("the option must not relax pending CI")
	}
}

func TestSummaryIsReadable(t *testing.T) {
	in := good()
	in.PR.CIState = "PENDING"
	in.PR.Additions = 500
	s := Evaluate(rules(), in).Summary()
	if !strings.Contains(s, "510 changed lines") || !strings.Contains(s, "limit 100") || !strings.Contains(s, "CI is pending") {
		t.Errorf("summary = %q", s)
	}
}
```

Append to `internal/config/validate_test.go`:

```go
func TestMaxChangedLinesNotNegative(t *testing.T) {
	cfg := mustParse(t, minimal+"[auto_approve]\nmax_changed_lines = -1\n")
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "max_changed_lines") {
		t.Fatalf("want max_changed_lines error, got %v", err)
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/policy/ ./internal/config/` → FAIL (`undefined: Evaluate`; no max_changed_lines error)

- [ ] **Step 3: Implement**

`internal/config/config.go`: add to `AutoApprove` after `RequireCIGreen`:

```go
	NoChecksIsGreen    bool `toml:"no_checks_is_green"`
```

`internal/config/default_config.toml`: in `[auto_approve]` after `require_ci_green = true` add:

```toml
# A PR with no CI checks at all is not "green" unless you set this.
no_checks_is_green = false
```

`internal/config/validate.go`: after the `post_mode` check add:

```go
	if c.AutoApprove.MaxChangedLines < 0 {
		add("auto_approve.max_changed_lines must be 0 (no limit) or more")
	}
```

`internal/policy/policy.go`:

```go
// Package policy decides whether a reviewed PR may be approved automatically.
package policy

import (
	"fmt"
	"strings"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
)

type Input struct {
	PR       github.PR
	Verdict  string
	Comments []agent.InlineComment
}

type Decision struct {
	Eligible bool
	// Waiting: not eligible now, but only for reasons that can change on this
	// commit (CI, draft, a human's requested changes), so re-check later.
	Waiting bool
	Reasons []string
}

func (d Decision) Summary() string { return strings.Join(d.Reasons, "; ") }

// Evaluate applies the [auto_approve] rules. CI counts as green when GitHub
// reports SUCCESS, or when there are no checks and no_checks_is_green is set.
func Evaluate(c config.AutoApprove, in Input) Decision {
	var final, transient []string
	if !c.Enabled {
		final = append(final, "auto-approve is disabled")
	}
	if in.Verdict != string(agent.Approve) {
		final = append(final, fmt.Sprintf("agent verdict is %s", in.Verdict))
	}
	if c.NoBlockingComments {
		n := 0
		for _, cm := range in.Comments {
			if cm.Severity == "blocking" {
				n++
			}
		}
		if n > 0 {
			final = append(final, fmt.Sprintf("%d blocking comments", n))
		}
	}
	if lines := in.PR.Additions + in.PR.Deletions; c.MaxChangedLines > 0 && lines > c.MaxChangedLines {
		final = append(final, fmt.Sprintf("%d changed lines is over the limit %d", lines, c.MaxChangedLines))
	}
	if in.PR.IsDraft {
		transient = append(transient, "PR is a draft")
	}
	green := in.PR.CIState == "SUCCESS" || (in.PR.CIState == "" && c.NoChecksIsGreen)
	if c.RequireCIGreen && !green {
		state := strings.ToLower(in.PR.CIState)
		if state == "" {
			state = "missing (no checks)"
		}
		transient = append(transient, "CI is "+state)
	}
	if c.NoChangesRequested && in.PR.ReviewDecision == "CHANGES_REQUESTED" {
		transient = append(transient, "a reviewer requested changes")
	}
	reasons := append(final, transient...)
	return Decision{Eligible: len(reasons) == 0, Waiting: len(final) == 0 && len(transient) > 0, Reasons: reasons}
}
```

- [ ] **Step 4: Run, verify pass** — `go test ./internal/policy/ ./internal/config/` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/policy internal/config
git commit -m "Add auto-approve policy"
```

---

### Task 2: Store approval state

**Files:**
- Modify: `internal/store/store.go`, `internal/store/reviews.go`, `internal/store/reviews_test.go`

**Interfaces:**
- Produces: consts `ApprovalWaiting = "waiting"`, `ApprovalPending = "pending"`, `ApprovalApproving = "approving"`, `ApprovalApproved = "approved"`, `ApprovalNo = "not eligible"`, `ApprovalFailed = "failed"`, `ApprovalUnknown = "unknown"`; `Review.Approval, Review.ApprovalNote string`; `(*Store) TransitionApproval(id int64, from, to string) (bool, error)`; `(*Store) WaitingApprovals() ([]Review, error)`; `FailInterrupted` also moves `approving` → `unknown`.

- [ ] **Step 1: Write failing tests** — append to `internal/store/reviews_test.go`:

```go
func TestApprovalFieldsRoundTrip(t *testing.T) {
	s := openTest(t)
	prID, _ := s.UpsertPR(pr(1, "a"))
	s.EnqueueReview(prID, "a", "claude", false)
	r, _ := s.ClaimNext()
	r.State, r.Approval, r.ApprovalNote = Posted, ApprovalWaiting, "CI is pending"
	if err := s.SaveReview(r); err != nil {
		t.Fatal(err)
	}
	got, _ := s.LatestReview("o/r", 1)
	if got.Approval != ApprovalWaiting || got.ApprovalNote != "CI is pending" {
		t.Errorf("got %+v", got)
	}
	w, err := s.WaitingApprovals()
	if err != nil || len(w) != 1 || w[0].ID != r.ID || w[0].Repo != "o/r" {
		t.Fatalf("waiting = %+v, %v", w, err)
	}
	if ok, _ := s.TransitionApproval(r.ID, ApprovalWaiting, ApprovalApproving); !ok {
		t.Fatal("transition failed")
	}
	if ok, _ := s.TransitionApproval(r.ID, ApprovalWaiting, ApprovalApproving); ok {
		t.Error("second transition from a stale state must fail")
	}
	if w, _ := s.WaitingApprovals(); len(w) != 0 {
		t.Errorf("no longer waiting, got %+v", w)
	}
}

func TestFailInterruptedSettlesApproving(t *testing.T) {
	s := openTest(t)
	prID, _ := s.UpsertPR(pr(1, "a"))
	s.EnqueueReview(prID, "a", "claude", false)
	r, _ := s.ClaimNext()
	r.State, r.Approval = Posted, ApprovalApproving
	s.SaveReview(r)
	if _, err := s.FailInterrupted(1.5); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LatestReview("o/r", 1); got.Approval != ApprovalUnknown || got.State != Posted {
		t.Errorf("got %+v", got)
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/store/` → FAIL (`r.Approval undefined`)

- [ ] **Step 3: Implement**

`internal/store/store.go`: append migration 3:

```go
	`ALTER TABLE reviews ADD COLUMN approval TEXT NOT NULL DEFAULT '';
	ALTER TABLE reviews ADD COLUMN approval_note TEXT NOT NULL DEFAULT '';
	CREATE INDEX reviews_approval ON reviews (approval);`,
```

`internal/store/reviews.go`:

1. After the review-state consts add:

```go
// Approval states (Review.Approval). Empty means approval wasn't considered.
const (
	ApprovalWaiting   = "waiting"      // not yet: CI, draft or a human's requested changes
	ApprovalPending   = "pending"      // qualifies; waiting for the operator (key a)
	ApprovalApproving = "approving"    // approval in flight
	ApprovalApproved  = "approved"
	ApprovalNo        = "not eligible" // final for this commit
	ApprovalFailed    = "failed"       // GitHub refused (4xx), e.g. own PR
	ApprovalUnknown   = "unknown"      // may have reached GitHub; check before retrying
)
```

2. Add fields to `Review` after `CostUSD`:

```go
	Approval     string
	ApprovalNote string
```

3. Extend `reviewCols` (append `, r.approval, r.approval_note`) and `scanReview`'s `Scan` (append `&r.Approval, &r.ApprovalNote` after `&finished`):

```go
const reviewCols = `r.id, r.pr_id, p.repo, p.number, r.head_sha, r.agent, r.state, r.prompt, r.verdict,
	r.summary, r.comments, r.error, r.cost_usd, r.created_at, COALESCE(r.finished_at, ''), r.approval, r.approval_note`
```

```go
	err := row.Scan(&r.ID, &r.PRID, &r.Repo, &r.Number, &r.HeadSHA, &r.Agent, &r.State, &r.Prompt, &r.Verdict,
		&r.Summary, &r.Comments, &r.Error, &r.CostUSD, &created, &finished, &r.Approval, &r.ApprovalNote)
```

4. In `SaveReview`, change the statement to also write the approval columns:

```go
	_, err := s.db.Exec(`UPDATE reviews SET state = ?, prompt = ?, verdict = ?, summary = ?, comments = ?,
		error = ?, cost_usd = ?, finished_at = ?, approval = ?, approval_note = ? WHERE id = ?`,
		r.State, r.Prompt, r.Verdict, r.Summary, r.Comments, r.Error, r.CostUSD, finished, r.Approval, r.ApprovalNote, r.ID)
```

5. Add:

```go
// TransitionApproval moves a review's approval state only if it is still
// from; it reports whether it did.
func (s *Store) TransitionApproval(id int64, from, to string) (bool, error) {
	res, err := s.db.Exec(`UPDATE reviews SET approval = ? WHERE id = ? AND approval = ?`, to, id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// WaitingApprovals returns reviews whose approval waits on a PR change.
func (s *Store) WaitingApprovals() ([]Review, error) {
	rows, err := s.db.Query(`SELECT `+reviewCols+` FROM reviews r JOIN prs p ON p.id = r.pr_id WHERE r.approval = ?`, ApprovalWaiting)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		r, err := scanReview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}
```

6. In `FailInterrupted`, before `return out, nil` add:

```go
	if _, err := s.db.Exec(`UPDATE reviews SET approval = ? WHERE approval = ?`, ApprovalUnknown, ApprovalApproving); err != nil {
		return nil, err
	}
```

- [ ] **Step 4: Run, verify pass** — `go test ./internal/store/` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "Store approval state on reviews"
```

---

### Task 3: `github.Approve`

**Files:**
- Modify: `internal/github/post.go`, `internal/github/post_test.go`

**Interfaces:**
- Produces: `func (c *Client) Approve(ctx context.Context, repo string, number int, commitID, body string) error`.

- [ ] **Step 1: Write failing tests** — append to `internal/github/post_test.go`:

```go
func TestApprove(t *testing.T) {
	f := &fakeREST{}
	if err := New(nil).WithREST(f).Approve(context.Background(), "o/r", 7, "abc", "Approved"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0].path != "repos/o/r/pulls/7/reviews" {
		t.Fatalf("calls = %+v", f.calls)
	}
	if b := f.calls[0].body; b.Event != "APPROVE" || b.CommitID != "abc" || b.Body != "Approved" || len(b.Comments) != 0 {
		t.Errorf("body = %+v", b)
	}
}

func TestApproveOwnPRIsAnError(t *testing.T) {
	own := &api.HTTPError{StatusCode: 422, Message: "Unprocessable Entity",
		Errors: []api.HTTPErrorItem{{Message: "Review Can not approve your own pull request"}}}
	f := &fakeREST{errs: []error{own}}
	err := New(nil).WithREST(f).Approve(context.Background(), "o/r", 7, "abc", "Approved")
	if err == nil || !strings.Contains(err.Error(), "own pull request") || !IsClientError(err) {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("approval must never be retried as a comment (%d calls)", len(f.calls))
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/github/` → FAIL `undefined: Approve`

- [ ] **Step 3: Implement** — append to `internal/github/post.go`:

```go
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
```

- [ ] **Step 4: Run, verify pass** — `go test ./internal/github/` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/github
git commit -m "Add github.Approve"
```

---

### Task 4: Engine approvals

**Files:**
- Create: `internal/engine/approve.go`, `internal/engine/approve_test.go`
- Modify: `internal/engine/engine.go`, `internal/engine/post.go`, `internal/engine/engine_test.go`

**Interfaces:**
- Consumes: `policy.Evaluate`, store approval API (Task 2), `github.IsClientError`.
- Produces: `Deps.Approve func(ctx context.Context, repo string, number int, commitID, body string) error`; `func (e *Engine) Approve(ctx context.Context, repo string, number int) error`.

- [ ] **Step 1: Update the harness and write failing tests**

In `internal/engine/engine_test.go`:

1. Add fields to `harness` after `onGH bool`:

```go
	ci        map[int]string // PR number → CI state reported by GetPR (default SUCCESS)
	approvals []string       // bodies passed to Approve
	approveFn func() error
```

2. In `newHarness`, initialise `ci: map[int]string{}` in the `&harness{…}` literal, and replace the `GetPR` dep with:

```go
		GetPR: func(_ context.Context, repo string, n int) (github.PRDetail, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			ci, ok := h.ci[n]
			if !ok {
				ci = "SUCCESS"
			}
			return github.PRDetail{PR: github.PR{Repo: repo, Number: n, Title: "T", BaseRef: "main", HeadSHA: h.heads[n], CIState: ci, Additions: 10}, Files: []string{"a.go"}}, nil
		},
		Approve: func(_ context.Context, repo string, n int, commitID, body string) error {
			h.mu.Lock()
			h.approvals = append(h.approvals, body)
			fn := h.approveFn
			h.mu.Unlock()
			if fn != nil {
				return fn()
			}
			return nil
		},
```

3. Add helpers after `postCount`:

```go
func (h *harness) approveCount() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.approvals) }

func (h *harness) approval(n int) string {
	r, _ := h.st.LatestReview("o/r", n)
	if r == nil {
		return ""
	}
	return r.Approval
}
```

Create `internal/engine/approve_test.go`:

```go
package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/poller"
	"github.com/AdamIsrael/ghost/internal/store"
)

// approvable: auto-approve on, agent approves with only a nit.
func approvable(mutate func(*config.Config)) func(*config.Config) {
	return func(c *config.Config) {
		c.AutoApprove = config.AutoApprove{Enabled: true, RequireConfirm: true, RequireCIGreen: true,
			NoBlockingComments: true, NoChangesRequested: true, MaxChangedLines: 400}
		if mutate != nil {
			mutate(c)
		}
	}
}

func approvingHarness(t *testing.T, mutate func(*config.Config)) *harness {
	h := newHarness(t, approvable(mutate))
	h.rev.res = agent.ReviewResult{Verdict: agent.Approve, Summary: "Looks good.", CostUSD: 0.1,
		Comments: []agent.InlineComment{{Path: "a.go", Line: 1, Body: "typo", Severity: "nit"}}}
	return h
}

// postedReview runs PR 1 through review and posting.
func (h *harness) postedReview(t *testing.T) {
	t.Helper()
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	h.waitAudit(t, 1, "ready to post")
	if err := h.e.PostPending(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalPendingThenApprove(t *testing.T) {
	h := approvingHarness(t, nil)
	h.postedReview(t)
	if h.approval(1) != store.ApprovalPending || h.approveCount() != 0 {
		t.Fatalf("approval %q calls %d", h.approval(1), h.approveCount())
	}
	if err := h.e.Approve(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
	if h.approval(1) != store.ApprovalApproved || h.approveCount() != 1 {
		t.Fatalf("approval %q calls %d", h.approval(1), h.approveCount())
	}
	if body := h.approvals[0]; !strings.Contains(body, "Approved by Ghost") || !strings.Contains(body, "<!-- ghost-approval-id:") {
		t.Errorf("body = %q", body)
	}
	if got := kinds(h.e.PRState("o/r", 1).Audit); !strings.HasSuffix(got, "approval pending,approved") {
		t.Errorf("audit = %s", got)
	}
}

func TestAutoApproveWithoutConfirm(t *testing.T) {
	h := approvingHarness(t, func(c *config.Config) { c.PostMode = "auto"; c.AutoApprove.RequireConfirm = false })
	h.start()
	h.poll("Team", ghPR(1, "sha1"))
	waitFor(t, "approved", func() bool { return h.approval(1) == store.ApprovalApproved })
	if h.postCount() != 1 || h.approveCount() != 1 {
		t.Fatalf("posts %d approvals %d", h.postCount(), h.approveCount())
	}
}

func TestApprovalWaitsForCIThenApprovesOnPoll(t *testing.T) {
	h := approvingHarness(t, func(c *config.Config) { c.AutoApprove.RequireConfirm = false })
	h.ci[1] = "PENDING"
	h.postedReview(t)
	if h.approval(1) != store.ApprovalWaiting {
		t.Fatalf("approval = %q", h.approval(1))
	}
	if r, _ := h.st.LatestReview("o/r", 1); !strings.Contains(r.ApprovalNote, "CI is pending") {
		t.Errorf("note = %q", r.ApprovalNote)
	}
	h.mu.Lock()
	h.ci[1] = "SUCCESS"
	h.mu.Unlock()
	green := ghPR(1, "sha1")
	green.CIState = "SUCCESS"
	h.e.HandlePoll(poller.Result{View: "Watch", PRs: []github.PR{green}, At: time.Now()})
	waitFor(t, "approved after CI", func() bool { return h.approval(1) == store.ApprovalApproved })
}

func TestNotEligibleWithBlockingComment(t *testing.T) {
	h := approvingHarness(t, nil)
	h.rev.res.Comments[0].Severity = "blocking"
	h.postedReview(t)
	r, _ := h.st.LatestReview("o/r", 1)
	if r.Approval != store.ApprovalNo || !strings.Contains(r.ApprovalNote, "blocking") || h.approveCount() != 0 {
		t.Fatalf("review %+v approvals %d", r, h.approveCount())
	}
	if err := h.e.Approve(context.Background(), "o/r", 1); err == nil {
		t.Error("ineligible PR must not be approvable with a")
	}
}

func TestRequestChangesVerdictNeverApproved(t *testing.T) {
	h := approvingHarness(t, func(c *config.Config) { c.AutoApprove.RequireConfirm = false })
	h.rev.res.Verdict = agent.RequestChanges
	h.postedReview(t)
	if h.approval(1) != store.ApprovalNo || h.approveCount() != 0 {
		t.Fatalf("approval %q calls %d", h.approval(1), h.approveCount())
	}
}

func TestApproveRechecksAtApproveTime(t *testing.T) {
	h := approvingHarness(t, nil)
	h.postedReview(t)
	h.mu.Lock()
	h.ci[1] = "FAILURE"
	h.mu.Unlock()
	if err := h.e.Approve(context.Background(), "o/r", 1); err == nil {
		t.Fatal("want error: CI turned red")
	}
	if h.approval(1) != store.ApprovalWaiting || h.approveCount() != 0 {
		t.Fatalf("approval %q calls %d", h.approval(1), h.approveCount())
	}
}

func TestApproveSupersededHead(t *testing.T) {
	h := approvingHarness(t, nil)
	h.postedReview(t)
	h.mu.Lock()
	h.heads[1] = "sha2"
	h.mu.Unlock()
	if err := h.e.Approve(context.Background(), "o/r", 1); err == nil {
		t.Fatal("want error: head moved")
	}
	if h.approval(1) != store.ApprovalNo || h.approveCount() != 0 {
		t.Fatalf("approval %q calls %d", h.approval(1), h.approveCount())
	}
}

func TestApproveTwiceConcurrentlyApprovesOnce(t *testing.T) {
	h := approvingHarness(t, nil)
	release := make(chan struct{})
	h.approveFn = func() error { <-release; return nil }
	h.postedReview(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = h.e.Approve(context.Background(), "o/r", 1) }(i)
	}
	waitFor(t, "one approval in flight", func() bool { return h.approveCount() == 1 })
	close(release)
	wg.Wait()
	if h.approveCount() != 1 || (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("approvals %d errs %v", h.approveCount(), errs)
	}
}

func TestApproveOwnPRFailsCleanly(t *testing.T) {
	h := approvingHarness(t, nil)
	h.approveFn = func() error {
		return &api.HTTPError{StatusCode: 422, Message: "Can not approve your own pull request"}
	}
	h.postedReview(t)
	if err := h.e.Approve(context.Background(), "o/r", 1); err == nil {
		t.Fatal("want error")
	}
	r, _ := h.st.LatestReview("o/r", 1)
	if r.Approval != store.ApprovalFailed || !strings.Contains(r.ApprovalNote, "own pull request") {
		t.Fatalf("review = %+v", r)
	}
	if h.postCount() != 1 {
		t.Error("a failed approval must not post anything else")
	}
}

func TestUncertainApprovalChecksGitHub(t *testing.T) {
	h := approvingHarness(t, nil)
	h.approveFn = func() error { return errors.New("context deadline exceeded") }
	h.postedReview(t)
	h.e.Approve(context.Background(), "o/r", 1)
	if h.approval(1) != store.ApprovalUnknown {
		t.Fatalf("approval = %q", h.approval(1))
	}
	h.mu.Lock()
	h.onGH, h.approveFn = true, nil
	h.mu.Unlock()
	if err := h.e.Approve(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
	if h.approval(1) != store.ApprovalApproved || h.approveCount() != 1 {
		t.Fatalf("approval %q calls %d", h.approval(1), h.approveCount())
	}
}

func TestApprovalDisabledDoesNothing(t *testing.T) {
	h := newHarness(t, nil) // auto_approve disabled by default
	h.rev.res.Verdict = agent.Approve
	h.rev.res.Comments = nil
	h.postedReview(t)
	if h.approval(1) != "" || strings.Contains(kinds(h.e.PRState("o/r", 1).Audit), "approv") {
		t.Fatalf("approval %q audit %s", h.approval(1), kinds(h.e.PRState("o/r", 1).Audit))
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/engine/` → FAIL (`unknown field Approve in struct literal of type Deps`)

- [ ] **Step 3: Implement**

`internal/engine/engine.go`:

1. Add to `Deps` after `HasReview`:

```go
	Approve     func(ctx context.Context, repo string, number int, commitID, body string) error
```

2. In `Run`, change `case r := <-e.polls:` body from `e.enqueue(r)` to:

```go
			e.enqueue(r)
			e.recheckApprovals(ctx, r)
```

`internal/engine/post.go`: at the end of `post`, replace

```go
	e.audit(r.PRID, r.ID, "posted", msg)
	return nil
}
```

with

```go
	e.audit(r.PRID, r.ID, "posted", msg)
	e.evaluateApproval(ctx, r, nil)
	return nil
}
```

Also in `post`, the branch that finds an earlier post on GitHub (`if found {`): after its `e.audit(... "posted", "found on GitHub …")` add `e.evaluateApproval(ctx, r, nil)` before `return nil`.

Create `internal/engine/approve.go`:

```go
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/poller"
	"github.com/AdamIsrael/ghost/internal/policy"
	"github.com/AdamIsrael/ghost/internal/store"
)

func approvalMarker(id int64) string { return fmt.Sprintf("<!-- ghost-approval-id:%d -->", id) }

func (e *Engine) decide(r *store.Review, pr github.PR) policy.Decision {
	var comments []agent.InlineComment
	_ = json.Unmarshal([]byte(r.Comments), &comments)
	return policy.Evaluate(e.cfg.AutoApprove, policy.Input{PR: pr, Verdict: r.Verdict, Comments: comments})
}

// evaluateApproval sets a posted review's approval state from the rules. pr
// is the PR's latest known state; nil fetches it.
func (e *Engine) evaluateApproval(ctx context.Context, r *store.Review, pr *github.PR) {
	if !e.cfg.AutoApprove.Enabled || r.State != store.Posted {
		return
	}
	if pr == nil {
		d, err := e.d.GetPR(ctx, r.Repo, r.Number)
		if err != nil {
			e.setApproval(r, store.ApprovalWaiting, "could not read the PR's status: "+err.Error())
			return
		}
		pr = &d.PR
	}
	if pr.HeadSHA != r.HeadSHA {
		e.setApproval(r, store.ApprovalNo, fmt.Sprintf("superseded: PR head moved to %.7s", pr.HeadSHA))
		return
	}
	dec := e.decide(r, *pr)
	switch {
	case dec.Waiting:
		e.setApproval(r, store.ApprovalWaiting, dec.Summary())
	case !dec.Eligible:
		e.setApproval(r, store.ApprovalNo, dec.Summary())
	case e.cfg.AutoApprove.RequireConfirm:
		e.setApproval(r, store.ApprovalPending, "meets the auto-approve rules; press a to approve")
	default:
		e.setApproval(r, store.ApprovalPending, "meets the auto-approve rules")
		_ = e.approve(ctx, r) // outcome is audited
	}
}

// setApproval saves and audits an approval state change (repeats are silent).
func (e *Engine) setApproval(r *store.Review, state, note string) {
	if r.Approval == state && r.ApprovalNote == note {
		return
	}
	r.Approval, r.ApprovalNote = state, note
	e.save(r)
	kind := map[string]string{
		store.ApprovalWaiting: "approval waiting", store.ApprovalPending: "approval pending",
		store.ApprovalNo: "not approved", store.ApprovalApproved: "approved",
		store.ApprovalFailed: "approval failed", store.ApprovalUnknown: "approval failed",
	}[state]
	if kind != "" {
		e.audit(r.PRID, r.ID, kind, note)
	}
}

// recheckApprovals re-evaluates waiting approvals for PRs in a poll result.
func (e *Engine) recheckApprovals(ctx context.Context, res poller.Result) {
	if !e.cfg.AutoApprove.Enabled || res.PRs == nil {
		return
	}
	waiting, err := e.d.Store.WaitingApprovals()
	if err != nil || len(waiting) == 0 {
		return
	}
	for i := range waiting {
		r := &waiting[i]
		for _, pr := range res.PRs {
			if pr.Repo == r.Repo && pr.Number == r.Number {
				pr := pr
				e.evaluateApproval(ctx, r, &pr)
			}
		}
	}
}

// Approve approves a PR whose review is pending approval, or retries one
// whose earlier approval may not have reached GitHub.
func (e *Engine) Approve(ctx context.Context, repo string, number int) error {
	r, err := e.d.Store.LatestReview(repo, number)
	if err != nil {
		return err
	}
	if r == nil || (r.Approval != store.ApprovalPending && r.Approval != store.ApprovalUnknown) {
		return errors.New("nothing is waiting for approval")
	}
	return e.approve(ctx, r)
}

// approve claims the review's approval (pending/unknown → approving), checks
// the rules against the PR as it is now, and posts the approval.
func (e *Engine) approve(ctx context.Context, r *store.Review) error {
	from := r.Approval
	ok, err := e.d.Store.TransitionApproval(r.ID, from, store.ApprovalApproving)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("this PR is already being approved")
	}
	r.Approval = store.ApprovalApproving
	e.notify()
	marker := approvalMarker(r.ID)

	if from == store.ApprovalUnknown && e.d.HasReview != nil {
		found, err := e.d.HasReview(ctx, r.Repo, r.Number, marker)
		if err != nil {
			e.setApproval(r, store.ApprovalUnknown, "could not check GitHub: "+err.Error())
			return err
		}
		if found {
			e.setApproval(r, store.ApprovalApproved, "found on GitHub from the earlier attempt; not approved again")
			return nil
		}
	}

	d, err := e.d.GetPR(ctx, r.Repo, r.Number)
	if err != nil {
		r.Approval = from // release the claim; nothing was sent
		e.save(r)
		return err
	}
	if d.HeadSHA != r.HeadSHA {
		e.setApproval(r, store.ApprovalNo, fmt.Sprintf("superseded: PR head moved to %.7s", d.HeadSHA))
		return errors.New(r.ApprovalNote)
	}
	if dec := e.decide(r, d.PR); !dec.Eligible {
		state := store.ApprovalNo
		if dec.Waiting {
			state = store.ApprovalWaiting
		}
		e.setApproval(r, state, dec.Summary())
		return fmt.Errorf("no longer meets the auto-approve rules: %s", dec.Summary())
	}

	body := fmt.Sprintf("Approved by Ghost: the review found no blocking issues and the PR meets the auto-approve rules.\n\n<sub>agent `%s` · commit %.7s</sub>\n%s",
		r.Agent, r.HeadSHA, marker)
	if err := e.d.Approve(ctx, r.Repo, r.Number, r.HeadSHA, body); err != nil {
		if github.IsClientError(err) {
			e.setApproval(r, store.ApprovalFailed, err.Error())
		} else {
			e.setApproval(r, store.ApprovalUnknown, err.Error()+"; it may have reached GitHub, so a checks before approving again")
		}
		return err
	}
	e.setApproval(r, store.ApprovalApproved, "APPROVE posted")
	return nil
}
```

- [ ] **Step 4: Run, verify pass** — `go vet ./... && go test -race -count=5 ./internal/engine/ && go test -count=100 ./internal/engine/` → PASS (stress per the beads memory on concurrency tests)

- [ ] **Step 5: Commit**

```bash
git add internal/engine
git commit -m "Engine: evaluate auto-approve rules and approve on confirm or automatically"
```

---

### Task 5: TUI `a` key and approval display

**Files:**
- Modify: `internal/tui/model.go`, `internal/tui/render.go`, `internal/tui/engine_test.go`

**Interfaces:**
- Produces: `Engine` interface gains `Approve(ctx context.Context, repo string, number int) error`.

- [ ] **Step 1: Write failing tests** — in `internal/tui/engine_test.go`:

1. Add field `approved []string` to `fakeEngine` and method:

```go
func (f *fakeEngine) Approve(_ context.Context, repo string, n int) error {
	f.approved = append(f.approved, repo)
	return nil
}
```

2. Append:

```go
func TestApproveKey(t *testing.T) {
	fe := &fakeEngine{}
	m := withEngine(t, fe)
	m, cmd := m.Update(key("a"))
	runCmd(m, cmd)
	if len(fe.approved) != 1 {
		t.Errorf("a: approved = %v", fe.approved)
	}
}

func TestDetailShowsApprovalState(t *testing.T) {
	for state, want := range map[string]string{
		store.ApprovalPending:  "press a to approve",
		store.ApprovalWaiting:  "Approval waiting: CI is pending",
		store.ApprovalApproved: "Approved",
		store.ApprovalNo:       "Not approved: CI is pending",
		store.ApprovalFailed:   "Approval failed: CI is pending",
		store.ApprovalUnknown:  "press a to check GitHub",
	} {
		fe := &fakeEngine{state: engine.PRState{Latest: &store.Review{State: store.Posted, Approval: state, ApprovalNote: "CI is pending"}}}
		if out := screen(withEngine(t, fe)); !strings.Contains(out, want) {
			t.Errorf("%s: missing %q:\n%s", state, want, out)
		}
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/tui/` → FAIL: `TestApproveKey` (a does nothing) and `TestDetailShowsApprovalState` (no approval line). It compiles because the fake's extra method is harmless before the interface grows.

- [ ] **Step 3: Implement**

`internal/tui/model.go`:
1. Add `Approve(ctx context.Context, repo string, number int) error` to the `Engine` interface.
2. In `handleKey`, add before `case "o", "enter":`:

```go
	case "a":
		pr := m.selectedPR()
		if pr == nil || m.opts.Engine == nil {
			return m, nil
		}
		e, repo, n := m.opts.Engine, pr.Repo, pr.Number
		return m, func() tea.Msg {
			return engineDoneMsg{"approve", e.Approve(context.Background(), repo, n)}
		}
```

`internal/tui/render.go`:
1. Filter-bar hint: `"←/→ switch · o open · r review · P post · a approve · p pause · R refresh · q quit"`.
2. In `reviewLines`, before the final `return ls`, add:

```go
	if line := approvalLine(r); line != "" {
		ls = append(ls, "", line)
	}
```

3. Add:

```go
func approvalLine(r *store.Review) string {
	note := cleanLine(r.ApprovalNote)
	switch r.Approval {
	case store.ApprovalPending:
		return bold.Render("── Approval pending: meets the auto-approve rules (press a to approve) ──")
	case store.ApprovalWaiting:
		return "Approval waiting: " + note
	case store.ApprovalApproving:
		return "Approving…"
	case store.ApprovalApproved:
		return bold.Render("✓ Approved")
	case store.ApprovalNo:
		return dim.Render("Not approved: " + note)
	case store.ApprovalFailed:
		return errText.Render("Approval failed: " + note)
	case store.ApprovalUnknown:
		return errText.Render("Approval result unknown; press a to check GitHub and retry")
	}
	return ""
}
```

- [ ] **Step 4: Run, verify pass** — `go vet ./... && go test ./internal/tui/` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tui
git commit -m "TUI: approval state and a key"
```

---

### Task 6: Wire approvals into `ghost`

**Files:**
- Modify: `cmd/ghost/main.go`

**Interfaces:**
- Produces: `deps.approve`; engine gets `Approve`.

- [ ] **Step 1: Tests** — no new unit test: this task only passes a function through, and `TestStartRunsEngine` already proves the engine is wired. Behavior is covered by Task 4 and the manual run below.

- [ ] **Step 2: Implement** — `cmd/ghost/main.go`:

1. Add to `deps`:

```go
	approve     func(ctx context.Context, repo string, number int, commitID, body string) error
```

2. In `defaultDeps()`:

```go
		approve: func(ctx context.Context, repo string, number int, commitID, body string) error {
			c, err := github.NewDefault()
			if err != nil {
				return err
			}
			return c.Approve(ctx, repo, number, commitID, body)
		},
```

3. Add `Approve: d.approve,` to the `engine.Deps{…}` literal in `run`.

- [ ] **Step 3: Run** — `go mod tidy && go vet ./... && go test -race ./...` → all PASS

- [ ] **Step 4: Manual end-to-end** (operator-approved sandbox; GitHub forbids approving your own PR, so the expected end state is a clean "Approval failed: … own pull request" — this proves evaluation, the `a` key, and the no-fallback rule without posting anything but the review):

```bash
# sandbox: private repo + PR as in M4 (recreate if deleted), with a clean change the agent should approve
# config: post_mode confirm, [auto_approve] enabled = true, require_confirm = true, require_ci_green = false
# run ghost, P to post the review, expect "Approval pending … press a", press a,
# expect "Approval failed: approve: GitHub does not allow approving your own pull request"
# and `gh api repos/AdamIsrael/ghost-sandbox/pulls/1/reviews` shows only the COMMENTED review.
```

- [ ] **Step 5: Commit**

```bash
git add cmd/ghost
git commit -m "Wire approvals into ghost"
```
