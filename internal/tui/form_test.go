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
	f := newViewForm(config.View{}, "", []string{"claude", "sec"}, "", 0)
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
	f := newViewForm(config.View{Name: "Team", Query: " is:pr ", AutoReview: true, Agent: "sec"}, "Team", []string{"claude", "sec"}, "", 0)
	if got := f.view(); got.Name != "Team" || got.Query != "is:pr" || !got.AutoReview || got.Agent != "sec" {
		t.Errorf("prefill = %+v", got)
	}
	if f.editing != "Team" {
		t.Error("editing name not kept")
	}
}

func TestFormEscCancels(t *testing.T) {
	if _, act := formKeys(newViewForm(config.View{}, "", nil, "", 0), esc); act != formCancel {
		t.Fatalf("action = %v", act)
	}
}

func TestFormAgentCycleIncludesDefault(t *testing.T) {
	f := newViewForm(config.View{}, "", []string{"claude"}, "", 0)
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
	f := newViewForm(config.View{Name: "Team", Query: "is:pr"}, "Team", nil, "", 0)
	f.err = "query is required"
	out := ansi.Strip(f.render(testStyles, 80, 12))
	for _, want := range []string{"Edit view", "Name", "Query", "Auto-review", "Agent", "(default)", "query is required", "enter save", "esc cancel"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(ansi.Strip(newViewForm(config.View{}, "", nil, "", 0).render(testStyles, 80, 12)), "New view") {
		t.Error("new form title missing")
	}
}

func TestFormFieldsFitWidth(t *testing.T) {
	f := newViewForm(config.View{Name: "cli", Query: "repo:cli/cli is:open is:pr"}, "", nil, "", 0)
	for _, line := range strings.Split(ansi.Strip(f.render(testStyles, 150, 12)), "\n") {
		if strings.HasSuffix(strings.TrimRight(line, " "), "…") {
			t.Errorf("field overflows and is truncated: %q", line)
		}
	}
}

func labelField(f viewForm) viewForm {
	f, _ = formKeys(f, tab, tab, tab, tab) // name → query → auto → agent → label
	return f
}

func TestFormLabelStates(t *testing.T) {
	cases := []struct {
		typed string
		want  *string
	}{
		{"", nil},
		{"-", strPtr("")},
		{" needs-ai ", strPtr("needs-ai")},
	}
	for _, tc := range cases {
		f := labelField(newViewForm(config.View{Name: "V", Query: "q"}, "", nil, "ai-review", 0))
		f, _ = formKeys(f, typed(tc.typed)...)
		got := f.view().Label
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("typed %q: label = %v, want %v", tc.typed, got, tc.want)
		}
	}
}

func TestFormLabelPrefill(t *testing.T) {
	for _, tc := range []struct {
		in   *string
		show string
	}{{nil, ""}, {strPtr(""), "-"}, {strPtr("needs-ai"), "needs-ai"}} {
		f := newViewForm(config.View{Name: "V", Query: "q", Label: tc.in}, "V", nil, "ai-review", 0)
		if f.label.Value() != tc.show {
			t.Errorf("prefill %v: field = %q, want %q", tc.in, f.label.Value(), tc.show)
		}
		if got := f.view().Label; (got == nil) != (tc.in == nil) || (got != nil && *got != *tc.in) {
			t.Errorf("prefill %v did not round-trip: %v", tc.in, got)
		}
	}
}

func TestFormLabelPlaceholderShowsDefault(t *testing.T) {
	if f := newViewForm(config.View{}, "", nil, "ai-review", 0); !strings.Contains(f.label.Placeholder, "ai-review") {
		t.Errorf("placeholder = %q", f.label.Placeholder)
	}
	if f := newViewForm(config.View{}, "", nil, "", 0); !strings.Contains(f.label.Placeholder, "none") {
		t.Errorf("placeholder = %q", f.label.Placeholder)
	}
}

func strPtr(s string) *string { return &s }

func maxPRsField(f viewForm) viewForm {
	f, _ = formKeys(f, tab, tab, tab, tab, tab) // name → query → auto → agent → label → max PRs
	return f
}

func TestFormMaxPRs(t *testing.T) {
	for typed_, want := range map[string]int{"": 0, "80": 80, " 100 ": 100, "1": 1} {
		f := maxPRsField(newViewForm(config.View{Name: "V", Query: "q"}, "", nil, "", 50))
		f, act := formKeys(f, append(typed(typed_), enter)...)
		if act != formSave || f.view().MaxPRs != want {
			t.Errorf("typed %q: action %v, MaxPRs %d, want %d (err %q)", typed_, act, f.view().MaxPRs, want, f.err)
		}
	}
	for _, bad := range []string{"0", "101", "-1", "abc"} {
		f := maxPRsField(newViewForm(config.View{Name: "V", Query: "q"}, "", nil, "", 50))
		f, act := formKeys(f, append(typed(bad), enter)...)
		if act == formSave || !strings.Contains(f.err, "1 to 100") {
			t.Errorf("typed %q: action %v, err %q", bad, act, f.err)
		}
	}
}

func TestFormMaxPRsPrefillAndPlaceholder(t *testing.T) {
	f := newViewForm(config.View{Name: "V", Query: "q", MaxPRs: 80}, "V", nil, "", 50)
	if f.maxPRs.Value() != "80" || f.view().MaxPRs != 80 {
		t.Errorf("prefill: field %q, view %d", f.maxPRs.Value(), f.view().MaxPRs)
	}
	if f := newViewForm(config.View{}, "", nil, "", 30); f.maxPRs.Value() != "" || !strings.Contains(f.maxPRs.Placeholder, "30") {
		t.Errorf("no override: field %q placeholder %q", f.maxPRs.Value(), f.maxPRs.Placeholder)
	}
}
