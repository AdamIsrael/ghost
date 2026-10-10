package tui

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

)

func noColor(c color.Color) bool {
	if c == nil {
		return true
	}
	_, ok := c.(lipgloss.NoColor)
	return ok
}

func TestDarkThemeMutedIsReadable(t *testing.T) {
	th, _ := themeByName("dark")
	if fg := th.Muted.GetForeground(); noColor(fg) || fg == lipgloss.Color("8") {
		t.Errorf("dark muted foreground = %v; ANSI 8 is hard to read on dark backgrounds", fg)
	}
}

func TestMonoHasNoColors(t *testing.T) {
	th, _ := themeByName("mono")
	for name, st := range map[string]lipgloss.Style{"muted": th.Muted, "error": th.Error, "heading": th.Heading, "selected": th.Selected, "tab": th.ActiveTab} {
		if !noColor(st.GetForeground()) || !noColor(st.GetBackground()) {
			t.Errorf("mono %s has a color", name)
		}
	}
}

func themeOf(m tea.Model) string { return m.(Model).theme.Name }

func TestThemeFromOptions(t *testing.T) {
	if got := themeOf(New(Options{Theme: "high-contrast"})); got != "high-contrast" {
		t.Errorf("theme = %q", got)
	}
	if got := themeOf(New(Options{Theme: "light", NoColor: true})); got != "mono" {
		t.Errorf("NO_COLOR must force mono, got %q", got)
	}
	if got := themeOf(New(Options{})); got != "dark" {
		t.Errorf("default theme = %q, want dark", got)
	}
}

// keepDefaultTheme restores the package styles after a test that renders.
func keepDefaultTheme(t *testing.T) {
	t.Cleanup(func() {
		th, _ := themeByName("dark")
		th.apply()
	})
}

func TestAutoThemeFollowsBackground(t *testing.T) {
	m := tea.Model(New(Options{Theme: "auto"}))
	if themeOf(m) != "auto" || !noColor(m.(Model).theme.Muted.GetForeground()) {
		t.Fatalf("until the terminal answers, auto must be readable on any background: %q %v",
			themeOf(m), m.(Model).theme.Muted.GetForeground())
	}
	m, _ = m.Update(tea.BackgroundColorMsg{Color: color.White})
	if themeOf(m) != "light" {
		t.Fatalf("light background → %q", themeOf(m))
	}
	m, _ = m.Update(tea.BackgroundColorMsg{Color: color.Black})
	if themeOf(m) != "dark" {
		t.Fatalf("dark background → %q", themeOf(m))
	}
	fixed := tea.Model(New(Options{Theme: "high-contrast"}))
	fixed, _ = fixed.Update(tea.BackgroundColorMsg{Color: color.White})
	if themeOf(fixed) != "high-contrast" {
		t.Error("an explicit theme must ignore the terminal background")
	}
}

func TestThemeKeyCycles(t *testing.T) {
	keepDefaultTheme(t)
	m := send(t, New(Options{Views: twoViews, Theme: "dark", ConfigPath: "/home/me/.config/ghost/config.toml"}), tea.WindowSizeMsg{Width: 200, Height: 10})
	m = send(t, m, key("t"))
	if themeOf(m) != "light" || !strings.Contains(screen(m), `theme = "light"`) || !strings.Contains(screen(m), "/home/me/.config/ghost/config.toml") {
		t.Fatalf("theme %q screen:\n%s", themeOf(m), screen(m))
	}
	for i := 0; i < 3; i++ {
		m = send(t, m, key("t"))
	}
	if themeOf(m) != "dark" {
		t.Errorf("cycle should wrap to dark, got %q", themeOf(m))
	}
	m, _ = m.Update(tea.BackgroundColorMsg{Color: color.White})
	if themeOf(m) != "dark" {
		t.Error("after t, the background must not override the chosen theme")
	}
}

func TestThemeChangesRenderedOutput(t *testing.T) {
	keepDefaultTheme(t)
	dark := send(t, New(Options{Views: twoViews, Theme: "dark"}), tea.WindowSizeMsg{Width: 120, Height: 10}).View().Content
	mono := send(t, New(Options{Views: twoViews, Theme: "mono"}), tea.WindowSizeMsg{Width: 120, Height: 10}).View().Content
	if dark == mono {
		t.Error("themes should render differently")
	}
}

func TestHighContrastMutedReadableOnAnyBackground(t *testing.T) {
	th, _ := themeByName("high-contrast")
	if !noColor(th.Muted.GetForeground()) {
		t.Errorf("high-contrast muted must use the terminal's own foreground, got %v", th.Muted.GetForeground())
	}
}

func TestThemeKeyUnderNoColor(t *testing.T) {
	keepDefaultTheme(t)
	m := send(t, New(Options{Views: twoViews, NoColor: true}), tea.WindowSizeMsg{Width: 160, Height: 10}, key("t"))
	if themeOf(m) != "mono" || !strings.Contains(screen(m), "NO_COLOR") {
		t.Errorf("theme %q screen:\n%s", themeOf(m), screen(m))
	}
}
