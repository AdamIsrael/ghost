package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/AdamIsrael/ghost/internal/status"
	"github.com/AdamIsrael/ghost/internal/theme"
)

// styles is everything the TUI draws with, built from the active theme.
type styles struct {
	muted, border, heading, activeTab, selected     lipgloss.Style
	err, warning, success, attention, special, info lipgloss.Style

	selectionBg            color.Color // nil: selected row uses reverse video
	background, foreground color.Color // whole-screen colors; nil keeps the terminal's
}

func pick(colors ...string) string {
	for _, c := range colors {
		if c != "" {
			return c
		}
	}
	return ""
}

func newStyles(p theme.Palette) styles {
	base := lipgloss.NewStyle()
	fg := func(c string) lipgloss.Style {
		if c == "" {
			return base
		}
		return base.Foreground(lipgloss.Color(c))
	}
	s := styles{
		muted:     fg(p.Muted),
		border:    fg(pick(p.Border, p.Muted)),
		heading:   fg(pick(p.Heading, p.Accent)).Bold(true),
		err:       fg(p.Error),
		warning:   fg(p.Warning),
		success:   fg(p.Success),
		attention: fg(p.Attention),
		special:   fg(p.Special),
		info:      fg(p.Info),
	}
	if p.Accent != "" {
		s.activeTab = base.Bold(true).Background(lipgloss.Color(p.Accent))
		if p.OnAccent != "" {
			s.activeTab = s.activeTab.Foreground(lipgloss.Color(p.OnAccent))
		}
	} else {
		s.activeTab = base.Bold(true).Reverse(true)
	}
	if p.Selection != "" {
		s.selectionBg = lipgloss.Color(p.Selection)
		s.selected = base.Background(s.selectionBg)
	} else {
		s.selected = base.Reverse(true)
	}
	if p.Background != "" {
		s.background = lipgloss.Color(p.Background)
	}
	if p.Foreground != "" {
		s.foreground = lipgloss.Color(p.Foreground)
	}
	return s
}

// status returns the style for a PR status (see the spec's status → role).
func (s styles) status(st status.Status) lipgloss.Style {
	switch st {
	case status.Ready:
		return s.success
	case status.Draft:
		return s.muted
	case status.WaitingOnCI:
		return s.warning
	case status.CIFailing:
		return s.err
	case status.ChangesRequested:
		return s.attention
	case status.Approved:
		return s.special
	}
	return lipgloss.NewStyle()
}

// onSelection gives st the selected-row look, so colored segments of a
// selected row share one highlight.
func (s styles) onSelection(st lipgloss.Style) lipgloss.Style {
	if s.selectionBg != nil {
		return st.Background(s.selectionBg)
	}
	return st.Reverse(true)
}
