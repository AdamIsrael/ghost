# M3 Agent + Dry-run Review Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `ghost review <pr-url> --dry-run` checks out the PR in an isolated git worktree, runs the configured agent (`claude`) with the agent's own review prompt, and prints the structured review (verdict, summary, inline comments, cost) without posting anything. `--show-prompt` prints the rendered prompt and stops (free).

**Architecture:** `agent` defines the `Reviewer` interface, result types, the fixed output contract (text + JSON schema), and a kind→constructor registry; `agent/claude.go` shells out to `claude -p` with structured output. `workspace` keeps a blobless bare clone per repo under the cache dir and adds a detached worktree per review. `github` gains `GetPR` (single PR + changed files). A small `run` package provides the shared exec function so tests can fake subprocesses. `cmd/ghost` gets a `review` subcommand wired through `deps`.

**Tech Stack:** Go 1.27, existing deps only. `claude` CLI 2.1.x flags verified: `-p --output-format json --json-schema --max-budget-usd --model --allowedTools --permission-mode dontAsk --setting-sources --strict-mcp-config --no-session-persistence`.

**Spec:** `docs/superpowers/specs/2026-10-09-ghost-design.md`

**Bead:** `ghost-qz7.3` (also closes items 1, 2 and 4 of `ghost-qz7.8`)

## Global Constraints

- Module path `github.com/AdamIsrael/ghost`.
- **Claude never writes to GitHub — Ghost posts** (spec). M3 posts nothing at all; `review` without `--dry-run` or `--show-prompt` is an error.
- Agent runs in the PR worktree with **read-only tools only**: `Read Grep Glob Bash(git diff:*) Bash(git log:*) Bash(git show:*)`, `--permission-mode dontAsk` (anything else is denied without prompting).
- **Untrusted checkout safety:** always pass `--setting-sources user` (never load the PR's `.claude/settings.json`, whose hooks would run arbitrary commands) and `--strict-mcp-config` with no `--mcp-config` (never start the PR's `.mcp.json` servers). Do NOT use `--bare` (it refuses OAuth logins).
- Prompt = operator prompt (rendered template) + `"\n\n"` + `agent.OutputContract`. Structure is enforced by `--json-schema agent.ResultSchema`.
- Per-review cap: `--max-budget-usd` = `config.MaxCostPerReviewUSD`. A failed run still reports its cost; `Review` returns `CostUSD` even when it returns an error (M4 counts it toward the daily cap).
- Verdicts: `approve`, `comment`, `request_changes`. Severities: `blocking`, `nit`.
- Cache dir: `$XDG_CACHE_HOME/ghost`, else `~/.cache/ghost`. Bare clones at `<cache>/repos/<owner>/<name>.git`, worktrees under `<cache>/worktrees/`.
- **Git:** one commit per task on branch `m3-agent`; ask before merging or pushing.

## Review Focus

1. PR URL forms: `https://github.com/o/r/pull/7`, trailing `/files` or `#discussion…`, and `o/r#7` → all parse; anything else → clear error (Task 2 `TestParsePRRef`).
2. Claude exceeds its budget → error says so, and the cost is still returned (Task 4 `TestClaudeBudgetErrorKeepsCost`).
3. Claude returns a verdict/severity outside the allowed set → error, not a silently wrong review (Task 4 `TestClaudeRejectsInvalidResult`).
4. Two reviews of the same repo at once (M4 runs them concurrently) → no clone/fetch race (Task 3 `TestConcurrentCheckoutsSameRepo`).
5. Worktree cleanup runs even when the review fails, and leaves no registered worktree behind (Task 3 `TestCleanupRemovesWorktree`; Task 5 `TestReviewCleansUpOnAgentError`).

---

### File Structure

```
internal/config/prompt.go           + join template func, sampleData
internal/config/validate.go         validate with sampleData; reject empty prompts
internal/run/run.go                 Func, Exec (shared subprocess runner)
internal/run/run_test.go
internal/github/github.go           + PRDetail, GetPR, ParsePRRef, toPR refactor
internal/github/github_test.go
internal/github/testdata/pr.json
internal/paths/paths.go             + CacheDir
internal/workspace/workspace.go     Manager, New, Checkout
internal/workspace/workspace_test.go
internal/agent/agent.go             Verdict, InlineComment, ReviewResult, ReviewRequest, Reviewer, registry, OutputContract, ResultSchema
internal/agent/claude.go            claude Reviewer
internal/agent/agent_test.go
internal/agent/claude_test.go
internal/agent/testdata/claude_success.json        (captured from a real run)
internal/agent/testdata/claude_budget_error.json   (captured from a real run)
cmd/ghost/main.go                   + deps fields, "review" dispatch
cmd/ghost/review.go                 runReview
cmd/ghost/review_test.go
```

---

### Task 1: Prompt template fixes (from `ghost-qz7.8`)

**Files:**
- Modify: `internal/config/prompt.go`, `internal/config/validate.go`, `internal/config/validate_test.go`

**Interfaces:**
- Produces: template func `join` (`{{join .ChangedFiles ", "}}`); `RenderPrompt` unchanged signature. Validation now renders against realistic sample data and rejects empty/whitespace prompts.

- [ ] **Step 1: Write failing tests** — append to `internal/config/validate_test.go`:

```go
func TestRenderPromptJoin(t *testing.T) {
	out, err := RenderPrompt(`{{join .ChangedFiles ", "}}`, PromptData{ChangedFiles: []string{"a.go", "b.go"}})
	if err != nil || out != "a.go, b.go" {
		t.Fatalf("join = %q, %v", out, err)
	}
}

func TestValidateAcceptsIndexIntoChangedFiles(t *testing.T) {
	cfg := mustParse(t, "[agents.a]\nkind = \"claude\"\nprompt = \"first: {{index .ChangedFiles 0}}\"\n")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}
}

func TestValidateRejectsEmptyPrompt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "empty.md"), []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[agents.a]\nkind = \"claude\"\nprompt_file = \"empty.md\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(cfgPath); err == nil || !strings.Contains(err.Error(), "prompt is empty") {
		t.Fatalf("want empty prompt error, got %v", err)
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/config/` → FAIL: `function "join" not defined`, `index out of range`, and no empty-prompt error.

- [ ] **Step 3: Implement** — in `internal/config/prompt.go`, replace `RenderPrompt` and add `sampleData`:

```go
var promptFuncs = template.FuncMap{"join": strings.Join}

// RenderPrompt executes a review prompt template against d.
func RenderPrompt(text string, d PromptData) (string, error) {
	tmpl, err := template.New("prompt").Funcs(promptFuncs).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

// sampleData is a realistic PR used to check prompt templates at load time,
// so templates that index into fields (e.g. the first changed file) validate.
var sampleData = PromptData{
	Repo: "owner/repo", Number: 1, Title: "Example", Author: "octocat", Body: "Example body",
	BaseRef: "main", HeadRef: "feature", HeadSHA: "0123456789abcdef", URL: "https://github.com/owner/repo/pull/1",
	ChangedFiles: []string{"main.go", "main_test.go"},
}
```

In `internal/config/validate.go`, replace the block after `text, err := c.AgentPrompt(name)`:

```go
		text, err := c.AgentPrompt(name)
		if err != nil {
			add("agents.%s: %w", name, err)
			continue
		}
		if strings.TrimSpace(text) == "" {
			add("agents.%s: prompt is empty", name)
			continue
		}
		if _, err := RenderPrompt(text, sampleData); err != nil {
			add("agents.%s prompt: %w", name, err)
		}
```

and add `"strings"` to its imports.

- [ ] **Step 4: Run, verify pass** — `go test ./internal/config/` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "Prompt templates: add join, validate with sample data, reject empty prompts"
```

---

### Task 2: Shared runner, cache dir, and single-PR lookup

**Files:**
- Create: `internal/run/run.go`, `internal/run/run_test.go`, `internal/github/testdata/pr.json`
- Modify: `internal/paths/paths.go`, `internal/paths/paths_test.go`, `internal/github/github.go`, `internal/github/github_test.go`

**Interfaces:**
- Produces:
  - `type run.Func func(ctx context.Context, dir, stdin, name string, args ...string) (stdout, stderr []byte, err error)`; `func run.Exec(...)` (real implementation)
  - `func paths.CacheDir() string`
  - `type github.PRDetail struct { PR; Files []string }`
  - `func (c *Client) GetPR(ctx context.Context, repo string, number int) (PRDetail, error)`
  - `func ParsePRRef(s string) (repo string, number int, err error)`

- [ ] **Step 1: Write failing tests**

`internal/run/run_test.go`:

```go
package run

import (
	"context"
	"strings"
	"testing"
)

func TestExecPassesDirStdinAndSplitsOutput(t *testing.T) {
	dir := t.TempDir()
	out, errOut, err := Exec(context.Background(), dir, "hello", "sh", "-c", "cat; pwd; echo oops >&2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "hello") || !strings.Contains(string(out), dir) {
		t.Errorf("stdout = %q", out)
	}
	if strings.TrimSpace(string(errOut)) != "oops" {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestExecReportsExitError(t *testing.T) {
	if _, _, err := Exec(context.Background(), "", "", "sh", "-c", "exit 3"); err == nil {
		t.Fatal("want error for non-zero exit")
	}
}
```

Append to `internal/paths/paths_test.go`:

```go
func TestCacheDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "")
	if got, want := CacheDir(), filepath.Join(home, ".cache/ghost"); got != want {
		t.Errorf("CacheDir = %q, want %q", got, want)
	}
	t.Setenv("XDG_CACHE_HOME", "/xc")
	if got := CacheDir(); got != "/xc/ghost" {
		t.Errorf("CacheDir with XDG = %q", got)
	}
}
```

`internal/github/testdata/pr.json`:

```json
{
  "repository": {
    "pullRequest": {
      "number": 7,
      "title": "Fix parser",
      "url": "https://github.com/o/r/pull/7",
      "body": "Fixes the parser.",
      "isDraft": false,
      "updatedAt": "2026-10-09T10:00:00Z",
      "additions": 5,
      "deletions": 1,
      "changedFiles": 2,
      "baseRefName": "main",
      "headRefName": "fix",
      "headRefOid": "deadbeef00",
      "reviewDecision": "REVIEW_REQUIRED",
      "author": {"login": "octocat"},
      "repository": {"nameWithOwner": "o/r"},
      "commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "SUCCESS"}}}]},
      "files": {"nodes": [{"path": "parser.go"}, {"path": "parser_test.go"}]}
    }
  }
}
```

Append to `internal/github/github_test.go`:

```go
func TestGetPR(t *testing.T) {
	f := &fakeGQL{fixture: "testdata/pr.json"}
	d, err := New(f).GetPR(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if d.Number != 7 || d.Title != "Fix parser" || d.HeadSHA != "deadbeef00" || d.CIState != "SUCCESS" || d.Author != "octocat" {
		t.Errorf("PR = %+v", d.PR)
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
		"https://github.com/o/r/pull/7":                     {"o/r", 7},
		"https://github.com/o/r/pull/7/files":               {"o/r", 7},
		"https://github.com/o/r/pull/7#discussion_r1":       {"o/r", 7},
		"http://github.com/Org-1/repo.name/pull/12?x=1":     {"Org-1/repo.name", 12},
		"o/r#7":                                             {"o/r", 7},
		"  o/r#7  ":                                         {"o/r", 7},
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
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/run/ ./internal/paths/ ./internal/github/` → FAIL (`undefined: Exec`, `CacheDir`, `GetPR`, `ParsePRRef`)

- [ ] **Step 3: Implement**

`internal/run/run.go`:

```go
// Package run executes subprocesses; tests substitute a fake Func.
package run

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
)

// Func runs name with args in dir (empty = current), feeding stdin.
type Func func(ctx context.Context, dir, stdin, name string, args ...string) (stdout, stderr []byte, err error)

// Exec is the real Func.
func Exec(ctx context.Context, dir, stdin, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}
```

`internal/paths/paths.go` — add:

```go
func CacheDir() string {
	if base := os.Getenv("XDG_CACHE_HOME"); base != "" {
		return filepath.Join(base, "ghost")
	}
	return filepath.Join(ExpandHome("~/.cache"), "ghost")
}
```

`internal/github/github.go`:

1. Add `"regexp"`, `"strconv"`, `"strings"` to imports.
2. Extract the per-node conversion in `SearchPRs` into `toPR` and call it (`prs = append(prs, toPR(n))`):

```go
func toPR(n prNode) PR {
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
	return pr
}
```

3. Add:

```go
// PRDetail is a PR plus the paths it changes (first 100).
type PRDetail struct {
	PR
	Files []string
}

const prQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number title url body isDraft updatedAt additions deletions changedFiles
      baseRefName headRefName headRefOid reviewDecision
      author { login }
      repository { nameWithOwner }
      commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
      files(first: 100) { nodes { path } }
    }
  }
}`

type prResponse struct {
	Repository *struct {
		PullRequest *struct {
			prNode
			Files struct {
				Nodes []struct {
					Path string `json:"path"`
				} `json:"nodes"`
			} `json:"files"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

// GetPR fetches one pull request and its changed file paths.
func (c *Client) GetPR(ctx context.Context, repo string, number int) (PRDetail, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return PRDetail{}, fmt.Errorf("repo %q is not owner/name", repo)
	}
	var resp prResponse
	vars := map[string]interface{}{"owner": owner, "name": name, "number": number}
	if err := c.gql.DoWithContext(ctx, prQuery, vars, &resp); err != nil {
		return PRDetail{}, fmt.Errorf("get %s#%d: %w", repo, number, err)
	}
	if resp.Repository == nil || resp.Repository.PullRequest == nil {
		return PRDetail{}, fmt.Errorf("%s#%d not found", repo, number)
	}
	n := resp.Repository.PullRequest
	d := PRDetail{PR: toPR(n.prNode)}
	for _, f := range n.Files.Nodes {
		d.Files = append(d.Files, f.Path)
	}
	return d, nil
}

var (
	prURL   = regexp.MustCompile(`^https?://github\.com/([\w.-]+/[\w.-]+)/pull/(\d+)(?:[/?#].*)?$`)
	prShort = regexp.MustCompile(`^([\w.-]+/[\w.-]+)#(\d+)$`)
)

// ParsePRRef accepts a GitHub PR URL or owner/name#number.
func ParsePRRef(s string) (string, int, error) {
	s = strings.TrimSpace(s)
	m := prURL.FindStringSubmatch(s)
	if m == nil {
		m = prShort.FindStringSubmatch(s)
	}
	if m == nil {
		return "", 0, fmt.Errorf("%q is not a PR URL or owner/repo#number", s)
	}
	n, err := strconv.Atoi(m[2])
	if err != nil || n < 1 {
		return "", 0, fmt.Errorf("%q has an invalid PR number", s)
	}
	return m[1], n, nil
}
```

- [ ] **Step 4: Run, verify pass** — `go test ./internal/run/ ./internal/paths/ ./internal/github/` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/run internal/paths internal/github
git commit -m "Add shared runner, cache dir, and single-PR lookup"
```

---

### Task 3: Workspace (bare clone + per-review worktree)

**Files:**
- Create: `internal/workspace/workspace.go`, `internal/workspace/workspace_test.go`

**Interfaces:**
- Consumes: `run.Func` (Task 2).
- Produces:
  - `func New(root string, run run.Func) *Manager`
  - `func (m *Manager) Checkout(ctx context.Context, repo string, number int, baseRef, headSHA string) (dir string, cleanup func(), err error)` — `cleanup` is always non-nil and safe to call more than once.

- [ ] **Step 1: Write failing tests** — `internal/workspace/workspace_test.go` (real git against a local "remote")

```go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/AdamIsrael/ghost/internal/run"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, errOut, err := run.Exec(context.Background(), dir, "", "git", args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, errOut)
	}
	return strings.TrimSpace(string(out))
}

// remote builds a repo with main and a PR head exposed as refs/pull/7/head.
func remote(t *testing.T) (dir, headSHA string) {
	t.Helper()
	dir = t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "t")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	git(t, dir, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644)
	git(t, dir, "commit", "-q", "-am", "change")
	headSHA = git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "update-ref", "refs/pull/7/head", headSHA)
	git(t, dir, "checkout", "-q", "main")
	return dir, headSHA
}

// fakeGh turns `gh repo clone o/r DEST -- ARGS...` into a local git clone.
func fakeGh(remoteDir string, clones *int32) run.Func {
	return func(ctx context.Context, dir, stdin, name string, args ...string) ([]byte, []byte, error) {
		if name == "gh" {
			atomic.AddInt32(clones, 1)
			dest, extra := args[3], args[5:]
			return run.Exec(ctx, dir, stdin, "git", append(append([]string{"clone", "-q"}, extra...), remoteDir, dest)...)
		}
		return run.Exec(ctx, dir, stdin, name, args...)
	}
}

func TestCheckoutAtHeadWithBaseRef(t *testing.T) {
	rdir, head := remote(t)
	var clones int32
	m := New(t.TempDir(), fakeGh(rdir, &clones))
	dir, cleanup, err := m.Checkout(context.Background(), "o/r", 7, "main", head)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got := git(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD = %s, want %s", got, head)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(data) != "two\n" {
		t.Errorf("a.txt = %q", data)
	}
	if got := git(t, dir, "diff", "--name-only", "origin/main...HEAD"); got != "a.txt" {
		t.Errorf("diff against origin/main = %q", got)
	}
}

func TestSecondCheckoutReusesClone(t *testing.T) {
	rdir, head := remote(t)
	var clones int32
	m := New(t.TempDir(), fakeGh(rdir, &clones))
	for i := 0; i < 2; i++ {
		_, cleanup, err := m.Checkout(context.Background(), "o/r", 7, "main", head)
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
	}
	if clones != 1 {
		t.Errorf("cloned %d times, want 1", clones)
	}
}

func TestCleanupRemovesWorktree(t *testing.T) {
	rdir, head := remote(t)
	var clones int32
	root := t.TempDir()
	m := New(root, fakeGh(rdir, &clones))
	dir, cleanup, err := m.Checkout(context.Background(), "o/r", 7, "main", head)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	cleanup() // idempotent
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("worktree dir still exists: %v", err)
	}
	if list := git(t, filepath.Join(root, "repos", "o", "r.git"), "worktree", "list"); strings.Contains(list, dir) {
		t.Errorf("worktree still registered:\n%s", list)
	}
}

func TestConcurrentCheckoutsSameRepo(t *testing.T) {
	rdir, head := remote(t)
	var clones int32
	m := New(t.TempDir(), fakeGh(rdir, &clones))
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, cleanup, err := m.Checkout(context.Background(), "o/r", 7, "main", head)
			if err != nil {
				errs <- err
				return
			}
			cleanup()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if clones != 1 {
		t.Errorf("cloned %d times, want 1", clones)
	}
}

func TestCheckoutUnknownSHAFails(t *testing.T) {
	rdir, _ := remote(t)
	var clones int32
	m := New(t.TempDir(), fakeGh(rdir, &clones))
	_, cleanup, err := m.Checkout(context.Background(), "o/r", 7, "main", strings.Repeat("0", 40))
	cleanup()
	if err == nil {
		t.Fatal("want error for SHA not in the PR")
	}
}

func TestCheckoutRejectsBadRepo(t *testing.T) {
	m := New(t.TempDir(), run.Exec)
	for _, repo := range []string{"noslash", "../x/y", "o/.."} {
		if _, cleanup, err := m.Checkout(context.Background(), repo, 1, "main", "abc"); err == nil {
			cleanup()
			t.Errorf("repo %q should be rejected", repo)
		}
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/workspace/` → FAIL `undefined: New`

- [ ] **Step 3: Implement** — `internal/workspace/workspace.go`

```go
// Package workspace checks out pull requests into isolated git worktrees.
package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/AdamIsrael/ghost/internal/run"
)

// Manager keeps one blobless bare clone per repo under root/repos and adds a
// detached worktree per review under root/worktrees.
type Manager struct {
	root string
	run  run.Func

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func New(root string, r run.Func) *Manager {
	return &Manager{root: root, run: r, locks: map[string]*sync.Mutex{}}
}

func (m *Manager) repoLock(repo string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[repo]
	if !ok {
		l = &sync.Mutex{}
		m.locks[repo] = l
	}
	return l
}

func (m *Manager) git(ctx context.Context, dir string, args ...string) error {
	_, errOut, err := m.run(ctx, dir, "", "git", args...)
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(errOut)))
	}
	return nil
}

func validRepo(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	bad := func(s string) bool { return s == "" || s == "." || s == ".." || strings.ContainsAny(s, `/\`) }
	return ok && !bad(owner) && !bad(name)
}

// Checkout fetches PR number's head and baseRef into the repo's bare clone and
// adds a detached worktree at headSHA. origin/<baseRef> is available inside it.
// cleanup removes the worktree; it is always non-nil and idempotent.
func (m *Manager) Checkout(ctx context.Context, repo string, number int, baseRef, headSHA string) (string, func(), error) {
	noop := func() {}
	if !validRepo(repo) {
		return "", noop, fmt.Errorf("invalid repo %q", repo)
	}
	bare := filepath.Join(m.root, "repos", repo+".git")

	lock := m.repoLock(repo)
	lock.Lock()
	err := m.prepare(ctx, repo, bare, number, baseRef)
	var dir string
	if err == nil {
		dir, err = m.addWorktree(ctx, bare, headSHA)
	}
	lock.Unlock()
	if err != nil {
		return "", noop, err
	}

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			l := m.repoLock(repo)
			l.Lock()
			defer l.Unlock()
			if m.git(context.Background(), bare, "worktree", "remove", "--force", dir) != nil {
				os.RemoveAll(dir)
				m.git(context.Background(), bare, "worktree", "prune")
			}
		})
	}
	return dir, cleanup, nil
}

func (m *Manager) prepare(ctx context.Context, repo, bare string, number int, baseRef string) error {
	if _, err := os.Stat(bare); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(bare), 0o700); err != nil {
			return err
		}
		if _, errOut, err := m.run(ctx, "", "", "gh", "repo", "clone", repo, bare, "--", "--bare", "--filter=blob:none"); err != nil {
			os.RemoveAll(bare)
			return fmt.Errorf("clone %s: %w: %s", repo, err, strings.TrimSpace(string(errOut)))
		}
	}
	return m.git(ctx, bare, "fetch", "--quiet", "--force", "origin",
		fmt.Sprintf("+refs/pull/%d/head:refs/ghost/pr/%d", number, number),
		fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", baseRef, baseRef))
}

func (m *Manager) addWorktree(ctx context.Context, bare, headSHA string) (string, error) {
	parent := filepath.Join(m.root, "worktrees")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(parent, "review-")
	if err != nil {
		return "", err
	}
	if err := m.git(ctx, bare, "worktree", "add", "--quiet", "--detach", dir, headSHA); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}
```

- [ ] **Step 4: Run, verify pass** — `go test -race ./internal/workspace/` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/workspace
git commit -m "Add workspace manager: bare clone cache and per-review worktrees"
```

---

### Task 4: Agent interface and Claude reviewer

**Files:**
- Create: `internal/agent/agent.go`, `internal/agent/claude.go`, `internal/agent/agent_test.go`, `internal/agent/claude_test.go`
- Existing fixtures: `internal/agent/testdata/claude_success.json`, `internal/agent/testdata/claude_budget_error.json` (captured from real `claude` 2.1.285 runs)

**Interfaces:**
- Consumes: `config.Agent`, `config.KnownAgentKinds`, `run.Func`.
- Produces:
  - `type Verdict string` (`Approve = "approve"`, `Comment = "comment"`, `RequestChanges = "request_changes"`)
  - `type InlineComment struct { Path string; Line int; Body string; Severity string }` (JSON tags `path line body severity`)
  - `type ReviewResult struct { Summary string; Verdict Verdict; Comments []InlineComment; CostUSD float64; Raw string }`
  - `type ReviewRequest struct { Dir, Prompt string; MaxCostUSD float64 }`
  - `type Reviewer interface { Name() string; Review(ctx context.Context, req ReviewRequest) (ReviewResult, error) }`
  - `func New(name string, a config.Agent, r run.Func) (Reviewer, error)`
  - `const OutputContract string`, `const ResultSchema string`
  - `func BuildPrompt(rendered string) string` — appends the contract

- [ ] **Step 1: Write failing tests**

`internal/agent/agent_test.go`:

```go
package agent

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/run"
)

func TestRegistryMatchesConfigKinds(t *testing.T) {
	var reg, cfg []string
	for k := range registry {
		reg = append(reg, k)
	}
	for k := range config.KnownAgentKinds {
		cfg = append(cfg, k)
	}
	sort.Strings(reg)
	sort.Strings(cfg)
	if strings.Join(reg, ",") != strings.Join(cfg, ",") {
		t.Errorf("agent registry %v != config.KnownAgentKinds %v", reg, cfg)
	}
}

func TestNewUnknownKind(t *testing.T) {
	if _, err := New("x", config.Agent{Kind: "gpt"}, run.Exec); err == nil {
		t.Fatal("want error for unknown kind")
	}
}

func TestNewClaude(t *testing.T) {
	r, err := New("sec", config.Agent{Kind: "claude"}, run.Exec)
	if err != nil || r.Name() != "sec" {
		t.Fatalf("New = %v, %v", r, err)
	}
}

func TestBuildPromptAppendsContract(t *testing.T) {
	p := BuildPrompt("Review carefully.")
	if !strings.HasPrefix(p, "Review carefully.\n\n") || !strings.HasSuffix(p, OutputContract) {
		t.Errorf("prompt = %q", p)
	}
}

func TestResultSchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal([]byte(ResultSchema), &v); err != nil {
		t.Fatalf("ResultSchema is not JSON: %v", err)
	}
}
```

`internal/agent/claude_test.go`:

```go
package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/AdamIsrael/ghost/internal/config"
)

type call struct {
	dir, stdin, name string
	args             []string
}

func fakeClaude(t *testing.T, fixture string, exitErr error) (*call, func(context.Context, string, string, string, ...string) ([]byte, []byte, error)) {
	t.Helper()
	c := &call{}
	return c, func(_ context.Context, dir, stdin, name string, args ...string) ([]byte, []byte, error) {
		c.dir, c.stdin, c.name, c.args = dir, stdin, name, args
		out, err := os.ReadFile(fixture)
		if err != nil {
			t.Fatal(err)
		}
		return out, []byte("stderr noise"), exitErr
	}
}

func argAfter(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func TestClaudeSuccess(t *testing.T) {
	c, f := fakeClaude(t, "testdata/claude_success.json", nil)
	r, _ := New("claude", config.Agent{Kind: "claude", Model: "claude-opus-5-5", ExtraArgs: []string{"--verbose"}}, f)
	res, err := r.Review(context.Background(), ReviewRequest{Dir: "/wt", Prompt: "PROMPT", MaxCostUSD: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != Comment || res.Summary != "ok" || len(res.Comments) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if got := res.Comments[0]; got != (InlineComment{Path: "a.go", Line: 3, Body: "x", Severity: "nit"}) {
		t.Errorf("comment = %+v", got)
	}
	if res.CostUSD < 0.1 || res.CostUSD > 0.11 || !strings.Contains(res.Raw, `"subtype":"success"`) {
		t.Errorf("cost/raw = %v / %.40s", res.CostUSD, res.Raw)
	}
	if c.name != "claude" || c.dir != "/wt" || c.stdin != "PROMPT" {
		t.Errorf("call = %+v", c)
	}
	for flag, want := range map[string]string{
		"--output-format":   "json",
		"--model":           "claude-opus-5-5",
		"--max-budget-usd":  "1.50",
		"--permission-mode": "dontAsk",
		"--setting-sources": "user",
		"--json-schema":     ResultSchema,
	} {
		if got, ok := argAfter(c.args, flag); !ok || got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	joined := strings.Join(c.args, " ")
	for _, want := range []string{"-p", "--strict-mcp-config", "--no-session-persistence", "Bash(git diff:*)", "--verbose"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, c.args)
		}
	}
	for _, bad := range []string{"--bare", "--mcp-config", "Edit", "Write", "bypassPermissions"} {
		for _, a := range c.args {
			if a == bad {
				t.Errorf("args must not contain %q", bad)
			}
		}
	}
}

func TestClaudeBudgetErrorKeepsCost(t *testing.T) {
	_, f := fakeClaude(t, "testdata/claude_budget_error.json", errors.New("exit status 1"))
	r, _ := New("claude", config.Agent{Kind: "claude"}, f)
	res, err := r.Review(context.Background(), ReviewRequest{Dir: "/wt", Prompt: "p", MaxCostUSD: 0.05})
	if err == nil || !strings.Contains(err.Error(), "maximum budget") {
		t.Fatalf("want budget error, got %v", err)
	}
	if res.CostUSD < 0.08 {
		t.Errorf("cost must be reported even on failure, got %v", res.CostUSD)
	}
}

func TestClaudeNonJSONOutput(t *testing.T) {
	_, f := fakeClaude(t, "claude_test.go", errors.New("exit status 2")) // any non-JSON file
	r, _ := New("claude", config.Agent{Kind: "claude"}, f)
	_, err := r.Review(context.Background(), ReviewRequest{Dir: "/wt", Prompt: "p"})
	if err == nil || !strings.Contains(err.Error(), "stderr noise") {
		t.Fatalf("want error including stderr, got %v", err)
	}
}

func TestClaudeRejectsInvalidResult(t *testing.T) {
	for name, structured := range map[string]string{
		"bad verdict":  `{"summary":"s","verdict":"lgtm","comments":[]}`,
		"bad severity": `{"summary":"s","verdict":"comment","comments":[{"path":"a","line":1,"body":"b","severity":"major"}]}`,
		"no path":      `{"summary":"s","verdict":"comment","comments":[{"path":"","line":1,"body":"b","severity":"nit"}]}`,
		"bad line":     `{"summary":"s","verdict":"comment","comments":[{"path":"a","line":0,"body":"b","severity":"nit"}]}`,
		"missing":      `null`,
	} {
		t.Run(name, func(t *testing.T) {
			fixture := t.TempDir() + "/out.json"
			os.WriteFile(fixture, []byte(`{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.2,"structured_output":`+structured+`}`), 0o644)
			_, f := fakeClaude(t, fixture, nil)
			r, _ := New("claude", config.Agent{Kind: "claude"}, f)
			res, err := r.Review(context.Background(), ReviewRequest{Dir: "/wt", Prompt: "p"})
			if err == nil {
				t.Fatalf("want error, got %+v", res)
			}
			if res.CostUSD != 0.2 {
				t.Errorf("cost should still be reported, got %v", res.CostUSD)
			}
		})
	}
}

func TestClaudeOmitsOptionalFlags(t *testing.T) {
	c, f := fakeClaude(t, "testdata/claude_success.json", nil)
	r, _ := New("claude", config.Agent{Kind: "claude"}, f)
	r.Review(context.Background(), ReviewRequest{Dir: "/wt", Prompt: "p"})
	for _, flag := range []string{"--model", "--max-budget-usd"} {
		if _, ok := argAfter(c.args, flag); ok {
			t.Errorf("%s should be omitted when unset", flag)
		}
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./internal/agent/` → FAIL `undefined: registry`, `New`, …

- [ ] **Step 3: Implement**

`internal/agent/agent.go`:

```go
// Package agent runs AI code reviewers behind a common interface.
package agent

import (
	"context"
	"fmt"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/run"
)

type Verdict string

const (
	Approve        Verdict = "approve"
	Comment        Verdict = "comment"
	RequestChanges Verdict = "request_changes"
)

type InlineComment struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Body     string `json:"body"`
	Severity string `json:"severity"` // "blocking" or "nit"
}

type ReviewResult struct {
	Summary  string          `json:"summary"`
	Verdict  Verdict         `json:"verdict"`
	Comments []InlineComment `json:"comments"`
	CostUSD  float64         `json:"-"`
	Raw      string          `json:"-"` // full agent output, for the audit trail
}

type ReviewRequest struct {
	Dir        string  // PR worktree; the agent's working directory
	Prompt     string  // operator prompt + OutputContract
	MaxCostUSD float64 // 0 = no per-review cap
}

type Reviewer interface {
	Name() string
	// Review runs one review. CostUSD is set whenever the agent reported a
	// cost, including when an error is returned.
	Review(ctx context.Context, req ReviewRequest) (ReviewResult, error)
}

// registry maps config kind → constructor. Keep in sync with
// config.KnownAgentKinds (enforced by TestRegistryMatchesConfigKinds).
var registry = map[string]func(name string, a config.Agent, r run.Func) Reviewer{
	"claude": newClaude,
}

func New(name string, a config.Agent, r run.Func) (Reviewer, error) {
	ctor, ok := registry[a.Kind]
	if !ok {
		return nil, fmt.Errorf("unknown agent kind %q", a.Kind)
	}
	return ctor(name, a, r), nil
}

// OutputContract is appended to every operator prompt. It is fixed so a
// custom prompt cannot change what Ghost parses.
const OutputContract = `## Output
Respond with a JSON object matching the provided schema:
- "summary": a short overall assessment in Markdown.
- "verdict": "approve" if the change can merge as is, "request_changes" if any comment is blocking, otherwise "comment".
- "comments": inline comments. "path" is relative to the repository root. "line" is the line number in the new version of the file and must be a line changed or added by this PR. "severity" is "blocking" only for issues that should stop the merge, otherwise "nit".
Do not modify any files.`

// ResultSchema is passed to agents that support structured output.
const ResultSchema = `{"type":"object","properties":{"summary":{"type":"string"},"verdict":{"type":"string","enum":["approve","comment","request_changes"]},"comments":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"line":{"type":"integer","minimum":1},"body":{"type":"string"},"severity":{"type":"string","enum":["blocking","nit"]}},"required":["path","line","body","severity"]}}},"required":["summary","verdict","comments"]}`

func BuildPrompt(rendered string) string { return rendered + "\n\n" + OutputContract }

func (r ReviewResult) validate() error {
	switch r.Verdict {
	case Approve, Comment, RequestChanges:
	default:
		return fmt.Errorf("invalid verdict %q", r.Verdict)
	}
	for i, c := range r.Comments {
		if c.Path == "" || c.Line < 1 || (c.Severity != "blocking" && c.Severity != "nit") {
			return fmt.Errorf("invalid comment %d: %+v", i, c)
		}
	}
	return nil
}
```

`internal/agent/claude.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/run"
)

// readOnlyTools is everything the reviewer may use. With --permission-mode
// dontAsk, any other tool call is denied without prompting.
var readOnlyTools = []string{"Read", "Grep", "Glob", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)"}

type claude struct {
	name string
	cfg  config.Agent
	run  run.Func
}

func newClaude(name string, a config.Agent, r run.Func) Reviewer {
	return &claude{name: name, cfg: a, run: r}
}

func (c *claude) Name() string { return c.name }

func (c *claude) args(req ReviewRequest) []string {
	args := []string{
		"-p",
		"--output-format", "json",
		"--no-session-persistence",
		// The worktree is untrusted PR content: never load its settings
		// (hooks run arbitrary commands) or its MCP servers.
		"--setting-sources", "user",
		"--strict-mcp-config",
		"--permission-mode", "dontAsk",
		"--json-schema", ResultSchema,
	}
	if c.cfg.Model != "" {
		args = append(args, "--model", c.cfg.Model)
	}
	if req.MaxCostUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", req.MaxCostUSD))
	}
	args = append(args, c.cfg.ExtraArgs...)
	// --allowedTools is variadic, so it goes last.
	return append(args, append([]string{"--allowedTools"}, readOnlyTools...)...)
}

type claudeOutput struct {
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	Errors           []string        `json:"errors"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

func (c *claude) Review(ctx context.Context, req ReviewRequest) (ReviewResult, error) {
	stdout, stderr, runErr := c.run(ctx, req.Dir, req.Prompt, "claude", c.args(req)...)
	res := ReviewResult{Raw: string(stdout)}

	var out claudeOutput
	if err := json.Unmarshal(stdout, &out); err != nil {
		return res, fmt.Errorf("claude: unreadable output (%v): %w: %s", runErr, err, strings.TrimSpace(string(stderr)))
	}
	res.CostUSD = out.TotalCostUSD
	if out.IsError || out.Subtype != "success" {
		return res, fmt.Errorf("claude %s: %s", out.Subtype, strings.Join(out.Errors, "; "))
	}
	if runErr != nil {
		return res, fmt.Errorf("claude: %w: %s", runErr, strings.TrimSpace(string(stderr)))
	}

	structured := out.StructuredOutput
	if len(structured) == 0 || string(structured) == "null" {
		structured = json.RawMessage(out.Result)
	}
	var parsed ReviewResult
	if err := json.Unmarshal(structured, &parsed); err != nil {
		return res, fmt.Errorf("claude: result is not a review: %w", err)
	}
	if err := parsed.validate(); err != nil {
		return res, fmt.Errorf("claude: %w", err)
	}
	if parsed.Comments == nil {
		parsed.Comments = []InlineComment{}
	}
	parsed.CostUSD, parsed.Raw = res.CostUSD, res.Raw
	return parsed, nil
}
```

Note on the `"missing"` test case: `structured_output: null` falls back to `result` (empty string), which fails `json.Unmarshal` → error, as required.

- [ ] **Step 4: Run, verify pass** — `go test ./internal/agent/` → PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent
git commit -m "Add agent interface and Claude reviewer with structured output"
```

---

### Task 5: `ghost review` command

**Files:**
- Create: `cmd/ghost/review.go`, `cmd/ghost/review_test.go`
- Modify: `cmd/ghost/main.go`, `cmd/ghost/main_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces: `deps` gains `getPR func(ctx context.Context, repo string, number int) (github.PRDetail, error)`, `checkout func(ctx context.Context, repo string, number int, baseRef, headSHA string) (string, func(), error)`, `newReviewer func(name string, a config.Agent) (agent.Reviewer, error)`. CLI: `ghost [--config P] review <pr> [--agent NAME] (--dry-run | --show-prompt)`.

- [ ] **Step 1: Write failing tests** — `cmd/ghost/review_test.go`

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
)

type fakeReviewer struct {
	got agent.ReviewRequest
	res agent.ReviewResult
	err error
}

func (f *fakeReviewer) Name() string { return "fake" }
func (f *fakeReviewer) Review(_ context.Context, req agent.ReviewRequest) (agent.ReviewResult, error) {
	f.got = req
	return f.res, f.err
}

type reviewHarness struct {
	cfg        string
	rev        *fakeReviewer
	checkedOut bool
	cleaned    bool
	agentName  string
}

func newReviewHarness(t *testing.T, extraConfig string) (*reviewHarness, deps) {
	t.Helper()
	dir := t.TempDir()
	h := &reviewHarness{cfg: filepath.Join(dir, "config.toml"), rev: &fakeReviewer{res: agent.ReviewResult{
		Verdict: agent.RequestChanges, Summary: "Needs a nil check.", CostUSD: 0.42,
		Comments: []agent.InlineComment{{Path: "parser.go", Line: 12, Body: "nil deref", Severity: "blocking"}},
	}}}
	var out, errb bytes.Buffer
	run([]string{"--config", h.cfg, "init"}, &out, &errb, depsFor(okRunner))
	if extraConfig != "" {
		appendFile(t, h.cfg, extraConfig)
	}
	d := depsFor(okRunner)
	d.getPR = func(_ context.Context, repo string, n int) (github.PRDetail, error) {
		return github.PRDetail{PR: github.PR{Repo: repo, Number: n, Title: "Fix parser", BaseRef: "main", HeadSHA: "deadbeef"}, Files: []string{"parser.go"}}, nil
	}
	d.checkout = func(_ context.Context, repo string, n int, base, sha string) (string, func(), error) {
		h.checkedOut = true
		return "/tmp/wt", func() { h.cleaned = true }, nil
	}
	d.newReviewer = func(name string, a config.Agent) (agent.Reviewer, error) {
		h.agentName = name
		return h.rev, nil
	}
	return h, d
}

func TestReviewDryRun(t *testing.T) {
	h, d := newReviewHarness(t, "")
	var out, errb bytes.Buffer
	code := run([]string{"--config", h.cfg, "review", "https://github.com/o/r/pull/7", "--dry-run"}, &out, &errb, d)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"o/r#7", "Fix parser", "request_changes", "Needs a nil check.", "parser.go:12", "[blocking]", "nil deref", "$0.42", "dry run"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if !h.checkedOut || !h.cleaned {
		t.Error("worktree not checked out and cleaned up")
	}
	if h.rev.got.Dir != "/tmp/wt" || h.rev.got.MaxCostUSD != 1.5 {
		t.Errorf("request = %+v", h.rev.got)
	}
	if !strings.Contains(h.rev.got.Prompt, "o/r#7") || !strings.HasSuffix(h.rev.got.Prompt, agent.OutputContract) {
		t.Errorf("prompt not rendered with contract:\n%s", h.rev.got.Prompt)
	}
	if h.agentName != "claude" {
		t.Errorf("agent = %q, want default", h.agentName)
	}
}

func TestReviewShowPromptSpendsNothing(t *testing.T) {
	h, d := newReviewHarness(t, "")
	var out, errb bytes.Buffer
	if code := run([]string{"--config", h.cfg, "review", "o/r#7", "--show-prompt"}, &out, &errb, d); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "o/r#7") || !strings.Contains(out.String(), agent.OutputContract) {
		t.Errorf("prompt not printed:\n%s", out.String())
	}
	if h.checkedOut || h.agentName != "" {
		t.Error("--show-prompt must not check out or run the agent")
	}
}

func TestReviewUsesNamedAgentPrompt(t *testing.T) {
	h, d := newReviewHarness(t, "\n[agents.sec]\nkind = \"claude\"\nprompt = \"SECURITY ONLY for {{.Repo}}\"\n")
	var out, errb bytes.Buffer
	if code := run([]string{"--config", h.cfg, "review", "o/r#7", "--agent", "sec", "--dry-run"}, &out, &errb, d); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if h.agentName != "sec" || !strings.HasPrefix(h.rev.got.Prompt, "SECURITY ONLY for o/r") {
		t.Errorf("agent %q prompt %q", h.agentName, h.rev.got.Prompt)
	}
}

func TestReviewCleansUpOnAgentError(t *testing.T) {
	h, d := newReviewHarness(t, "")
	h.rev.err = errors.New("claude error_max_budget_usd: Reached maximum budget")
	h.rev.res = agent.ReviewResult{CostUSD: 1.51}
	var out, errb bytes.Buffer
	if code := run([]string{"--config", h.cfg, "review", "o/r#7", "--dry-run"}, &out, &errb, d); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !h.cleaned {
		t.Error("worktree not cleaned up after agent error")
	}
	if !strings.Contains(errb.String(), "maximum budget") || !strings.Contains(errb.String(), "$1.51") {
		t.Errorf("stderr should name the error and the cost spent: %q", errb.String())
	}
}

func TestReviewArgErrors(t *testing.T) {
	h, d := newReviewHarness(t, "")
	for name, args := range map[string][]string{
		"no mode":       {"review", "o/r#7"},
		"no pr":         {"review", "--dry-run"},
		"bad pr":        {"review", "not-a-pr", "--dry-run"},
		"unknown agent": {"review", "o/r#7", "--agent", "nope", "--dry-run"},
		"both modes":    {"review", "o/r#7", "--dry-run", "--show-prompt"},
	} {
		var out, errb bytes.Buffer
		if code := run(append([]string{"--config", h.cfg}, args...), &out, &errb, d); code == 0 {
			t.Errorf("%s: exit 0, want failure", name)
		}
	}
	if h.checkedOut {
		t.Error("bad args should fail before checkout")
	}
}
```

Append to `cmd/ghost/main_test.go`:

```go
func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./cmd/ghost/` → FAIL (`d.getPR undefined`, …)

- [ ] **Step 3: Implement**

`cmd/ghost/main.go`:

1. Add imports `"github.com/AdamIsrael/ghost/internal/agent"`, `"github.com/AdamIsrael/ghost/internal/run"`, `"github.com/AdamIsrael/ghost/internal/workspace"`.
2. Add fields to `deps`:

```go
	getPR       func(ctx context.Context, repo string, number int) (github.PRDetail, error)
	checkout    func(ctx context.Context, repo string, number int, baseRef, headSHA string) (string, func(), error)
	newReviewer func(name string, a config.Agent) (agent.Reviewer, error)
```

3. In `defaultDeps()` add:

```go
		getPR: func(ctx context.Context, repo string, number int) (github.PRDetail, error) {
			c, err := github.NewDefault()
			if err != nil {
				return github.PRDetail{}, err
			}
			return c.GetPR(ctx, repo, number)
		},
		checkout: workspace.New(paths.CacheDir(), run.Exec).Checkout,
		newReviewer: func(name string, a config.Agent) (agent.Reviewer, error) {
			return agent.New(name, a, run.Exec)
		},
```

4. Update usage text to `"usage: ghost [flags] [init | review <pr> [--agent NAME] (--dry-run | --show-prompt)]"`.
5. In `run`, right after the `isInit` block and before the `fs.NArg() > 0` check, add:

```go
	if fs.Arg(0) == "review" {
		if *check {
			fs.Usage()
			return 2
		}
		return runReview(paths.ExpandHome(*cfgPath), fs.Args()[1:], stdout, stderr, d)
	}
```

`cmd/ghost/review.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
)

func runReview(cfgPath string, args []string, stdout, stderr io.Writer, d deps) int {
	fs := flag.NewFlagSet("ghost review", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agentName := fs.String("agent", "", "agent from config (default: default_agent)")
	dryRun := fs.Bool("dry-run", false, "run the review and print it; post nothing")
	showPrompt := fs.Bool("show-prompt", false, "print the rendered prompt and exit; runs nothing")
	// Accept the PR ref before or after flags.
	var ref string
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		ref, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if ref == "" && fs.NArg() == 1 {
		ref = fs.Arg(0)
	} else if fs.NArg() > 0 || ref == "" {
		fmt.Fprintln(stderr, "usage: ghost review <pr-url | owner/repo#N> [--agent NAME] (--dry-run | --show-prompt)")
		return 2
	}
	if *dryRun == *showPrompt {
		fmt.Fprintln(stderr, "ghost review: pass exactly one of --dry-run or --show-prompt (posting reviews arrives in M4)")
		return 2
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	name := *agentName
	if name == "" {
		name = cfg.DefaultAgent
	}
	agentCfg, ok := cfg.Agents[name]
	if !ok {
		fmt.Fprintf(stderr, "ghost: unknown agent %q\n", name)
		return 1
	}
	repo, number, err := github.ParsePRRef(ref)
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	pr, err := d.getPR(ctx, repo, number)
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	prompt, err := renderReviewPrompt(&cfg, name, pr)
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	if *showPrompt {
		fmt.Fprintln(stdout, prompt)
		return 0
	}

	reviewer, err := d.newReviewer(name, agentCfg)
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	fmt.Fprintf(stderr, "Checking out %s#%d at %.7s…\n", repo, number, pr.HeadSHA)
	dir, cleanup, err := d.checkout(ctx, repo, number, pr.BaseRef, pr.HeadSHA)
	defer cleanup()
	if err != nil {
		fmt.Fprintln(stderr, "ghost:", err)
		return 1
	}
	fmt.Fprintf(stderr, "Running %s (cap $%.2f)…\n", name, cfg.MaxCostPerReviewUSD)
	res, err := reviewer.Review(ctx, agent.ReviewRequest{Dir: dir, Prompt: prompt, MaxCostUSD: cfg.MaxCostPerReviewUSD})
	if err != nil {
		fmt.Fprintf(stderr, "ghost: review failed after spending $%.2f: %v\n", res.CostUSD, err)
		return 1
	}
	printReview(stdout, pr, name, res)
	return 0
}

func renderReviewPrompt(cfg *config.Config, agentName string, pr github.PRDetail) (string, error) {
	text, err := cfg.AgentPrompt(agentName)
	if err != nil {
		return "", err
	}
	rendered, err := config.RenderPrompt(text, config.PromptData{
		Repo: pr.Repo, Number: pr.Number, Title: pr.Title, Author: pr.Author, Body: pr.Body,
		BaseRef: pr.BaseRef, HeadRef: pr.HeadRef, HeadSHA: pr.HeadSHA, URL: pr.URL,
		ChangedFiles: pr.Files,
	})
	if err != nil {
		return "", fmt.Errorf("agents.%s prompt: %w", agentName, err)
	}
	return agent.BuildPrompt(rendered), nil
}

func printReview(w io.Writer, pr github.PRDetail, agentName string, res agent.ReviewResult) {
	fmt.Fprintf(w, "%s#%d  %s\n", pr.Repo, pr.Number, pr.Title)
	fmt.Fprintf(w, "Agent: %s   Verdict: %s   Cost: $%.2f\n\n", agentName, res.Verdict, res.CostUSD)
	fmt.Fprintf(w, "%s\n\n", res.Summary)
	if len(res.Comments) == 0 {
		fmt.Fprintln(w, "No inline comments.")
	}
	for _, c := range res.Comments {
		fmt.Fprintf(w, "%s:%d [%s]\n  %s\n", c.Path, c.Line, c.Severity, c.Body)
	}
	fmt.Fprintln(w, "\n(dry run: nothing was posted to GitHub)")
}
```

- [ ] **Step 4: Run, verify pass** — `go mod tidy && go vet ./... && go test -race ./...` → all PASS

- [ ] **Step 5: Manual end-to-end** (the second command spends real money: capped at `max_cost_per_review_usd = 1.5` from the default config)

```bash
go build -o bin/ghost ./cmd/ghost
T=$(mktemp -d)
./bin/ghost --config $T/config.toml init
# free: prints the rendered prompt
./bin/ghost --config $T/config.toml review https://github.com/cli/cli/pull/14629 --show-prompt
# paid (≤ $1.50): real review of a 1-line PR, nothing posted
./bin/ghost --config $T/config.toml review https://github.com/cli/cli/pull/14629 --dry-run
ls ~/.cache/ghost/worktrees/   # expect empty: worktree cleaned up
```

Expected: `--show-prompt` prints the default prompt filled with cli/cli#14629's details and the output contract. `--dry-run` prints progress lines on stderr, then `Agent: claude   Verdict: …   Cost: $…`, a summary, any inline comments, and `(dry run: nothing was posted to GitHub)`. The worktrees directory is empty afterwards.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum cmd/ghost
git commit -m "Add ghost review --dry-run and --show-prompt"
```
