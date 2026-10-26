package tui

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/engine"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/poller"
)

func TestLastPollOnlyOnSuccess(t *testing.T) {
	ok := time.Date(2026, 10, 10, 9, 15, 0, 0, time.Local)
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 160, Height: 10},
		resultMsg(poller.Result{View: "Team", PRs: []github.PR{samplePR(1, "x")}, At: ok}),
		resultMsg(poller.Result{View: "Team", Err: errors.New("HTTP 502"), At: ok.Add(10 * time.Minute)}))
	if out := screen(m); !strings.Contains(out, "Last poll 09:15") {
		t.Errorf("a failed poll must not update Last poll:\n%s", out)
	}
}

func TestFilterBarKeepsActiveTabVisible(t *testing.T) {
	var views []config.View
	for i := 0; i < 20; i++ {
		views = append(views, config.View{Name: fmt.Sprintf("view-number-%02d", i)})
	}
	out := ansi.Strip(testStyles.renderFilterBar(views, 15, nil, nil, 80))
	if !strings.Contains(out, "view-number-15") || !strings.Contains(out, "‹") || !strings.Contains(out, "›") {
		t.Errorf("active tab hidden or no scroll markers: %q", out)
	}
	if few := ansi.Strip(testStyles.renderFilterBar(views[:2], 1, nil, nil, 120)); strings.ContainsAny(few, "‹›") {
		t.Errorf("no markers when everything fits: %q", few)
	}
}

func TestRefreshWhenPollingDisabled(t *testing.T) {
	m := send(t, New(Options{Views: twoViews, PollDisabled: "GitHub unavailable: not logged in"}),
		tea.WindowSizeMsg{Width: 160, Height: 10}, key("R"))
	if !strings.Contains(screen(m), "⚠ GitHub unavailable") {
		t.Errorf("R should explain why polling is off:\n%s", screen(m))
	}
}

func TestLongBodyShowsMoreMarker(t *testing.T) {
	pr := samplePR(1, "t")
	var body []string
	for i := 0; i < 50; i++ {
		body = append(body, fmt.Sprintf("line %d", i))
	}
	pr.Body = strings.Join(body, "\n")
	out := ansi.Strip(testStyles.renderDetail(&pr, engine.PRState{}, 80, 20))
	if !strings.Contains(out, "more lines (o opens the PR)") {
		t.Errorf("clipped body needs a marker:\n%s", out)
	}
	if n := len(strings.Split(out, "\n")); n != 20 {
		t.Errorf("height = %d", n)
	}
	pr.Body = "short"
	if strings.Contains(ansi.Strip(testStyles.renderDetail(&pr, engine.PRState{}, 80, 20)), "more lines") {
		t.Error("no marker when the body fits")
	}
}

func TestCtrlCQuitsInsideForm(t *testing.T) {
	m := sendKeys(viewsModel(t, &viewSaver{}, managed), key("n"))
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c in the form returned no command")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Error("ctrl+c must quit even inside the form")
	}
}

func TestDeletedViewCountsCleared(t *testing.T) {
	s := &viewSaver{}
	m := send(t, New(Options{Views: managed, SaveViews: s.save, Agents: []string{"claude"}}),
		tea.WindowSizeMsg{Width: 140, Height: 20},
		result("Team", samplePR(1, "a"), samplePR(2, "b")))
	m = sendKeys(m, key("l"), key("d"), key("y")) // delete Team
	m = sendKeys(m, key("n"))
	keys := append(typed("Team"), tab)
	keys = append(keys, typed("other")...)
	m = sendKeys(m, append(keys, enter)...)
	if out := screen(m); strings.Contains(out, "Team (2)") || !strings.Contains(out, "Team (…)") {
		t.Errorf("a re-created view must not show the old view's counts:\n%s", out)
	}
}

func TestTruncatedResultShowsPlus(t *testing.T) {
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 160, Height: 10},
		resultMsg(poller.Result{View: "Team", PRs: []github.PR{samplePR(1, "x"), samplePR(2, "y")}, At: time.Now(), Truncated: true}))
	if out := screen(m); !strings.Contains(out, "Team (2+)") {
		t.Errorf("truncated view should show 2+:\n%s", out)
	}
	// A failed poll keeps the last state.
	m = send(t, m, resultMsg(poller.Result{View: "Team", Err: errors.New("HTTP 502"), At: time.Now()}))
	if out := screen(m); !strings.Contains(out, "Team (2+)") {
		t.Errorf("a failed poll should keep 2+:\n%s", out)
	}
	// A later complete result clears it.
	m = send(t, m, resultMsg(poller.Result{View: "Team", PRs: []github.PR{samplePR(1, "x")}, At: time.Now()}))
	if out := screen(m); !strings.Contains(out, "Team (1)") || strings.Contains(out, "Team (1+)") {
		t.Errorf("complete result should clear the +:\n%s", out)
	}
}

func TestBlockedSearchExplainsEmptyView(t *testing.T) {
	why := &github.SearchBlockedError{Reason: "Access to the requested organization (acme) is blocked by SAML single sign-on.", SAML: true}
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 200, Height: 12},
		resultMsg(poller.Result{View: "Team", PRs: []github.PR{}, Err: why, At: time.Now()}))
	out := screen(m)
	if strings.Contains(out, "No pull requests match this view") || !strings.Contains(out, "blocked by SAML") {
		t.Errorf("empty view should say why:\n%s", out)
	}
	// The narrow list pane wraps the message so the fix stays visible.
	m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 16})
	if out := screen(m); !strings.Contains(out, "gh auth refresh") {
		t.Errorf("the fix hint should be visible in a narrow pane:\n%s", out)
	}
}

func TestBlockedViewIsAWarningInStatusBar(t *testing.T) {
	why := &github.SearchBlockedError{Reason: "Access to the requested organization (acme) is blocked by SAML single sign-on.", SAML: true}
	m := send(t, New(Options{Views: twoViews}), tea.WindowSizeMsg{Width: 200, Height: 12},
		resultMsg(poller.Result{View: twoViews[0].Name, PRs: []github.PR{}, Err: why, At: time.Now()}),
		resultMsg(poller.Result{View: twoViews[1].Name, Err: errors.New("HTTP 502"), At: time.Now()}))
	if out := screen(m); !strings.Contains(out, twoViews[1].Name+": HTTP 502") {
		t.Errorf("a real failure in another view must outrank a blocked view:\n%s", out)
	}
}

func logModel(t *testing.T) (tea.Model, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	m := send(t, New(Options{Views: twoViews, Log: slog.New(slog.NewTextHandler(&buf, nil))}), tea.WindowSizeMsg{Width: 160, Height: 10})
	return m, &buf
}

func TestPollErrorsAreLoggedOnChange(t *testing.T) {
	m, buf := logModel(t)
	team := twoViews[0].Name
	e502 := errors.New("search: HTTP 502: 502 Bad Gateway (https://api.github.com/graphql) and a lot more text the status bar cuts off")
	m = send(t, m,
		resultMsg(poller.Result{View: team, Err: e502, At: time.Now()}),
		resultMsg(poller.Result{View: team, Err: e502, At: time.Now()}), // same again: not re-logged
		resultMsg(poller.Result{View: team, Err: errors.New("search: HTTP 503"), At: time.Now()}),
		resultMsg(poller.Result{View: team, PRs: []github.PR{samplePR(1, "x")}, At: time.Now()}),
		resultMsg(poller.Result{View: team, PRs: []github.PR{samplePR(1, "x")}, At: time.Now()}), // healthy: nothing
	)
	out := buf.String()
	if n := strings.Count(out, "HTTP 502"); n != 1 {
		t.Errorf("the same error was logged %d times, want 1:\n%s", n, out)
	}
	for _, want := range []string{
		`level=ERROR msg="poll failed" view=` + team,
		"a lot more text the status bar cuts off",
		"HTTP 503",
		`level=INFO msg="poll recovered" view=` + team,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "poll recovered"); n != 1 {
		t.Errorf("recovered logged %d times, want 1", n)
	}
	_ = m
}

func TestPollWarningIsLoggedAsWarn(t *testing.T) {
	m, buf := logModel(t)
	send(t, m, resultMsg(poller.Result{View: twoViews[0].Name, PRs: []github.PR{}, Err: &github.SearchBlockedError{Reason: "blocked by SAML"}, At: time.Now()}))
	if out := buf.String(); !strings.Contains(out, `level=WARN msg="poll warning"`) || !strings.Contains(out, "blocked by SAML") {
		t.Errorf("log:\n%s", out)
	}
}

func TestActionErrorsAreLogged(t *testing.T) {
	m, buf := logModel(t)
	send(t, m,
		engineDoneMsg{what: "post", pr: "o/r#42", err: errors.New("HTTP 422: a long validation message")},
		openedMsg{err: errors.New("exec: open: not found")})
	out := buf.String()
	for _, want := range []string{`level=ERROR msg="post failed" pr=o/r#42`, "a long validation message", `level=ERROR msg="open failed"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
}

func TestDeletedViewResultIsNotLogged(t *testing.T) {
	m, buf := logModel(t)
	send(t, m, resultMsg(poller.Result{View: "Gone", Err: errors.New("HTTP 502"), At: time.Now()}))
	if out := buf.String(); strings.Contains(out, "Gone") {
		t.Errorf("logged a result for a view that no longer exists:\n%s", out)
	}
}
