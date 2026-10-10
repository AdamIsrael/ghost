package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/preflight"
	"github.com/AdamIsrael/ghost/internal/textsafe"
)

var (
	activeTab = lipgloss.NewStyle().Bold(true).Reverse(true)
	dim       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	bold      = lipgloss.NewStyle().Bold(true)
	selected  = lipgloss.NewStyle().Reverse(true)
	errText   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

// clean and cleanLine make untrusted GitHub text safe to draw.
var clean, cleanLine = textsafe.Clean, textsafe.Line

// fit truncates s with "…" and pads it to exactly w terminal cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// block fits each line to width and clips or pads to exactly height lines.
func block(ls []string, width, height int) string {
	out := make([]string, height)
	for i := range out {
		line := ""
		if i < len(ls) {
			line = ls[i]
		}
		out[i] = fit(line, width)
	}
	return strings.Join(out, "\n")
}

func renderFilterBar(views []config.View, active int, counts map[string]int, width int) string {
	parts := []string{"Views:"}
	for i, v := range views {
		count := "…"
		if n, ok := counts[v.Name]; ok {
			count = fmt.Sprint(n)
		}
		label := fmt.Sprintf(" %s (%s) ", v.Name, count)
		if i == active {
			label = activeTab.Render(label)
		}
		parts = append(parts, label)
	}
	return fit(strings.Join(parts, " ")+"  "+dim.Render("←/→ switch · o open · R refresh · q quit"), width)
}

func renderRow(pr github.PR, width int, sel bool) string {
	st := pr.Status()
	line := fit(fmt.Sprintf("%s %-17s %s#%d %s", st.Icon(), st, cleanLine(pr.Repo), pr.Number, cleanLine(pr.Title)), width)
	if sel {
		return selected.Render(line)
	}
	return line
}

func renderList(prs []github.PR, cursor, width, height int, empty string) string {
	if len(prs) == 0 {
		return block([]string{dim.Render(empty)}, width, height)
	}
	start := 0
	if cursor >= height {
		start = cursor - height + 1
	}
	var ls []string
	for i := start; i < len(prs) && i < start+height; i++ {
		ls = append(ls, renderRow(prs[i], width, i == cursor))
	}
	return block(ls, width, height)
}

func ciLabel(state string) string {
	switch state {
	case "":
		return "none"
	case "SUCCESS":
		return "✓ passing"
	case "PENDING", "EXPECTED":
		return "◐ pending"
	case "FAILURE", "ERROR":
		return "✗ failing"
	}
	return strings.ToLower(state)
}

func reviewLabel(decision string) string {
	if decision == "" {
		return "none"
	}
	return strings.ToLower(strings.ReplaceAll(decision, "_", " "))
}

func renderDetail(pr *github.PR, width, height int) string {
	if pr == nil {
		return block([]string{dim.Render("No pull request selected")}, width, height)
	}
	st := pr.Status()
	sha := cleanLine(pr.HeadSHA)
	if len(sha) > 7 {
		sha = sha[:7]
	}
	ls := []string{
		bold.Render(fmt.Sprintf("%s#%d  %s", cleanLine(pr.Repo), pr.Number, cleanLine(pr.Title))),
		fmt.Sprintf("by @%s  +%d −%d  %d files", cleanLine(pr.Author), pr.Additions, pr.Deletions, pr.ChangedFiles),
		fmt.Sprintf("Status: %s %s   CI: %s   Review: %s", st.Icon(), st, cleanLine(ciLabel(pr.CIState)), cleanLine(reviewLabel(pr.ReviewDecision))),
		fmt.Sprintf("%s → %s  (%s)", cleanLine(pr.HeadRef), cleanLine(pr.BaseRef), sha),
		dim.Render(cleanLine(pr.URL)),
		"",
		bold.Render("── Audit trail ──"),
		dim.Render("No activity yet. Automated reviews arrive in a later milestone."),
		"",
		bold.Render("── Description ──"),
	}
	body := strings.TrimSpace(clean(pr.Body))
	if body == "" {
		ls = append(ls, dim.Render("(no description)"))
	} else {
		ls = append(ls, strings.Split(ansi.Wordwrap(body, width, " "), "\n")...)
	}
	return block(ls, width, height)
}

type statusInfo struct {
	SpendUSD    float64
	MaxDailyUSD float64
	LastPoll    time.Time
	Err         string
	Checks      []preflight.Result
}

func renderStatusBar(si statusInfo, width int) string {
	last := "—"
	if !si.LastPoll.IsZero() {
		last = si.LastPoll.Format("15:04")
	}
	parts := []string{
		fmt.Sprintf("Spend today $%.2f / $%.2f", si.SpendUSD, si.MaxDailyUSD),
		"Reviews 0 running · 0 queued",
		"Last poll " + last,
	}
	var checks []string
	for _, c := range si.Checks {
		mark := "✓"
		if !c.OK {
			mark = "✗"
		}
		checks = append(checks, c.Name+" "+mark)
	}
	if len(checks) > 0 {
		parts = append(parts, strings.Join(checks, " "))
	}
	line := strings.Join(parts, " · ")
	if si.Err != "" {
		line += "  " + errText.Render("⚠ "+cleanLine(si.Err))
	}
	return fit(line, width)
}
