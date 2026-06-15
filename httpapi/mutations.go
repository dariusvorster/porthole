package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/supervisor"
)

// Supervision records mediated start/stop intent (spec §3.1) and applies policy
// changes from the REST endpoint. Implemented by *supervisor.Supervisor.
type Supervision interface {
	RecordStart(id string)
	RecordStop(id string)
	ApplyPolicy(p supervisor.Policy) error
}

// serialized wraps a mutation handler so calls for the same container id run one
// at a time — using the shared keyed lock, so a supervision restart and a user
// action on the same container also serialize.
func (s *Server) serialized(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		unlock := s.locks.Lock(r.PathValue("id"))
		defer unlock()
		h(w, r)
	}
}

// recordStart / recordStop forward mediated intent to the supervisor if wired.
func (s *Server) recordStart(id string) {
	if s.sup != nil {
		s.sup.RecordStart(id)
	}
}

func (s *Server) recordStop(id string) {
	if s.sup != nil {
		s.sup.RecordStop(id)
	}
}

// Mutations return 202 Accepted with no body: the reconcile loop observes the
// resulting state and emits it over SSE, so the client learns the new state from
// the stream, not the response.

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.eng.StartContainer(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	s.recordStart(id) // mediated intent: desired=running
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.eng.StopContainer(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	s.recordStop(id) // mediated intent: desired=stopped → not auto-restarted
	w.WriteHeader(http.StatusAccepted)
}

// handleRestart is stop-then-start. stop is idempotent on an already-stopped
// container (exits 0), so restart also works as a plain start.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.eng.StopContainer(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	if err := s.eng.StartContainer(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	s.recordStart(id) // ends running → desired=running
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.eng.KillContainer(r.Context(), id, r.URL.Query().Get("signal")); err != nil {
		writeError(w, err)
		return
	}
	s.recordStop(id) // a kill is a mediated stop: desired=stopped
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "true"
	if err := s.eng.DeleteContainer(r.Context(), r.PathValue("id"), force); err != nil {
		writeError(w, err)
		return
	}
	s.emitResource() // a removed container changes the resource lists (PF1)
	w.WriteHeader(http.StatusAccepted)
}

type policyRequest struct {
	Restart string                   `json:"restart"`
	Health  *supervisor.HealthPolicy `json:"health,omitempty"`
}

// handlePolicy sets a container's supervision policy (PUT). Store write only —
// labels are immutable post-create so running containers are never recreated to
// add one. The supervisor emits the new state on the supervision stream.
func (s *Server) handlePolicy(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrUnknown), Message: "supervision not enabled",
		}})
		return
	}

	var req policyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrUnknownOption), Message: "invalid JSON body",
		}})
		return
	}

	restart := supervisor.RestartPolicy(req.Restart)
	switch restart {
	case supervisor.RestartNo, supervisor.RestartAlways, supervisor.RestartUnlessStopped:
	default:
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
			Kind:    string(engine.ErrUnknownOption),
			Message: "unsupported restart policy: " + req.Restart + " (valid: no, always, unless-stopped — on-failure is not supported)",
		}})
		return
	}

	if req.Health != nil && req.Health.Type != "" && req.Health.Type != "http" && req.Health.Type != "tcp" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrUnknownOption), Message: "health type must be http or tcp",
		}})
		return
	}

	p := supervisor.Policy{ContainerID: r.PathValue("id"), Restart: restart, Health: req.Health}
	if err := s.sup.ApplyPolicy(p); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
