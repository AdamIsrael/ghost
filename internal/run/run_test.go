package run

import (
	"context"
	"strings"
	"testing"
)

func TestExecPassesDirStdinAndSplitsOutput(t *testing.T) {
	dir := t.TempDir()
	out, errOut, err := Exec(context.Background(), dir, "hello", "sh", "-c", "cat; pwd; echo oops >&2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "hello") || !strings.Contains(string(out), dir) {
		t.Errorf("stdout = %q", out)
	}
	if strings.TrimSpace(string(errOut)) != "oops" {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestExecReportsExitError(t *testing.T) {
	if _, _, err := Exec(context.Background(), "", "", "sh", "-c", "exit 3"); err == nil {
		t.Fatal("want error for non-zero exit")
	}
}
