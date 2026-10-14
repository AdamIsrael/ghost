package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaxPRsDefault(t *testing.T) {
	cfg := mustParse(t, minimal)
	if cfg.MaxPRsPerView != DefaultMaxPRs || DefaultMaxPRs != 50 {
		t.Fatalf("MaxPRsPerView = %d, DefaultMaxPRs = %d", cfg.MaxPRsPerView, DefaultMaxPRs)
	}
	if got := cfg.PRLimitFor(View{}); got != 50 {
		t.Errorf("PRLimitFor(no override) = %d", got)
	}
}

func TestPRLimitFor(t *testing.T) {
	c := Config{MaxPRsPerView: 30}
	if got := c.PRLimitFor(View{}); got != 30 {
		t.Errorf("inherit: %d", got)
	}
	if got := c.PRLimitFor(View{MaxPRs: 100}); got != 100 {
		t.Errorf("override: %d", got)
	}
	if got := (&Config{}).PRLimitFor(View{}); got != DefaultMaxPRs {
		t.Errorf("zero global falls back to the default: %d", got)
	}
}

func TestValidateMaxPRs(t *testing.T) {
	for _, ok := range []string{"max_prs_per_view = 1\n", "max_prs_per_view = 100\n"} {
		cfg := mustParse(t, ok+minimal)
		if err := cfg.Validate(); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"max_prs_per_view = 0\n", "max_prs_per_view = 101\n", "max_prs_per_view = -5\n"} {
		cfg := mustParse(t, bad+minimal)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "max_prs_per_view") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	view := func(n string) string { return minimal + "[[view]]\nname = \"V\"\nquery = \"q\"\nmax_prs = " + n + "\n" }
	for _, ok := range []string{"0", "1", "100"} {
		cfg := mustParse(t, view(ok))
		if err := cfg.Validate(); err != nil {
			t.Errorf("max_prs = %s: %v", ok, err)
		}
	}
	for _, bad := range []string{"-1", "101"} {
		cfg := mustParse(t, view(bad))
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), `view "V": max_prs`) {
			t.Errorf("max_prs = %s: err = %v", bad, err)
		}
	}
}

func TestViewMaxPRsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	writeFile(t, cfg, minimal)
	if err := SaveViews(ViewsPath(cfg), []View{{Name: "A", Query: "q"}, {Name: "B", Query: "q", MaxPRs: 80}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(ViewsPath(cfg))
	if strings.Count(string(data), "max_prs") != 1 {
		t.Errorf("views.toml =\n%s", data)
	}
	c, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c.Views[0].MaxPRs != 0 || c.Views[1].MaxPRs != 80 {
		t.Errorf("max_prs = %d, %d", c.Views[0].MaxPRs, c.Views[1].MaxPRs)
	}
}

func TestDefaultConfigShowsMaxPRs(t *testing.T) {
	if !strings.Contains(DefaultConfigTOML, "# max_prs_per_view = 50") {
		t.Error("the starter config should show max_prs_per_view commented out")
	}
}
