package status

import "testing"

func TestDerive(t *testing.T) {
	cases := []struct {
		name     string
		draft    bool
		decision string
		ci       string
		want     Status
	}{
		{"draft wins over everything", true, "APPROVED", "FAILURE", Draft},
		{"changes requested beats CI", false, "CHANGES_REQUESTED", "FAILURE", ChangesRequested},
		{"ci failure", false, "", "FAILURE", CIFailing},
		{"ci error", false, "REVIEW_REQUIRED", "ERROR", CIFailing},
		{"ci pending", false, "", "PENDING", WaitingOnCI},
		{"ci expected", false, "", "EXPECTED", WaitingOnCI},
		{"approved but ci pending waits", false, "APPROVED", "PENDING", WaitingOnCI},
		{"approved and green", false, "APPROVED", "SUCCESS", Approved},
		{"approved, no checks", false, "APPROVED", "", Approved},
		{"ready: green, awaiting review", false, "REVIEW_REQUIRED", "SUCCESS", Ready},
		{"ready: no checks, no decision", false, "", "", Ready},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Derive(tc.draft, tc.decision, tc.ci); got != tc.want {
				t.Errorf("Derive = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStringAndIcon(t *testing.T) {
	for _, s := range []Status{Ready, Draft, WaitingOnCI, CIFailing, ChangesRequested, Approved} {
		if s.String() == "" || s.String() == "unknown" || s.Icon() == "" {
			t.Errorf("status %d missing name or icon", s)
		}
	}
	if ChangesRequested.String() != "Changes requested" || WaitingOnCI.String() != "Waiting on CI" {
		t.Error("unexpected status names")
	}
}
