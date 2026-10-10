package theme

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinsLoadInOrder(t *testing.T) {
	r, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.IDs(), ","); got != strings.Join(BuiltinOrder, ",") {
		t.Errorf("ids = %s", got)
	}
	p, _ := r.Get("tokyo-night")
	if p.Name != "Tokyo Night" || p.Background != "#1a1b26" || p.Source != "built-in" || p.ID != "tokyo-night" {
		t.Errorf("tokyo-night = %+v", p)
	}
	if m, _ := r.Get("mono"); m.Muted != "" || m.Accent != "" {
		t.Errorf("mono should have no colors: %+v", m)
	}
}

func TestBuiltinsAreReadable(t *testing.T) {
	r, _ := Load("")
	for _, id := range r.IDs() {
		p, _ := r.Get(id)
		if probs := p.ReadabilityProblems(); len(probs) > 0 {
			t.Errorf("%s: %s", id, strings.Join(probs, "; "))
		}
	}
}

func writeTheme(t *testing.T, dir, id, body string) string {
	t.Helper()
	p := filepath.Join(dir, id+".toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUserThemesOverrideAndAppend(t *testing.T) {
	dir := t.TempDir()
	override := writeTheme(t, dir, "tokyo-night", "name = \"Mine TN\"\nmuted = \"#aaaaaa\"\n")
	writeTheme(t, dir, "zeta", "accent = \"5\"\n")
	writeTheme(t, dir, "alpha", "")
	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids := r.IDs()
	if len(ids) != len(BuiltinOrder)+2 || ids[len(ids)-2] != "alpha" || ids[len(ids)-1] != "zeta" {
		t.Errorf("ids = %v", ids)
	}
	if p, _ := r.Get("tokyo-night"); p.Name != "Mine TN" || p.Source != override || ids[4] != "tokyo-night" {
		t.Errorf("override = %+v at %v", p, ids)
	}
	if p, _ := r.Get("alpha"); p.Name != "alpha" {
		t.Errorf("name should default to the id: %+v", p)
	}
}

func TestMissingUserDirIsFine(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidThemeFileNamesFileAndRole(t *testing.T) {
	for body, want := range map[string]string{
		"muted = \"grey\"\n":     "muted",
		"accent = \"#12345\"\n":  "accent",
		"info = \"256\"\n":       "info",
		"colour = \"#ffffff\"\n": "colour",
		"muted = \n":             "",
	} {
		dir := t.TempDir()
		path := writeTheme(t, dir, "bad", body)
		_, err := Load(dir)
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v", body, err)
		}
	}
}

func TestCheckColor(t *testing.T) {
	for _, ok := range []string{"", "#000000", "#FFaa00", "0", "15", "255"} {
		if err := checkColor(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"red", "#fff", "#gggggg", "-1", "256", "1.5"} {
		if checkColor(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestContrast(t *testing.T) {
	if c, ok := Contrast("#ffffff", "#000000"); !ok || math.Abs(c-21) > 0.01 {
		t.Errorf("white/black = %v %v", c, ok)
	}
	if _, ok := Contrast("7", "#000000"); ok {
		t.Error("ANSI colors have no fixed contrast")
	}
}

func TestReadabilityProblemsFlagsDimMuted(t *testing.T) {
	p := Palette{Background: "#1a1b26", Foreground: "#c0caf5", Muted: "#3b4261"}
	if probs := p.ReadabilityProblems(); len(probs) != 1 || !strings.Contains(probs[0], "muted") {
		t.Errorf("problems = %v", probs)
	}
	if probs := (Palette{Muted: "#000000"}).ReadabilityProblems(); len(probs) != 0 {
		t.Error("themes without a background use the terminal's and are not checked")
	}
}
