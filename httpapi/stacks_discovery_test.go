package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/porthole/porthole/stacks"
)

func TestStackDiscoveryToggle(t *testing.T) {
	srv := testStackServer(newStackFake())
	importStack(t, srv, "shop", twoSvcCompose)

	// default: off
	if got := getStackView(t, srv, "shop").Discovery; got {
		t.Fatal("discovery should default off")
	}

	// toggle on → 202, reflected in GET
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/stacks/shop/discovery", `{"enabled":true}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("toggle on: status %d (%s)", rec.Code, rec.Body.String())
	}
	if !getStackView(t, srv, "shop").Discovery {
		t.Error("discovery flag not reflected after toggle on")
	}

	// toggle off
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/stacks/shop/discovery", `{"enabled":false}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("toggle off: status %d", rec.Code)
	}
	if getStackView(t, srv, "shop").Discovery {
		t.Error("discovery flag should be off after toggle off")
	}
}

func TestStackDiscoveryUnknownStack(t *testing.T) {
	srv := testStackServer(newStackFake())
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, postJSON("/api/stacks/ghost/discovery", `{"enabled":true}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown stack: status %d, want 404", rec.Code)
	}
}

func TestStackDiscoveryForeignOriginRejected(t *testing.T) {
	srv := testStackServer(newStackFake())
	importStack(t, srv, "shop", twoSvcCompose)
	r := httptest.NewRequest("POST", "/api/stacks/shop/discovery", nil)
	r.Host = "127.0.0.1:9191"
	r.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin: status %d, want 403", rec.Code)
	}
}

func getStackView(t *testing.T, srv *Server, name string) stacks.StackView {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req("GET", "/api/stacks/"+name))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET stack %s: status %d", name, rec.Code)
	}
	var v stacks.StackView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
