// Package config loads and validates Ghost's TOML configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Duration is a time.Duration that reads from TOML strings like "5m".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", string(b), err)
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

type Config struct {
	PollInterval         Duration         `toml:"poll_interval"`
	MaxConcurrentReviews int              `toml:"max_concurrent_reviews"`
	MaxDailyCostUSD      float64          `toml:"max_daily_cost_usd"`
	MaxCostPerReviewUSD  float64          `toml:"max_cost_per_review_usd"`
	PostMode             string           `toml:"post_mode"`
	Theme                string           `toml:"theme"`
	DefaultAgent         string           `toml:"default_agent"`
	Agents               map[string]Agent `toml:"agents"`
	AutoApprove          AutoApprove      `toml:"auto_approve"`
	Views                []View           `toml:"view"`

	// baseDir is the directory of the loaded config file; relative
	// prompt_file paths resolve against it.
	baseDir string
}

type Agent struct {
	Kind       string   `toml:"kind"`
	Model      string   `toml:"model"`
	ExtraArgs  []string `toml:"extra_args"`
	Prompt     string   `toml:"prompt"`
	PromptFile string   `toml:"prompt_file"`
}

type AutoApprove struct {
	Enabled            bool `toml:"enabled"`
	RequireConfirm     bool `toml:"require_confirm"`
	RequireCIGreen     bool `toml:"require_ci_green"`
	NoChecksIsGreen    bool `toml:"no_checks_is_green"`
	NoBlockingComments bool `toml:"no_blocking_comments"`
	NoChangesRequested bool `toml:"no_changes_requested"`
	MaxChangedLines    int  `toml:"max_changed_lines"`
}

type View struct {
	Name       string `toml:"name"`
	Query      string `toml:"query"`
	AutoReview bool   `toml:"auto_review"`
	Agent      string `toml:"agent,omitempty"`

	// Source is the file that defined the view: "config.toml" or
	// "views.toml". Only views.toml views are edited from the TUI.
	Source string `toml:"-"`
}

// Default returns the configuration used for any key the file leaves unset.
func Default() Config {
	return Config{
		PollInterval:         Duration{5 * time.Minute},
		MaxConcurrentReviews: 2,
		MaxDailyCostUSD:      10,
		MaxCostPerReviewUSD:  1.5,
		PostMode:             "confirm",
		Theme:                "auto",
		Agents:               map[string]Agent{},
		AutoApprove: AutoApprove{
			RequireConfirm:     true,
			RequireCIGreen:     true,
			NoBlockingComments: true,
			NoChangesRequested: true,
			MaxChangedLines:    400,
		},
	}
}

// Parse decodes TOML over Default(). Unknown keys are an error so typos surface.
func Parse(data []byte) (Config, error) {
	cfg := Default()
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, err
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return Config{}, fmt.Errorf("unknown config keys: %s", strings.Join(keys, ", "))
	}
	return cfg, nil
}

// Load reads and parses the config file at path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, fmt.Errorf("config not found at %s: run `ghost init` to create one", path)
	}
	if err != nil {
		return Config{}, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	cfg.baseDir = filepath.Dir(path)
	for i := range cfg.Views {
		cfg.Views[i].Source = "config.toml"
	}
	managed, err := loadViewsFile(ViewsPath(path))
	if err != nil {
		return Config{}, err
	}
	cfg.Views = append(cfg.Views, managed...)
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}
