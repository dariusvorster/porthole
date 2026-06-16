package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/porthole/porthole/engine"
)

func TestNetworkCreateEndpoint(t *testing.T) {
	res := &fakeResources{}
	srv := resServer(upEngine(), res)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/networks", `{"name":"nw","subnet":"10.88.0.0/24","internal":true,"labels":{"env":"dev"}}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	// the spec reached the engine
	if res.createdNet.Name != "nw" || res.createdNet.Subnet != "10.88.0.0/24" || !res.createdNet.Internal || res.createdNet.Labels["env"] != "dev" {
		t.Errorf("spec not mapped: %+v", res.createdNet)
	}
	// the returned Network carries the auto-assigned subnet for the UI
	var n engine.Network
	json.Unmarshal(rec.Body.Bytes(), &n)
	if n.Configuration.Name != "nw" || n.Status.IPv4Subnet == "" {
		t.Errorf("response missing network/subnet: %+v", n)
	}
}

func TestNetworkCreateRequiresName(t *testing.T) {
	srv := resServer(upEngine(), &fakeResources{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/networks", `{"subnet":"10.0.0.0/24"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestNetworkCreateConflict(t *testing.T) {
	res := &fakeResources{netCreateErr: &engine.CLIError{Kind: engine.ErrNameConflict, Message: "network nw already exists"}}
	srv := resServer(upEngine(), res)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/networks", `{"name":"nw"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	var env errorEnvelope
	json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Kind != string(engine.ErrNameConflict) {
		t.Errorf("kind = %q, want name_conflict", env.Error.Kind)
	}
}

func TestNetworkCreateGatedAndOrigin(t *testing.T) {
	down := &fakeEngine{status: engine.SystemStatus{APIServerRunning: false}}
	rec := httptest.NewRecorder()
	resServer(down, &fakeResources{}).ServeHTTP(rec, postJSON("/api/networks", `{"name":"nw"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("gated status %d, want 503", rec.Code)
	}
	r := httptest.NewRequest("POST", "/api/networks", strings.NewReader(`{"name":"nw"}`))
	r.Host = "127.0.0.1:9191"
	r.Header.Set("Origin", "https://evil.example.com")
	rec = httptest.NewRecorder()
	resServer(upEngine(), &fakeResources{}).ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin status %d, want 403", rec.Code)
	}
}

func TestNetworkDeleteInUse(t *testing.T) {
	res := &fakeResources{netRemoveErr: &engine.CLIError{Kind: engine.ErrNetworkInUse, Message: "network is in use by nw-c"}}
	srv := resServer(upEngine(), res)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req("DELETE", "/api/networks/nw"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	var env errorEnvelope
	json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Kind != string(engine.ErrNetworkInUse) || !strings.Contains(env.Error.Message, "nw-c") {
		t.Errorf("expected network_in_use naming nw-c, got %+v", env.Error)
	}
}

func TestNetworkDeleteSuccess(t *testing.T) {
	srv := resServer(upEngine(), &fakeResources{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req("DELETE", "/api/networks/nw"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202", rec.Code)
	}
}
