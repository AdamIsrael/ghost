package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// ViewsFileName holds the views Ghost manages from the TUI. It sits next to
// config.toml, which Ghost never writes (so its comments survive).
const ViewsFileName = "views.toml"

func ViewsPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), ViewsFileName)
}

type viewsFile struct {
	View []View `toml:"view"`
}

const viewsHeader = `# Views managed by Ghost (TUI keys: n new, e edit, d delete). Saving from the TUI
# rewrites this file, so comments are not kept; hand-edit only while Ghost is
# stopped. Views can also be defined in config.toml; those are read-only in the TUI.

`

// loadViewsFile reads views.toml; a missing file means no views.
func loadViewsFile(path string) ([]View, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var vf viewsFile
	md, err := toml.Decode(string(data), &vf)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("%s: unknown keys %s (only [[view]] tables with name, query, auto_review, agent and label belong here; written by a newer version of Ghost?)", path, strings.Join(keys, ", "))
	}
	for i := range vf.View {
		vf.View[i].Source = ViewsFileName
	}
	return vf.View, nil
}

// SaveViews replaces views.toml with views, atomically and durably: it
// writes a synced temp file next to the real file and renames it over it.
// A symlinked views.toml (dotfile managers) stays a symlink, and an existing
// file keeps its permissions.
func SaveViews(path string, views []View) error {
	var b bytes.Buffer
	b.WriteString(viewsHeader)
	if len(views) > 0 {
		enc := toml.NewEncoder(&b)
		enc.Indent = ""
		if err := enc.Encode(viewsFile{View: views}); err != nil {
			return err
		}
	}
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".views-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(b.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(target)); err == nil {
		d.Sync() // persist the rename
		d.Close()
	}
	return nil
}
