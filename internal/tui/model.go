// Package tui renders Ghost's terminal interface.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/engine"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/poller"
	"github.com/AdamIsrael/ghost/internal/preflight"
)

type Options struct {
	Views           []config.View
	MaxDailyCostUSD float64
	Checks          []preflight.Result
	Results         <-chan poller.Result // nil when polling is disabled
	PollNow         func()
	PollDisabled    string // why polling is off; shown in empty lists
	OpenURL         func(string) error
	Engine          Engine                    // nil: browse only
	SaveViews       func([]config.View) error // persists views; nil disables n/e/d
	Agents          []string                  // agent names for the view form
}

// Engine is what the TUI needs from the review engine.
type Engine interface {
	HandlePoll(poller.Result)
	Snapshot() engine.Snapshot
	PRState(repo string, number int) engine.PRState
	Rereview(github.PR) error
	PostPending(ctx context.Context, repo string, number int) error
	TogglePause() bool
	Changes() <-chan struct{}
	Approve(ctx context.Context, repo string, number int) error
}

type Model struct {
	opts     Options
	active   int
	cursor   int
	prs      map[string][]github.PR
	errs     map[string]error
	lastPoll time.Time
	flash    string
	width    int
	height   int
	snap     engine.Snapshot // cached; refreshed on updates, never in View
	prState  engine.PRState  // selected PR's state, cached likewise

	views         []config.View // owned copy; edited with n/e/d
	form          *viewForm
	confirmDelete string // name of the view awaiting y/n
}

type resultMsg poller.Result

type openedMsg struct{ err error }

type changeMsg struct{}

type engineDoneMsg struct {
	what string
	err  error
}

func waitForChange(e Engine) tea.Cmd {
	if e == nil {
		return nil
	}
	ch := e.Changes()
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return nil
		}
		return changeMsg{}
	}
}

func New(o Options) Model {
	return Model{opts: o, prs: map[string][]github.PR{}, errs: map[string]error{},
		views: append([]config.View(nil), o.Views...)}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(waitForResult(m.opts.Results), waitForChange(m.opts.Engine))
}

func waitForResult(ch <-chan poller.Result) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return nil
		}
		return resultMsg(r)
	}
}

func (m Model) currentView() (config.View, bool) {
	if len(m.views) == 0 {
		return config.View{}, false
	}
	return m.views[m.active], true
}

func (m Model) currentPRs() []github.PR {
	v, ok := m.currentView()
	if !ok {
		return nil
	}
	return m.prs[v.Name]
}

func (m Model) selectedPR() *github.PR {
	prs := m.currentPRs()
	if m.cursor < 0 || m.cursor >= len(prs) {
		return nil
	}
	return &prs[m.cursor]
}

func (m *Model) clampCursor() {
	if n := len(m.currentPRs()); m.cursor >= n {
		m.cursor = max(n-1, 0)
	}
}

// Update handles msg, then refreshes cached engine state for messages that
// can change it, so View never touches the database.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	nm, ok := next.(Model)
	if !ok {
		return next, cmd
	}
	switch msg.(type) {
	case tea.WindowSizeMsg, resultMsg, changeMsg, engineDoneMsg, tea.KeyPressMsg:
		nm.refresh()
	}
	return nm, cmd
}

func (m *Model) refresh() {
	if m.opts.Engine == nil {
		return
	}
	m.snap = m.opts.Engine.Snapshot()
	m.prState = engine.PRState{}
	if pr := m.selectedPR(); pr != nil {
		m.prState = m.opts.Engine.PRState(pr.Repo, pr.Number)
	}
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case resultMsg:
		if v, ok := m.viewNamed(msg.View); ok && msg.Query != "" && msg.Query != v.Query {
			// From before the view was edited; the new query's result follows.
			return m, waitForResult(m.opts.Results)
		}
		if m.opts.Engine != nil {
			m.opts.Engine.HandlePoll(poller.Result(msg))
		}
		m.lastPoll = msg.At
		if msg.Err != nil {
			m.errs[msg.View] = msg.Err
		} else {
			delete(m.errs, msg.View)
		}
		// A failed poll (error, no PRs) keeps the last good results.
		if msg.Err == nil || msg.PRs != nil {
			// Non-nil marks the view as polled, so an empty result reads
			// "no PRs match" instead of "Loading…".
			prs := msg.PRs
			if prs == nil {
				prs = []github.PR{}
			}
			m.prs[msg.View] = prs
		}
		m.clampCursor()
		return m, waitForResult(m.opts.Results)
	case changeMsg:
		return m, waitForChange(m.opts.Engine)
	case engineDoneMsg:
		m.flash = ""
		if msg.err != nil {
			m.flash = msg.err.Error()
			if !strings.HasPrefix(m.flash, msg.what+":") {
				m.flash = msg.what + ": " + m.flash
			}
		}
	case openedMsg:
		m.flash = ""
		if msg.err != nil {
			m.flash = "open failed: " + msg.err.Error()
		}
	case tea.PasteMsg:
		if m.form != nil {
			f, cmd := m.form.paste(msg)
			m.form = &f
			return m, cmd
		}
	case tea.KeyPressMsg:
		switch {
		case m.form != nil:
			return m.handleFormKey(msg)
		case m.confirmDelete != "":
			return m.handleConfirmKey(msg.String())
		}
		return m.handleKey(msg.String())
	}
	return m, nil
}

func (m Model) handleKey(k string) (tea.Model, tea.Cmd) {
	n := len(m.views)
	switch k {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "right", "l", "tab":
		if n > 0 {
			m.active, m.cursor = (m.active+1)%n, 0
		}
	case "left", "h", "shift+tab":
		if n > 0 {
			m.active, m.cursor = (m.active-1+n)%n, 0
		}
	case "down", "j":
		if m.cursor < len(m.currentPRs())-1 {
			m.cursor++
		}
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "R":
		if m.opts.PollNow != nil {
			m.opts.PollNow()
		}
	case "r":
		pr := m.selectedPR()
		if pr == nil || m.opts.Engine == nil {
			return m, nil
		}
		e, p := m.opts.Engine, *pr
		return m, func() tea.Msg { return engineDoneMsg{"re-review", e.Rereview(p)} }
	case "P":
		pr := m.selectedPR()
		if pr == nil || m.opts.Engine == nil {
			return m, nil
		}
		e, repo, n := m.opts.Engine, pr.Repo, pr.Number
		return m, func() tea.Msg {
			return engineDoneMsg{"post", e.PostPending(context.Background(), repo, n)}
		}
	case "p":
		if m.opts.Engine != nil {
			m.opts.Engine.TogglePause()
		}
	case "a":
		pr := m.selectedPR()
		if pr == nil || m.opts.Engine == nil {
			return m, nil
		}
		e, repo, n := m.opts.Engine, pr.Repo, pr.Number
		return m, func() tea.Msg {
			return engineDoneMsg{"approve", e.Approve(context.Background(), repo, n)}
		}
	case "n":
		if m.opts.SaveViews == nil {
			m.flash = "view editing is unavailable"
			return m, nil
		}
		f := newViewForm(config.View{}, "", m.opts.Agents)
		m.form = &f
	case "e", "d":
		v, ok := m.currentView()
		switch {
		case !ok:
		case m.opts.SaveViews == nil:
			m.flash = "view editing is unavailable"
		case v.Source == "config.toml":
			m.flash = fmt.Sprintf("view %q is defined in config.toml; edit it there", v.Name)
		case k == "e":
			f := newViewForm(v, v.Name, m.opts.Agents)
			m.form = &f
		default:
			m.confirmDelete = v.Name
		}
	case "o", "enter":
		pr := m.selectedPR()
		if pr == nil || m.opts.OpenURL == nil {
			return m, nil
		}
		url, open := pr.URL, m.opts.OpenURL
		return m, func() tea.Msg { return openedMsg{err: open(url)} }
	}
	return m, nil
}

func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m Model) emptyMessage() string {
	v, ok := m.currentView()
	switch {
	case !ok:
		return "No views configured. Add a [[view]] to config.toml."
	case m.opts.PollDisabled != "":
		return m.opts.PollDisabled
	case m.prs[v.Name] == nil:
		return "Loading…"
	default:
		return "No pull requests match this view."
	}
}

func (m Model) statusErr() string {
	if m.confirmDelete != "" {
		return fmt.Sprintf("Delete view %q? y/n", m.confirmDelete)
	}
	if m.flash != "" {
		return m.flash
	}
	// A view that failed outright outranks one that only hid some PRs.
	var warning string
	for _, v := range m.views {
		err := m.errs[v.Name]
		if err == nil {
			continue
		}
		var pe *github.PartialError
		if !errors.As(err, &pe) {
			return fmt.Sprintf("%s: %v", v.Name, err)
		}
		if warning == "" {
			warning = fmt.Sprintf("%s: %v", v.Name, err)
		}
	}
	return warning
}

const minDetailWidth = 30

func (m Model) render() string {
	if m.width <= 0 || m.height <= 0 {
		return "Loading…"
	}
	counts := map[string]int{}
	for name, prs := range m.prs {
		counts[name] = len(prs)
	}
	top := renderFilterBar(m.views, m.active, counts, m.width)
	si := statusInfo{
		MaxDailyUSD: m.opts.MaxDailyCostUSD,
		LastPoll:    m.lastPoll,
		Err:         m.statusErr(),
		Checks:      m.opts.Checks,
	}
	st := m.prState
	if m.opts.Engine != nil {
		snap := m.snap
		si.SpendUSD, si.MaxDailyUSD = snap.SpendTodayUSD, snap.MaxDailyUSD
		si.Running, si.Queued, si.Paused, si.BudgetPaused = snap.Running, snap.Queued, snap.Paused, snap.BudgetPaused
	}
	bottom := renderStatusBar(si, m.width)
	bodyH := m.height - 2
	if bodyH < 1 {
		return top
	}
	if m.form != nil {
		return lipgloss.JoinVertical(lipgloss.Left, top, m.form.render(m.width, bodyH), bottom)
	}

	listW := max(m.width*2/5, 20)
	detailW := m.width - listW - 1
	if detailW < minDetailWidth {
		list := renderList(m.currentPRs(), m.cursor, m.width, bodyH, m.emptyMessage())
		return lipgloss.JoinVertical(lipgloss.Left, top, list, bottom)
	}
	list := renderList(m.currentPRs(), m.cursor, listW, bodyH, m.emptyMessage())
	sep := strings.TrimSuffix(strings.Repeat("│\n", bodyH), "\n")
	detail := renderDetail(m.selectedPR(), st, detailW, bodyH)
	body := lipgloss.JoinHorizontal(lipgloss.Top, list, dim.Render(sep), detail)
	return lipgloss.JoinVertical(lipgloss.Left, top, body, bottom)
}

func (m Model) handleFormKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	f, act, cmd := m.form.update(msg)
	switch act {
	case formCancel:
		m.form = nil
		return m, nil
	case formSave:
		v := f.view()
		v.Source = config.ViewsFileName
		views := append([]config.View(nil), m.views...)
		idx := len(views)
		if f.editing == "" {
			views = append(views, v)
		} else {
			for i := range views {
				if views[i].Name == f.editing {
					views[i], idx = v, i
				}
			}
		}
		if err := m.opts.SaveViews(views); err != nil {
			f.err = err.Error()
			m.form = &f
			return m, nil
		}
		m.views, m.form = views, nil
		m.active, m.cursor = idx, 0
		m.flash = ""
		if v.AutoReview {
			m.flash = fmt.Sprintf("saved %q; auto-review is on, so matching PRs will be reviewed (and charged) from now on", v.Name)
		}
		return m, nil
	}
	m.form = &f
	return m, cmd
}

func (m Model) handleConfirmKey(k string) (tea.Model, tea.Cmd) {
	name := m.confirmDelete
	m.confirmDelete = ""
	if k != "y" {
		return m, nil
	}
	var views []config.View
	for _, v := range m.views {
		if v.Name != name {
			views = append(views, v)
		}
	}
	if err := m.opts.SaveViews(views); err != nil {
		m.flash = "delete: " + err.Error()
		return m, nil
	}
	m.views = views
	if m.active >= len(views) {
		m.active = max(len(views)-1, 0)
	}
	m.cursor = 0
	return m, nil
}

func (m Model) viewNamed(name string) (config.View, bool) {
	for _, v := range m.views {
		if v.Name == name {
			return v, true
		}
	}
	return config.View{}, false
}
