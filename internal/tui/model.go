// Package tui renders Ghost's terminal interface.
package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/AdamIsrael/ghost/internal/config"
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
}

type resultMsg poller.Result

type openedMsg struct{ err error }

func New(o Options) Model {
	return Model{opts: o, prs: map[string][]github.PR{}, errs: map[string]error{}}
}

func (m Model) Init() tea.Cmd { return waitForResult(m.opts.Results) }

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
	if len(m.opts.Views) == 0 {
		return config.View{}, false
	}
	return m.opts.Views[m.active], true
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

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case resultMsg:
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
	case openedMsg:
		m.flash = ""
		if msg.err != nil {
			m.flash = "open failed: " + msg.err.Error()
		}
	case tea.KeyPressMsg:
		return m.handleKey(msg.String())
	}
	return m, nil
}

func (m Model) handleKey(k string) (tea.Model, tea.Cmd) {
	n := len(m.opts.Views)
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
	if m.flash != "" {
		return m.flash
	}
	// A view that failed outright outranks one that only hid some PRs.
	var warning string
	for _, v := range m.opts.Views {
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
	top := renderFilterBar(m.opts.Views, m.active, counts, m.width)
	bottom := renderStatusBar(statusInfo{
		MaxDailyUSD: m.opts.MaxDailyCostUSD,
		LastPoll:    m.lastPoll,
		Err:         m.statusErr(),
		Checks:      m.opts.Checks,
	}, m.width)
	bodyH := m.height - 2
	if bodyH < 1 {
		return top
	}

	listW := max(m.width*2/5, 20)
	detailW := m.width - listW - 1
	if detailW < minDetailWidth {
		list := renderList(m.currentPRs(), m.cursor, m.width, bodyH, m.emptyMessage())
		return lipgloss.JoinVertical(lipgloss.Left, top, list, bottom)
	}
	list := renderList(m.currentPRs(), m.cursor, listW, bodyH, m.emptyMessage())
	sep := strings.TrimSuffix(strings.Repeat("│\n", bodyH), "\n")
	detail := renderDetail(m.selectedPR(), detailW, bodyH)
	body := lipgloss.JoinHorizontal(lipgloss.Top, list, dim.Render(sep), detail)
	return lipgloss.JoinVertical(lipgloss.Left, top, body, bottom)
}
