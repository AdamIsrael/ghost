// Package paths resolves Ghost's default file locations.
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome replaces a leading "~" or "~/" with the user's home directory.
func ExpandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}

func ConfigFile() string { return xdg("XDG_CONFIG_HOME", ".config", "config.toml") }

func DataFile() string { return xdg("XDG_DATA_HOME", ".local/share", "ghost.db") }

func xdg(env, fallback, file string) string {
	if base := os.Getenv(env); base != "" {
		return filepath.Join(base, "ghost", file)
	}
	return filepath.Join(ExpandHome("~/"+fallback), "ghost", file)
}
