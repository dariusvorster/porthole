package engine

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// Engine is the single abstraction every handler talks to. v1 ships exactly
// one implementation, CLIEngine. A future NativeEngine (a Swift sidecar over
// the apiserver socket) can satisfy the same interface with no change to
// handlers, the API contract, or the SQLite schema. Nothing else in the
// codebase may call `container` directly.
type Engine interface {
	// System
	SystemVersion(ctx context.Context) ([]VersionEntry, error)
	SystemStatus(ctx context.Context) (SystemStatus, error)
	DiskUsage(ctx context.Context) (DiskUsage, error)

	// Containers (reads)
	ListContainers(ctx context.Context, all bool) ([]Container, error)
	InspectContainer(ctx context.Context, id string) (Container, error)
	Stats(ctx context.Context, ids ...string) ([]Stats, error)

	// Containers (mutations) — signatures only in Phase 0; implemented in Phase 1.
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string) error
	KillContainer(ctx context.Context, id, signal string) error
	DeleteContainer(ctx context.Context, id string, force bool) error

	// Streams — Phase 2. Logs spawns `container logs` and returns a reader over
	// the child's merged stdout+stderr; canceling ctx (or closing the reader)
	// kills the child — the kill lever the logs endpoint relies on, since
	// `container logs -f` does NOT exit when the container stops (spec §7.4).
	Logs(ctx context.Context, id string, opts LogOpts) (io.ReadCloser, error)

	// Exec opens an interactive PTY session (`container exec -it`) into a running
	// container (spec §8.1, confirmed real). The returned session reads PTY output
	// and writes stdin; canceling ctx or Close() reaps BOTH the child and the PTY.
	Exec(ctx context.Context, id string, cmd []string, initial WinSize) (ExecSession, error)

	// Networks / Images / Volumes (reads)
	ListNetworks(ctx context.Context) ([]Network, error)
	InspectNetwork(ctx context.Context, id string) (Network, error)
	// ListImages / ListVolumes intentionally omitted until their JSON shape is
	// captured — do not guess these.
}

// SystemStatus is Porthole's own health summary of the container runtime.
// The exact JSON of `container system status` was not captured yet; this is
// the Porthole-side shape the handler returns regardless of how we detect it
// (status command exit code and/or `launchctl list | grep com.apple.container`).
type SystemStatus struct {
	APIServerRunning bool   `json:"apiServerRunning"`
	CLIVersion       string `json:"cliVersion"`
	Detail           string `json:"detail,omitempty"`
}

// ---------------------------------------------------------------------------
// Typed error model.
//
// The CLI emits unstructured stderr + a non-zero exit code, e.g.:
//   Error: Unknown option '--bogusflag'
// The wrapping layer captures both, classifies the common cases, and always
// preserves raw stderr behind Raw for a "details" disclosure in the UI.
// ---------------------------------------------------------------------------

// LogOpts configures a Logs invocation. Tail>0 prepends `-n <Tail>` (last N
// lines — the runtime has no `--tail`); Follow adds `-f` to stream live.
type LogOpts struct {
	Tail   int
	Follow bool
}

// WinSize is a terminal window size. A zero value means "use a default" (80×24)
// to dodge the 0×0 startup window the capture surfaced (spec §8.2/§8.5).
type WinSize struct {
	Cols uint16
	Rows uint16
}

// ExecSession is a live interactive PTY session: Read = terminal output,
// Write = stdin, Resize = SIGWINCH, Close = reap child + PTY.
type ExecSession interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
}

type ErrorKind string

const (
	ErrUnknown         ErrorKind = "unknown"
	ErrDaemonDown      ErrorKind = "daemon_down"
	ErrNotFound        ErrorKind = "not_found"
	ErrNameConflict    ErrorKind = "name_conflict"
	ErrNotRunning      ErrorKind = "not_running"
	ErrInvalidState    ErrorKind = "invalid_state"
	ErrUnknownOption   ErrorKind = "unknown_option"
	ErrImagePullFailed ErrorKind = "image_pull_failed"
	ErrVolumeInUse     ErrorKind = "volume_in_use"
)

// CLIError is the typed error surfaced from any failed `container` invocation.
type CLIError struct {
	Args     []string
	ExitCode int
	Kind     ErrorKind
	Message  string // the first "Error: ..." line, cleaned
	Raw      string // full raw stderr, never discarded
}

func (e *CLIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("container %s: %s", strings.Join(e.Args, " "), e.Message)
	}
	return fmt.Sprintf("container %s: exit %d", strings.Join(e.Args, " "), e.ExitCode)
}

// classify maps raw stderr to a typed kind. Every case below is backed by a
// real captured `container` failure (see classify_test.go for the verbatim
// strings). Order matters: daemon-down is checked before not_found because a
// down apiserver can surface in many command contexts.
func classify(stderr string) (ErrorKind, string) {
	line := firstErrorLine(stderr)
	low := strings.ToLower(line)
	full := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "unknown option"):
		return ErrUnknownOption, line
	// Daemon-down, verified against real stderr:
	//   Error: internalError: "failed to list containers"
	//     (cause: "interrupted: "XPC connection error: Connection invalid"")
	//   Ensure container system service has been started with `container system start`.
	// The XPC phrase is on the first Error: line; the hint phrase is on a later
	// line, so we check the full stderr for it as a robust fallback.
	case strings.Contains(low, "xpc connection") ||
		strings.Contains(low, "connection invalid") ||
		strings.Contains(full, "container system start") ||
		strings.Contains(full, "system service has been started") ||
		strings.Contains(low, "apiserver"):
		return ErrDaemonDown, line
	// image_pull_failed, verified from a run of a nonexistent image (create spec
	// §8.6). Because run auto-pulls, a missing/typo'd/private image fails as a
	// registry pull error — Docker Hub returns 401 for nonexistent repos:
	//   Error: HTTP request to https://registry-1.docker.io/v2/library/<x>/
	//   manifests/latest failed with response: 401 Unauthorized … no credentials
	//   found for host registry-1.docker.io
	// Distinct from not_found (a missing container). Checked before not_found so a
	// registry error never falls through to it.
	case strings.Contains(full, "no credentials found") ||
		(strings.Contains(full, "manifests") && (strings.Contains(full, "401") || strings.Contains(full, "unauthorized"))) ||
		(strings.Contains(full, "failed with response") && strings.Contains(full, "registry")):
		return ErrImagePullFailed, line
	// not_found, verified from two shapes:
	//   inspect: `Error: container not found: <id>`
	//   stop:    `Error: internalError: "failed to stop container"
	//             (cause: "notFound: "container with ID <id> not found"")`
	// The stop phrasing buries it in a nested cause, so we scan the full
	// lowercased stderr for "not found" or the cause token "notfound:".
	case strings.Contains(full, "not found") ||
		strings.Contains(full, "notfound:") ||
		strings.Contains(low, "no such"):
		return ErrNotFound, line
	// name_conflict, verified: `Error: container with id web already exists`.
	case strings.Contains(low, "already exists"):
		return ErrNameConflict, line
	// invalid_state, verified from deleting a running container:
	//   `internalError: ... (cause: "invalidState: "container web is running
	//    and can not be deleted"")`. Distinct from not_running (that is exec on a
	//   stopped container). Scanned on the full stderr — it lives in a nested
	//   cause, like notFound.
	case strings.Contains(full, "invalidstate:") || strings.Contains(full, "can not be deleted"):
		return ErrInvalidState, line
	// volume_in_use, verified from deleting a volume mounted by a container
	// (running OR stopped, resources spec §8.3):
	//   failed to delete volume: [..."volume 'capvol' is currently in use and
	//   cannot be accessed by another container, or deleted"...]
	// NOTE: there is NO image-in-use equivalent — image delete is never refused.
	case strings.Contains(full, "is currently in use"):
		return ErrVolumeInUse, line
	// not_running applies to exec only: `Error: container <id> is not running`.
	// NOTE: `stop` on an already-stopped container exits 0 (idempotent), so it
	// never reaches the classifier — there is no not_running case for stop.
	case strings.Contains(low, "is not running") || strings.Contains(low, "not running"):
		return ErrNotRunning, line
	default:
		return ErrUnknown, line
	}
}

// firstErrorLine returns the first line beginning with "Error:" (trimmed), or
// the first non-empty line as a fallback.
func firstErrorLine(stderr string) string {
	var fallback string
	for _, raw := range strings.Split(stderr, "\n") {
		l := strings.TrimSpace(raw)
		if l == "" {
			continue
		}
		if fallback == "" {
			fallback = l
		}
		if strings.HasPrefix(l, "Error:") {
			return strings.TrimSpace(strings.TrimPrefix(l, "Error:"))
		}
	}
	return fallback
}
