package engine

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"time"
)

// RegistryAuth is one stored registry login from `registry list` — host +
// username only (NO secret; the credential lives in container's own store). Used
// to render login STATE.
type RegistryAuth struct {
	Host     string    `json:"host"`
	Username string    `json:"username"`
	Created  time.Time `json:"created"`
}

// rawRegistry mirrors `container registry list --format json` (registry spec §8):
// [{ id, name, username, creationDate, modificationDate, labels }]. id == name ==
// the host (e.g. registry-1.docker.io).
type rawRegistry struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Username     string    `json:"username"`
	CreationDate time.Time `json:"creationDate"`
}

// registryLoginArgs builds the login argv. NOTE the token is DELIBERATELY absent —
// it travels over stdin (--password-stdin), never the argument list, so it can
// never leak via a process listing / shell history / a logged command. Exported-
// for-test boundary: a test asserts these args carry user+host but no secret.
func registryLoginArgs(user, host string) []string {
	return []string{"registry", "login", "--password-stdin", "-u", user, host}
}

// RegistryLogin authenticates to a registry. The token is an io.Reader piped to
// the child's STDIN — it is NOT a string parameter and NOT in the arg slice, so
// argument-leakage is structurally impossible (registry spec §2). On failure the
// returned error is a FIXED friendly message with Raw scrubbed empty — login
// stderr is never surfaced (it could echo supplied input). The token is never
// logged.
func (e *CLIEngine) RegistryLogin(ctx context.Context, host, user string, token io.Reader) error {
	args := registryLoginArgs(user, host)
	cmd := exec.CommandContext(ctx, e.Bin, args...)
	cmd.Stdin = token // the secret goes here, never to args
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// stdout is discarded — not needed, and never logged.
	if err := cmd.Run(); err != nil {
		// SCRUBBED: do not echo stderr (could contain the supplied token/input);
		// build a fixed message from the failure condition.
		return &CLIError{
			Args:    args,
			Kind:    ErrRegistryLoginFailed,
			Message: "login failed — check your username and token (SSO/2FA accounts need an access token, not a password)",
			Raw:     "", // scrubbed: registry-login stderr is never exposed
		}
	}
	return nil
}

// RegistryList returns the stored logins (host + username), JSON-parsed. The
// `--format json` shape is confirmed (registry spec §8), so there is no table
// fallback. The output carries no secret, so it is safe to read for state.
func (e *CLIEngine) RegistryList(ctx context.Context) ([]RegistryAuth, error) {
	var raw []rawRegistry
	if err := e.runJSON(ctx, &raw, "registry", "list", "--format", "json"); err != nil {
		return nil, err
	}
	out := make([]RegistryAuth, 0, len(raw))
	for _, r := range raw {
		host := r.Name
		if host == "" {
			host = r.ID
		}
		out = append(out, RegistryAuth{Host: host, Username: r.Username, Created: r.CreationDate})
	}
	return out, nil
}

// RegistryLogout drops the stored login for a host (`registry logout <host>`).
func (e *CLIEngine) RegistryLogout(ctx context.Context, host string) error {
	_, err := e.run(ctx, "registry", "logout", host)
	return err
}
