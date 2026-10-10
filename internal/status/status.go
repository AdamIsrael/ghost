// Package status derives a pull request's display status from GitHub fields.
package status

type Status int

const (
	Ready Status = iota
	Draft
	WaitingOnCI
	CIFailing
	ChangesRequested
	Approved
)

// Derive maps GitHub's isDraft, reviewDecision and statusCheckRollup.state to
// a Status. Precedence: draft, changes requested, CI failing, CI pending,
// approved, ready. A PR with no checks counts as green.
func Derive(isDraft bool, reviewDecision, ciState string) Status {
	switch {
	case isDraft:
		return Draft
	case reviewDecision == "CHANGES_REQUESTED":
		return ChangesRequested
	case ciState == "FAILURE" || ciState == "ERROR":
		return CIFailing
	case ciState == "PENDING" || ciState == "EXPECTED":
		return WaitingOnCI
	case reviewDecision == "APPROVED":
		return Approved
	default:
		return Ready
	}
}

var info = map[Status]struct{ name, icon string }{
	Ready:            {"Ready", "●"},
	Draft:            {"Draft", "✎"},
	WaitingOnCI:      {"Waiting on CI", "◐"},
	CIFailing:        {"CI failing", "✗"},
	ChangesRequested: {"Changes requested", "↺"},
	Approved:         {"Approved", "✓"},
}

func (s Status) String() string {
	if i, ok := info[s]; ok {
		return i.name
	}
	return "unknown"
}

func (s Status) Icon() string {
	if i, ok := info[s]; ok {
		return i.icon
	}
	return "?"
}
