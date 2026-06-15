package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/resources"
)

// ResourceEngine is the focused engine slice the Resources endpoints need
// (interface segregation, so the shared Engine fakes are untouched).
// *engine.CLIEngine satisfies it.
type ResourceEngine interface {
	ImageList(ctx context.Context) ([]engine.Image, error)
	VolumeList(ctx context.Context) ([]engine.Volume, error)
	ListNetworks(ctx context.Context) ([]engine.Network, error)
	ImageRemove(ctx context.Context, force bool, refs ...string) error
	ImagePrune(ctx context.Context, all bool) (engine.PruneResult, error)
	ImageTag(ctx context.Context, src, dst string) error
	ImagePull(ctx context.Context, ref string) <-chan engine.RunUpdate
	VolumeRemove(ctx context.Context, name string) error
	VolumePrune(ctx context.Context) (engine.PruneResult, error)
	NetworkPrune(ctx context.Context) (engine.PruneResult, error)
	ContainerPrune(ctx context.Context) (engine.PruneResult, error)
}

// resourceBundle is the GET /api/resources response: the df summary + annotated
// lists (in-use / anonymous / protected computed by the pure core).
type resourceBundle struct {
	Summary  engine.DiskUsage             `json:"summary"`
	Images   []resources.AnnotatedImage   `json:"images"`
	Volumes  []resources.AnnotatedVolume  `json:"volumes"`
	Networks []resources.AnnotatedNetwork `json:"networks"`
}

// annotate fetches the live lists and runs the pure annotator. Shared by the
// list endpoint and the prune preview.
func (s *Server) annotate(ctx context.Context) (resources.Annotated, []engine.Container, error) {
	containers, err := s.eng.ListContainers(ctx, true)
	if err != nil {
		return resources.Annotated{}, nil, err
	}
	images, err := s.res.ImageList(ctx)
	if err != nil {
		return resources.Annotated{}, nil, err
	}
	volumes, err := s.res.VolumeList(ctx)
	if err != nil {
		return resources.Annotated{}, nil, err
	}
	networks, err := s.res.ListNetworks(ctx)
	if err != nil {
		return resources.Annotated{}, nil, err
	}
	return resources.Annotate(images, volumes, networks, containers), containers, nil
}

// GET /api/resources — annotated images/volumes/networks + the df summary.
func (s *Server) handleResources(w http.ResponseWriter, r *http.Request) {
	a, _, err := s.annotate(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	df, _ := s.eng.DiskUsage(r.Context()) // best-effort summary
	bundle := resourceBundle{Summary: df, Images: a.Images, Volumes: a.Volumes, Networks: a.Networks}
	if bundle.Images == nil {
		bundle.Images = []resources.AnnotatedImage{}
	}
	if bundle.Volumes == nil {
		bundle.Volumes = []resources.AnnotatedVolume{}
	}
	if bundle.Networks == nil {
		bundle.Networks = []resources.AnnotatedNetwork{}
	}
	writeJSON(w, http.StatusOK, bundle)
}

// POST /api/prune/{kind}?preview=true&all=true — preview returns the PrunePlan
// (no mutation, the safety mechanism); without preview it applies and returns the
// actual reclaimed figure. kind ∈ images|volumes|networks|containers.
func (s *Server) handlePrune(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	all := r.URL.Query().Get("all") == "true"
	ctx := r.Context()

	if r.URL.Query().Get("preview") == "true" {
		a, containers, err := s.annotate(ctx)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resources.PrunePreview(kind, a, containers, all))
		return
	}

	var res engine.PruneResult
	var err error
	switch kind {
	case "images":
		res, err = s.res.ImagePrune(ctx, all)
	case "volumes":
		res, err = s.res.VolumePrune(ctx)
	case "networks":
		res, err = s.res.NetworkPrune(ctx)
	case "containers":
		res, err = s.res.ContainerPrune(ctx)
	default:
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrUnknownOption), Message: "unknown prune kind: " + kind,
		}})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	s.emitResource()
	writeJSON(w, http.StatusOK, res)
}

// DELETE /api/volumes/{name} — refused with volume_in_use (409) when mounted.
func (s *Server) handleVolumeDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.res.VolumeRemove(r.Context(), r.PathValue("name")); err != nil {
		writeError(w, err) // ErrVolumeInUse → 409
		return
	}
	s.emitResource()
	w.WriteHeader(http.StatusAccepted)
}

// DELETE /api/images?ref=<ref>&force=true — the ref is a query param (it contains
// slashes). Succeeds even if running-referenced (no runtime guard) — the advisory
// warning is the frontend's job.
func (s *Server) handleImageDelete(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrUnknownOption), Message: "ref query parameter required",
		}})
		return
	}
	force := r.URL.Query().Get("force") == "true"
	if err := s.res.ImageRemove(r.Context(), force, ref); err != nil {
		writeError(w, err)
		return
	}
	s.emitResource()
	w.WriteHeader(http.StatusAccepted)
}

// POST /api/images/tag {src,dst}
func (s *Server) handleImageTag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Src == "" || req.Dst == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrUnknownOption), Message: "src and dst required",
		}})
		return
	}
	if err := s.res.ImageTag(r.Context(), req.Src, req.Dst); err != nil {
		writeError(w, err)
		return
	}
	s.emitResource()
	w.WriteHeader(http.StatusAccepted)
}

// POST /api/images/pull {ref} — standalone pull, SSE progress (reuses the create
// stream framing). Emits `progress` phases then a terminal `created`/`error`.
func (s *Server) handleImagePull(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ref string `json:"ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Ref == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrUnknownOption), Message: "ref required",
		}})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for u := range s.res.ImagePull(r.Context(), req.Ref) {
		switch u.Kind {
		case "progress":
			writeCreateSSE(w, "progress", map[string]any{"index": u.Index, "total": u.Total, "phase": u.Phase})
		case "created":
			s.emitResource()
			writeCreateSSE(w, "created", map[string]string{"id": u.ID})
		case "error":
			writeCreateSSE(w, "error", createErrorBody(u.Err))
		}
		flusher.Flush()
	}
}

// emitResource nudges SSE subscribers to refetch the resource lists after a
// mutation (like stackEpoch). The df summary already streams.
func (s *Server) emitResource() {
	if s.hub != nil {
		s.hub.Emit("resource", map[string]string{})
	}
}
