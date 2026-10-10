package policy

import (
	"strings"
	"testing"

	"github.com/AdamIsrael/ghost/internal/agent"
	"github.com/AdamIsrael/ghost/internal/config"
	"github.com/AdamIsrael/ghost/internal/github"
)

func rules() config.AutoApprove {
	return config.AutoApprove{Enabled: true, RequireConfirm: true, RequireCIGreen: true,
		NoBlockingComments: true, NoChangesRequested: true, MaxChangedLines: 100}
}

func good() Input {
	return Input{
		PR:       github.PR{CIState: "SUCCESS", ReviewDecision: "REVIEW_REQUIRED", Additions: 30, Deletions: 10},
		Verdict:  "approve",
		Comments: []agent.InlineComment{{Severity: "nit"}},
	}
}

func TestEligible(t *testing.T) {
	d := Evaluate(rules(), good())
	if !d.Eligible || d.Waiting || len(d.Reasons) != 0 {
		t.Fatalf("decision = %+v", d)
	}
}

func TestFinalReasons(t *testing.T) {
	cases := map[string]func(*config.AutoApprove, *Input){
		"disabled":        func(c *config.AutoApprove, _ *Input) { c.Enabled = false },
		"verdict comment": func(_ *config.AutoApprove, in *Input) { in.Verdict = "comment" },
		"blocking":        func(_ *config.AutoApprove, in *Input) { in.Comments = []agent.InlineComment{{Severity: "blocking"}} },
		"too large":       func(_ *config.AutoApprove, in *Input) { in.PR.Additions = 95 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c, in := rules(), good()
			mutate(&c, &in)
			d := Evaluate(c, in)
			if d.Eligible || d.Waiting || len(d.Reasons) == 0 {
				t.Fatalf("decision = %+v", d)
			}
		})
	}
}

func TestTransientReasonsWait(t *testing.T) {
	cases := map[string]func(*Input){
		"ci pending":        func(in *Input) { in.PR.CIState = "PENDING" },
		"ci failing":        func(in *Input) { in.PR.CIState = "FAILURE" },
		"no checks":         func(in *Input) { in.PR.CIState = "" },
		"draft":             func(in *Input) { in.PR.IsDraft = true },
		"changes requested": func(in *Input) { in.PR.ReviewDecision = "CHANGES_REQUESTED" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := good()
			mutate(&in)
			d := Evaluate(rules(), in)
			if d.Eligible || !d.Waiting || len(d.Reasons) != 1 {
				t.Fatalf("decision = %+v", d)
			}
		})
	}
}

func TestFinalBeatsTransient(t *testing.T) {
	in := good()
	in.Verdict = "request_changes"
	in.PR.CIState = "PENDING"
	if d := Evaluate(rules(), in); d.Waiting || len(d.Reasons) != 2 {
		t.Fatalf("a final reason must stop waiting: %+v", d)
	}
}

func TestRulesCanBeTurnedOff(t *testing.T) {
	c := rules()
	c.RequireCIGreen, c.NoBlockingComments, c.NoChangesRequested, c.MaxChangedLines = false, false, false, 0
	in := good()
	in.PR.CIState, in.PR.ReviewDecision, in.PR.Additions = "FAILURE", "CHANGES_REQUESTED", 10000
	in.Comments = []agent.InlineComment{{Severity: "blocking"}}
	if d := Evaluate(c, in); !d.Eligible {
		t.Fatalf("disabled rules still applied: %+v", d)
	}
}

func TestNoChecksIsGreenOption(t *testing.T) {
	c := rules()
	in := good()
	in.PR.CIState = ""
	if d := Evaluate(c, in); d.Eligible {
		t.Fatal("no checks must not be green by default")
	}
	c.NoChecksIsGreen = true
	if d := Evaluate(c, in); !d.Eligible {
		t.Fatalf("no_checks_is_green = true should allow it: %+v", d)
	}
	in.PR.CIState = "PENDING"
	if d := Evaluate(c, in); d.Eligible {
		t.Fatal("the option must not relax pending CI")
	}
}

func TestSummaryIsReadable(t *testing.T) {
	in := good()
	in.PR.CIState = "PENDING"
	in.PR.Additions = 500
	s := Evaluate(rules(), in).Summary()
	if !strings.Contains(s, "510 changed lines") || !strings.Contains(s, "limit 100") || !strings.Contains(s, "CI is pending") {
		t.Errorf("summary = %q", s)
	}
}
