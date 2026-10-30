package tui

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AdamIsrael/ghost/internal/engine"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/store"
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

// fgSGR / bgSGR are the truecolor escape fragments lipgloss emits for a hex color.
func fgSGR(hex string) string { return "38;2;" + rgb(hex) }
func bgSGR(hex string) string { return "48;2;" + rgb(hex) }

func rgb(hex string) string {
	var r, g, b int
	fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
	return fmt.Sprintf("%d;%d;%d", r, g, b)
}

var tokyo = newStyles(mustPalette("tokyo-night"))

func prWith(st string) github.PR {
	pr := samplePR(1, "T")
	switch st {
	case "ready":
		pr.CIState = "SUCCESS"
	case "failing":
		pr.CIState = "FAILURE"
	case "draft":
		pr.IsDraft = true
	}
	return pr
}

func TestRowStatusUsesRoleColor(t *testing.T) {
	for st, hex := range map[string]string{"ready": "#9ece6a", "failing": "#f7768e", "draft": "#7b81a7"} {
		if row := tokyo.renderRow(prWith(st), 80, false); !strings.Contains(row, fgSGR(hex)) {
			t.Errorf("%s row missing %s: %q", st, hex, row)
		}
	}
}

func TestSelectedRowKeepsBackgroundAcrossSegments(t *testing.T) {
	row := tokyo.renderRow(prWith("ready"), 80, true)
	if strings.Count(row, bgSGR("#292e42")) < 2 {
		t.Errorf("both the status cell and the title need the selection background: %q", row)
	}
	if lipgloss.Width(row) != 80 {
		t.Errorf("width = %d", lipgloss.Width(row))
	}
	if !strings.Contains(ansi.Strip(row), "o/r#1 T") {
		t.Errorf("text = %q", ansi.Strip(row))
	}
}

func TestDetailColors(t *testing.T) {
	pr := prWith("ready")
	out := renderDetailNow(tokyo, &pr, engine.PRState{}, 80, 20)
	for name, hex := range map[string]string{"title": "#7aa2f7", "additions": "#9ece6a", "deletions": "#f7768e", "author": "#7dcfff", "url": "#7b81a7"} {
		if !strings.Contains(out, fgSGR(hex)) {
			t.Errorf("%s not colored %s", name, hex)
		}
	}
}

func TestStatusBarColors(t *testing.T) {
	out := tokyo.renderStatusBar(statusInfo{SpendUSD: 3.12, MaxDailyUSD: 10, Err: "boom", BudgetPaused: true}, 200)
	if !strings.Contains(out, fgSGR("#e0af68")) || !strings.Contains(out, fgSGR("#f7768e")) {
		t.Errorf("spend/budget should be warning, errors error: %q", out)
	}
}

func TestApprovalAndReviewLineColors(t *testing.T) {
	for state, hex := range map[string]string{
		store.ApprovalApproved: "#9ece6a", store.ApprovalWaiting: "#e0af68",
		store.ApprovalPending: "#bb9af7", store.ApprovalFailed: "#f7768e",
	} {
		if line := tokyo.approvalLine(&store.Review{Approval: state}); !strings.Contains(line, fgSGR(hex)) {
			t.Errorf("%s approval line not %s: %q", state, hex, line)
		}
	}
	ls := tokyo.reviewLines(engine.PRState{Latest: &store.Review{State: store.Ready, HeadSHA: "abc", Verdict: "approve"}}, "abc", 80)
	if !strings.Contains(strings.Join(ls, "\n"), fgSGR("#bb9af7")) {
		t.Error("pending review heading should be special")
	}
}

func TestDividerUsesBorderRole(t *testing.T) {
	m := send(t, New(Options{Views: twoViews, Theme: "tokyo-night"}), tea.WindowSizeMsg{Width: 120, Height: 10},
		result("Team", samplePR(1, "x")))
	if !strings.Contains(m.View().Content, fgSGR("#3b4261")) {
		t.Error("divider should use the border role")
	}
}

func TestSelectedRowStatusUsesForeground(t *testing.T) {
	plain := tokyo.renderRow(prWith("failing"), 80, false)
	sel := tokyo.renderRow(prWith("failing"), 80, true)
	if !strings.Contains(plain, fgSGR("#f7768e")) {
		t.Fatal("unselected row should use the status color")
	}
	if strings.Contains(sel, fgSGR("#f7768e")) {
		t.Error("selected row status must use the foreground: status colors aren't checked against the selection color")
	}
}

func TestNoColorIgnoresUserMonoOverride(t *testing.T) {
	dir := t.TempDir()
	writeUserTheme(t, dir, "mono", "success = \"#00ff00\"\n")
	reg, err := theme.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := New(Options{Themes: reg, NoColor: true})
	if !noColor(m.sty.success.GetForeground()) {
		t.Error("NO_COLOR must mean no color, even if a user theme overrides mono")
	}
}
