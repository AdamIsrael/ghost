package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLogPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xs")
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := []struct {
		toml, want string
	}{
		{"", "/xs/ghost/ghost.log"},  // default
		{`log_file = ""` + "\n", ""}, // off
		{`log_file = "/var/log/g.log"` + "\n", "/var/log/g.log"},
		{`log_file = "~/g.log"` + "\n", filepath.Join(home, "g.log")},
		{`log_file = "logs/g.log"` + "\n", "/cfgdir/logs/g.log"}, // relative to config.toml
	}
	for _, tc := range cases {
		cfg := mustParse(t, tc.toml+minimal)
		cfg.baseDir = "/cfgdir"
		if got := cfg.LogPath(); got != tc.want {
			t.Errorf("%q: LogPath = %q, want %q", tc.toml, got, tc.want)
		}
	}
}

func TestDefaultConfigShowsLogFile(t *testing.T) {
	if !strings.Contains(DefaultConfigTOML, `# log_file = ""`) {
		t.Error("the starter config should show how to turn logging off")
	}
}
