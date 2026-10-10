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
	if len(created) != 2 {
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
		t.Errorf("created = %v, want only the prompt", created)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "mine" {
		t.Error("existing config was overwritten")
	}
}
