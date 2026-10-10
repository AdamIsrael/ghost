package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string) Config {
	t.Helper()
	cfg, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

const minimal = "[agents.a]\nkind = \"claude\"\n"

func TestValidateMinimalFillsDefaultAgent(t *testing.T) {
	cfg := mustParse(t, minimal)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultAgent != "a" {
		t.Errorf("DefaultAgent = %q, want a", cfg.DefaultAgent)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"no agents", "", "at least one [agents.<name>]"},
		{"two agents no default", minimal + "[agents.b]\nkind = \"claude\"\n", "default_agent is required"},
		{"unknown default", "default_agent = \"x\"\n" + minimal, `default_agent "x"`},
		{"unknown kind", "[agents.a]\nkind = \"gpt\"\n", `unknown kind "gpt"`},
		{"missing kind", "[agents.a]\nmodel = \"m\"\n", "kind is required"},
		{"prompt and file", "[agents.a]\nkind = \"claude\"\nprompt = \"x\"\nprompt_file = \"y.md\"\n", "set prompt or prompt_file, not both"},
		{"missing prompt file", "[agents.a]\nkind = \"claude\"\nprompt_file = \"/nonexistent/p.md\"\n", "/nonexistent/p.md"},
		{"bad template", "[agents.a]\nkind = \"claude\"\nprompt = \"{{.Repo\"\n", "agents.a prompt"},
		{"unknown template field", "[agents.a]\nkind = \"claude\"\nprompt = \"{{.Nope}}\"\n", "Nope"},
		{"poll too short", "poll_interval = \"1s\"\n" + minimal, "poll_interval"},
		{"zero concurrency", "max_concurrent_reviews = 0\n" + minimal, "max_concurrent_reviews"},
		{"zero daily cost", "max_daily_cost_usd = 0.0\n" + minimal, "max_daily_cost_usd"},
		{"view no query", minimal + "[[view]]\nname = \"v\"\n", `view "v": query is required`},
		{"view no name", minimal + "[[view]]\nquery = \"is:pr\"\n", "view 1: name is required"},
		{"dup views", minimal + "[[view]]\nname = \"v\"\nquery = \"q\"\n[[view]]\nname = \"v\"\nquery = \"q\"\n", `duplicate view name "v"`},
		{"view bad agent", minimal + "[[view]]\nname = \"v\"\nquery = \"q\"\nagent = \"zz\"\n", `unknown agent "zz"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mustParse(t, tc.src)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestAgentPromptInline(t *testing.T) {
	cfg := mustParse(t, "[agents.a]\nkind = \"claude\"\nprompt = \"hello {{.Repo}}\"\n")
	got, err := cfg.AgentPrompt("a")
	if err != nil || got != "hello {{.Repo}}" {
		t.Fatalf("AgentPrompt = %q, %v", got, err)
	}
}

func TestAgentPromptDefault(t *testing.T) {
	cfg := mustParse(t, minimal)
	got, err := cfg.AgentPrompt("a")
	if err != nil || got != DefaultPrompt {
		t.Fatalf("want DefaultPrompt, got %q, %v", got, err)
	}
}

func TestAgentPromptFileResolution(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "home.md"), []byte("from home"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfgDir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "prompts", "rel.md"), []byte("from rel"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "default_agent = \"h\"\n" +
		"[agents.h]\nkind = \"claude\"\nprompt_file = \"~/home.md\"\n" +
		"[agents.r]\nkind = \"claude\"\nprompt_file = \"prompts/rel.md\"\n"
	cfgPath := filepath.Join(cfgDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"h": "from home", "r": "from rel"} {
		got, err := cfg.AgentPrompt(name)
		if err != nil || got != want {
			t.Errorf("AgentPrompt(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
}

func TestRenderPrompt(t *testing.T) {
	out, err := RenderPrompt("{{.Repo}}#{{.Number}} {{range .ChangedFiles}}[{{.}}]{{end}}",
		PromptData{Repo: "o/r", Number: 7, ChangedFiles: []string{"a.go", "b.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if out != "o/r#7 [a.go][b.go]" {
		t.Errorf("got %q", out)
	}
}

func TestDefaultPromptRenders(t *testing.T) {
	if _, err := RenderPrompt(DefaultPrompt, PromptData{}); err != nil {
		t.Fatalf("default prompt does not render: %v", err)
	}
}

func TestLoadValidates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "at least one [agents.<name>]") {
		t.Fatalf("Load should validate, got %v", err)
	}
}

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

func TestPostModeDefaultAndValidation(t *testing.T) {
	cfg := mustParse(t, minimal)
	if cfg.PostMode != "confirm" {
		t.Errorf("default post_mode = %q, want confirm", cfg.PostMode)
	}
	cfg = mustParse(t, "post_mode = \"auto\"\n"+minimal)
	if err := cfg.Validate(); err != nil {
		t.Errorf("auto rejected: %v", err)
	}
	cfg = mustParse(t, "post_mode = \"yolo\"\n"+minimal)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "post_mode") {
		t.Errorf("want post_mode error, got %v", err)
	}
}

func TestMaxChangedLinesNotNegative(t *testing.T) {
	cfg := mustParse(t, minimal+"[auto_approve]\nmax_changed_lines = -1\n")
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "max_changed_lines") {
		t.Fatalf("want max_changed_lines error, got %v", err)
	}
}

func TestThemeDefaultAndValidation(t *testing.T) {
	if cfg := mustParse(t, minimal); cfg.Theme != "auto" {
		t.Errorf("default theme = %q", cfg.Theme)
	}
	cfg := mustParse(t, "theme = \"neon\"\n"+minimal)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "theme") {
		t.Errorf("want theme error, got %v", err)
	}
	cfg = mustParse(t, "theme = \"high-contrast\"\n"+minimal)
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid theme rejected: %v", err)
	}
}

func TestUnknownThemeListsOptions(t *testing.T) {
	cfg := mustParse(t, "theme = \"neon\"\n"+minimal)
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "tokyo-night") || !strings.Contains(err.Error(), "neon.toml") {
		t.Fatalf("want error listing built-ins and the file to add, got %v", err)
	}
}

func TestUserThemeAccepted(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "themes"), 0o755)
	writeFile(t, filepath.Join(dir, "themes", "mine.toml"), "muted = \"#aaaaaa\"\n")
	cfg := filepath.Join(dir, "config.toml")
	writeFile(t, cfg, "theme = \"mine\"\n"+minimal)
	if _, err := Load(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestBrokenThemeFileFailsLoad(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "themes"), 0o755)
	bad := filepath.Join(dir, "themes", "bad.toml")
	writeFile(t, bad, "muted = \"grey\"\n")
	cfg := filepath.Join(dir, "config.toml")
	writeFile(t, cfg, minimal) // theme is auto; the broken file still fails
	if _, err := Load(cfg); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("want error naming %s, got %v", bad, err)
	}
}
