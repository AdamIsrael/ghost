package preflight

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// fake answers commands keyed by "name arg1 arg2".
func fake(responses map[string]error) Runner {
	return func(_ context.Context, name string, args ...string) (string, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		if err, ok := responses[key]; ok && err != nil {
			return "", err
		}
		return name + " version 1.0", nil
	}
}

func find(rs []Result, name string) Result {
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}
	return Result{}
}

func TestRunAllOK(t *testing.T) {
	rs := Run(context.Background(), fake(nil))
	if len(rs) != 3 || !AllOK(rs) {
		t.Fatalf("want 3 passing results, got %+v", rs)
	}
	if rs[0].Name != "git" || rs[1].Name != "gh" || rs[2].Name != "claude" {
		t.Errorf("order = %s,%s,%s", rs[0].Name, rs[1].Name, rs[2].Name)
	}
	if find(rs, "git").Detail != "git version 1.0" {
		t.Errorf("detail = %q", find(rs, "git").Detail)
	}
}

func TestRunMissingBinary(t *testing.T) {
	rs := Run(context.Background(), fake(map[string]error{"claude --version": exec.ErrNotFound}))
	r := find(rs, "claude")
	if r.OK || !strings.Contains(r.Detail, "not found") || r.Fix == "" {
		t.Fatalf("claude result = %+v", r)
	}
	if AllOK(rs) {
		t.Error("AllOK should be false")
	}
}

func TestRunGhNotAuthenticated(t *testing.T) {
	rs := Run(context.Background(), fake(map[string]error{"gh auth status": errors.New("exit status 1")}))
	r := find(rs, "gh")
	if r.OK || !strings.Contains(r.Fix, "gh auth login") {
		t.Fatalf("gh result = %+v", r)
	}
}
