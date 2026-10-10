package config

import (
	_ "embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed default_config.toml
var DefaultConfigTOML string

// WriteDefaults writes a starter config and prompt beside configPath,
// skipping any file that already exists. It returns the files it created.
func WriteDefaults(configPath string) ([]string, error) {
	dir := filepath.Dir(configPath)
	files := []struct{ path, content string }{
		{configPath, DefaultConfigTOML},
		{filepath.Join(dir, "prompts", "default.md"), DefaultPrompt},
	}
	var created []string
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return created, err
		}
		fh, err := os.OpenFile(f.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return created, err
		}
		_, werr := fh.WriteString(f.content)
		if cerr := fh.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return created, werr
		}
		created = append(created, f.path)
	}
	return created, nil
}
