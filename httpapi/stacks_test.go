package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/idlock"
	"github.com/porthole/porthole/stacks"
)

// stackFake extends fakeEngine with the stacks.Engine verbs (run/network) and
// makes start/stop/delete stateful so up/down/status round-trips are observable.
type stackFake struct {
	*fakeEngine
	mu sync.Mutex
}

func newStackFake() *stackFake {
	return &stackFake{fakeEngine: &fakeEngine{
		status: engine.SystemStatus{APIServerRunning: true, CLIVersion: "1.0.0"},
	}}
}

func (f *stackFake) ListContainers(context.Context, bool) ([]engine.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]engine.Container(nil), f.containers...), f.err
}

func (f *stackFake) ListNetworks(context.Context) ([]engine.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]engine.Network(nil), f.networks...), nil
}

func (f *stackFake) CreateNetwork(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := engine.Network{ID: name}
	n.Configuration.Name = name
	f.networks = append(f.networks, n)
	return nil
}

func (f *stackFake) RemoveNetwork(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.networks[:0]
	for _, n := range f.networks {
		if n.Configuration.Name != name {
			out = append(out, n)
		}
	}
	f.networks = out
	return nil
}

func (f *stackFake) RunContainer(_ context.Context, spec engine.RunSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := engine.Container{ID: spec.Name}
	c.Configuration.Labels = spec.Labels
	c.Configuration.Image.Reference = spec.Image
	c.Status.State = engine.StateRunning
	c.Status.Networks = []engine.NetworkStatus{{Network: spec.Network, IPv4Address: "192.168.65.5/24"}}
	f.containers = append(f.containers, c)
	return spec.Name, nil
}

func (f *stackFake) StartContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.containers {
		if f.containers[i].ID == id {
			f.containers[i].Status.State = engine.StateRunning
		}
	}
	return nil
}

func (f *stackFake) StopContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.containers {
		if f.containers[i].ID == id {
			f.containers[i].Status.State = engine.StateStopped
		}
	}
	return nil
}

func (f *stackFake) DeleteContainer(_ context.Context, id string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.containers[:0]
	for _, c := range f.containers {
		if c.ID != id {
			out = append(out, c)
		}
	}
	f.containers = out
	return nil
}

func (f *stackFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.containers)
}

var (
	_ engine.Engine = (*stackFake)(nil)
	_ stacks.Engine = (*stackFake)(nil)
)

func testStackServer(f *stackFake) *Server {
	return New(f, Config{
		AllowedHosts:   []string{"127.0.0.1:9191", "localhost:9191"},
		AllowedOrigins: []string{"http://127.0.0.1:9191"},
		Stacks:         stacks.NewManager(stacks.NewMemStore(), f, idlock.New()),
	})
}

// postJSON builds a same-origin POST with a JSON body that clears the guard.
func postJSON(target, body string) *http.Request {
	r := httptest.NewRequest("POST", target, strings.NewReader(body))
	r.Host = "127.0.0.1:9191"
	r.Header.Set("Origin", "http://127.0.0.1:9191")
	r.Header.Set("Content-Type", "application/json")
	return r
}

const twoSvcCompose = `services:
  api:
    image: node
    restart: always
  web:
    image: nginx
    depends_on: [api]
`

func importStack(t *testing.T, srv *Server, name, compose string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "compose": compose})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/stacks", string(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("import %s: status %d, body %s", name, rec.Code, rec.Body.String())
	}
}

func TestStackValidateEndpoint(t *testing.T) {
	srv := testStackServer(newStackFake())

	// Valid compose → Valid true.
	body, _ := json.Marshal(map[string]string{"name": "shop", "compose": twoSvcCompose})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/stacks/validate", string(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("validate status %d", rec.Code)
	}
	var rep stacks.ValidationReport
	json.Unmarshal(rec.Body.Bytes(), &rep)
	if !rep.Valid {
		t.Errorf("expected valid, got %+v", rep)
	}

	// build: key → Valid false + rejected, NO side effects.
	bad := "services:\n  web:\n    image: nginx\n    build: .\n"
	body, _ = json.Marshal(map[string]string{"name": "shop", "compose": bad})
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/stacks/validate", string(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("validate(bad) status %d", rec.Code)
	}
	json.Unmarshal(rec.Body.Bytes(), &rep)
	if rep.Valid || len(rep.Rejected) == 0 {
		t.Errorf("expected invalid with rejected keys, got %+v", rep)
	}
}

func TestStackImportRejectsInvalid(t *testing.T) {
	srv := testStackServer(newStackFake())
	bad := "services:\n  web:\n    image: nginx\n    restart: on-failure\n"
	body, _ := json.Marshal(map[string]string{"name": "shop", "compose": bad})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/stacks", string(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("import(invalid) status %d, want 400", rec.Code)
	}
	// And it must NOT be stored.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req("GET", "/api/stacks"))
	var views []stacks.StackView
	json.Unmarshal(rec.Body.Bytes(), &views)
	if len(views) != 0 {
		t.Errorf("invalid stack should not be stored: %+v", views)
	}
}

func TestStackUpDownLifecycle(t *testing.T) {
	f := newStackFake()
	srv := testStackServer(f)
	importStack(t, srv, "shop", twoSvcCompose)

	// up → 200, status up.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/stacks/shop/up", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("up status %d: %s", rec.Code, rec.Body.String())
	}
	var up stacks.UpResult
	json.Unmarshal(rec.Body.Bytes(), &up)
	if up.Status != "up" {
		t.Errorf("up status field = %q", up.Status)
	}
	if f.count() != 2 {
		t.Fatalf("want 2 containers after up, got %d", f.count())
	}

	// GET reflects up + members + IPs.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req("GET", "/api/stacks/shop"))
	var view stacks.StackView
	json.Unmarshal(rec.Body.Bytes(), &view)
	if view.Status != "up" || len(view.Members) != 2 {
		t.Fatalf("view = %+v", view)
	}
	for _, m := range view.Members {
		if m.State != engine.StateRunning || m.IP == "" {
			t.Errorf("member %+v missing running state/IP", m)
		}
	}

	// down → 200, removed; GET shows down + no members.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/stacks/shop/down", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("down status %d", rec.Code)
	}
	if f.count() != 0 {
		t.Errorf("want 0 containers after down, got %d", f.count())
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req("GET", "/api/stacks/shop"))
	json.Unmarshal(rec.Body.Bytes(), &view)
	if view.Status != "down" || len(view.Members) != 0 {
		t.Errorf("after down view = %+v", view)
	}
}

func TestStackPlanIsReadOnly(t *testing.T) {
	f := newStackFake()
	srv := testStackServer(f)
	importStack(t, srv, "shop", twoSvcCompose)

	before := f.count()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/stacks/shop/plan", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("plan status %d", rec.Code)
	}
	var plan stacks.Plan
	json.Unmarshal(rec.Body.Bytes(), &plan)
	if len(plan.Actions) != 2 {
		t.Errorf("plan actions = %+v", plan.Actions)
	}
	for _, a := range plan.Actions {
		if a.Action != stacks.ActionCreate {
			t.Errorf("expected create, got %s", a.Action)
		}
	}
	if f.count() != before {
		t.Errorf("plan mutated state: %d -> %d", before, f.count())
	}
}

func TestStackUpForeignOriginRejected(t *testing.T) {
	srv := testStackServer(newStackFake())
	r := httptest.NewRequest("POST", "/api/stacks/shop/up", nil)
	r.Host = "127.0.0.1:9191"
	r.Header.Set("Origin", "https://evil.example.com") // CSRF attempt
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (origin not allowed)", rec.Code)
	}
}

func TestStackUpGatedWhenDaemonDown(t *testing.T) {
	f := newStackFake()
	f.status = engine.SystemStatus{APIServerRunning: false, Detail: "XPC connection error"}
	srv := testStackServer(f)
	importStack(t, srv, "shop", twoSvcCompose) // import is store-only, works when down

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/stacks/shop/up", ""))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("up status = %d, want 503 (gated)", rec.Code)
	}
}

func TestStackNotFound(t *testing.T) {
	srv := testStackServer(newStackFake())
	for _, target := range []string{"/api/stacks/ghost/up", "/api/stacks/ghost/plan"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, postJSON(target, ""))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", target, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req("GET", "/api/stacks/ghost"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET ghost status = %d, want 404", rec.Code)
	}
}

func TestStackDeleteKeepsContainers(t *testing.T) {
	f := newStackFake()
	srv := testStackServer(f)
	importStack(t, srv, "shop", twoSvcCompose)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/stacks/shop/up", ""))
	if f.count() != 2 {
		t.Fatalf("setup: want 2 containers")
	}

	// DELETE removes the definition but NOT the running containers.
	r := httptest.NewRequest("DELETE", "/api/stacks/shop", nil)
	r.Host = "127.0.0.1:9191"
	r.Header.Set("Origin", "http://127.0.0.1:9191")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("delete status %d", rec.Code)
	}
	if f.count() != 2 {
		t.Errorf("delete must not remove containers: count=%d", f.count())
	}
	// The stored definition is gone.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req("GET", "/api/stacks/shop"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("stack should be deleted, GET status=%d", rec.Code)
	}
}
