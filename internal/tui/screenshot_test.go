package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/engine"
	"github.com/AdamIsrael/ghost/internal/github"
	"github.com/AdamIsrael/ghost/internal/poller"
	"github.com/AdamIsrael/ghost/internal/preflight"
	"github.com/AdamIsrael/ghost/internal/store"
)

// TestScreenshot renders the TUI with made-up data for the README. It runs
// only when GHOST_SCREENSHOT_DIR is set and writes <theme>.ansi there for
// scripts/screenshot.sh to turn into SVGs.
func TestScreenshot(t *testing.T) {
	dir := os.Getenv("GHOST_SCREENSHOT_DIR")
	if dir == "" {
		t.Skip("set GHOST_SCREENSHOT_DIR to render the README screenshots")
	}
	for _, name := range []string{"tokyo-night", "tokyo-night-day", "catppuccin-mocha"} {
		if err := os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(withForeground(demoModel(name).View())), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func demoModel(themeName string) tea.Model {
	at := func(hh, mm int) time.Time { return time.Date(2026, 10, 28, hh, mm, 0, 0, time.Local) }
	pr := func(n int, title, author, ci, decision string, draft bool, add, del int) github.PR {
		return github.PR{
			Repo: "acme/api", Number: n, State: "OPEN", Title: title, Author: author,
			URL: fmt.Sprintf("https://github.com/acme/api/pull/%d", n), BaseRef: "main", HeadRef: "feature",
			HeadSHA: "9f3c2a1b7e", CIState: ci, ReviewDecision: decision, IsDraft: draft,
			Additions: add, Deletions: del, ChangedFiles: 4,
		}
	}
	first := pr(142, "Retry webhooks with backoff", "mona", "SUCCESS", "REVIEW_REQUIRED", false, 186, 41)
	first.HeadRef = "webhook-retries"
	first.Body = "Failed webhook deliveries are now retried with exponential backoff (1s, 2s, 4s… up to 5 tries). Deliveries that still fail go to the **dead-letter table** so they can be replayed.\n\n<details>\n<summary>Test plan</summary>\n\n- Unit tests for the backoff schedule\n- Replayed 200 failed deliveries in staging\n</details>"
	prs := []github.PR{
		first,
		pr(140, "Bump golang.org/x/net to v0.47.0", "dependabot", "PENDING", "REVIEW_REQUIRED", false, 12, 12),
		pr(139, "Paginate the audit log endpoint", "hubot", "FAILURE", "REVIEW_REQUIRED", false, 240, 38),
		pr(137, "Cache org settings per request", "octocat", "SUCCESS", "APPROVED", false, 64, 9),
		pr(135, "Rate-limit login attempts", "mona", "SUCCESS", "CHANGES_REQUESTED", false, 118, 7),
		pr(131, "WIP: new billing export", "octocat", "PENDING", "", true, 512, 80),
	}
	comments := []agent.InlineComment{
		{Path: "webhook/retry.go", Line: 48, Severity: "blocking", Body: "The backoff can overflow after ~30 tries; cap the shift."},
		{Path: "webhook/retry.go", Line: 77, Severity: "nit", Body: "Name the 5 as maxAttempts."},
	}
	fe := &fakeEngine{
		changes: make(chan struct{}, 1),
		snap:    engine.Snapshot{SpendTodayUSD: 3.12, MaxDailyUSD: 10, Running: 1, Queued: 1},
		state: engine.PRState{
			Audit: []store.AuditEvent{
				{At: at(10, 2), Kind: "queued", Message: `view "Team", agent claude, commit 9f3c2a1`},
				{At: at(10, 2), Kind: "started", Message: "agent claude"},
				{At: at(10, 5), Kind: "reviewed", Message: "comment, 2 comments, $0.42"},
				{At: at(10, 5), Kind: "ready to post", Message: "press P to post it"},
			},
			Latest: &store.Review{
				State: store.Ready, HeadSHA: "9f3c2a1b7e", Verdict: "comment", CostUSD: 0.42,
				Summary: "Solid change: retries are bounded and failures land in the dead-letter table. One overflow risk in the backoff math.",
			},
			Comments: comments,
		},
	}
	views := []config.View{
		{Name: "Team", Query: "is:open is:pr org:acme", AutoReview: true},
		{Name: "Mine", Query: "is:open is:pr author:@me"},
		{Name: "Security", Query: "is:open is:pr label:security"},
	}
	m := tea.Model(New(Options{
		Views: views, MaxDailyCostUSD: 10, Engine: fe, Theme: themeName,
		Checks: []preflight.Result{{Name: "gh", OK: true}, {Name: "claude", OK: true}, {Name: "git", OK: true}},
	}))
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 136, Height: 27},
		resultMsg(poller.Result{View: "Mine", PRs: prs[3:5], At: at(10, 4)}),
		resultMsg(poller.Result{View: "Security", PRs: prs[4:5], At: at(10, 4)}),
		resultMsg(poller.Result{View: "Team", PRs: prs, At: at(10, 4)}),
	} {
		m, _ = m.Update(msg)
	}
	return settleBodies(m)
}

// withForeground puts the view's default text color into its content: a
// terminal gets it separately (v.ForegroundColor), but freeze only sees the
// content, and it styles each SGR sequence on its own and resets at each
// line. So every SGR without a text color gets the default one, and so does
// every line.
func withForeground(v tea.View) string {
	if v.ForegroundColor == nil {
		return v.Content
	}
	r, g, b, _ := v.ForegroundColor.RGBA()
	fg := fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
	out := sgr.ReplaceAllStringFunc(v.Content, func(seq string) string {
		params := seq[2 : len(seq)-1]
		if strings.Contains(params, "38;") {
			return seq
		}
		if params == "" {
			params = "0"
		}
		return "\x1b[" + params + ";" + fg + "m"
	})
	return "\x1b[" + fg + "m" + strings.ReplaceAll(out, "\n", "\n\x1b["+fg+"m")
}

var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*m`)
