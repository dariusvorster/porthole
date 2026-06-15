package httpapi

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/idlock"
	"github.com/porthole/porthole/reconcile"
	"github.com/porthole/porthole/stacks"
)

// Config configures a Server.
type Config struct {
	// AllowedHosts is the set of Host header values accepted (anti-rebinding).
	// Typically {"127.0.0.1:<port>", "localhost:<port>"}.
	AllowedHosts []string
	// AllowedOrigins is the set of Origin values accepted (anti-CSRF).
	// Typically {"http://127.0.0.1:<port>", "http://localhost:<port>"}.
	AllowedOrigins []string
	// HealthTTL caps how often the bootstrap gate probes the runtime. Defaults
	// to 2s so a down apiserver is noticed quickly without hammering the CLI.
	HealthTTL time.Duration
	// Hub, when non-nil, enables the SSE stream at GET /api/stream.
	Hub *reconcile.Hub
	// Locks serializes mutations per container id. When nil a private one is
	// created; pass a shared instance so the supervisor and handlers don't race.
	Locks *idlock.KeyedMutex
	// Supervision, when non-nil, records mediated start/stop intent (Phase 3).
	Supervision Supervision
	// LogWatcher, when non-nil, signals container-stop for log-stream teardown
	// (Phase 2). *reconcile.Hub satisfies it.
	LogWatcher LogWatcher
	// Stacks, when non-nil, enables the Stacks endpoints (Phase 4).
	Stacks *stacks.Manager
	// Creator, when non-nil, enables the create endpoints (Phase 5):
	// POST /api/containers + GET /api/images. *engine.CLIEngine satisfies it.
	Creator CreateEngine
	// Resources, when non-nil, enables the Resources/Disk endpoints (Phase 6).
	// *engine.CLIEngine satisfies it.
	Resources ResourceEngine
	// Registry, when non-nil, enables the registry login endpoints (Phase 7).
	// *engine.CLIEngine satisfies it.
	Registry RegistryEngine
	// PullStallTimeout caps how long a create/pull may sit at the INITIAL phase
	// ([0/N]) with no progress before the watchdog declares it stalled — almost
	// always a macOS keychain authorization the headless daemon can't surface
	// (Phase 7b) — cancels the child, and emits a typed pull_stalled event. <=0
	// reads PORTHOLE_PULL_STALL_SECS (default 25s). Tests set a small value.
	PullStallTimeout time.Duration
}

// Server is the localhost HTTP surface over an Engine.
type Server struct {
	eng     engine.Engine
	hub     *reconcile.Hub
	mux     *http.ServeMux
	handler http.Handler

	healthTTL  time.Duration
	healthMu   sync.Mutex
	healthAt   time.Time
	healthLast engine.SystemStatus

	// Per-container mutation lock (spec §8 gap 11), shared with the supervisor.
	locks *idlock.KeyedMutex
	sup   Supervision

	// Per-container log streaming (Phase 2).
	logs       *logRegistry
	logWatcher LogWatcher

	// Concurrent exec sessions (Phase 2) — NOT deduped; each open is its own shell.
	execActive int32

	// Stacks orchestration (Phase 4).
	stacks *stacks.Manager

	// Create / run container (Phase 5).
	creator CreateEngine

	// Resources / disk (Phase 6).
	res ResourceEngine

	// Registry login (Phase 7).
	registry RegistryEngine

	// Keychain-stall watchdog timeout for the create/pull stream (Phase 7b).
	pullStall time.Duration
}

// New builds a Server. The returned handler already has security middleware
// applied; mount it directly on an http.Server bound to loopback.
func New(eng engine.Engine, cfg Config) *Server {
	if cfg.HealthTTL <= 0 {
		cfg.HealthTTL = 2 * time.Second
	}
	if cfg.PullStallTimeout <= 0 {
		cfg.PullStallTimeout = pullStallFromEnv()
	}
	locks := cfg.Locks
	if locks == nil {
		locks = idlock.New()
	}
	s := &Server{
		eng: eng, hub: cfg.Hub, mux: http.NewServeMux(), healthTTL: cfg.HealthTTL,
		locks: locks, sup: cfg.Supervision,
		logs: newLogRegistry(), logWatcher: cfg.LogWatcher,
		stacks: cfg.Stacks, creator: cfg.Creator, res: cfg.Resources, registry: cfg.Registry,
		pullStall: cfg.PullStallTimeout,
	}
	s.routes()

	// Order: security headers → browser guard → router.
	guarded := newBrowserGuard(cfg.AllowedHosts, cfg.AllowedOrigins, s.mux)
	s.handler = securityHeaders(guarded)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// pullStallFromEnv reads PORTHOLE_PULL_STALL_SECS (whole seconds), defaulting to
// 25s. A garbage or non-positive value falls back to the default rather than
// disabling the watchdog.
func pullStallFromEnv() time.Duration {
	const def = 25 * time.Second
	v := os.Getenv("PORTHOLE_PULL_STALL_SECS")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return time.Duration(n) * time.Second
}

func (s *Server) routes() {
	// Porthole's own liveness — never gated, never touches the runtime.
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// System — status reports the runtime's health (including "down"), so it is
	// not behind the bootstrap gate.
	s.mux.HandleFunc("GET /api/system/status", s.handleSystemStatus)
	s.mux.HandleFunc("GET /api/system/version", s.handleSystemVersion)

	// Runtime-dependent reads — behind the bootstrap gate.
	s.mux.HandleFunc("GET /api/system/df", s.gate(s.handleDiskUsage))
	s.mux.HandleFunc("GET /api/containers", s.gate(s.handleListContainers))
	s.mux.HandleFunc("GET /api/containers/{id}", s.gate(s.handleInspectContainer))
	s.mux.HandleFunc("GET /api/containers/{id}/logs", s.gate(s.handleLogs))
	s.mux.HandleFunc("GET /api/containers/{id}/exec", s.gate(s.handleExec))
	s.mux.HandleFunc("GET /api/networks", s.gate(s.handleListNetworks))
	s.mux.HandleFunc("GET /api/networks/{id}", s.gate(s.handleInspectNetwork))
	s.mux.HandleFunc("GET /api/stats", s.gate(s.handleStats))

	// Live deltas over SSE — only when a hub is wired in. Not gated: it reports
	// daemon-down via a `system` event itself.
	if s.hub != nil {
		s.mux.HandleFunc("GET /api/stream", s.handleStream)
	}

	// Create / run a standalone container (Phase 5). SSE progress (run auto-pulls
	// + blocks); gated + browser-guarded. The image picker + network dropdown back
	// the form. Registered only when a Creator is wired in.
	if s.creator != nil {
		s.mux.HandleFunc("POST /api/containers", s.gate(s.handleCreate))
		s.mux.HandleFunc("GET /api/images", s.gate(s.handleImages))
	}

	// Resources / disk (Phase 6) — annotated lists + preview-then-apply prune +
	// typed deletes + standalone pull. Gated + guarded. Registered only when wired.
	if s.res != nil {
		s.mux.HandleFunc("GET /api/resources", s.gate(s.handleResources))
		s.mux.HandleFunc("POST /api/prune/{kind}", s.gate(s.handlePrune))
		s.mux.HandleFunc("DELETE /api/volumes/{name}", s.gate(s.handleVolumeDelete))
		s.mux.HandleFunc("DELETE /api/images", s.gate(s.handleImageDelete))
		s.mux.HandleFunc("POST /api/images/tag", s.gate(s.handleImageTag))
		s.mux.HandleFunc("POST /api/images/pull", s.gate(s.handleImagePull))
	}

	// Registry login (Phase 7) — the secret-handling feature. Gated + browser-
	// guarded like every mutation; the login handler additionally never logs the
	// body / never echoes the token (see registry.go). Registered only when wired.
	if s.registry != nil {
		s.mux.HandleFunc("GET /api/registry", s.gate(s.handleRegistryList))
		s.mux.HandleFunc("POST /api/registry/login", s.gate(s.handleRegistryLogin))
		s.mux.HandleFunc("POST /api/registry/logout", s.gate(s.handleRegistryLogout))
	}

	// Container mutations (Phase 1). Behind the bootstrap gate (503 when the
	// daemon is down) and serialized per id so two tabs can't race the same
	// container. The browser guard already enforces Origin/CSRF on these.
	s.mux.HandleFunc("POST /api/containers/{id}/start", s.gate(s.serialized(s.handleStart)))
	s.mux.HandleFunc("POST /api/containers/{id}/stop", s.gate(s.serialized(s.handleStop)))
	s.mux.HandleFunc("POST /api/containers/{id}/restart", s.gate(s.serialized(s.handleRestart)))
	s.mux.HandleFunc("POST /api/containers/{id}/kill", s.gate(s.serialized(s.handleKill)))
	s.mux.HandleFunc("DELETE /api/containers/{id}", s.gate(s.serialized(s.handleDelete)))
	s.mux.HandleFunc("PUT /api/containers/{id}/policy", s.gate(s.serialized(s.handlePolicy)))

	// Stacks (Phase 4) — only when wired in. validate/import/list/get/delete are
	// store/parse-only (ungated); plan/up/down/restart touch the runtime (gated).
	// Per-stack serialization lives in the executor's stack lock.
	if s.stacks != nil {
		s.mux.HandleFunc("POST /api/stacks/validate", s.handleStackValidate)
		s.mux.HandleFunc("POST /api/stacks", s.handleStackImport)
		s.mux.HandleFunc("GET /api/stacks", s.handleStackList)
		s.mux.HandleFunc("GET /api/stacks/{name}", s.handleStackGet)
		s.mux.HandleFunc("DELETE /api/stacks/{name}", s.handleStackDelete)
		s.mux.HandleFunc("POST /api/stacks/{name}/plan", s.gate(s.handleStackPlan))
		s.mux.HandleFunc("POST /api/stacks/{name}/up", s.gate(s.handleStackUp))
		s.mux.HandleFunc("POST /api/stacks/{name}/down", s.gate(s.handleStackDown))
		s.mux.HandleFunc("POST /api/stacks/{name}/restart", s.gate(s.handleStackRestart))
	}

	// Embedded SPA — lowest-priority catch-all. The specific /api/* and /healthz
	// patterns above always win; everything else serves the console.
	s.mux.HandleFunc("GET /", s.handleSPA)
}

// --- bootstrap gate ------------------------------------------------------

// gate wraps a handler so it short-circuits with a 503 blocked state when the
// apiserver is down — the UI renders the "Start services" banner from this
// instead of showing stale or empty data. The probe is cached for HealthTTL.
func (s *Server) gate(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st := s.health(r.Context())
		if !st.APIServerRunning {
			writeJSON(w, http.StatusServiceUnavailable, errorEnvelope{Error: errorBody{
				Kind:    string(engine.ErrDaemonDown),
				Message: "container system service is not running",
				Raw:     st.Detail,
			}})
			return
		}
		h(w, r)
	}
}

// health returns a cached SystemStatus, refreshing at most every HealthTTL.
func (s *Server) health(ctx context.Context) engine.SystemStatus {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	if time.Since(s.healthAt) < s.healthTTL && !s.healthAt.IsZero() {
		return s.healthLast
	}
	st, err := s.eng.SystemStatus(ctx)
	if err != nil {
		// SystemStatus only errors on unexpected failures; treat as down so the
		// UI degrades safely rather than rendering a half-broken console.
		st = engine.SystemStatus{APIServerRunning: false, Detail: err.Error()}
	}
	s.healthLast = st
	s.healthAt = time.Now()
	return st
}

// --- handlers ------------------------------------------------------------

func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.eng.SystemStatus(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleSystemVersion(w http.ResponseWriter, r *http.Request) {
	v, err := s.eng.SystemVersion(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleDiskUsage(w http.ResponseWriter, r *http.Request) {
	d, err := s.eng.DiskUsage(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleListContainers(w http.ResponseWriter, r *http.Request) {
	// Default to all=true: the console always shows stopped containers too.
	cs, err := s.eng.ListContainers(r.Context(), true)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) handleInspectContainer(w http.ResponseWriter, r *http.Request) {
	c, err := s.eng.InspectContainer(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleListNetworks(w http.ResponseWriter, r *http.Request) {
	ns, err := s.eng.ListNetworks(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ns)
}

func (s *Server) handleInspectNetwork(w http.ResponseWriter, r *http.Request) {
	n, err := s.eng.InspectNetwork(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	// Optional ?id=a&id=b filter; empty means all running containers.
	ids := r.URL.Query()["id"]
	st, err := s.eng.Stats(r.Context(), ids...)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
