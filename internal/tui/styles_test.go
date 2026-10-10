package tui

import (
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/AdamIsrael/ghost/internal/theme"
)

func noColor(c color.Color) bool {
	if c == nil {
		return true
	}
	_, ok := c.(lipgloss.NoColor)
	return ok
}

func mustPalette(id string) theme.Palette {
	reg, err := theme.Load("")
	if err != nil {
		panic(err)
	}
	p, ok := reg.Get(id)
	if !ok {
		panic("no theme " + id)
	}
	return p
}

// testStyles renders helpers in tests; dark matches the app default.
var testStyles = newStyles(mustPalette("dark"))

func writeUserTheme(t *testing.T, dir, id, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, id+".toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDarkMutedIsReadable(t *testing.T) {
	if fg := newStyles(mustPalette("dark")).muted.GetForeground(); noColor(fg) || fg == lipgloss.Color("8") {
		t.Errorf("dark muted = %v; ANSI 8 is hard to read on dark backgrounds", fg)
	}
}

func TestMonoHasNoColors(t *testing.T) {
	s := newStyles(mustPalette("mono"))
	for name, st := range map[string]lipgloss.Style{"muted": s.muted, "err": s.err, "heading": s.heading, "selected": s.selected, "tab": s.activeTab, "success": s.success} {
		if !noColor(st.GetForeground()) || !noColor(st.GetBackground()) {
			t.Errorf("mono %s has a color", name)
		}
	}
}
