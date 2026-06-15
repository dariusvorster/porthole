// Package httpapi exposes the engine over a localhost HTTP surface. It owns
// transport concerns only — error mapping, security middleware, JSON encoding —
// and never talks to `container` directly; that is the engine's job.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/porthole/porthole/engine"
)

// errorEnvelope is the typed error body every failed request returns. It mirrors
// the engine's CLIError so the UI can branch on `kind` and reveal `raw` behind a
// details disclosure.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Raw     string `json:"raw,omitempty"`
}

// errTooManyStreams is returned when the concurrent log-stream cap is reached.
var errTooManyStreams = errors.New("too many concurrent log streams")

// statusFor maps an engine error kind to an HTTP status.
func statusFor(kind engine.ErrorKind) int {
	switch kind {
	case engine.ErrDaemonDown:
		return http.StatusServiceUnavailable // 503 — bootstrap banner territory
	case engine.ErrNotFound:
		return http.StatusNotFound
	case engine.ErrNameConflict:
		return http.StatusConflict
	case engine.ErrNotRunning:
		return http.StatusConflict
	case engine.ErrInvalidState:
		return http.StatusConflict
	case engine.ErrUnknownOption:
		return http.StatusBadRequest
	case engine.ErrImagePullFailed:
		return http.StatusUnprocessableEntity // 422 — image not found or inaccessible
	case engine.ErrVolumeInUse:
		return http.StatusConflict // 409 — volume mounted by a container
	case engine.ErrRegistryLoginFailed:
		return http.StatusUnauthorized // 401 — the registry rejected the credentials
	default:
		return http.StatusInternalServerError
	}
}

// writeError translates any error into the JSON envelope + appropriate status.
// A *engine.CLIError carries a kind; anything else is treated as an internal
// error with no raw leak.
func writeError(w http.ResponseWriter, err error) {
	var ce *engine.CLIError
	if errors.As(err, &ce) {
		writeJSON(w, statusFor(ce.Kind), errorEnvelope{Error: errorBody{
			Kind:    string(ce.Kind),
			Message: ce.Message,
			Raw:     ce.Raw,
		}})
		return
	}
	writeJSON(w, http.StatusInternalServerError, errorEnvelope{Error: errorBody{
		Kind:    string(engine.ErrUnknown),
		Message: err.Error(),
	}})
}

// writeJSON encodes v as JSON with the given status. Encoding errors are not
// recoverable mid-response, so they are intentionally swallowed after headers.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
