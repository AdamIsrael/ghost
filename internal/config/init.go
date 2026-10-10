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

// defaultViews is the starter views.toml. auto_review is off so nothing is
// spent until the operator opts in.
const defaultViews = viewsHeader + `[[view]]
name = "Review requested"
query = "is:open is:pr review-requested:@me"
auto_review = false
`

// WriteDefaults writes a starter config and prompt beside configPath,
// skipping any file that already exists. It returns the files it created.
func WriteDefaults(configPath string) ([]string, error) {
	dir := filepath.Dir(configPath)
	files := []struct{ path, content string }{
		{configPath, DefaultConfigTOML},
		{filepath.Join(dir, "prompts", "default.md"), DefaultPrompt},
		{filepath.Join(dir, ViewsFileName), defaultViews},
	}
	var created []string
	for i, f := range files {
		// The starter views.toml only comes with a brand-new config.toml: an
		// existing install may already define that view, and a duplicate name
		// would stop Ghost from starting.
		if i == 2 && (len(created) == 0 || created[0] != configPath) {
			continue
		}
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
