// Package run executes subprocesses; tests substitute a fake Func.
package run

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
)

// Func runs name with args in dir (empty = current), feeding stdin.
type Func func(ctx context.Context, dir, stdin, name string, args ...string) (stdout, stderr []byte, err error)

// Exec is the real Func.
func Exec(ctx context.Context, dir, stdin, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}
