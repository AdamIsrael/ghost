package config

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMergesViewsFile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	writeFile(t, cfg, minimal+"[[view]]\nname = \"Old\"\nquery = \"q1\"\n")
	writeFile(t, filepath.Join(dir, "views.toml"), "[[view]]\nname = \"New\"\nquery = \"q2\"\nauto_review = true\n")
	c, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Views) != 2 || c.Views[0].Name != "Old" || c.Views[0].Source != "config.toml" ||
		c.Views[1].Name != "New" || c.Views[1].Source != "views.toml" || !c.Views[1].AutoReview {
		t.Fatalf("views = %+v", c.Views)
	}
}

func TestLoadWithoutViewsFile(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, cfg, minimal)
	if c, err := Load(cfg); err != nil || len(c.Views) != 0 {
		t.Fatalf("views %+v err %v", c.Views, err)
	}
}

func TestViewNamesUniqueAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	writeFile(t, cfg, minimal+"[[view]]\nname = \"Same\"\nquery = \"q1\"\n")
	writeFile(t, filepath.Join(dir, "views.toml"), "[[view]]\nname = \"Same\"\nquery = \"q2\"\n")
	if _, err := Load(cfg); err == nil || !strings.Contains(err.Error(), `duplicate view name "Same"`) {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestViewsFileRejectsOtherKeys(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	writeFile(t, cfg, minimal)
	writeFile(t, filepath.Join(dir, "views.toml"), "poll_interval = \"1m\"\n")
	if _, err := Load(cfg); err == nil || !strings.Contains(err.Error(), "views.toml") {
		t.Fatalf("want views.toml error, got %v", err)
	}
}

func TestSaveViewsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	writeFile(t, cfg, minimal)
	views := []View{{Name: "A", Query: "is:pr", AutoReview: true, Agent: "a"}, {Name: "B", Query: "is:pr author:@me"}}
	if err := SaveViews(ViewsPath(cfg), views); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(ViewsPath(cfg))
	if !strings.HasPrefix(string(data), "# Views managed by Ghost") || strings.Contains(string(data), `agent = ""`) {
		t.Errorf("views.toml =\n%s", data)
	}
	c, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Views) != 2 || c.Views[0] != (View{Name: "A", Query: "is:pr", AutoReview: true, Agent: "a", Source: "views.toml"}) {
		t.Fatalf("views = %+v", c.Views)
	}
	if err := SaveViews(ViewsPath(cfg), nil); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(cfg); err != nil || len(c.Views) != 0 {
		t.Fatalf("after saving no views: %+v %v", c.Views, err)
	}
}

func TestSaveViewsLeavesConfigUntouched(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	writeFile(t, cfg, "# my precious comment\n"+minimal)
	before, _ := os.ReadFile(cfg)
	SaveViews(ViewsPath(cfg), []View{{Name: "A", Query: "q"}})
	after, _ := os.ReadFile(cfg)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("config.toml changed")
	}
}

func TestSaveViewsAtomicLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	if err := SaveViews(filepath.Join(dir, "views.toml"), []View{{Name: "A", Query: "q"}}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "views.toml" {
		t.Errorf("dir = %v", entries)
	}
}
