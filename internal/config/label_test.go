package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

func TestLabelFor(t *testing.T) {
	c := Config{ReviewLabel: "ai-review"}
	cases := []struct {
		v    View
		want string
	}{
		{View{}, "ai-review"},
		{View{Label: ptr("")}, ""},
		{View{Label: ptr("needs-ai")}, "needs-ai"},
	}
	for _, tc := range cases {
		if got := c.LabelFor(tc.v); got != tc.want {
			t.Errorf("LabelFor(%v) = %q, want %q", tc.v.Label, got, tc.want)
		}
	}
	if got := (&Config{}).LabelFor(View{}); got != "" {
		t.Errorf("no global label: got %q", got)
	}
}

func TestValidateLabels(t *testing.T) {
	ok := mustParse(t, "review_label = \"ai-review\"\n"+minimal+"[[view]]\nname = \"V\"\nquery = \"q\"\nlabel = \"\"\n")
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []string{" ai", "ai ", "   ", "a,b", "-", strings.Repeat("x", 51)}
	for _, b := range bad {
		cfg := mustParse(t, "review_label = \""+b+"\"\n"+minimal)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "review_label") {
			t.Errorf("review_label %q: err = %v", b, err)
		}
		cfg = mustParse(t, minimal+"[[view]]\nname = \"V\"\nquery = \"q\"\nlabel = \""+b+"\"\n")
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), `view "V": label`) {
			t.Errorf("view label %q: err = %v", b, err)
		}
	}
	if cfg := mustParse(t, "review_label = \""+strings.Repeat("é", 50)+"\"\n"+minimal); cfg.Validate() != nil {
		t.Error("50 characters (not bytes) should be allowed")
	}
}

func TestViewLabelRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	writeFile(t, cfg, minimal)
	views := []View{
		{Name: "Inherit", Query: "q"},
		{Name: "Off", Query: "q", Label: ptr("")},
		{Name: "Own", Query: "q", Label: ptr("needs-ai")},
	}
	if err := SaveViews(ViewsPath(cfg), views); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(ViewsPath(cfg))
	if strings.Count(string(data), "label =") != 2 {
		t.Errorf("views.toml =\n%s", data)
	}
	c, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c.Views[0].Label != nil || c.Views[1].Label == nil || *c.Views[1].Label != "" ||
		c.Views[2].Label == nil || *c.Views[2].Label != "needs-ai" {
		t.Errorf("labels = %v %v %v", c.Views[0].Label, c.Views[1].Label, c.Views[2].Label)
	}
}

func TestDefaultConfigLeavesLabelOff(t *testing.T) {
	if strings.Contains(DefaultConfigTOML, "\nreview_label") {
		t.Error("the starter config must not turn the label on")
	}
	if !strings.Contains(DefaultConfigTOML, "# review_label = \"ai-review\"") {
		t.Error("the starter config should show review_label commented out")
	}
}
