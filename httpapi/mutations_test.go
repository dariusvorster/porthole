package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/porthole/porthole/engine"
)

// postReq is a mutation request with a valid same-origin Origin so it clears the
// browser guard's CSRF check.
func postReq(method, target string) *http.Request {
	r := req(method, target)
	r.Header.Set("Origin", "http://127.0.0.1:9191")
	return r
}

func TestMutationHappyPaths(t *testing.T) {
	cases := []struct{ method, target string }{
		{"POST", "/api/containers/web/start"},
		{"POST", "/api/containers/web/stop"},
		{"POST", "/api/containers/web/restart"},
		{"POST", "/api/containers/web/kill"},
		{"DELETE", "/api/containers/web"},
	}
	for _, c := range cases {
		f := &fakeEngine{status: engine.SystemStatus{APIServerRunning: true}}
		rec := httptest.NewRecorder()
		testServer(f).ServeHTTP(rec, postReq(c.method, c.target))
		if rec.Code != http.StatusAccepted {
			t.Errorf("%s %s: status = %d, want 202", c.method, c.target, rec.Code)
		}
	}
}

func TestMutationErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"name_conflict", &engine.CLIError{Kind: engine.ErrNameConflict, Message: "already exists"}, http.StatusConflict},
		{"not_found", &engine.CLIError{Kind: engine.ErrNotFound, Message: "no such container"}, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeEngine{status: engine.SystemStatus{APIServerRunning: true}, mutErr: c.err}
			rec := httptest.NewRecorder()
			testServer(f).ServeHTTP(rec, postReq("POST", "/api/containers/web/start"))
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d", rec.Code, c.want)
			}
			var env errorEnvelope
			_ = json.Unmarshal(rec.Body.Bytes(), &env)
			if env.Error.Kind == "" {
				t.Errorf("missing typed error kind in body")
			}
		})
	}
}

func TestMutationForeignOriginRejected(t *testing.T) {
	f := &fakeEngine{status: engine.SystemStatus{APIServerRunning: true}}
	r := req("POST", "/api/containers/web/stop")
	r.Header.Set("Origin", "https://evil.example.com") // CSRF attempt
	rec := httptest.NewRecorder()
	testServer(f).ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (origin not allowed)", rec.Code)
	}
}

func TestMutationBlockedWhenDaemonDown(t *testing.T) {
	f := &fakeEngine{status: engine.SystemStatus{APIServerRunning: false}}
	rec := httptest.NewRecorder()
	testServer(f).ServeHTTP(rec, postReq("POST", "/api/containers/web/stop"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (gated)", rec.Code)
	}
}

// TestPerIDLockSerializesSameContainer fires two concurrent stops at the same
// container and asserts the engine never saw them overlap (run under -race).
func TestPerIDLockSerializesSameContainer(t *testing.T) {
	f := &fakeEngine{status: engine.SystemStatus{APIServerRunning: true}, mutDelay: 40 * time.Millisecond}
	srv := testServer(f)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, postReq("POST", "/api/containers/web/stop"))
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&f.maxActive); got > 1 {
		t.Fatalf("maxActive = %d, want 1 — mutations on the same id must serialize", got)
	}
}

// TestDifferentIDsRunInParallel confirms the lock is per-id, not global.
func TestDifferentIDsRunInParallel(t *testing.T) {
	f := &fakeEngine{status: engine.SystemStatus{APIServerRunning: true}, mutDelay: 40 * time.Millisecond}
	srv := testServer(f)

	var wg sync.WaitGroup
	for _, id := range []string{"web", "api"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, postReq("POST", "/api/containers/"+id+"/stop"))
		}(id)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&f.maxActive); got < 2 {
		t.Fatalf("maxActive = %d, want 2 — distinct ids should not block each other", got)
	}
}
