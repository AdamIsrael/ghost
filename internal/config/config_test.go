package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte("[agents.a]\nkind = \"claude\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollInterval.Duration != 5*time.Minute {
		t.Errorf("PollInterval = %v, want 5m", cfg.PollInterval.Duration)
	}
	if cfg.MaxConcurrentReviews != 2 {
		t.Errorf("MaxConcurrentReviews = %d, want 2", cfg.MaxConcurrentReviews)
	}
	if cfg.MaxDailyCostUSD != 10 || cfg.MaxCostPerReviewUSD != 1.5 {
		t.Errorf("cost defaults = %v/%v", cfg.MaxDailyCostUSD, cfg.MaxCostPerReviewUSD)
	}
	if !cfg.AutoApprove.RequireConfirm {
		t.Error("AutoApprove.RequireConfirm should default to true")
	}
}

func TestParseFullConfig(t *testing.T) {
	src := `
poll_interval = "2m"
max_concurrent_reviews = 3
max_daily_cost_usd = 20.0
default_agent = "sec"

[agents.sec]
kind = "claude"
model = "claude-opus-5-5"
extra_args = ["--verbose"]
prompt = "Review {{.Repo}}#{{.Number}} for security"

[auto_approve]
enabled = true
require_confirm = false
max_changed_lines = 100

[[view]]
name = "Team"
query = "is:open is:pr review-requested:@me"
auto_review = true
agent = "sec"
`
	cfg, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollInterval.Duration != 2*time.Minute || cfg.MaxConcurrentReviews != 3 {
		t.Errorf("top-level values not parsed: %+v", cfg)
	}
	a := cfg.Agents["sec"]
	if a.Kind != "claude" || a.Model != "claude-opus-5-5" || len(a.ExtraArgs) != 1 || !strings.Contains(a.Prompt, "security") {
		t.Errorf("agent not parsed: %+v", a)
	}
	if !cfg.AutoApprove.Enabled || cfg.AutoApprove.RequireConfirm || cfg.AutoApprove.MaxChangedLines != 100 {
		t.Errorf("auto_approve not parsed: %+v", cfg.AutoApprove)
	}
	if !cfg.AutoApprove.RequireCIGreen {
		t.Error("unset auto_approve fields should keep defaults")
	}
	if len(cfg.Views) != 1 || cfg.Views[0].Agent != "sec" || !cfg.Views[0].AutoReview {
		t.Errorf("views not parsed: %+v", cfg.Views)
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	_, err := Parse([]byte("max_concurent_reviews = 3\n"))
	if err == nil || !strings.Contains(err.Error(), "max_concurent_reviews") {
		t.Fatalf("want error naming unknown key, got %v", err)
	}
}

func TestParseRejectsBadDuration(t *testing.T) {
	_, err := Parse([]byte("poll_interval = \"5 minutes\"\n"))
	if err == nil || !strings.Contains(err.Error(), "5 minutes") {
		t.Fatalf("want duration error, got %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err == nil || !strings.Contains(err.Error(), "ghost init") {
		t.Fatalf("want hint to run ghost init, got %v", err)
	}
}

func TestLoadSetsBaseDir(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("[agents.a]\nkind = \"claude\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.baseDir != dir {
		t.Errorf("baseDir = %q, want %q", cfg.baseDir, dir)
	}
}
