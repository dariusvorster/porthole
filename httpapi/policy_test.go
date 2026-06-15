package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/supervisor"
)

func policyServer(t *testing.T) (*Server, supervisor.Store) {
	t.Helper()
	store := supervisor.NewMemStore()
	sup := supervisor.New(store, &fakeEngine{}, nil, nil, supervisor.DefaultConfig(), nil)
	srv := New(&fakeEngine{status: engine.SystemStatus{APIServerRunning: true}}, Config{
		AllowedHosts:   []string{"127.0.0.1:9191", "localhost:9191"},
		AllowedOrigins: []string{"http://127.0.0.1:9191"},
		Supervision:    sup,
	})
	return srv, store
}

func putPolicy(body string) *http.Request {
	r := httptest.NewRequest("PUT", "/api/containers/web/policy", strings.NewReader(body))
	r.Host = "127.0.0.1:9191"
	r.Header.Set("Origin", "http://127.0.0.1:9191")
	return r
}

func TestPolicyEndpointPersists(t *testing.T) {
	srv, store := policyServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, putPolicy(`{"restart":"always","health":{"type":"http","port":80,"path":"/"}}`))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	p, ok, err := store.GetPolicy("web")
	if err != nil || !ok {
		t.Fatalf("GetPolicy ok=%v err=%v", ok, err)
	}
	if p.Restart != supervisor.RestartAlways {
		t.Errorf("restart = %q, want always", p.Restart)
	}
	if p.Health == nil || p.Health.Type != "http" || p.Health.Port != 80 {
		t.Errorf("health not persisted: %+v", p.Health)
	}
}

func TestPolicyEndpointRejectsOnFailure(t *testing.T) {
	srv, _ := policyServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, putPolicy(`{"restart":"on-failure"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (on-failure unsupported)", rec.Code)
	}
}

func TestPolicyEndpointForeignOriginRejected(t *testing.T) {
	srv, _ := policyServer(t)
	r := httptest.NewRequest("PUT", "/api/containers/web/policy", strings.NewReader(`{"restart":"always"}`))
	r.Host = "127.0.0.1:9191"
	r.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
