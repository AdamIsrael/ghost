# Ghost — Initial Plan

## Context
Ghost is a terminal app that polls GitHub for PRs matching user-defined views, shows their status, and runs AI code reviews (via `claude` CLI) that post comments back to the PR. Repo is empty (only `bd init`). This plan covers language choice, architecture, and a milestone order.

**Decisions made (from Q&A):**
- Language: **Go**
- Views: each view = an arbitrary GitHub search query
- Autonomy: comments post automatically; approvals need a keypress at first, with a config flag to make them fully autonomous later
- Storage: local SQLite

## Why Go over Rust
1. `gh` is written in Go; `github.com/cli/go-gh/v2` reuses the user's `gh auth` token and gives REST + GraphQL clients for free.
2. Bubble Tea + Bubbles + Lip Gloss is the most mature TUI stack in either language (lists, viewports, text inputs, tables ready-made).
3. Goroutines + channels map directly onto "poller + N concurrent review workers + UI".
4. Faster to build. Rust's wins (memory safety, performance) don't matter for an I/O-bound app that mostly waits on GitHub and an LLM.

## Stack
| Concern | Choice |
|---|---|
| TUI | `charmbracelet/bubbletea`, `bubbles`, `lipgloss`, `glamour` (render markdown review text) |
| GitHub | `cli/go-gh/v2` (GraphQL for search + status; REST for posting reviews) |
| Config | TOML via `BurntSushi/toml`, at `~/.config/ghost/config.toml` |
| DB | `modernc.org/sqlite` (pure Go, no cgo) at `~/.local/share/ghost/ghost.db` |
| Agent | `claude -p --output-format json` subprocess (reports `total_cost_usd`) |

## Architecture
```
cmd/ghost/main.go            entry: load config, open DB, start engine + TUI
internal/config/             Config structs, load/validate/save (save used later by config TUI)
internal/github/             go-gh wrapper: SearchPRs(query), PRDetail, PostReview, Approve
internal/status/             derive PRStatus from GraphQL fields (pure func, heavily tested)
internal/agent/              Reviewer interface + claude implementation
internal/workspace/          bare-clone cache + git worktree per review, cleanup
internal/store/              SQLite: views, prs, reviews, audit_events, spend
internal/engine/             poller, review queue, worker pool, budget guard, policy (auto-approve rules)
internal/tui/                root model, filterbar, prlist, detail, statusbar, confirm dialogs
```
TUI never calls GitHub or the agent directly. Engine emits events on a channel → wrapped as `tea.Msg`; TUI sends commands (approve, re-review, pause) back to engine.

### PR status (`internal/status`)
Derived from GraphQL `isDraft`, `reviewDecision`, `statusCheckRollup.state`, `mergeable`:
- **Draft** — `isDraft`
- **Changes requested** — `reviewDecision == CHANGES_REQUESTED`
- **Waiting on CI** — rollup `PENDING`/`EXPECTED` (CI `FAILURE` shown as **CI failing**, an extra state)
- **Approved** — `reviewDecision == APPROVED`
- **Ready** — not draft, CI green, no blocking review, awaiting review

### Agent abstraction (`internal/agent`)
```go
type Reviewer interface {
    Name() string
    Review(ctx context.Context, req ReviewRequest) (ReviewResult, error)
}
type ReviewResult struct {
    Summary  string
    Verdict  Verdict          // Approve | Comment | RequestChanges
    Comments []InlineComment  // path, line, side, body, severity (blocking|nit)
    CostUSD  float64
    Raw      string           // full transcript for the audit trail
}
```
Agents are named instances in config (`[agents.<name>]`); each has its own kind, model, args, and **review prompt** (see Config). A registry maps `kind` → constructor, so a new agent type is one new file. `ReviewRequest` carries the rendered prompt; the engine renders it, not the agent.

Claude impl: runs in the PR's worktree with `--allowedTools` limited to read-only (Read, Grep, Glob, `Bash(git diff:*)`, `Bash(git log:*)`). Prompt = operator prompt + fixed JSON output contract. **Claude never writes to GitHub — Ghost posts.** Keeps the trust boundary in one place and makes agents swappable.

### Review flow (`internal/engine`)
1. Poller runs each view's query every `poll_interval`; upserts PRs into store.
2. A PR is enqueued when: matches a view with `auto_review = true`, status not Draft, and `head_sha` has no completed review (new commits → re-review).
3. Worker pool (`max_concurrent_reviews`) pulls jobs. Budget guard checks today's spend < `max_daily_cost_usd` before starting; over budget → job waits, status bar shows "budget paused".
4. Workspace: `git worktree add` at head SHA from a cached bare clone.
5. Run reviewer → store result + cost → post review (COMMENT or REQUEST_CHANGES, with inline comments) via REST.
6. Policy evaluates auto-approve criteria. If met: `require_approval_confirm = true` → mark "approval pending" and wait for keypress; else approve directly.
7. Every step writes an `audit_events` row (enqueued, started, cost, posted, approved, error).

### Config (`~/.config/ghost/config.toml`)
```toml
poll_interval = "5m"
max_concurrent_reviews = 2
max_daily_cost_usd = 10.0
max_cost_per_review_usd = 1.5   # passed to claude as a budget cap if supported, else enforced post-hoc

default_agent = "claude-default"

[agents.claude-default]
kind = "claude"
model = "claude-opus-5-5"
extra_args = []
# Prompt that starts the review. Either inline `prompt` or `prompt_file` (path, ~ expanded).
# Go text/template; available vars: {{.Repo}} {{.Number}} {{.Title}} {{.Author}}
# {{.Body}} {{.BaseRef}} {{.HeadRef}} {{.HeadSHA}} {{.URL}} {{.ChangedFiles}}
prompt_file = "~/.config/ghost/prompts/default.md"

[agents.claude-security]
kind = "claude"
model = "claude-opus-5-5"
prompt = """
Review {{.Repo}}#{{.Number}} ("{{.Title}}") focusing only on security issues...
"""

[auto_approve]
enabled = true
require_confirm = true          # flip to false for fully autonomous
require_ci_green = true
no_blocking_comments = true     # ghost's own review had zero "blocking" comments
no_changes_requested = true     # no human reviewer requested changes
max_changed_lines = 400

[[view]]
name = "Team reviews"
query = "is:open is:pr review-requested:@me"
auto_review = true
agent = "claude-default"        # optional; falls back to default_agent
```
**Prompt handling:**
- Operator prompt = the review instructions (what to look for, tone, focus). Rendered with the template vars above.
- Ghost always appends a fixed, non-editable **output contract** (the JSON schema for `ReviewResult`) after the operator prompt, so a custom prompt can't break parsing.
- If neither `prompt` nor `prompt_file` is set, a built-in default prompt is used (embedded via `go:embed`, also written out by `ghost init` as a starting file to edit).
- Config validation fails at startup on: unknown agent referenced by a view, both `prompt` and `prompt_file` set, missing prompt file, template parse errors.
- The rendered prompt is stored on the review's audit record, so the trail shows exactly what was asked.
**Views file (decision 2026-10-10):** Ghost never writes `config.toml` (it would lose comments). Views created or edited in the TUI live in `views.toml` next to it; `[[view]]` tables in `config.toml` still work and are read-only in the TUI. Names are unique across both files.

### TUI layout
```
┌ Views: [Team reviews] [Mine] [+]           (filter bar: n=new e=edit d=delete ←/→ switch) ┐
├──────────────────────────────┬─────────────────────────────────────────────────────────────┤
│ ● Ready   org/repo#123 Title │ org/repo#123  Title           by @author  +120 −30           │
│ ◐ CI      org/repo#124 ...   │ Status: Ready  CI: ✓  Reviews: none                          │
│ ✎ Draft   ...                │ ── Audit trail ──                                            │
│ ✓ Approved ...               │ 10:02 queued · 10:03 review started · 10:05 posted 4 comments│
│                              │ 10:05 cost $0.42 · approval pending  [a] approve  [r] rerun   │
├──────────────────────────────┴─────────────────────────────────────────────────────────────┤
│ Spend today $3.12 / $10.00 · Reviews 2 running, 3 queued · Last poll 10:04 · gh ✓ claude ✓ │
└─────────────────────────────────────────────────────────────────────────────────────────────┘
```
Keys: `a` approve pending, `r` re-review, `o` open in browser, `p` pause engine, `q` quit.

### Error handling
- `gh`/`claude`/`git` missing or unauthenticated → checked at startup, shown in status bar, engine disabled (TUI still browses).
- GitHub rate limit → back off poll interval, show in status bar.
- Agent non-JSON output or crash → audit event with error, no post, PR marked "review failed"; `r` retries.
- Startup recovery: jobs left "running" in DB are reset to "queued".

## Milestones (each one is a bead)
1. **Skeleton** — go mod, config load, SQLite store + migrations, `ghost --check` (verifies gh/claude/git). ~half day
2. **Read-only TUI** — views from config, poller, PR list with status, detail pane. ~1–2 days
3. **Agent + workspace** — Reviewer interface, claude impl, worktree mgmt, `ghost review <url> --dry-run` CLI that prints the result without posting. ~1–2 days
4. **Engine** — queue, worker pool, budget guard, posting comments, audit trail in detail pane, status bar spend. ~2 days
5. **Approvals** — policy evaluation, approval-pending + keypress, autonomous flag. ~1 day
6. **View editing in TUI** — create/edit/delete views, write config back. ~1 day
7. **Later** — full config editor in TUI, second agent implementation.

## Verification
- Unit tests: `status` derivation (table-driven over GraphQL fixtures), policy rules, budget guard, config round-trip + validation (agent refs, prompt/prompt_file), prompt template rendering, claude JSON parsing from captured fixtures. `go test ./...`
- Fake `Reviewer` + fake GitHub client for engine tests (no network, no spend).
- Manual end-to-end: create a test PR on a scratch repo, add a view `repo:<you>/ghost-sandbox is:open`, run `ghost`, confirm: PR appears with correct status → review runs → comments appear on GitHub → spend updates in status bar → `a` approves.
- `ghost review <url> --dry-run` for iterating on the prompt without posting.
