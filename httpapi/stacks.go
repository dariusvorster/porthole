package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/stacks"
)

// Stacks endpoints (Phase 4, v1). validate/import/list/get/delete are store- or
// parse-only and ungated; plan/up/down/restart touch the runtime and sit behind
// the bootstrap gate. Per-stack serialization is handled inside the executor's
// stack lock, so two ups on one stack can't race even across handlers. The
// browser guard (Origin/Host) applies to all of these via middleware.

type stackBody struct {
	Name    string `json:"name"`
	Compose string `json:"compose"`
}

func decodeStackBody(w http.ResponseWriter, r *http.Request) (stackBody, bool) {
	var req stackBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrUnknownOption), Message: "invalid JSON body",
		}})
		return stackBody{}, false
	}
	return req, true
}

// POST /api/stacks/validate — parse + report, no side effects.
func (s *Server) handleStackValidate(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeStackBody(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.stacks.Validate(req.Name, []byte(req.Compose)))
}

// POST /api/stacks — validate then store. Invalid files are reported (400) and
// NOT stored; valid files return 201 with the report.
func (s *Server) handleStackImport(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeStackBody(w, r)
	if !ok {
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrUnknownOption), Message: "stack name is required",
		}})
		return
	}
	rep, stored, err := s.stacks.Import(req.Name, []byte(req.Compose))
	if err != nil {
		writeError(w, err)
		return
	}
	if !stored {
		writeJSON(w, http.StatusBadRequest, rep) // invalid compose — report, don't store
		return
	}
	writeJSON(w, http.StatusCreated, rep)
}

// GET /api/stacks — stored stacks + live status.
func (s *Server) handleStackList(w http.ResponseWriter, r *http.Request) {
	views, err := s.stacks.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if views == nil {
		views = []stacks.StackView{}
	}
	writeJSON(w, http.StatusOK, views)
}

// GET /api/stacks/{name} — one stored stack + status + members.
func (s *Server) handleStackGet(w http.ResponseWriter, r *http.Request) {
	v, ok, err := s.stacks.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	if !ok {
		writeStackNotFound(w, r.PathValue("name"))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// POST /api/stacks/{name}/plan — dry-run reconcile diff (no mutation).
func (s *Server) handleStackPlan(w http.ResponseWriter, r *http.Request) {
	p, ok, err := s.stacks.Plan(r.Context(), r.PathValue("name"))
	if !s.handleStackErr(w, r, ok, err) {
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// POST /api/stacks/{name}/up
func (s *Server) handleStackUp(w http.ResponseWriter, r *http.Request) {
	res, ok, err := s.stacks.Up(r.Context(), r.PathValue("name"))
	if !s.handleStackErr(w, r, ok, err) {
		return
	}
	s.emitStack(r.Context(), r.PathValue("name"))
	writeJSON(w, http.StatusOK, res)
}

// POST /api/stacks/{name}/down
func (s *Server) handleStackDown(w http.ResponseWriter, r *http.Request) {
	res, ok, err := s.stacks.Down(r.Context(), r.PathValue("name"))
	if !s.handleStackErr(w, r, ok, err) {
		return
	}
	s.emitStack(r.Context(), r.PathValue("name"))
	writeJSON(w, http.StatusOK, res)
}

// POST /api/stacks/{name}/restart
func (s *Server) handleStackRestart(w http.ResponseWriter, r *http.Request) {
	res, ok, err := s.stacks.Restart(r.Context(), r.PathValue("name"))
	if !s.handleStackErr(w, r, ok, err) {
		return
	}
	s.emitStack(r.Context(), r.PathValue("name"))
	writeJSON(w, http.StatusOK, res)
}

// POST /api/stacks/{name}/remediate — APPLY the planner's recreates (Phase 10,
// destructive). Distinct from the read-only plan: this stops/removes/re-creates
// drifted services with spec-snapshot rollback. Returns per-service outcomes.
// Gated + browser-guarded like every mutation.
func (s *Server) handleStackRemediate(w http.ResponseWriter, r *http.Request) {
	res, ok, err := s.stacks.Remediate(r.Context(), r.PathValue("name"))
	if !s.handleStackErr(w, r, ok, err) {
		return
	}
	s.emitStack(r.Context(), r.PathValue("name")) // members changed → refresh UI + resources
	writeJSON(w, http.StatusOK, res)
}

// POST /api/stacks/{name}/discovery — toggle service discovery (Phase 8). Writes
// the DB flag (working truth); the discovery controller injects/strips peers'
// /etc/hosts on the next reconcile cycle. Gated + browser-guarded like every
// mutation.
func (s *Server) handleStackDiscovery(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrUnknownOption), Message: "invalid JSON body",
		}})
		return
	}
	ok, err := s.stacks.SetDiscovery(r.PathValue("name"), body.Enabled)
	if err != nil {
		writeError(w, err)
		return
	}
	if !ok {
		writeStackNotFound(w, r.PathValue("name"))
		return
	}
	s.emitStack(r.Context(), r.PathValue("name")) // nudges the UI to refetch the flag
	w.WriteHeader(http.StatusAccepted)
}

// DELETE /api/stacks/{name} — remove the stored definition only (never the
// running containers; the caller must `down` first).
func (s *Server) handleStackDelete(w http.ResponseWriter, r *http.Request) {
	ok, err := s.stacks.Delete(r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	if !ok {
		writeStackNotFound(w, r.PathValue("name"))
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleStackErr maps the (ok, err) result of a mutation/plan to a response,
// returning false when it has already written an error (caller should stop).
func (s *Server) handleStackErr(w http.ResponseWriter, r *http.Request, ok bool, err error) bool {
	if errors.Is(err, stacks.ErrStackInvalid) {
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrUnknownOption), Message: err.Error(),
		}})
		return false
	}
	if err != nil {
		writeError(w, err)
		return false
	}
	if !ok {
		writeStackNotFound(w, r.PathValue("name"))
		return false
	}
	return true
}

// emitStack broadcasts the post-mutation stack status on the SSE stream so the UI
// updates live without re-fetching. A stack up/down also creates/removes the
// stack's network, volumes, and member containers, so it nudges the resource
// lists too (PF1 — the F8 stale-Resources seam).
func (s *Server) emitStack(ctx context.Context, name string) {
	if s.hub == nil {
		return
	}
	s.hub.Emit("stack", s.stacks.StatusEvent(ctx, name))
	s.emitResource()
}

func writeStackNotFound(w http.ResponseWriter, name string) {
	writeJSON(w, http.StatusNotFound, errorEnvelope{Error: errorBody{
		Kind: string(engine.ErrNotFound), Message: "no such stack: " + name,
	}})
}
