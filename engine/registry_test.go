package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeToken = "dckr_pat_SUPERSECRET_should_never_leak"

// TestRegistryLoginTokenToStdinNotArgs is the load-bearing secret test: the token
// must travel over the child's STDIN, never appear in the argument list.
func TestRegistryLoginTokenToStdinNotArgs(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stdinFile := filepath.Join(dir, "stdin.txt")
	// Fake `container` records its argv and its stdin to separate files.
	bin := writeScriptBin(t, `printf '%s\n' "$@" > "`+argsFile+`"; cat > "`+stdinFile+`"`)

	err := NewCLIEngine(bin).RegistryLogin(
		context.Background(), "registry-1.docker.io", "alice", strings.NewReader(fakeToken),
	)
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	argv, _ := os.ReadFile(argsFile)
	stdin, _ := os.ReadFile(stdinFile)

	if strings.Contains(string(argv), fakeToken) {
		t.Fatalf("SECRET LEAK: token appeared in argv: %q", argv)
	}
	if !strings.Contains(string(argv), "alice") || !strings.Contains(string(argv), "registry-1.docker.io") {
		t.Errorf("argv missing user/host: %q", argv)
	}
	if !strings.Contains(string(argv), "--password-stdin") {
		t.Errorf("argv missing --password-stdin: %q", argv)
	}
	if strings.TrimSpace(string(stdin)) != fakeToken {
		t.Errorf("token did not reach stdin: got %q", stdin)
	}
}

func TestRegistryLoginArgsHasNoSecret(t *testing.T) {
	// The args builder doesn't even take the token — structurally leak-proof.
	args := registryLoginArgs("bob", "ghcr.io")
	for _, a := range args {
		if strings.Contains(a, fakeToken) {
			t.Fatalf("token in args: %v", args)
		}
	}
	if strings.Join(args, " ") != "registry login --password-stdin -u bob ghcr.io" {
		t.Errorf("args = %v", args)
	}
}

// TestRegistryLoginErrorScrubbed: a failed login must NOT echo stderr (which could
// contain the supplied token); the message is fixed and Raw is empty.
func TestRegistryLoginErrorScrubbed(t *testing.T) {
	// Fake bin echoes a token-like string to stderr and fails.
	bin := writeScriptBin(t, `echo "denied for token `+fakeToken+`" >&2; exit 1`)
	err := NewCLIEngine(bin).RegistryLogin(
		context.Background(), "registry-1.docker.io", "alice", strings.NewReader(fakeToken),
	)
	if err == nil {
		t.Fatal("expected login failure")
	}
	var ce *CLIError
	if !errors.As(err, &ce) || ce.Kind != ErrRegistryLoginFailed {
		t.Fatalf("want registry_login_failed, got %v", err)
	}
	if strings.Contains(ce.Message, fakeToken) || strings.Contains(ce.Raw, fakeToken) {
		t.Fatalf("SECRET LEAK in error: msg=%q raw=%q", ce.Message, ce.Raw)
	}
	if ce.Raw != "" {
		t.Errorf("Raw should be scrubbed empty, got %q", ce.Raw)
	}
}

func TestRegistryList(t *testing.T) {
	bin := writeScriptBin(t, `cat <<'EOF'
[{"id":"registry-1.docker.io","name":"registry-1.docker.io","username":"butterfingerza","creationDate":"2026-06-15T18:57:57Z","modificationDate":"2026-06-15T18:57:57Z","labels":{}}]
EOF`)
	logins, err := NewCLIEngine(bin).RegistryList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(logins) != 1 {
		t.Fatalf("want 1 login, got %d", len(logins))
	}
	if logins[0].Host != "registry-1.docker.io" || logins[0].Username != "butterfingerza" {
		t.Errorf("login = %+v", logins[0])
	}
	if logins[0].Created.Year() != 2026 {
		t.Errorf("created = %v", logins[0].Created)
	}
}

func TestRegistryLogoutArgs(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "a.txt")
	bin := writeScriptBin(t, `printf '%s\n' "$@" > "`+argsFile+`"`)
	if err := NewCLIEngine(bin).RegistryLogout(context.Background(), "registry-1.docker.io"); err != nil {
		t.Fatal(err)
	}
	argv, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(argv), "registry") || !strings.Contains(string(argv), "logout") || !strings.Contains(string(argv), "registry-1.docker.io") {
		t.Errorf("logout argv = %q", argv)
	}
}
