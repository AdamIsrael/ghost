package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/poller"
)

type viewSaver struct {
	saved [][]config.View
	err   error
}

func (s *viewSaver) save(v []config.View) error {
	if s.err != nil {
		return s.err
	}
	s.saved = append(s.saved, append([]config.View(nil), v...))
	return nil
}

func viewsModel(t *testing.T, s *viewSaver, views []config.View) tea.Model {
	t.Helper()
	return send(t, New(Options{Views: views, SaveViews: s.save, Agents: []string{"claude"}}),
		tea.WindowSizeMsg{Width: 140, Height: 20})
}

func sendKeys(m tea.Model, keys ...tea.KeyPressMsg) tea.Model {
	for _, k := range keys {
		m, _ = m.Update(k)
	}
	return m
}

var managed = []config.View{
	{Name: "Pinned", Query: "q0", Source: "config.toml"},
	{Name: "Team", Query: "q1", Source: "views.toml"},
}

func TestNewView(t *testing.T) {
	s := &viewSaver{}
	m := viewsModel(t, s, managed)
	m = sendKeys(m, key("n"))
	if !strings.Contains(screen(m), "New view") {
		t.Fatalf("form not shown:\n%s", screen(m))
	}
	keys := append(typed("Mine"), tab)
	keys = append(keys, typed("is:pr author:@me")...)
	m = sendKeys(m, append(keys, enter)...)
	if len(s.saved) != 1 || len(s.saved[0]) != 3 {
		t.Fatalf("saved = %+v", s.saved)
	}
	if got := s.saved[0][2]; got.Name != "Mine" || got.Query != "is:pr author:@me" || got.Source != "views.toml" {
		t.Errorf("new view = %+v", got)
	}
	out := screen(m)
	if strings.Contains(out, "New view") || !strings.Contains(out, "Mine (…)") {
		t.Errorf("form should close and the new view be selected:\n%s", out)
	}
}

func TestEditView(t *testing.T) {
	s := &viewSaver{}
	m := sendKeys(viewsModel(t, s, managed), key("l"), key("e")) // select Team
	if !strings.Contains(screen(m), `Edit view "Team"`) {
		t.Fatalf("edit form not shown:\n%s", screen(m))
	}
	m = sendKeys(m, tab, tea.KeyPressMsg{Code: tea.KeyEnd})
	m = sendKeys(m, append(typed(" is:open"), enter)...)
	if len(s.saved) != 1 || s.saved[0][1].Query != "q1 is:open" || s.saved[0][1].Name != "Team" || len(s.saved[0]) != 2 {
		t.Fatalf("saved = %+v", s.saved)
	}
}

func TestConfigViewIsReadOnly(t *testing.T) {
	s := &viewSaver{}
	m := sendKeys(viewsModel(t, s, managed), key("e")) // Pinned (config.toml)
	if strings.Contains(screen(m), "Edit view") || !strings.Contains(screen(m), "defined in config.toml") {
		t.Fatalf("config.toml view must be read-only:\n%s", screen(m))
	}
	m = sendKeys(m, key("d"))
	if strings.Contains(screen(m), "Delete view") || len(s.saved) != 0 {
		t.Fatal("config.toml view must not be deletable")
	}
}

func TestDeleteViewConfirm(t *testing.T) {
	s := &viewSaver{}
	m := sendKeys(viewsModel(t, s, managed), key("l"), key("d"))
	if !strings.Contains(screen(m), `Delete view "Team"? y/n`) {
		t.Fatalf("no confirmation:\n%s", screen(m))
	}
	m = sendKeys(m, key("x")) // anything but y cancels
	if len(s.saved) != 0 || strings.Contains(screen(m), "Delete view") {
		t.Fatal("non-y must cancel")
	}
	m = sendKeys(m, key("d"), key("y"))
	if len(s.saved) != 1 || len(s.saved[0]) != 1 || s.saved[0][0].Name != "Pinned" {
		t.Fatalf("saved = %+v", s.saved)
	}
	if out := screen(m); strings.Contains(out, "Team (") || !strings.Contains(out, "Pinned") {
		t.Errorf("selection after delete:\n%s", out)
	}
}

func TestFormShowsSaveError(t *testing.T) {
	s := &viewSaver{err: errors.New(`view "X": query is required`)}
	m := sendKeys(viewsModel(t, s, managed), key("n"))
	m = sendKeys(m, append(typed("X"), enter)...)
	out := screen(m)
	if !strings.Contains(out, "New view") || !strings.Contains(out, "query is required") {
		t.Fatalf("form should stay open with the error:\n%s", out)
	}
	if strings.Contains(out, "X (") {
		t.Error("failed save must not add the view")
	}
}

func TestFormEscCancelsInModel(t *testing.T) {
	s := &viewSaver{}
	m := sendKeys(viewsModel(t, s, managed), key("n"))
	m = sendKeys(m, append(typed("Q"), esc)...)
	if strings.Contains(screen(m), "New view") || len(s.saved) != 0 {
		t.Fatal("esc should close the form without saving")
	}
	// q typed into the form must not quit
	m = sendKeys(m, key("n"))
	_, cmd := m.Update(key("q"))
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("q inside the form must type, not quit")
		}
	}
}

func TestEditingUnavailableWithoutSaver(t *testing.T) {
	m := send(t, New(Options{Views: managed}), tea.WindowSizeMsg{Width: 140, Height: 20}, key("n"))
	if strings.Contains(screen(m), "New view") || !strings.Contains(screen(m), "unavailable") {
		t.Fatalf("got:\n%s", screen(m))
	}
}

func TestTUIIgnoresResultFromOldQuery(t *testing.T) {
	m := send(t, New(Options{Views: []config.View{{Name: "Team", Query: "narrow"}}}), tea.WindowSizeMsg{Width: 140, Height: 20},
		resultMsg(poller.Result{View: "Team", Query: "wide", PRs: []github.PR{samplePR(1, "Stale")}}))
	if strings.Contains(screen(m), "Stale") {
		t.Error("result from an old query shown")
	}
}

func TestPasteIntoForm(t *testing.T) {
	s := &viewSaver{}
	m := sendKeys(viewsModel(t, s, managed), key("n"))
	m, _ = m.Update(tea.PasteMsg{Content: "Pasted name"})
	m = sendKeys(m, tab)
	m, _ = m.Update(tea.PasteMsg{Content: "repo:o/r is:open"})
	m = sendKeys(m, enter)
	if len(s.saved) != 1 || s.saved[0][2].Name != "Pasted name" || s.saved[0][2].Query != "repo:o/r is:open" {
		t.Fatalf("saved = %+v", s.saved)
	}
}

func TestFilterBarSanitizesViewNames(t *testing.T) {
	m := send(t, New(Options{Views: []config.View{{Name: "evil\x1b]52;c;eA==\x07", Query: "q"}}}), tea.WindowSizeMsg{Width: 140, Height: 10})
	if raw := m.View().Content; strings.Contains(raw, "\x1b]52") || strings.Contains(raw, "\x07") {
		t.Errorf("escape sequence from a view name reached the terminal: %q", raw)
	}
}

func TestSavingAutoReviewViewWarnsAboutCost(t *testing.T) {
	s := &viewSaver{}
	m := sendKeys(viewsModel(t, s, managed), key("n"))
	keys := append(typed("Auto"), tab)
	keys = append(keys, typed("is:pr")...)
	m = sendKeys(m, append(keys, tab, space, enter)...)
	if out := screen(m); !strings.Contains(out, "auto-review is on") {
		t.Errorf("no cost notice after enabling auto-review:\n%s", out)
	}
}
