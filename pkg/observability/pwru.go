// Package observability integrates Cilium's pwru tool for live packet tracing.
package observability

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
)

// PWRU runs the pwru binary (must be in PATH) with the supplied arguments and
// streams its output to w until ctx is cancelled or pwru exits.
// Example args: []string{"--filter-dst-ip", "10.244.0.5"}
func PWRU(ctx context.Context, w io.Writer, args ...string) error {
	path, err := exec.LookPath("pwru")
	if err != nil {
		return fmt.Errorf("pwru not found in PATH: %w", err)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stderr = w
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start pwru: %w", err)
	}
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		fmt.Fprintln(w, scanner.Text())
	}
	return cmd.Wait()
}
