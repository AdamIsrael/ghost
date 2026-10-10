package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/engine"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/preflight"
	"github.com/AdamIsrael/ghost/internal/store"
	"github.com/AdamIsrael/ghost/internal/textsafe"
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

func (sty styles) renderFilterBar(views []config.View, active int, counts map[string]int, width int) string {
	parts := []string{"Views:"}
	for i, v := range views {
		count := "…"
		if n, ok := counts[v.Name]; ok {
			count = fmt.Sprint(n)
		}
		label := fmt.Sprintf(" %s (%s) ", cleanLine(v.Name), count)
		if i == active {
			label = sty.activeTab.Render(label)
		}
		parts = append(parts, label)
	}
	return fit(strings.Join(parts, " ")+"  "+sty.muted.Render("←/→ switch · n new · e edit · d delete · o open · r review · P post · a approve · p pause · R refresh · t theme · q quit"), width)
}

func (sty styles) renderRow(pr github.PR, width int, sel bool) string {
	st := pr.Status()
	line := fit(fmt.Sprintf("%s %-17s %s#%d %s", st.Icon(), st, cleanLine(pr.Repo), pr.Number, cleanLine(pr.Title)), width)
	if sel {
		return sty.selected.Render(line)
	}
	return line
}

func (sty styles) renderList(prs []github.PR, cursor, width, height int, empty string) string {
	if len(prs) == 0 {
		return block([]string{sty.muted.Render(empty)}, width, height)
	}
	start := 0
	if cursor >= height {
		start = cursor - height + 1
	}
	var ls []string
	for i := start; i < len(prs) && i < start+height; i++ {
		ls = append(ls, sty.renderRow(prs[i], width, i == cursor))
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

func (sty styles) renderDetail(pr *github.PR, rs engine.PRState, width, height int) string {
	if pr == nil {
		return block([]string{sty.muted.Render("No pull request selected")}, width, height)
	}
	st := pr.Status()
	sha := cleanLine(pr.HeadSHA)
	if len(sha) > 7 {
		sha = sha[:7]
	}
	ls := []string{
		sty.heading.Render(fmt.Sprintf("%s#%d  %s", cleanLine(pr.Repo), pr.Number, cleanLine(pr.Title))),
		fmt.Sprintf("by @%s  +%d −%d  %d files", cleanLine(pr.Author), pr.Additions, pr.Deletions, pr.ChangedFiles),
		fmt.Sprintf("Status: %s %s   CI: %s   Review: %s", st.Icon(), st, cleanLine(ciLabel(pr.CIState)), cleanLine(reviewLabel(pr.ReviewDecision))),
		fmt.Sprintf("%s → %s  (%s)", cleanLine(pr.HeadRef), cleanLine(pr.BaseRef), sha),
		sty.muted.Render(cleanLine(pr.URL)),
		"",
	}
	ls = append(ls, sty.reviewLines(rs, pr.HeadSHA, width)...)
	ls = append(ls, "", sty.heading.Render("── Description ──"))
	body := strings.TrimSpace(clean(pr.Body))
	if body == "" {
		ls = append(ls, sty.muted.Render("(no description)"))
	} else {
		ls = append(ls, strings.Split(ansi.Wordwrap(body, width, " "), "\n")...)
	}
	return block(ls, width, height)
}

type statusInfo struct {
	SpendUSD     float64
	MaxDailyUSD  float64
	LastPoll     time.Time
	Err          string
	Checks       []preflight.Result
	Running      int
	Queued       int
	Paused       bool
	BudgetPaused bool
}

func (sty styles) renderStatusBar(si statusInfo, width int) string {
	last := "—"
	if !si.LastPoll.IsZero() {
		last = si.LastPoll.Format("15:04")
	}
	parts := []string{
		fmt.Sprintf("Spend today $%.2f / $%.2f", si.SpendUSD, si.MaxDailyUSD),
		fmt.Sprintf("Reviews %d running · %d queued", si.Running, si.Queued),
		"Last poll " + last,
	}
	if si.Paused {
		parts = append(parts, "⏸ paused")
	}
	if si.BudgetPaused {
		parts = append(parts, "budget reached")
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
		line += "  " + sty.err.Render("⚠ "+cleanLine(si.Err))
	}
	return fit(line, width)
}

func (sty styles) reviewLines(st engine.PRState, prHead string, width int) []string {
	ls := []string{sty.heading.Render("── Audit trail ──")}
	if len(st.Audit) == 0 {
		ls = append(ls, sty.muted.Render("No activity yet."))
	}
	for _, ev := range st.Audit {
		ls = append(ls, fmt.Sprintf("%s %s — %s", ev.At.Local().Format("15:04"), cleanLine(ev.Kind), cleanLine(ev.Message)))
	}
	r := st.Latest
	if r == nil {
		return ls
	}
	waiting := r.State == store.Ready || r.State == store.PostUnknown || r.Approval == store.ApprovalPending
	if waiting && prHead != "" && r.HeadSHA != prHead {
		ls = append(ls, "", sty.err.Render(fmt.Sprintf("⚠ PR has new commits since this review (reviewed %.7s, now %.7s)",
			cleanLine(r.HeadSHA), cleanLine(prHead))))
	}
	switch r.State {
	case store.Ready:
		ls = append(ls, "", sty.heading.Render(fmt.Sprintf("── Pending review: %s, $%.2f (press P to post) ──", cleanLine(r.Verdict), r.CostUSD)))
		ls = append(ls, strings.Split(ansi.Wordwrap(clean(r.Summary), width, " "), "\n")...)
		for _, c := range st.Comments {
			ls = append(ls, fmt.Sprintf("%s:%d [%s] %s", cleanLine(c.Path), c.Line, cleanLine(c.Severity), cleanLine(c.Body)))
		}
	case store.Posting:
		ls = append(ls, "", sty.heading.Render("── Posting… ──"))
	case store.PostUnknown:
		ls = append(ls, "", sty.err.Render("── Post result unknown; press P to check GitHub and retry ──"))
		ls = append(ls, strings.Split(ansi.Wordwrap(clean(r.Summary), width, " "), "\n")...)
	case store.Blocked:
		ls = append(ls, "", sty.err.Render("── Review blocked; it will not be posted ──"), cleanLine(r.Error))
	}
	if line := sty.approvalLine(r); line != "" {
		ls = append(ls, "", line)
	}
	return ls
}

func (sty styles) approvalLine(r *store.Review) string {
	note := cleanLine(r.ApprovalNote)
	switch r.Approval {
	case store.ApprovalPending:
		return sty.heading.Render("── Approval pending: meets the auto-approve rules (press a to approve) ──")
	case store.ApprovalWaiting:
		return "Approval waiting: " + note
	case store.ApprovalApproving:
		return "Approving…"
	case store.ApprovalApproved:
		return sty.heading.Render("✓ Approved")
	case store.ApprovalNo:
		return sty.muted.Render("Not approved: " + note)
	case store.ApprovalFailed:
		return sty.err.Render("Approval failed: " + note)
	case store.ApprovalUnknown:
		return sty.err.Render("Approval result unknown; press a to check GitHub and retry")
	}
	return ""
}
