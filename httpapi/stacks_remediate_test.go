package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/stacks"
)

func driftedMember(id, svc, image string) engine.Container {
	c := engine.Container{ID: id}
	c.Configuration.ID = id
	c.Configuration.Labels = map[string]string{"porthole.stack": "shop", "porthole.service": svc}
	c.Configuration.Image.Reference = image
	c.Configuration.Networks = []engine.NetworkAttach{{Network: "shop"}}
	c.Status.State = engine.StateRunning
	return c
}

func TestStackRemediateEndpoint(t *testing.T) {
	f := newStackFake()
	srv := testStackServer(f)
	importStack(t, srv, "shop", "services:\n  api:\n    image: nginx:2.0\n")
	// seed a running member on the OLD image → planner flags a recreate
	f.containers = append(f.containers, driftedMember("shop-api", "api", "nginx:1.0"))

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/stacks/shop/remediate", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var res stacks.RemediateResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || res.Applied[0].Service != "api" || res.Applied[0].Outcome != stacks.OutcomeRecreated {
		t.Fatalf("expected api recreated, got %+v", res.Applied)
	}
}

func TestStackRemediateUnknown(t *testing.T) {
	srv := testStackServer(newStackFake())
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/stacks/ghost/remediate", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

func TestStackRemediateForeignOriginRejected(t *testing.T) {
	f := newStackFake()
	srv := testStackServer(f)
	importStack(t, srv, "shop", "services:\n  api:\n    image: nginx:2.0\n")
	r := httptest.NewRequest("POST", "/api/stacks/shop/remediate", nil)
	r.Host = "127.0.0.1:9191"
	r.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}
