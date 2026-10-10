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

const viewsHeader = `# Views managed by Ghost. Edit them in the TUI (n new, e edit, d delete) or here.
# Views can also be defined in config.toml; those are read-only in the TUI.

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
		return nil, fmt.Errorf("%s: only [[view]] tables belong here; unknown keys: %s", path, strings.Join(keys, ", "))
	}
	for i := range vf.View {
		vf.View[i].Source = ViewsFileName
	}
	return vf.View, nil
}

// SaveViews replaces views.toml with views, atomically: it writes a temp
// file in the same directory and renames it over the old one.
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
	tmp, err := os.CreateTemp(filepath.Dir(path), ".views-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(b.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
