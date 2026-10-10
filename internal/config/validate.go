package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AdamIsrael/ghost/internal/paths"
	"github.com/AdamIsrael/ghost/internal/theme"
)

// KnownAgentKinds lists the agent implementations Ghost can run.
var KnownAgentKinds = map[string]bool{"claude": true}

const minPollInterval = 30 * time.Second

// Validate checks the config and reports every problem at once. When exactly
// one agent is defined and default_agent is unset, it becomes the default.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if c.PollInterval.Duration < minPollInterval {
		add("poll_interval must be at least %s", minPollInterval)
	}
	if c.MaxConcurrentReviews < 1 {
		add("max_concurrent_reviews must be at least 1")
	}
	if c.MaxDailyCostUSD <= 0 {
		add("max_daily_cost_usd must be greater than 0")
	}
	if c.MaxCostPerReviewUSD <= 0 {
		add("max_cost_per_review_usd must be greater than 0")
	} else if c.MaxCostPerReviewUSD > c.MaxDailyCostUSD && c.MaxDailyCostUSD > 0 {
		add("max_cost_per_review_usd ($%.2f) is more than max_daily_cost_usd ($%.2f), so the daily budget could never fit a review",
			c.MaxCostPerReviewUSD, c.MaxDailyCostUSD)
	}
	if c.PostMode != "confirm" && c.PostMode != "auto" {
		add(`post_mode must be "confirm" or "auto"`)
	}
	if c.AutoApprove.MaxChangedLines < 0 {
		add("auto_approve.max_changed_lines must be 0 (no limit) or more")
	}

	switch {
	case len(c.Agents) == 0:
		add("at least one [agents.<name>] section is required")
	case c.DefaultAgent == "" && len(c.Agents) == 1:
		for name := range c.Agents {
			c.DefaultAgent = name
		}
	case c.DefaultAgent == "":
		add("default_agent is required when more than one agent is defined")
	default:
		if _, ok := c.Agents[c.DefaultAgent]; !ok {
			add("default_agent %q is not defined under [agents]", c.DefaultAgent)
		}
	}

	names := make([]string, 0, len(c.Agents))
	for name := range c.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		a := c.Agents[name]
		switch {
		case a.Kind == "":
			add("agents.%s: kind is required", name)
		case !KnownAgentKinds[a.Kind]:
			add("agents.%s: unknown kind %q", name, a.Kind)
		}
		if a.Prompt != "" && a.PromptFile != "" {
			add("agents.%s: set prompt or prompt_file, not both", name)
			continue
		}
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
	}

	if err := c.ValidateViews(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ValidateViews checks only the views (names, queries, agent references), so
// saving a view isn't blocked by an unrelated file that broke after startup.
func (c *Config) ValidateViews() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	seen := map[string]bool{}
	for i, v := range c.Views {
		label := fmt.Sprintf("view %q", v.Name)
		if v.Name == "" {
			label = fmt.Sprintf("view %d", i+1)
			add("%s: name is required", label)
		} else if seen[v.Name] {
			add("duplicate view name %q", v.Name)
		}
		seen[v.Name] = true
		if v.Query == "" {
			add("%s: query is required", label)
		}
		if v.Agent != "" {
			if _, ok := c.Agents[v.Agent]; !ok {
				add("%s: unknown agent %q", label, v.Agent)
			}
		}
	}
	return errors.Join(errs...)
}

// ValidateThemes loads the theme files and checks that the configured theme
// exists. It is separate from Validate because only the TUI and --check use
// themes; a broken theme file shouldn't stop ghost review.
func (c *Config) ValidateThemes() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if themes, err := theme.Load(c.ThemesDir()); err != nil {
		add("%v", err)
	} else if _, ok := themes.Get(c.Theme); c.Theme != "auto" && !ok {
		dir := c.ThemesDir()
		if dir == "" {
			dir = "<config dir>/themes"
		}
		add("theme %q not found: use auto, a built-in (%s), or add %s",
			c.Theme, strings.Join(theme.BuiltinOrder, ", "), filepath.Join(dir, c.Theme+".toml"))
	}
	return errors.Join(errs...)
}

// AgentPrompt returns the raw prompt template text for the named agent:
// inline prompt, else the contents of prompt_file, else DefaultPrompt.
func (c *Config) AgentPrompt(name string) (string, error) {
	a, ok := c.Agents[name]
	if !ok {
		return "", fmt.Errorf("unknown agent %q", name)
	}
	switch {
	case a.Prompt != "":
		return a.Prompt, nil
	case a.PromptFile != "":
		data, err := os.ReadFile(c.resolve(a.PromptFile))
		if err != nil {
			return "", fmt.Errorf("prompt_file: %w", err)
		}
		return string(data), nil
	default:
		return DefaultPrompt, nil
	}
}

func (c *Config) resolve(p string) string {
	p = paths.ExpandHome(p)
	if filepath.IsAbs(p) || c.baseDir == "" {
		return p
	}
	return filepath.Join(c.baseDir, p)
}
