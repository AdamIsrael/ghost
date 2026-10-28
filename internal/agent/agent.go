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
	// OutsideDiff is set by Ghost (not the agent) when Line isn't part of
	// the PR's diff, so GitHub won't accept the comment inline.
	OutsideDiff bool `json:"outside_diff,omitempty"`
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
	ContextDir string  // Ghost-written review materials (diff, commits), outside Dir
	Prompt     string  // BuildPrompt output
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

// BuildPrompt appends Ghost's fixed sections to the operator's rendered
// prompt: where the review materials are, then the output contract.
func BuildPrompt(rendered, diffPath, commitsPath string) string {
	materials := fmt.Sprintf(`## Review materials
You cannot run commands. Your working directory is the repository at the PR head commit; explore it with Read, Grep and Glob.
- Full diff against the base branch: %s
- Commits in this PR: %s`, diffPath, commitsPath)
	return rendered + "\n\n" + materials + "\n\n" + OutputContract
}

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
