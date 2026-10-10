package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AdamIsrael/ghost/internal/config"
)

func formKeys(f viewForm, keys ...tea.KeyPressMsg) (viewForm, formAction) {
	var act formAction
	for _, k := range keys {
		f, act, _ = f.update(k)
	}
	return f, act
}

func typed(s string) []tea.KeyPressMsg {
	var ks []tea.KeyPressMsg
	for _, r := range s {
		ks = append(ks, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return ks
}

var (
	tab   = tea.KeyPressMsg{Code: tea.KeyTab}
	enter = tea.KeyPressMsg{Code: tea.KeyEnter}
	esc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	space = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	right = tea.KeyPressMsg{Code: tea.KeyRight}
)

func TestFormFillAndSave(t *testing.T) {
	f := newViewForm(config.View{}, "", []string{"claude", "sec"})
	keys := append(typed("Mine"), tab)
	keys = append(keys, typed("is:pr author:@me")...)
	keys = append(keys, tab, space, tab, right, right, enter)
	f, act := formKeys(f, keys...)
	if act != formSave {
		t.Fatalf("action = %v", act)
	}
	if got := f.view(); got != (config.View{Name: "Mine", Query: "is:pr author:@me", AutoReview: true, Agent: "sec"}) {
		t.Errorf("view = %+v", got)
	}
}

func TestFormEditPrefillsAndTrims(t *testing.T) {
	f := newViewForm(config.View{Name: "Team", Query: " is:pr ", AutoReview: true, Agent: "sec"}, "Team", []string{"claude", "sec"})
	if got := f.view(); got.Name != "Team" || got.Query != "is:pr" || !got.AutoReview || got.Agent != "sec" {
		t.Errorf("prefill = %+v", got)
	}
	if f.editing != "Team" {
		t.Error("editing name not kept")
	}
}

func TestFormEscCancels(t *testing.T) {
	if _, act := formKeys(newViewForm(config.View{}, "", nil), esc); act != formCancel {
		t.Fatalf("action = %v", act)
	}
}

func TestFormAgentCycleIncludesDefault(t *testing.T) {
	f := newViewForm(config.View{}, "", []string{"claude"})
	f, _ = formKeys(f, tab, tab, tab, right) // to agent, cycle once
	if f.view().Agent != "claude" {
		t.Fatalf("agent = %q", f.view().Agent)
	}
	f, _ = formKeys(f, right) // wraps to default
	if f.view().Agent != "" {
		t.Fatalf("agent = %q, want default", f.view().Agent)
	}
}

func TestFormRender(t *testing.T) {
	f := newViewForm(config.View{Name: "Team", Query: "is:pr"}, "Team", nil)
	f.err = "query is required"
	out := ansi.Strip(f.render(testStyles, 80, 12))
	for _, want := range []string{"Edit view", "Name", "Query", "Auto-review", "Agent", "(default)", "query is required", "enter save", "esc cancel"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(ansi.Strip(newViewForm(config.View{}, "", nil).render(testStyles, 80, 12)), "New view") {
		t.Error("new form title missing")
	}
}

func TestFormFieldsFitWidth(t *testing.T) {
	f := newViewForm(config.View{Name: "cli", Query: "repo:cli/cli is:open is:pr"}, "", nil)
	for _, line := range strings.Split(ansi.Strip(f.render(testStyles, 150, 12)), "\n") {
		if strings.HasSuffix(strings.TrimRight(line, " "), "…") {
			t.Errorf("field overflows and is truncated: %q", line)
		}
	}
}
