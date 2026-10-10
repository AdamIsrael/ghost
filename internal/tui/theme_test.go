package tui

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/AdamIsrael/ghost/internal/theme"
)

func themeOf(m tea.Model) string { return m.(Model).palette.ID }

func TestThemeFromOptions(t *testing.T) {
	cases := map[string]Options{
		"tokyo-night": {Theme: "tokyo-night"},
		"mono":        {Theme: "light", NoColor: true},
		"dark":        {},
		"auto":        {Theme: "auto"},
	}
	for want, o := range cases {
		if got := themeOf(New(o)); got != want {
			t.Errorf("%+v → %q, want %q", o, got, want)
		}
	}
	if m := New(Options{Theme: "auto"}); m.sty.muted.GetForeground() != nil && !noColor(m.sty.muted.GetForeground()) {
		t.Error("auto must stay neutral until the terminal answers")
	}
}

func TestAutoThemeFollowsBackground(t *testing.T) {
	m := tea.Model(New(Options{Theme: "auto"}))
	m, _ = m.Update(tea.BackgroundColorMsg{Color: color.White})
	if themeOf(m) != "light" {
		t.Fatalf("light background → %q", themeOf(m))
	}
	m, _ = m.Update(tea.BackgroundColorMsg{Color: color.Black})
	if themeOf(m) != "dark" {
		t.Fatalf("dark background → %q", themeOf(m))
	}
	fixed := tea.Model(New(Options{Theme: "nord"}))
	fixed, _ = fixed.Update(tea.BackgroundColorMsg{Color: color.White})
	if themeOf(fixed) != "nord" {
		t.Error("an explicit theme must ignore the terminal background")
	}
}

func TestThemeKeyCyclesAllThemes(t *testing.T) {
	dir := t.TempDir()
	writeUserTheme(t, dir, "zz-mine", "accent = \"5\"\n")
	reg, err := theme.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := send(t, New(Options{Views: twoViews, Theme: "solarized-light", Themes: reg, ConfigPath: "/c/config.toml"}),
		tea.WindowSizeMsg{Width: 220, Height: 10})
	m = send(t, m, key("t"))
	if themeOf(m) != "zz-mine" || !strings.Contains(screen(m), `theme = "zz-mine"`) || !strings.Contains(screen(m), "/c/config.toml") {
		t.Fatalf("after solarized-light: %q\n%s", themeOf(m), screen(m))
	}
	m = send(t, m, key("t"))
	if themeOf(m) != "dark" {
		t.Errorf("cycle should wrap to dark, got %q", themeOf(m))
	}
	m, _ = m.Update(tea.BackgroundColorMsg{Color: color.White})
	if themeOf(m) != "dark" {
		t.Error("after t, the background must not override the chosen theme")
	}
}

func TestThemeKeyUnderNoColor(t *testing.T) {
	m := send(t, New(Options{Views: twoViews, NoColor: true}), tea.WindowSizeMsg{Width: 160, Height: 10}, key("t"))
	if themeOf(m) != "mono" || !strings.Contains(screen(m), "NO_COLOR") {
		t.Errorf("theme %q screen:\n%s", themeOf(m), screen(m))
	}
}

func TestThemeSetsViewColors(t *testing.T) {
	v := send(t, New(Options{Views: twoViews, Theme: "tokyo-night"}), tea.WindowSizeMsg{Width: 120, Height: 10}).View()
	if v.BackgroundColor == nil || v.ForegroundColor == nil {
		t.Error("a theme with a background must set the view colors")
	}
	v = send(t, New(Options{Views: twoViews, Theme: "dark"}), tea.WindowSizeMsg{Width: 120, Height: 10}).View()
	if v.BackgroundColor != nil || v.ForegroundColor != nil {
		t.Error("a terminal-palette theme must leave the terminal's colors alone")
	}
}

func TestThemesRenderDifferently(t *testing.T) {
	a := send(t, New(Options{Views: twoViews, Theme: "dark"}), tea.WindowSizeMsg{Width: 120, Height: 10}).View().Content
	b := send(t, New(Options{Views: twoViews, Theme: "mono"}), tea.WindowSizeMsg{Width: 120, Height: 10}).View().Content
	if a == b {
		t.Error("themes should render differently")
	}
}
