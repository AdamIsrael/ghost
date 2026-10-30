package tui

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AdamIsrael/ghost/internal/engine"
	"github.com/AdamIsrael/ghost/internal/prbody"
)

// settleBodies delivers every description render the model has started, as
// the runtime would once the background commands finish.
func settleBodies(m tea.Model) tea.Model {
	for {
		mm := m.(Model)
		if len(mm.rendering) == 0 {
			return m
		}
		for k := range mm.rendering {
			m, _ = m.Update(bodyMsg{k, prbody.Render(clean(k.body), k.width)})
			break
		}
	}
}

// runBodyCmd runs cmd (possibly a batch) and returns the bodyMsg it yields.
func runBodyCmd(t *testing.T, cmd tea.Cmd) (bodyMsg, bool) {
	t.Helper()
	if cmd == nil {
		return bodyMsg{}, false
	}
	switch msg := cmd().(type) {
	case bodyMsg:
		return msg, true
	case tea.BatchMsg:
		for _, c := range msg {
			if b, ok := runBodyCmd(t, c); ok {
				return b, true
			}
		}
	}
	return bodyMsg{}, false
}

// GitHub #20: the description renders in the background, then shows
// formatted, with <details> collapsed.
func TestDescriptionRendersInBackground(t *testing.T) {
	raw, err := os.ReadFile("../prbody/testdata/dependabot.md")
	if err != nil {
		t.Fatal(err)
	}
	pr := samplePR(1, "Bump zizmor")
	pr.Body = string(raw)
	m, _ := tea.Model(New(Options{Views: twoViews, MaxDailyCostUSD: 10})).Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	m, cmd := m.Update(result("Team", pr))
	if got := lineAfter(screen(m), "── Description ──"); got != "…" {
		t.Errorf("line under the heading while rendering = %q, want the placeholder", got)
	}
	msg, ok := runBodyCmd(t, cmd)
	if !ok {
		t.Fatal("selecting a PR started no description render")
	}
	if _, again := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}); again != nil {
		if _, ok := runBodyCmd(t, again); ok {
			t.Error("a render already under way was started again")
		}
	}
	m, _ = m.Update(msg)
	out := screen(m)
	for _, want := range []string{"Bumps zizmorcore/zizmor-action from 0.6.3 to 0.8.0.", "▸ Release notes", "▸ Commits", "▸ Dependabot commands and options"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"<details>", "<summary>", "](https://", "Sourced from", "dependabot-automerge"} {
		if strings.Contains(out, bad) {
			t.Errorf("raw or collapsed text %q shown:\n%s", bad, out)
		}
	}

	// Cached: no new render for the same body and width; a resize renders again.
	if _, c := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}); c != nil {
		if _, ok := runBodyCmd(t, c); ok {
			t.Error("a cached description was rendered again")
		}
	}
	if _, c := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40}); c == nil {
		t.Error("a new width started no render")
	} else if _, ok := runBodyCmd(t, c); !ok {
		t.Error("a new width started no render")
	}
}

func TestDescriptionCacheIsBounded(t *testing.T) {
	m := New(Options{Views: twoViews})
	for i := 0; i < maxBodies+5; i++ {
		next, _ := m.Update(bodyMsg{bodyKey{strings.Repeat("x", i), 80}, nil})
		m = next.(Model)
	}
	if len(m.bodies) > maxBodies {
		t.Errorf("cache holds %d descriptions, max %d", len(m.bodies), maxBodies)
	}
}

// lineAfter returns the detail pane's text on the line below the one
// containing marker.
func lineAfter(screen, marker string) string {
	lines := strings.Split(screen, "\n")
	for i, l := range lines {
		if strings.Contains(l, marker) && i+1 < len(lines) {
			next := lines[i+1]
			if j := strings.LastIndex(next, "│"); j >= 0 {
				next = next[j+len("│"):]
			}
			return strings.TrimSpace(next)
		}
	}
	return ""
}

// Entities can decode to control characters; they must not reach the terminal.
func TestDescriptionStripsDecodedControls(t *testing.T) {
	pr := samplePR(1, "t")
	pr.Body = "a&#27;[8mhidden &#x1b;]52;c;eA==&#7; <b>&#27;[31m</b>red"
	raw := renderDetailNow(testStyles, &pr, engine.PRState{}, 80, 20)
	if strings.ContainsAny(raw, "\x07") || strings.Contains(raw, "\x1b[8m") || strings.Contains(raw, "\x1b]52") || strings.Contains(raw, "\x1b[31mred") {
		t.Errorf("decoded control reached the terminal: %q", raw)
	}
	if !strings.Contains(ansi.Strip(raw), "hidden") {
		t.Errorf("text lost:\n%s", ansi.Strip(raw))
	}
}
