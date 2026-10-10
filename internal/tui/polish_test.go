package tui

import (
	"errors"
	"fmt"
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
	out := ansi.Strip(testStyles.renderFilterBar(views, 15, nil, 80))
	if !strings.Contains(out, "view-number-15") || !strings.Contains(out, "‹") || !strings.Contains(out, "›") {
		t.Errorf("active tab hidden or no scroll markers: %q", out)
	}
	if few := ansi.Strip(testStyles.renderFilterBar(views[:2], 1, nil, 120)); strings.ContainsAny(few, "‹›") {
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
