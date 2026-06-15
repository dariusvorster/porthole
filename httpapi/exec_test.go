package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/porthole/porthole/engine"
)

func execServer(t *testing.T, f *fakeEngine) *httptest.Server {
	t.Helper()
	srv := New(f, Config{
		// Bare hosts so the random httptest port clears the Host guard; Origin is
		// the real check this suite exercises.
		AllowedHosts:   []string{"127.0.0.1", "localhost"},
		AllowedOrigins: []string{"http://127.0.0.1:9191"},
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func dialExec(t *testing.T, ts *httptest.Server, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/containers/web/exec"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	hdr := http.Header{}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	return websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
}

func runningFake() *fakeEngine {
	return &fakeEngine{
		status:     engine.SystemStatus{APIServerRunning: true},
		containers: []engine.Container{{ID: "web", Status: engine.ContainerStatus{State: "running"}}},
	}
}

// THE security assertion: a cross-origin upgrade is WebSocket CSRF → rejected by
// the browser-guard with 403, before any upgrade.
func TestExecForeignOriginRejected(t *testing.T) {
	ts := execServer(t, runningFake())
	conn, resp, err := dialExec(t, ts, "https://evil.example.com")
	if conn != nil {
		conn.Close(websocket.StatusNormalClosure, "")
	}
	if err == nil {
		t.Fatal("foreign-origin upgrade succeeded — must be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

func TestExecSameOriginEchoes(t *testing.T) {
	f := runningFake()
	ts := execServer(t, f)
	conn, _, err := dialExec(t, ts, "http://127.0.0.1:9191")
	if err != nil {
		t.Fatalf("same-origin upgrade failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("hi-there")); err != nil {
		t.Fatalf("write: %v", err)
	}
	mt, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if mt != websocket.MessageBinary || string(data) != "hi-there" {
		t.Errorf("echo = %q (%v), want hi-there binary", data, mt)
	}
	if got := f.execSpawns; got != 1 {
		t.Errorf("execSpawns = %d, want 1", got)
	}
}

func TestExecStoppedRefused(t *testing.T) {
	f := &fakeEngine{
		status:     engine.SystemStatus{APIServerRunning: true},
		containers: []engine.Container{{ID: "web", Status: engine.ContainerStatus{State: "stopped"}}},
	}
	ts := execServer(t, f)
	conn, resp, err := dialExec(t, ts, "http://127.0.0.1:9191")
	if conn != nil {
		conn.Close(websocket.StatusNormalClosure, "")
	}
	if err == nil {
		t.Fatal("exec on a stopped container succeeded — must be refused")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %v, want 409", resp)
	}
	if got := f.execSpawns; got != 0 {
		t.Errorf("execSpawns = %d, want 0 (no session for a stopped container)", got)
	}
}

func TestExecConcurrentCap(t *testing.T) {
	f := runningFake()
	ts := execServer(t, f)

	var open []*websocket.Conn
	for i := 0; i < execMaxSessions; i++ {
		c, _, err := dialExec(t, ts, "http://127.0.0.1:9191")
		if err != nil {
			t.Fatalf("session %d failed: %v", i, err)
		}
		open = append(open, c)
	}
	// Give the handlers a moment to increment the active counter.
	time.Sleep(200 * time.Millisecond)

	// One past the cap → 429.
	c, resp, err := dialExec(t, ts, "http://127.0.0.1:9191")
	if c != nil {
		c.Close(websocket.StatusNormalClosure, "")
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("over-cap status = %v, want 429", resp)
	}
	for _, c := range open {
		c.Close(websocket.StatusNormalClosure, "")
	}
}
