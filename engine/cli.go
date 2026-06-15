package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"

	"github.com/creack/pty"
)

// CLIEngine implements Engine by shelling out to the `container` binary and
// parsing `--format json`. It is the only place in Porthole that knows the CLI
// exists.
type CLIEngine struct {
	// Bin is the path to the container binary. Defaults to "container".
	Bin string
}

// NewCLIEngine returns a CLIEngine using the given binary path, or "container"
// on PATH when empty.
func NewCLIEngine(bin string) *CLIEngine {
	if bin == "" {
		bin = "container"
	}
	return &CLIEngine{Bin: bin}
}

// run executes `container <args...>`, returning stdout. On a non-zero exit it
// returns a *CLIError carrying the classified kind and raw stderr.
func (e *CLIEngine) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, e.Bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		exitCode := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		}
		kind, msg := classify(stderr.String())
		return nil, &CLIError{
			Args:     args,
			ExitCode: exitCode,
			Kind:     kind,
			Message:  msg,
			Raw:      stderr.String(),
		}
	}
	return stdout.Bytes(), nil
}

// runJSON executes a read command and unmarshals stdout into v.
func (e *CLIEngine) runJSON(ctx context.Context, v any, args ...string) error {
	out, err := e.run(ctx, args...)
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

// --- System --------------------------------------------------------------

func (e *CLIEngine) SystemVersion(ctx context.Context) ([]VersionEntry, error) {
	var v []VersionEntry
	err := e.runJSON(ctx, &v, "system", "version", "--format", "json")
	return v, err
}

func (e *CLIEngine) DiskUsage(ctx context.Context) (DiskUsage, error) {
	var d DiskUsage
	err := e.runJSON(ctx, &d, "system", "df", "--format", "json")
	return d, err
}

// SystemStatus detects whether the runtime is usable. We treat a successful
// `system version` as the liveness signal, and surface daemon-down as a clean
// status rather than an error so the UI can show the bootstrap banner.
func (e *CLIEngine) SystemStatus(ctx context.Context) (SystemStatus, error) {
	v, err := e.SystemVersion(ctx)
	if err != nil {
		var ce *CLIError
		if errors.As(err, &ce) && ce.Kind == ErrDaemonDown {
			return SystemStatus{APIServerRunning: false, Detail: ce.Message}, nil
		}
		return SystemStatus{}, err
	}
	return SystemStatus{APIServerRunning: true, CLIVersion: CLIVersion(v)}, nil
}

// --- Containers (reads) --------------------------------------------------

func (e *CLIEngine) ListContainers(ctx context.Context, all bool) ([]Container, error) {
	args := []string{"ls", "--format", "json"}
	if all {
		args = []string{"ls", "--all", "--format", "json"}
	}
	var cs []Container
	err := e.runJSON(ctx, &cs, args...)
	return cs, err
}

func (e *CLIEngine) InspectContainer(ctx context.Context, id string) (Container, error) {
	// inspect returns an array with a single element (same shape as ls).
	var cs []Container
	if err := e.runJSON(ctx, &cs, "inspect", id); err != nil {
		return Container{}, err
	}
	if len(cs) == 0 {
		return Container{}, &CLIError{Args: []string{"inspect", id}, Kind: ErrNotFound, Message: "no such container"}
	}
	return cs[0], nil
}

func (e *CLIEngine) Stats(ctx context.Context, ids ...string) ([]Stats, error) {
	args := append([]string{"stats", "--no-stream", "--format", "json"}, ids...)
	var s []Stats
	err := e.runJSON(ctx, &s, args...)
	return s, err
}

// --- Networks (reads) ----------------------------------------------------

func (e *CLIEngine) ListNetworks(ctx context.Context) ([]Network, error) {
	var ns []Network
	err := e.runJSON(ctx, &ns, "network", "ls", "--format", "json")
	return ns, err
}

func (e *CLIEngine) InspectNetwork(ctx context.Context, id string) (Network, error) {
	var ns []Network
	if err := e.runJSON(ctx, &ns, "network", "inspect", id); err != nil {
		return Network{}, err
	}
	if len(ns) == 0 {
		return Network{}, &CLIError{Args: []string{"network", "inspect", id}, Kind: ErrNotFound, Message: "no such network"}
	}
	return ns[0], nil
}

// --- Containers (mutations) — Phase 1 -------------------------------------

func (e *CLIEngine) StartContainer(ctx context.Context, id string) error {
	_, err := e.run(ctx, "start", id)
	return err
}

func (e *CLIEngine) StopContainer(ctx context.Context, id string) error {
	_, err := e.run(ctx, "stop", id)
	return err
}

func (e *CLIEngine) KillContainer(ctx context.Context, id, signal string) error {
	args := []string{"kill", id}
	if signal != "" {
		args = []string{"kill", "--signal", signal, id}
	}
	_, err := e.run(ctx, args...)
	return err
}

func (e *CLIEngine) DeleteContainer(ctx context.Context, id string, force bool) error {
	args := []string{"delete", id}
	if force {
		args = []string{"delete", "--force", id}
	}
	_, err := e.run(ctx, args...)
	return err
}

// --- Streams — Phase 2 ----------------------------------------------------

// Logs spawns `container logs [-n N] [-f] <id>` and returns a reader over the
// child's merged stdout+stderr. The child is bound to ctx via CommandContext, so
// canceling ctx kills it; Close() also force-kills and reaps. This is the only
// reliable teardown for a follow stream, because `container logs -f` does not
// exit when the container stops (spec §7.4).
func (e *CLIEngine) Logs(ctx context.Context, id string, opts LogOpts) (io.ReadCloser, error) {
	args := []string{"logs"}
	if opts.Follow {
		// Follow ALWAYS pins -n (even 0) so `logs -f` does NOT replay the whole
		// backlog: `-n 0 -f` follows NEW lines only (confirmed live). The history
		// read (`-n N`, non-follow) owns the backlog; the follower owns new lines.
		// This is the PF2 fix for the history/follow duplication.
		args = append(args, "-n", strconv.Itoa(opts.Tail), "-f")
	} else if opts.Tail > 0 {
		args = append(args, "-n", strconv.Itoa(opts.Tail))
	}
	args = append(args, id)

	cmd := exec.CommandContext(ctx, e.Bin, args...)
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	// The runtime already merges stderr into its stdout, but wire both to the
	// same pipe so a merged stream is guaranteed regardless.
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return nil, err
	}
	// The child holds its own dup of pw; close the parent's copy so the reader
	// sees EOF once the child's fds close.
	_ = pw.Close()
	return &cmdReadCloser{pr: pr, cmd: cmd}, nil
}

// cmdReadCloser reads a child's output and, on Close, force-kills + reaps it.
type cmdReadCloser struct {
	pr   *os.File
	cmd  *exec.Cmd
	once sync.Once
}

func (c *cmdReadCloser) Read(p []byte) (int, error) { return c.pr.Read(p) }

func (c *cmdReadCloser) Close() error {
	c.once.Do(func() {
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill() // hang-on-stop: never waits for self-exit
		}
		_ = c.pr.Close()
		_ = c.cmd.Wait() // reap; ignore "signal: killed"
	})
	return nil
}

// Exec opens an interactive `container exec -it <id> <cmd...>` attached to a host
// PTY (creack/pty). §8.1 confirmed this yields a real TTY; §8.2/§8.5 require an
// initial size so the shell doesn't start at 0×0.
func (e *CLIEngine) Exec(ctx context.Context, id string, cmd []string, initial WinSize) (ExecSession, error) {
	if len(cmd) == 0 {
		cmd = []string{"/bin/sh"}
	}
	if initial.Cols == 0 {
		initial.Cols = 80
	}
	if initial.Rows == 0 {
		initial.Rows = 24
	}
	args := append([]string{"exec", "-it", id}, cmd...)
	c := exec.CommandContext(ctx, e.Bin, args...)
	ptmx, err := pty.StartWithSize(c, &pty.Winsize{Cols: initial.Cols, Rows: initial.Rows})
	if err != nil {
		return nil, err
	}
	return &ptySession{ptmx: ptmx, cmd: c}, nil
}

// ptySession wraps the PTY master + child. Close force-kills the child and closes
// the master — both, no leak (spec §6).
type ptySession struct {
	ptmx *os.File
	cmd  *exec.Cmd
	once sync.Once
}

func (s *ptySession) Read(p []byte) (int, error)  { return s.ptmx.Read(p) }
func (s *ptySession) Write(p []byte) (int, error) { return s.ptmx.Write(p) }

func (s *ptySession) Resize(cols, rows uint16) error {
	return pty.Setsize(s.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

func (s *ptySession) Close() error {
	s.once.Do(func() {
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		_ = s.ptmx.Close()
		_ = s.cmd.Wait()
	})
	return nil
}

var _ ExecSession = (*ptySession)(nil)

// Compile-time assertion that CLIEngine satisfies Engine.
var _ Engine = (*CLIEngine)(nil)
