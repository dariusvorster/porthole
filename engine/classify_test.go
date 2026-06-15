package engine

import "testing"

// TestClassify pins the error classifier to real captured `container` stderr.
// Each string is verbatim from an actual failure (see C1 notes).
func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   ErrorKind
	}{
		{
			name:   "inspect_not_found",
			stderr: `Error: container not found: nonexistent-xyz`,
			want:   ErrNotFound,
		},
		{
			name:   "stop_not_found_nested_cause",
			stderr: `Error: internalError: "failed to stop container" (cause: "notFound: "container with ID nonexistent-xyz not found"")`,
			want:   ErrNotFound,
		},
		{
			name:   "name_conflict",
			stderr: `Error: container with id web already exists`,
			want:   ErrNameConflict,
		},
		{
			name:   "exec_not_running",
			stderr: `Error: container api is not running`,
			want:   ErrNotRunning,
		},
		{
			name:   "delete_running_invalid_state",
			stderr: `Error: internalError: "failed to delete container" (cause: "invalidState: "container web is running and can not be deleted"")`,
			want:   ErrInvalidState,
		},
		{
			name:   "unknown_option",
			stderr: `Error: Unknown option '--bogusflag'`,
			want:   ErrUnknownOption,
		},
		{
			name: "daemon_down",
			stderr: "Error: internalError: \"failed to list containers\" (cause: \"interrupted: \"XPC connection error: Connection invalid\"\")\n" +
				"Ensure container system service has been started with `container system start`.",
			want: ErrDaemonDown,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := classify(c.stderr)
			if got != c.want {
				t.Errorf("classify(%q) kind = %s, want %s", c.stderr, got, c.want)
			}
		})
	}
}
