package tui

import "charm.land/lipgloss/v2"

// Theme holds every style the TUI draws with.
type Theme struct {
	Name      string
	Muted     lipgloss.Style // hints, URLs, empty states, dividers
	Error     lipgloss.Style
	Heading   lipgloss.Style
	Selected  lipgloss.Style // selected PR row
	ActiveTab lipgloss.Style // current view in the filter bar
}

// themeOrder is the order the t key cycles through. Keep in sync with
// config.KnownThemes (minus "auto"); TestThemesCoverConfig enforces it.
var themeOrder = []string{"dark", "light", "high-contrast", "mono"}

func themeByName(name string) (Theme, bool) {
	s := lipgloss.NewStyle()
	switch name {
	case "dark":
		// Muted is ANSI 7 (light gray), not 8: many dark palettes render 8
		// barely above the background.
		return Theme{name, s.Foreground(lipgloss.Color("7")), s.Foreground(lipgloss.Color("9")),
			s.Bold(true), s.Reverse(true), s.Bold(true).Reverse(true)}, true
	case "light":
		return Theme{name, s.Foreground(lipgloss.Color("240")), s.Foreground(lipgloss.Color("1")),
			s.Bold(true), s.Reverse(true), s.Bold(true).Reverse(true)}, true
	case "auto":
		// Provisional until the terminal reports its background: muted text
		// in the terminal's own foreground stays readable on dark and light.
		return Theme{name, s, s.Foreground(lipgloss.Color("1")),
			s.Bold(true), s.Reverse(true), s.Bold(true).Reverse(true)}, true
	case "high-contrast":
		// Muted uses the terminal's own foreground so it is readable on any
		// background; hierarchy comes from bold and underline instead.
		return Theme{name, s, s.Foreground(lipgloss.Color("9")).Bold(true),
			s.Bold(true).Underline(true), s.Bold(true).Reverse(true), s.Bold(true).Reverse(true).Underline(true)}, true
	case "mono":
		return Theme{name, s, s.Bold(true), s.Bold(true), s.Reverse(true), s.Bold(true).Reverse(true)}, true
	}
	return Theme{}, false
}

// apply points the package's render styles at t.
func (t Theme) apply() {
	dim, errText, bold, selected, activeTab = t.Muted, t.Error, t.Heading, t.Selected, t.ActiveTab
}

func init() {
	t, _ := themeByName("dark")
	t.apply()
}
