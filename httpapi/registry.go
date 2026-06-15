package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/porthole/porthole/engine"
)

// RegistryEngine is the focused engine slice the registry endpoints need (the
// secret-handling feature). *engine.CLIEngine satisfies it. Login takes the token
// as an io.Reader so it can only ever reach the child's stdin, never an argument.
type RegistryEngine interface {
	RegistryLogin(ctx context.Context, host, user string, token io.Reader) error
	RegistryList(ctx context.Context) ([]engine.RegistryAuth, error)
	RegistryLogout(ctx context.Context, host string) error
}

// handleRegistryLogin is the ONE handler in Porthole that touches a secret.
// INVARIANTS (registry spec §2):
//   - the token is read from the body, piped straight to the engine's stdin, and
//     dropped — NEVER stored (no DB/file/cache);
//   - the request body is NEVER logged;
//   - the token is NEVER returned in the response or in an error (the engine
//     returns a scrubbed registry_login_failed; success returns 204, no body).
func (s *Server) handleRegistryLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host     string `json:"host"`
		Username string `json:"username"`
		Token    string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrUnknownOption), Message: "invalid JSON body",
		}})
		return
	}
	if req.Host == "" || req.Username == "" || req.Token == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrUnknownOption), Message: "host, username, and token are required",
		}})
		return
	}
	// Pipe the token to the engine (→ child stdin), then it goes out of scope.
	// Do NOT log req — it carries the secret.
	if err := s.registry.RegistryLogin(r.Context(), req.Host, req.Username, strings.NewReader(req.Token)); err != nil {
		writeError(w, err) // scrubbed registry_login_failed (401) — no token
		return
	}
	w.WriteHeader(http.StatusNoContent) // success: no body, nothing to leak
}

// GET /api/registry — login state (host + username only; no secret to leak).
func (s *Server) handleRegistryList(w http.ResponseWriter, r *http.Request) {
	logins, err := s.registry.RegistryList(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if logins == nil {
		logins = []engine.RegistryAuth{}
	}
	writeJSON(w, http.StatusOK, logins)
}

// POST /api/registry/logout — body {host}. Non-destructive (just drops auth).
func (s *Server) handleRegistryLogout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host string `json:"host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Host == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrUnknownOption), Message: "host is required",
		}})
		return
	}
	if err := s.registry.RegistryLogout(r.Context(), req.Host); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
