package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/run"
)

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
		// The worktree is untrusted PR content. --restricted removes every
		// command-running tool, ignores user/project/local settings, and
		// confines file tools to the working directories; --safe-mode skips
		// CLAUDE.md, skills, plugins and hooks; no MCP servers start; and
		// dontAsk denies anything not already allowed. The agent can only
		// read the worktree and the context dir.
		"--restricted",
		"--safe-mode",
		"--strict-mcp-config",
		"--permission-mode", "dontAsk",
		"--tools", "Read,Grep,Glob",
	}
	if req.ContextDir != "" {
		args = append(args, "--add-dir", req.ContextDir)
	}
	if c.cfg.Model != "" {
		args = append(args, "--model", c.cfg.Model)
	}
	if req.MaxCostUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", req.MaxCostUSD))
	}
	args = append(args, c.cfg.ExtraArgs...)
	// --tools and --add-dir are variadic; a single-value flag must come last.
	return append(args, "--json-schema", ResultSchema)
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
