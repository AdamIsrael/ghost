package config

import (
	"path/filepath"

	"github.com/AdamIsrael/ghost/internal/paths"
)

// LogPath returns where Ghost writes its log: log_file if set (~ expanded,
// relative paths against the config file's directory), the XDG state
// default when unset, or "" when log_file = "" turns logging off.
func (c *Config) LogPath() string {
	if c.LogFile == nil {
		return paths.LogFile()
	}
	p := paths.ExpandHome(*c.LogFile)
	if p == "" || filepath.IsAbs(p) || c.baseDir == "" {
		return p
	}
	return filepath.Join(c.baseDir, p)
}
