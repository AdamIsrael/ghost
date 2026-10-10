package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/poller"
)

var twoViews = []config.View{{Name: "Team", Query: "qa"}, {Name: "Mine", Query: "qb"}}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func send(t *testing.T, m tea.Model, msgs ...tea.Msg) tea.Model {
	t.Helper()
	for _, msg := range msgs {
		m, _ = m.Update(msg)
	}
	return m
}

func screen(m tea.Model) string { return ansi.Strip(m.View().Content) }

func result(view string, prs ...github.PR) resultMsg {
	return resultMsg(poller.Result{View: view, PRs: prs, At: time.Now()})
}

func TestShowsPolledPRsAndDetail(t *testing.T) {
	m := send(t, New(Options{Views: twoViews, MaxDailyCostUSD: 10}),
		tea.WindowSizeMsg{Width: 120, Height: 20},
		result("Team", samplePR(1, "First PR"), samplePR(2, "Second PR")),
	)
	out := screen(m)
	for _, want := range []string{"Team (2)", "Mine (…)", "First PR", "Second PR", "o/r#1  First PR", "Spend today $0.00 / $10.00"} {
		if !strings.Contains(out, want) {
			t.Errorf("screen missing %q:\n%s", want, out)
		}
	}
	m = send(t, m, key("j"))
	if !strings.Contains(screen(m), "o/r#2  Second PR") {
		t.Errorf("j did not move selection:\n%s", screen(m))
	}
	m = send(t, m, key("j"), key("k"))
	if !strings.Contains(screen(m), "o/r#1  First PR") {
		t.Error("k did not move selection up / j moved past end")
	}
}

func TestSwitchViews(t *testing.T) {
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 100, Height: 15}, result("Team", samplePR(1, "First PR")))
	m = send(t, m, key("right"))
	if out := screen(m); !strings.Contains(out, "Loading…") || strings.Contains(out, "First PR") {
		t.Errorf("right should show Mine (loading):\n%s", out)
	}
	m = send(t, m, key("right"))
	if !strings.Contains(screen(m), "First PR") {
		t.Error("right should wrap back to Team")
	}
	m = send(t, m, key("left"))
	if !strings.Contains(screen(m), "Loading…") {
		t.Error("left should wrap to Mine")
	}
}

func TestEmptyViewAndErrors(t *testing.T) {
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 120, Height: 15}, result("Team"))
	if !strings.Contains(screen(m), "No pull requests match this view") {
		t.Errorf("empty message missing:\n%s", screen(m))
	}
	m = send(t, m, resultMsg(poller.Result{View: "Team", Err: errors.New("rate limited"), At: time.Now()}))
	if !strings.Contains(screen(m), "Team: rate limited") {
		t.Errorf("error not shown:\n%s", screen(m))
	}
	m = send(t, m, result("Team", samplePR(1, "Back")))
	if strings.Contains(screen(m), "rate limited") {
		t.Error("error should clear after the view succeeds")
	}
}

func TestErrorKeepsPreviousPRs(t *testing.T) {
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 120, Height: 15},
		result("Team", samplePR(1, "Keep me")),
		resultMsg(poller.Result{View: "Team", Err: errors.New("boom"), At: time.Now()}))
	if !strings.Contains(screen(m), "Keep me") {
		t.Error("a failed poll should not erase the last good results")
	}
}

func TestCursorClampedWhenListShrinks(t *testing.T) {
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 120, Height: 15},
		result("Team", samplePR(1, "a"), samplePR(2, "b"), samplePR(3, "c")), key("j"), key("j"),
		result("Team", samplePR(9, "only")))
	if !strings.Contains(screen(m), "o/r#9  only") {
		t.Errorf("cursor not clamped:\n%s", screen(m))
	}
}

func TestOpenSelectedPR(t *testing.T) {
	var opened string
	m := send(t, New(Options{Views: twoViews, OpenURL: func(u string) error { opened = u; return nil }}),
		tea.WindowSizeMsg{Width: 120, Height: 15}, result("Team", samplePR(1, "x")))
	_, cmd := m.Update(key("o"))
	if cmd == nil {
		t.Fatal("o returned no command")
	}
	cmd()
	if opened != "https://github.com/o/r/pull/1" {
		t.Errorf("opened %q", opened)
	}
	_, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter returned no command")
	}
}

func TestRefreshAndQuit(t *testing.T) {
	polled := 0
	m := New(Options{Views: twoViews, PollNow: func() { polled++ }})
	m2, _ := m.Update(key("R"))
	if polled != 1 {
		t.Errorf("R called PollNow %d times", polled)
	}
	_, cmd := m2.Update(key("q"))
	if cmd == nil {
		t.Fatal("q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q should quit")
	}
}

func TestInitWaitsForResults(t *testing.T) {
	ch := make(chan poller.Result, 1)
	ch <- poller.Result{View: "Team", At: time.Now()}
	cmd := New(Options{Views: twoViews, Results: ch}).Init()
	if cmd == nil {
		t.Fatal("Init should wait on the results channel")
	}
	if msg, ok := cmd().(resultMsg); !ok || msg.View != "Team" {
		t.Errorf("Init cmd returned %#v", msg)
	}
	if New(Options{Views: twoViews}).Init() != nil {
		t.Error("Init with no results channel should return nil")
	}
}

func TestPollDisabledMessage(t *testing.T) {
	m := send(t, New(Options{Views: twoViews, PollDisabled: "GitHub unavailable: not logged in"}), tea.WindowSizeMsg{Width: 120, Height: 10})
	if !strings.Contains(screen(m), "GitHub unavailable: not logged in") {
		t.Errorf("disabled reason missing:\n%s", screen(m))
	}
}

func TestNoViews(t *testing.T) {
	m := send(t, New(Options{}), tea.WindowSizeMsg{Width: 100, Height: 10}, key("right"), key("left"), key("j"), key("o"))
	if !strings.Contains(screen(m), "No views configured") {
		t.Errorf("no-views hint missing:\n%s", screen(m))
	}
}

func TestNarrowTerminal(t *testing.T) {
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 40, Height: 4}, result("Team", samplePR(1, "Tiny")))
	out := screen(m)
	if !strings.Contains(out, "Tiny") {
		t.Errorf("list missing in narrow layout:\n%s", out)
	}
	m = send(t, m, tea.WindowSizeMsg{Width: 5, Height: 1})
	screen(m) // must not panic
}

func TestUsesAltScreen(t *testing.T) {
	if !New(Options{}).View().AltScreen {
		t.Error("View should request the alternate screen")
	}
}

func TestPartialResultShowsPRsAndWarning(t *testing.T) {
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 160, Height: 15},
		resultMsg(poller.Result{View: "Team", PRs: []github.PR{samplePR(1, "Visible")}, Err: errors.New("6 PRs hidden: SAML"), At: time.Now()}))
	out := screen(m)
	if !strings.Contains(out, "Visible") || !strings.Contains(out, "Team: 6 PRs hidden") {
		t.Errorf("want visible PR and hidden-PR warning:\n%s", out)
	}
}

func TestStatusErrorPrefersTotalFailures(t *testing.T) {
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 200, Height: 10},
		resultMsg(poller.Result{View: "Team", PRs: []github.PR{samplePR(1, "x")}, Err: &github.PartialError{Hidden: 6, Reason: "SAML"}, At: time.Now()}),
		resultMsg(poller.Result{View: "Mine", Err: errors.New("HTTP 502"), At: time.Now()}))
	if out := screen(m); !strings.Contains(out, "Mine: HTTP 502") {
		t.Errorf("a total failure should outrank a partial warning:\n%s", out)
	}
}
