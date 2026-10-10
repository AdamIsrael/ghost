// Package textsafe makes untrusted text (PR titles, bodies, agent output)
// safe to print to a terminal.
package textsafe

import (
	"strings"
	"unicode/utf8"
)

// Clean normalizes CRLF to LF, expands tabs, and drops every other control
// character — including ESC, so the text cannot inject terminal escape
// sequences, and CR, which would redraw from the line start.
func Clean(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f, r == utf8.RuneError:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Line is Clean for single-line fields: newlines become spaces.
func Line(s string) string { return strings.ReplaceAll(Clean(s), "\n", " ") }
