// Package policy decides whether a reviewed PR may be approved automatically.
package policy

import (
	"fmt"
	"strings"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
)

type Input struct {
	PR       github.PR
	Verdict  string
	Comments []agent.InlineComment
}

type Decision struct {
	Eligible bool
	// Waiting: not eligible now, but only for reasons that can change on this
	// commit (CI, draft, a human's requested changes), so re-check later.
	Waiting bool
	Reasons []string
}

func (d Decision) Summary() string { return strings.Join(d.Reasons, "; ") }

// Evaluate applies the [auto_approve] rules. CI counts as green when GitHub
// reports SUCCESS, or when there are no checks and no_checks_is_green is set.
func Evaluate(c config.AutoApprove, in Input) Decision {
	var final, transient []string
	if !c.Enabled {
		final = append(final, "auto-approve is disabled")
	}
	if in.Verdict != string(agent.Approve) {
		final = append(final, fmt.Sprintf("agent verdict is %s", in.Verdict))
	}
	if c.NoBlockingComments {
		n := 0
		for _, cm := range in.Comments {
			if cm.Severity == "blocking" {
				n++
			}
		}
		if n > 0 {
			final = append(final, fmt.Sprintf("%d blocking comments", n))
		}
	}
	if lines := in.PR.Additions + in.PR.Deletions; c.MaxChangedLines > 0 && lines > c.MaxChangedLines {
		final = append(final, fmt.Sprintf("%d changed lines is over the limit %d", lines, c.MaxChangedLines))
	}
	if in.PR.IsDraft {
		transient = append(transient, "PR is a draft")
	}
	green := in.PR.CIState == "SUCCESS" || (in.PR.CIState == "" && c.NoChecksIsGreen)
	if c.RequireCIGreen && !green {
		state := strings.ToLower(in.PR.CIState)
		if state == "" {
			state = "missing (no checks)"
		}
		transient = append(transient, "CI is "+state)
	}
	if c.NoChangesRequested && in.PR.ReviewDecision == "CHANGES_REQUESTED" {
		transient = append(transient, "a reviewer requested changes")
	}
	reasons := append(final, transient...)
	return Decision{Eligible: len(reasons) == 0, Waiting: len(final) == 0 && len(transient) > 0, Reasons: reasons}
}
