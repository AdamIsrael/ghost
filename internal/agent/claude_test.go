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
	res, err := r.Review(context.Background(), ReviewRequest{Dir: "/wt", ContextDir: "/ctx", Prompt: "PROMPT", MaxCostUSD: 1.5})
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
		"--tools":           "Read,Grep,Glob",
		"--add-dir":         "/ctx",
		"--json-schema":     ResultSchema,
	} {
		if got, ok := argAfter(c.args, flag); !ok || got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	joined := strings.Join(c.args, " ")
	for _, want := range []string{"-p", "--restricted", "--safe-mode", "--strict-mcp-config", "--no-session-persistence", "--verbose"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, c.args)
		}
	}
	for _, bad := range []string{"--bare", "--mcp-config", "--allowedTools", "--setting-sources", "bypassPermissions"} {
		for _, a := range c.args {
			if a == bad {
				t.Errorf("args must not contain %q", bad)
			}
		}
	}
	for _, a := range c.args {
		if a != ResultSchema && (strings.Contains(a, "Bash") || strings.Contains(a, "Edit") || strings.Contains(a, "Write")) {
			t.Errorf("agent must have no shell or write tools, got arg %q", a)
		}
	}
	if last := c.args[len(c.args)-2]; last != "--json-schema" {
		t.Errorf("a non-variadic flag must come last (variadic --tools/--add-dir would swallow args), got %q", last)
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
	for _, flag := range []string{"--model", "--max-budget-usd", "--add-dir"} {
		if _, ok := argAfter(c.args, flag); ok {
			t.Errorf("%s should be omitted when unset", flag)
		}
	}
}
