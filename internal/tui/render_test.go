package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/engine"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/preflight"
)

func samplePR(n int, title string) github.PR {
	return github.PR{
		Repo: "o/r", Number: n, Title: title, URL: "https://github.com/o/r/pull/1",
		Author: "octocat", Body: "Body text here.", BaseRef: "main", HeadRef: "feat",
		HeadSHA: "abc1234def", CIState: "SUCCESS", ReviewDecision: "REVIEW_REQUIRED",
		Additions: 10, Deletions: 2, ChangedFiles: 3,
	}
}

func lines(s string) []string { return strings.Split(ansi.Strip(s), "\n") }

func TestFitTruncatesAndPads(t *testing.T) {
	if got := fit("hello", 8); got != "hello   " {
		t.Errorf("pad: %q", got)
	}
	if got := fit("hello world", 6); lipgloss.Width(got) != 6 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncate: %q", got)
	}
	if got := fit("x", 0); got != "" {
		t.Errorf("zero width: %q", got)
	}
}

func TestRenderFilterBar(t *testing.T) {
	views := []config.View{{Name: "Team"}, {Name: "Mine"}}
	out := ansi.Strip(testStyles.renderFilterBar(views, 1, map[string]int{"Team": 3}, 80))
	if !strings.Contains(out, "Team (3)") || !strings.Contains(out, "Mine (…)") {
		t.Errorf("filter bar = %q", out)
	}
	if lipgloss.Width(out) != 80 {
		t.Errorf("width = %d", lipgloss.Width(out))
	}
}

func TestRenderListScrollsToCursor(t *testing.T) {
	prs := []github.PR{samplePR(1, "one"), samplePR(2, "two"), samplePR(3, "three"), samplePR(4, "four")}
	ls := lines(testStyles.renderList(prs, 3, 50, 2, ""))
	if len(ls) != 2 || !strings.Contains(ls[0], "three") || !strings.Contains(ls[1], "four") {
		t.Errorf("list = %q", ls)
	}
	if !strings.Contains(ls[1], "o/r#4") || !strings.Contains(ls[1], "Ready") {
		t.Errorf("row missing repo/number/status: %q", ls[1])
	}
}

func TestRenderListEmpty(t *testing.T) {
	ls := lines(testStyles.renderList(nil, 0, 30, 3, "No pull requests"))
	if len(ls) != 3 || !strings.Contains(ls[0], "No pull requests") {
		t.Errorf("empty list = %q", ls)
	}
}

func TestRenderDetail(t *testing.T) {
	pr := samplePR(42, "Add retries")
	out := ansi.Strip(testStyles.renderDetail(&pr, engine.PRState{}, 60, 20))
	for _, want := range []string{"o/r#42", "Add retries", "@octocat", "+10 −2", "3 files", "Ready", "passing", "review required", "feat → main", "abc1234", "Audit trail", "Body text here."} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q:\n%s", want, out)
		}
	}
	if n := len(lines(testStyles.renderDetail(&pr, engine.PRState{}, 60, 5))); n != 5 {
		t.Errorf("detail not clipped to height: %d lines", n)
	}
	if !strings.Contains(ansi.Strip(testStyles.renderDetail(nil, engine.PRState{}, 40, 3)), "No pull request selected") {
		t.Error("nil PR message missing")
	}
}

func TestRenderStatusBar(t *testing.T) {
	si := statusInfo{
		SpendUSD: 0, MaxDailyUSD: 10, LastPoll: time.Date(2026, 10, 9, 10, 4, 0, 0, time.Local),
		Checks: []preflight.Result{{Name: "gh", OK: true}, {Name: "claude", OK: false}},
		Err:    "Team: rate limited",
	}
	out := ansi.Strip(testStyles.renderStatusBar(si, 200))
	for _, want := range []string{"Spend today $0.00 / $10.00", "Last poll 10:04", "gh ✓", "claude ✗", "Team: rate limited"} {
		if !strings.Contains(out, want) {
			t.Errorf("status bar missing %q: %q", want, out)
		}
	}
	if !strings.Contains(ansi.Strip(testStyles.renderStatusBar(statusInfo{MaxDailyUSD: 10}, 100)), "Last poll —") {
		t.Error("zero LastPoll should render as —")
	}
}

func TestRenderDetailNormalizesCRLF(t *testing.T) {
	pr := samplePR(1, "t")
	pr.Body = "line one here\r\nline two"
	raw := testStyles.renderDetail(&pr, engine.PRState{}, 60, 20)
	if strings.Contains(raw, "\r") {
		t.Error("carriage return reached the terminal")
	}
	out := ansi.Strip(raw)
	if !strings.Contains(out, "line one here") || !strings.Contains(out, "line two") {
		t.Errorf("body lines missing:\n%s", out)
	}
}

func TestRenderStripsControlAndEscapeSequences(t *testing.T) {
	pr := samplePR(1, "evil\r✓ Approved \x1b[8mhidden\x07\x9b")
	pr.Body = "x\x1bP tmux;\x1b\x1b]52;c;ZXZpbA==\x07\x1b\\ y\tz"
	pr.Author = "bad\nactor"
	for name, raw := range map[string]string{
		"row":    testStyles.renderRow(pr, 80, false),
		"detail": testStyles.renderDetail(&pr, engine.PRState{}, 80, 20),
	} {
		if strings.ContainsAny(raw, "\r\x07") || strings.Contains(raw, "\x9b") || strings.Contains(raw, "\u009b") || strings.Contains(raw, "\x1b[8m") || strings.Contains(raw, "\x1bP") || strings.Contains(raw, "\x1b]52") {
			t.Errorf("%s: control or escape sequence reached the terminal: %q", name, raw)
		}
	}
	out := ansi.Strip(testStyles.renderDetail(&pr, engine.PRState{}, 80, 20))
	if !strings.Contains(out, "@bad actor") || !strings.Contains(out, "y    z") {
		t.Errorf("single-line fields should fold newlines; tabs should expand:\n%s", out)
	}
}

func TestRenderStatusBarStripsControls(t *testing.T) {
	if raw := testStyles.renderStatusBar(statusInfo{Err: "boom\r\x1b]0;title\x07"}, 100); strings.ContainsAny(raw, "\r\x07") || strings.Contains(raw, "\x1b]0") {
		t.Errorf("status bar passed control chars: %q", raw)
	}
}
