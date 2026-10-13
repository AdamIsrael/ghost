# Review Label — Design

## Context
The operator wants a heads-up before Ghost reviews a PR, with a way for the PR's people to opt out. Ghost adds a label (e.g. `ai-review`) to a PR first and reviews on a later poll. Removing the label before then (or later) opts the PR out.

Decisions (operator, 2026-10-13): the label is a heads-up with opt-out (removal is respected, not undone); configured globally with a per-view override.

## Behavior
For each poll of a view with `auto_review = true`, for each non-draft PR, with **L** = the view's effective label:

| PR state | Ghost |
|---|---|
| No L configured (off) | Queue the review as today |
| PR has L | Queue the review as usual. If Ghost had recorded an opt-out, clear it ("label re-added") |
| No L; Ghost never added L to this PR | Add L, record it, audit `labeled` ("added ai-review; reviews on the next poll"). Don't queue |
| No L; Ghost added it before | Opt-out. Record it and audit `opted out` ("ai-review was removed; not reviewing") once; afterwards skip silently |
| Adding L fails | Audit `label failed` with the error; don't queue. Retried next poll (no record is written, so the PR is still "never labeled") |

- "Next poll" is that view's next poll, so the delay is about `poll_interval`.
- Label names compare case-insensitively (as on GitHub). Stored lowercase.
- GitHub creates the label in the repo if it doesn't exist. Adding needs triage/write access; without it, every poll audits `label failed` (deduplicated: the same error message isn't audited twice in a row for a PR).
- Drafts are neither labeled nor reviewed.
- Manual re-review (`r`) bypasses the label check: the operator asked directly.
- Reviews already queued or running are not affected by the label (no un-queueing).
- A PR the opt-out applies to stays opted out for later commits until someone adds the label back.

## Config
```toml
# config.toml
review_label = "ai-review"   # empty or missing = off

# views.toml
[[view]]
name = "Team"
query = "is:open is:pr review-requested:@me"
auto_review = true
label = "needs-ai"           # optional; omitted = review_label; "" = no label for this view
```
- `View.Label` is `*string`: nil = inherit, `""` = off, otherwise the label.
- `Config.LabelFor(v View) string` returns the effective label (`""` = off).
- Validation (both keys): no leading/trailing whitespace, not whitespace-only, ≤ 50 characters, no commas (GitHub label limits / search syntax).
- TUI view form: a **Label** field. Blank = inherit (placeholder shows the global label, or "none"); `-` = no label for this view; any other text = that label. Editing round-trips all three states through `views.toml`.
- `ghost --check` shows the global label, or "off".

## Components
1. **`internal/github`**: search and GetPR queries fetch `labels(first: 50) { nodes { name } }` → `PR.Labels []string`; `PR.HasLabel(name) bool` (case-insensitive); `Client.AddLabel(ctx, repo, number, label) error` via `POST /repos/{repo}/issues/{number}/labels` with `{"labels":[label]}` (appends; does not replace).
2. **`internal/config`**: `ReviewLabel`, `View.Label`, `LabelFor`, validation, form support.
3. **`internal/store`**: migration 4 adds
   `pr_labels(pr_id INTEGER REFERENCES prs(id), label TEXT, added_at TIMESTAMP, opted_out_at TIMESTAMP NULL, PRIMARY KEY(pr_id, label))`.
   Methods (single-statement writes): `LabelState(prID, label) (LabelState, error)` returning `LabelNever | LabelAdded | LabelOptedOut`; `MarkLabeled`, `MarkOptedOut` (returns whether it changed, so the audit fires once), `ClearOptOut` (same).
4. **`internal/engine`**: `Deps.AddLabel`. `enqueue` evaluates the table above. PRs that need a label are collected and labeled **off the engine loop** in a goroutine guarded by a `TryLock` (like approval rechecks), each call with a 30s timeout, so a slow GitHub never blocks the loop and polls don't pile up. Audit kinds: `labeled`, `opted out`, `label re-added`, `label failed`.

## Error handling
- AddLabel failure: no store write, audit `label failed` (deduped), retry next poll. Never review without the label having been added at least once.
- Store errors: skip the PR this poll (fail closed: no review).
- PRs missing from a poll result: nothing happens.

## Testing
- Engine (fake AddLabel): one test per table row — labeled → queued; unlabeled → labeled, not queued, queued next poll; removed → opted out, audited once across several polls; re-added → resumes and audits `label re-added`; AddLabel fails → not queued, retried, error audited once; draft → untouched; `r` bypasses; per-view `""` → no label needed; per-view override label used instead of global; label off globally → today's behavior.
- Unit: `HasLabel` case-insensitivity; `LabelFor` precedence; view TOML round-trip of nil / `""` / value; form `-` and blank handling; validation errors; migration from version 3 preserves data; GraphQL parsing of labels; AddLabel request path and body.
- Stress: engine tests `-count=200` and `-race -count=20`.
- Live: one run against the private `ghost-sandbox` repo, with the operator's go-ahead before anything is written there.

## Out of scope
Removing the label after review; label colors/descriptions; per-agent labels; showing labels in the PR list; un-queueing reviews when a label disappears.
