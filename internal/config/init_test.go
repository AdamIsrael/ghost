package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteDefaultsProducesValidConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ghost", "config.toml")
	created, err := WriteDefaults(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 3 {
		t.Fatalf("created = %v, want 2 files", created)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("default config does not load: %v", err)
	}
	got, err := cfg.AgentPrompt("claude")
	if err != nil || got != DefaultPrompt {
		t.Errorf("default agent prompt = %q, %v", got, err)
	}
	if len(cfg.Views) != 1 || cfg.Views[0].Source != "views.toml" {
		t.Errorf("starter view should come from views.toml: %+v", cfg.Views)
	}
}

func TestWriteDefaultsNeverOverwrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := WriteDefaults(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || filepath.Base(created[0]) != "default.md" {
		t.Errorf("created = %v, want only the prompt (views.toml only comes with a new config)", created)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "mine" {
		t.Error("existing config was overwritten")
	}
}

func TestReinitWithOldStarterViewStillLoads(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	// An install from before views.toml: the starter view lives in config.toml.
	old := DefaultConfigTOML + "\n[[view]]\nname = \"Review requested\"\nquery = \"is:open is:pr review-requested:@me\"\n"
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteDefaults(p); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err != nil {
		t.Fatalf("re-running init broke the config: %v", err)
	}
}
