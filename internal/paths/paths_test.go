package paths

import (
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := map[string]string{
		"~":             home,
		"~/x/prompt.md": filepath.Join(home, "x/prompt.md"),
		"/abs/path":     "/abs/path",
		"rel/path":      "rel/path",
		"~other/x":      "~other/x",
	}
	for in, want := range cases {
		if got := ExpandHome(in); got != want {
			t.Errorf("ExpandHome(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultLocations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	if got, want := ConfigFile(), filepath.Join(home, ".config/ghost/config.toml"); got != want {
		t.Errorf("ConfigFile = %q, want %q", got, want)
	}
	if got, want := DataFile(), filepath.Join(home, ".local/share/ghost/ghost.db"); got != want {
		t.Errorf("DataFile = %q, want %q", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", "/xc")
	t.Setenv("XDG_DATA_HOME", "/xd")
	if got := ConfigFile(); got != "/xc/ghost/config.toml" {
		t.Errorf("ConfigFile with XDG = %q", got)
	}
	if got := DataFile(); got != "/xd/ghost/ghost.db" {
		t.Errorf("DataFile with XDG = %q", got)
	}
}

func TestCacheDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "")
	if got, want := CacheDir(), filepath.Join(home, ".cache/ghost"); got != want {
		t.Errorf("CacheDir = %q, want %q", got, want)
	}
	t.Setenv("XDG_CACHE_HOME", "/xc")
	if got := CacheDir(); got != "/xc/ghost" {
		t.Errorf("CacheDir with XDG = %q", got)
	}
}
