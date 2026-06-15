package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/supervisor"
)

// fakeCreator implements CreateEngine: RunStream replays a scripted update list;
// ImageList returns a fixed slice. It records the last RunSpec for assertions.
type fakeCreator struct {
	updates  []engine.RunUpdate
	images   []engine.Image
	lastSpec engine.RunSpec
}

func (f *fakeCreator) RunStream(_ context.Context, spec engine.RunSpec) <-chan engine.RunUpdate {
	f.lastSpec = spec
	ch := make(chan engine.RunUpdate, len(f.updates)+1)
	for _, u := range f.updates {
		ch <- u
	}
	close(ch)
	return ch
}

func (f *fakeCreator) ImageList(context.Context) ([]engine.Image, error) { return f.images, nil }

var _ CreateEngine = (*fakeCreator)(nil)

// recordingSup implements Supervision, recording mediated starts + policies.
type recordingSup struct {
	started  []string
	policies []supervisor.Policy
}

func (s *recordingSup) RecordStart(id string) { s.started = append(s.started, id) }
func (s *recordingSup) RecordStop(string)     {}
func (s *recordingSup) ApplyPolicy(p supervisor.Policy) error {
	s.policies = append(s.policies, p)
	return nil
}

var _ Supervision = (*recordingSup)(nil)

func createServer(eng *fakeEngine, cr *fakeCreator, sup Supervision) *Server {
	return New(eng, Config{
		AllowedHosts:   []string{"127.0.0.1:9191", "localhost:9191"},
		AllowedOrigins: []string{"http://127.0.0.1:9191"},
		Creator:        cr,
		Supervision:    sup,
	})
}

func upEngine() *fakeEngine {
	return &fakeEngine{status: engine.SystemStatus{APIServerRunning: true, CLIVersion: "1.0.0"}}
}

func TestCreateStreamsProgressThenCreated(t *testing.T) {
	cr := &fakeCreator{updates: []engine.RunUpdate{
		{Kind: "progress", Index: 1, Total: 6, Phase: "Fetching image"},
		{Kind: "progress", Index: 6, Total: 6, Phase: "Starting container"},
		{Kind: "created", ID: "web"},
	}}
	sup := &recordingSup{}
	srv := createServer(upEngine(), cr, sup)

	body := `{"image":"nginx","name":"web","ports":[{"hostPort":8080,"containerPort":80}]}`
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/containers", body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	if !strings.Contains(out, "event: progress") || !strings.Contains(out, "Fetching image") {
		t.Errorf("missing progress events: %s", out)
	}
	if !strings.Contains(out, "event: created") || !strings.Contains(out, `"id":"web"`) {
		t.Errorf("missing created event: %s", out)
	}
	// Mapping reached the engine.
	if cr.lastSpec.Image != "nginx" || cr.lastSpec.Name != "web" {
		t.Errorf("spec = %+v", cr.lastSpec)
	}
	if len(cr.lastSpec.Ports) != 1 || cr.lastSpec.Ports[0].HostPort != 8080 || cr.lastSpec.Ports[0].Proto != "tcp" {
		t.Errorf("ports = %+v", cr.lastSpec.Ports)
	}
	// Mediated start recorded (desired=running).
	if len(sup.started) != 1 || sup.started[0] != "web" {
		t.Errorf("recordStart = %v", sup.started)
	}
}

func TestCreateRestartWritesLabelAndDesired(t *testing.T) {
	cr := &fakeCreator{updates: []engine.RunUpdate{{Kind: "created", ID: "svc"}}}
	sup := &recordingSup{}
	srv := createServer(upEngine(), cr, sup)

	body := `{"image":"nginx","name":"svc","restart":"always"}`
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/containers", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if cr.lastSpec.Labels["porthole.restart"] != "always" {
		t.Errorf("restart label = %q", cr.lastSpec.Labels["porthole.restart"])
	}
	if len(sup.started) != 1 || sup.started[0] != "svc" {
		t.Errorf("desired=running not recorded: %v", sup.started)
	}
}

func TestCreateImagePullFailed(t *testing.T) {
	cr := &fakeCreator{updates: []engine.RunUpdate{
		{Kind: "progress", Index: 1, Total: 6, Phase: "Fetching image"},
		{Kind: "error", Err: &engine.CLIError{Kind: engine.ErrImagePullFailed, Message: "401 Unauthorized", Raw: "…401…"}},
	}}
	srv := createServer(upEngine(), cr, &recordingSup{})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/containers", `{"image":"nope"}`))
	out := rec.Body.String()
	if !strings.Contains(out, "event: error") || !strings.Contains(out, "image_pull_failed") {
		t.Errorf("missing pull-failure error: %s", out)
	}
	if !strings.Contains(out, "image not found or inaccessible") {
		t.Errorf("expected friendly message: %s", out)
	}
	if strings.Contains(out, "event: created") {
		t.Error("must not emit created on pull failure")
	}
}

func TestCreateInvalidSpec(t *testing.T) {
	srv := createServer(upEngine(), &fakeCreator{}, &recordingSup{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/containers", `{"name":"x"}`)) // no image
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	var env errorEnvelope
	json.Unmarshal(rec.Body.Bytes(), &env)
	if !strings.Contains(env.Error.Message, "image is required") {
		t.Errorf("message = %q", env.Error.Message)
	}
}

func TestCreateForeignOriginRejected(t *testing.T) {
	srv := createServer(upEngine(), &fakeCreator{}, &recordingSup{})
	r := httptest.NewRequest("POST", "/api/containers", strings.NewReader(`{"image":"nginx"}`))
	r.Host = "127.0.0.1:9191"
	r.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

func TestCreateGatedWhenDaemonDown(t *testing.T) {
	eng := &fakeEngine{status: engine.SystemStatus{APIServerRunning: false, Detail: "XPC connection error"}}
	srv := createServer(eng, &fakeCreator{}, &recordingSup{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/containers", `{"image":"nginx"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}

func TestImagesEndpoint(t *testing.T) {
	cr := &fakeCreator{images: []engine.Image{
		{Reference: "docker.io/library/alpine:latest", Digest: "sha256:a", Size: 100},
		{Reference: "docker.io/library/nginx:latest", Digest: "sha256:b", Size: 200},
	}}
	srv := createServer(upEngine(), cr, &recordingSup{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req("GET", "/api/images"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var imgs []engine.Image
	json.Unmarshal(rec.Body.Bytes(), &imgs)
	if len(imgs) != 2 || imgs[0].Reference != "docker.io/library/alpine:latest" {
		t.Errorf("images = %+v", imgs)
	}
}
