package engine

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// hosts.go is the thin I/O for service discovery (Phase 8): read and write a
// container's /etc/hosts via one-shot `container exec`. The gnarly logic (what to
// write) lives in the pure `discovery` package; this file only moves bytes. Both
// operations are only meaningful against a RUNNING container — a stopped one has
// no writable /etc/hosts and no IP.

// readHostsArgs / writeHostsArgs are pure so the exact exec surface is unit-
// testable without spawning the runtime.
func readHostsArgs(id string) []string {
	return []string{"exec", id, "cat", "/etc/hosts"}
}

func writeHostsArgs(id string) []string {
	// A SINGLE exec that replaces the whole file from stdin. `cat > /etc/hosts`
	// truncates-then-writes in one shot, so a mid-write failure leaves the prior
	// file intact — never a half-written one. Never write line-by-line.
	return []string{"exec", "-i", id, "sh", "-c", "cat > /etc/hosts"}
}

// ReadHosts returns the container's /etc/hosts contents.
func (e *CLIEngine) ReadHosts(ctx context.Context, id string) (string, error) {
	out, err := e.run(ctx, readHostsArgs(id)...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// WriteHosts replaces the container's /etc/hosts with content in a single exec,
// piping content to the child's stdin. Atomic-ish: the file is truncated and
// rewritten by one `cat`, so a failure can't leave a partial managed block.
func (e *CLIEngine) WriteHosts(ctx context.Context, id, content string) error {
	args := writeHostsArgs(id)
	cmd := exec.CommandContext(ctx, e.Bin, args...)
	cmd.Stdin = bytes.NewReader([]byte(content))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		exitCode := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		}
		kind, msg := classify(stderr.String())
		return &CLIError{Args: args, ExitCode: exitCode, Kind: kind, Message: msg, Raw: stderr.String()}
	}
	return nil
}
