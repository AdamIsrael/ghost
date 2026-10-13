package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/AdamIsrael/ghost/internal/config"
)

type formAction int

const (
	formNone formAction = iota
	formSave
	formCancel
)

const (
	fieldName = iota
	fieldQuery
	fieldAuto
	fieldAgent
	fieldLabel
	fieldCount
)

// viewForm creates or edits one view.
type viewForm struct {
	editing  string // original name when editing; "" when creating
	name     textinput.Model
	query    textinput.Model
	auto     bool
	agents   []string // agents[0] is "" (the default agent)
	agentIdx int
	label    textinput.Model
	focus    int
	err      string
}

func newViewForm(v config.View, editing string, agents []string, globalLabel string) viewForm {
	f := viewForm{editing: editing, auto: v.AutoReview, agents: append([]string{""}, agents...)}
	f.name = textinput.New()
	f.name.Placeholder = "My reviews"
	f.name.CharLimit = 60
	f.name.SetValue(v.Name)
	f.query = textinput.New()
	f.query.Placeholder = "is:open is:pr review-requested:@me"
	f.query.SetValue(v.Query)
	f.label = textinput.New()
	f.label.CharLimit = 50
	if globalLabel != "" {
		f.label.Placeholder = globalLabel + " (default; - for none)"
	} else {
		f.label.Placeholder = "none (default)"
	}
	switch {
	case v.Label == nil:
	case *v.Label == "":
		f.label.SetValue("-")
	default:
		f.label.SetValue(*v.Label)
	}
	for i, a := range f.agents {
		if a == v.Agent {
			f.agentIdx = i
		}
	}
	f.setFocus(fieldName)
	return f
}

func (f *viewForm) setFocus(i int) {
	f.focus = (i + fieldCount) % fieldCount
	f.name.Blur()
	f.query.Blur()
	f.label.Blur()
	switch f.focus {
	case fieldName:
		f.name.Focus()
	case fieldQuery:
		f.query.Focus()
	case fieldLabel:
		f.label.Focus()
	}
}

func (f viewForm) view() config.View {
	var lbl *string
	switch s := strings.TrimSpace(f.label.Value()); s {
	case "":
	case "-":
		lbl = new(string)
	default:
		lbl = &s
	}
	return config.View{
		Name:       strings.TrimSpace(f.name.Value()),
		Query:      strings.TrimSpace(f.query.Value()),
		AutoReview: f.auto,
		Agent:      f.agents[f.agentIdx],
		Label:      lbl,
	}
}

func (f viewForm) update(msg tea.KeyPressMsg) (viewForm, formAction, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return f, formCancel, nil
	case "enter":
		return f, formSave, nil
	case "tab", "down":
		f.setFocus(f.focus + 1)
		return f, formNone, nil
	case "shift+tab", "up":
		f.setFocus(f.focus - 1)
		return f, formNone, nil
	}
	var cmd tea.Cmd
	switch f.focus {
	case fieldName:
		f.name, cmd = f.name.Update(msg)
	case fieldQuery:
		f.query, cmd = f.query.Update(msg)
	case fieldLabel:
		f.label, cmd = f.label.Update(msg)
	case fieldAuto:
		if msg.String() == "space" {
			f.auto = !f.auto
		}
	case fieldAgent:
		switch msg.String() {
		case "right", "l":
			f.agentIdx = (f.agentIdx + 1) % len(f.agents)
		case "left", "h":
			f.agentIdx = (f.agentIdx - 1 + len(f.agents)) % len(f.agents)
		}
	}
	return f, formNone, cmd
}

func (f viewForm) render(sty styles, width, height int) string {
	title := "New view"
	if f.editing != "" {
		title = fmt.Sprintf("Edit view %q", f.editing)
	}
	marker := func(i int) string {
		if f.focus == i {
			return "▸ "
		}
		return "  "
	}
	f.name.SetWidth(max(width-20, 10)) // room for the marker, label and prompt
	f.query.SetWidth(max(width-20, 10))
	f.label.SetWidth(max(width-20, 10))
	auto := "[ ] off"
	if f.auto {
		auto = "[x] on (reviews cost money)"
	}
	agent := f.agents[f.agentIdx]
	if agent == "" {
		agent = "(default)"
	}
	ls := []string{
		sty.heading.Render("── " + title + " ──"),
		"",
		marker(fieldName) + "Name        " + f.name.View(),
		marker(fieldQuery) + "Query       " + f.query.View(),
		marker(fieldAuto) + "Auto-review " + auto,
		marker(fieldAgent) + "Agent       ← " + agent + " →",
		marker(fieldLabel) + "Label       " + f.label.View(),
		"",
	}
	if f.err != "" {
		ls = append(ls, sty.err.Render("✗ "+cleanLine(f.err)), "")
	}
	ls = append(ls, sty.muted.Render("tab next field · space toggle · ←/→ agent · enter save · esc cancel"))
	return block(ls, width, height)
}

// paste inserts pasted text into the focused text field.
func (f viewForm) paste(msg tea.PasteMsg) (viewForm, tea.Cmd) {
	var cmd tea.Cmd
	switch f.focus {
	case fieldName:
		f.name, cmd = f.name.Update(msg)
	case fieldQuery:
		f.query, cmd = f.query.Update(msg)
	case fieldLabel:
		f.label, cmd = f.label.Update(msg)
	}
	return f, cmd
}
