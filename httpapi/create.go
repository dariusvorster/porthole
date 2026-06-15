package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/stacks"
)

// CreateEngine is the focused slice of the engine the create path needs: a
// progress-streaming run (run auto-pulls + blocks, §8.1) and the image list for
// the picker. *engine.CLIEngine satisfies it (interface segregation, so the
// shared Engine fakes don't need these methods).
type CreateEngine interface {
	RunStream(ctx context.Context, spec engine.RunSpec) <-chan engine.RunUpdate
	ImageList(ctx context.Context) ([]engine.Image, error)
}

type createPort struct {
	HostPort      int    `json:"hostPort"`
	ContainerPort int    `json:"containerPort"`
	Proto         string `json:"proto"`
}

type createVolume struct {
	Source string `json:"source"` // named volume or host path (bind)
	Target string `json:"target"`
}

type createHealth struct {
	Type     string `json:"type"` // "http" | "tcp"
	Port     int    `json:"port"`
	Path     string `json:"path,omitempty"`
	Interval int    `json:"interval,omitempty"`
}

// createSpec is the create form body. It maps to a single-container engine
// RunSpec (the same path Stacks uses); restart/health become supervision labels.
type createSpec struct {
	Image   string            `json:"image"`
	Name    string            `json:"name"`
	Command string            `json:"command"` // shell-split server-side
	Env     map[string]string `json:"env"`
	EnvFile []string          `json:"envFile"`
	Ports   []createPort      `json:"ports"`
	Volumes []createVolume    `json:"volumes"`
	Labels  map[string]string `json:"labels"`
	Restart string            `json:"restart"` // "" | no | always | unless-stopped
	Health  *createHealth     `json:"health"`
	CPUs    int               `json:"cpus"`
	Memory  string            `json:"memory"` // e.g. "512m"
	Network string            `json:"network"`
	WorkDir string            `json:"workdir"`
	User    string            `json:"user"`
}

// errBadCreate marks a client spec error (→ 400).
type errBadCreate struct{ msg string }

func (e errBadCreate) Error() string { return e.msg }

// toRunSpec maps the form to a RunSpec, writing supervision labels for
// restart/health. Returns errBadCreate for invalid input.
func (c createSpec) toRunSpec() (engine.RunSpec, error) {
	if strings.TrimSpace(c.Image) == "" {
		return engine.RunSpec{}, errBadCreate{"image is required"}
	}
	cmd, err := stacks.ShellSplit(c.Command)
	if err != nil {
		return engine.RunSpec{}, errBadCreate{"command: " + err.Error()}
	}

	labels := map[string]string{}
	for k, v := range c.Labels {
		labels[k] = v
	}
	switch c.Restart {
	case "", "no":
	case "always", "unless-stopped":
		labels[stacks.LabelRestart] = c.Restart // porthole.restart — supervision adopts it
	default:
		return engine.RunSpec{}, errBadCreate{"invalid restart policy: " + c.Restart + " (no|always|unless-stopped)"}
	}
	if h := c.Health; h != nil && h.Type != "" {
		if h.Type != "http" && h.Type != "tcp" {
			return engine.RunSpec{}, errBadCreate{"health type must be http or tcp"}
		}
		labels["porthole.health.type"] = h.Type
		if h.Port > 0 {
			labels["porthole.health.port"] = strconv.Itoa(h.Port)
		}
		if h.Path != "" {
			labels["porthole.health.path"] = h.Path
		}
		if h.Interval > 0 {
			labels["porthole.health.interval"] = strconv.Itoa(h.Interval)
		}
	}

	rs := engine.RunSpec{
		Name: c.Name, Image: c.Image, Command: cmd,
		Env: c.Env, EnvFile: c.EnvFile, Labels: labels,
		Network: c.Network, CPUs: c.CPUs, Memory: c.Memory,
		WorkDir: c.WorkDir, User: c.User,
	}
	for _, p := range c.Ports {
		if p.HostPort < 1 || p.ContainerPort < 1 {
			return engine.RunSpec{}, errBadCreate{"port mapping needs host and container ports"}
		}
		proto := p.Proto
		if proto == "" {
			proto = "tcp"
		}
		rs.Ports = append(rs.Ports, engine.RunPort{HostPort: p.HostPort, ContainerPort: p.ContainerPort, Proto: proto})
	}
	for _, v := range c.Volumes {
		if v.Source == "" || v.Target == "" {
			return engine.RunSpec{}, errBadCreate{"volume needs source and target"}
		}
		rs.Volumes = append(rs.Volumes, engine.RunVolume{Source: v.Source, Target: v.Target})
	}
	return rs, nil
}

// handleCreate runs a container and STREAMS pull/start progress over SSE (run
// auto-pulls + blocks, §8.1 — a sync 202 would freeze). It emits `progress`
// events, then a terminal `created {id}` or `error {kind,message}`. On success
// it records desired=running (a mediated start) so supervision adopts a
// restart-at-create policy with correct intent. The child is tied to the request
// context — a client disconnect cancels it (the logs teardown discipline).
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var spec createSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrUnknownOption), Message: "invalid JSON body",
		}})
		return
	}
	rs, err := spec.toRunSpec()
	if err != nil {
		var bad errBadCreate
		if errors.As(err, &bad) {
			writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorBody{
				Kind: string(engine.ErrUnknownOption), Message: bad.msg,
			}})
			return
		}
		writeError(w, err)
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

	// Own the child's lifetime so the keychain-stall watchdog can cancel it — the
	// SAME context-cancel the Cancel button uses on a client disconnect (§7b).
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	updates := s.creator.RunStream(ctx, rs)

	// Watchdog: a private pull whose credential read blocks on an invisible macOS
	// keychain prompt sits at the INITIAL phase ([0/N]) and never prints a progress
	// line. The discriminator is exactly that — ANY progress line means the fetch
	// started (bytes moving), so a merely-slow pull is exempt no matter how long it
	// then goes quiet. Trip only on "no progress while still at phase 0".
	timer := time.NewTimer(s.pullStall)
	defer timer.Stop()
	gotProgress, stalled := false, false

	for {
		select {
		case u, ok := <-updates:
			if !ok {
				return // stream closed → done
			}
			switch u.Kind {
			case "progress":
				if !gotProgress {
					gotProgress = true // fetch is moving — disarm the watchdog
					timer.Stop()
				}
				writeCreateSSE(w, "progress", map[string]any{"index": u.Index, "total": u.Total, "phase": u.Phase})
			case "created":
				s.recordStart(u.ID) // mediated start → desired=running (correct restart intent)
				s.emitResource()    // a new container changes the resource lists (PF1)
				writeCreateSSE(w, "created", map[string]string{"id": u.ID})
			case "error":
				if stalled {
					continue // the cancel we issued produced this — already reported the stall
				}
				writeCreateSSE(w, "error", createErrorBody(u.Err))
			}
			flusher.Flush()
		case <-timer.C:
			if gotProgress || stalled {
				continue // disarmed (progress arrived) or already fired
			}
			// Likely a keychain-authorization stall. Kill the child via ctx (proven
			// teardown), then emit a HEDGED, typed pull_stalled event (NOT a hard
			// error) carrying the image ref so the UI can build the exact fix command.
			stalled = true
			cancel()
			writeCreateSSE(w, "pull_stalled", map[string]string{
				"image":   rs.Image,
				"message": "This pull appears stalled — likely waiting on one-time keychain authorization.",
			})
			flusher.Flush()
		}
	}
}

// handleImages lists locally-present images for the create picker + presence check.
func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	imgs, err := s.creator.ImageList(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if imgs == nil {
		imgs = []engine.Image{}
	}
	writeJSON(w, http.StatusOK, imgs)
}

// createErrorBody maps a run error to the typed body. A pull failure (the 401
// shape, §8.6) is surfaced as "image not found or inaccessible" — the 401 can't
// distinguish a typo from a private repo.
func createErrorBody(err error) errorBody {
	var ce *engine.CLIError
	if errors.As(err, &ce) {
		msg := ce.Message
		if ce.Kind == engine.ErrImagePullFailed {
			msg = "image not found or inaccessible"
		}
		return errorBody{Kind: string(ce.Kind), Message: msg, Raw: ce.Raw}
	}
	return errorBody{Kind: string(engine.ErrUnknown), Message: err.Error()}
}

func writeCreateSSE(w io.Writer, event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}
