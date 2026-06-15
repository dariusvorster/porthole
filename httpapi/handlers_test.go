package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/porthole/porthole/engine"
)

// fakeEngine is a programmable Engine for handler tests — no real `container`.
type fakeEngine struct {
	status     engine.SystemStatus
	containers []engine.Container
	networks   []engine.Network
	stats      []engine.Stats
	err        error // when set, read methods return it

	// Mutation controls.
	mutErr    error         // when set, mutation methods return it
	mutDelay  time.Duration // simulated work, to expose concurrency
	active    int32         // current in-flight mutations (atomic)
	maxActive int32         // high-water mark of concurrent mutations (atomic)

	// Logs controls.
	logHistory   string               // returned for a Tail (non-follow) read
	logFollow    func() io.ReadCloser // returns a fresh follow reader per call
	followSpawns int32                // count of follow spawns (atomic)

	// Exec controls.
	execErr    error // when set, Exec returns it
	execSpawns int32 // count of exec spawns (atomic)
}

// track records mutation concurrency and simulates work; returns a cleanup. Used
// as `defer f.track()()` so the increment/sleep happen at method entry and the
// decrement at return.
func (f *fakeEngine) track() func() {
	n := atomic.AddInt32(&f.active, 1)
	for {
		m := atomic.LoadInt32(&f.maxActive)
		if n <= m || atomic.CompareAndSwapInt32(&f.maxActive, m, n) {
			break
		}
	}
	if f.mutDelay > 0 {
		time.Sleep(f.mutDelay)
	}
	return func() { atomic.AddInt32(&f.active, -1) }
}

func (f *fakeEngine) SystemVersion(context.Context) ([]engine.VersionEntry, error) {
	return []engine.VersionEntry{{AppName: "container", Version: "1.0.0"}}, nil
}
func (f *fakeEngine) SystemStatus(context.Context) (engine.SystemStatus, error) {
	return f.status, nil
}
func (f *fakeEngine) DiskUsage(context.Context) (engine.DiskUsage, error) {
	return engine.DiskUsage{}, f.err
}
func (f *fakeEngine) ListContainers(context.Context, bool) ([]engine.Container, error) {
	return f.containers, f.err
}
func (f *fakeEngine) InspectContainer(_ context.Context, id string) (engine.Container, error) {
	if f.err != nil {
		return engine.Container{}, f.err
	}
	for _, c := range f.containers {
		if c.ID == id {
			return c, nil
		}
	}
	return engine.Container{}, &engine.CLIError{Kind: engine.ErrNotFound, Message: "no such container"}
}
func (f *fakeEngine) Stats(context.Context, ...string) ([]engine.Stats, error) {
	return f.stats, f.err
}
func (f *fakeEngine) StartContainer(context.Context, string) error        { defer f.track()(); return f.mutErr }
func (f *fakeEngine) StopContainer(context.Context, string) error         { defer f.track()(); return f.mutErr }
func (f *fakeEngine) KillContainer(context.Context, string, string) error { defer f.track()(); return f.mutErr }
func (f *fakeEngine) DeleteContainer(context.Context, string, bool) error { defer f.track()(); return f.mutErr }
func (f *fakeEngine) Logs(ctx context.Context, _ string, opts engine.LogOpts) (io.ReadCloser, error) {
	if !opts.Follow {
		return io.NopCloser(strings.NewReader(f.logHistory)), nil
	}
	atomic.AddInt32(&f.followSpawns, 1)
	var rc io.ReadCloser
	if f.logFollow != nil {
		rc = f.logFollow()
	} else {
		rc = io.NopCloser(strings.NewReader(""))
	}
	// Mirror exec.CommandContext: ctx cancel terminates the follow reader.
	go func() { <-ctx.Done(); _ = rc.Close() }()
	return rc, nil
}

func (f *fakeEngine) Exec(ctx context.Context, _ string, _ []string, _ engine.WinSize) (engine.ExecSession, error) {
	if f.execErr != nil {
		return nil, f.execErr
	}
	atomic.AddInt32(&f.execSpawns, 1)
	s := newEchoSession()
	go func() { <-ctx.Done(); _ = s.Close() }()
	return s, nil
}

// echoSession is a fake ExecSession: bytes written (stdin) come back on Read
// (output), so a WS round-trip can be asserted without a real PTY.
type echoSession struct {
	r       *io.PipeReader
	w       *io.PipeWriter
	mu      sync.Mutex
	resizes []engine.WinSize
}

func newEchoSession() *echoSession {
	r, w := io.Pipe()
	return &echoSession{r: r, w: w}
}

func (s *echoSession) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *echoSession) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s *echoSession) Resize(cols, rows uint16) error {
	s.mu.Lock()
	s.resizes = append(s.resizes, engine.WinSize{Cols: cols, Rows: rows})
	s.mu.Unlock()
	return nil
}
func (s *echoSession) Close() error {
	_ = s.r.Close()
	_ = s.w.Close()
	return nil
}
func (f *fakeEngine) ListNetworks(context.Context) ([]engine.Network, error) {
	return f.networks, f.err
}
func (f *fakeEngine) InspectNetwork(context.Context, string) (engine.Network, error) {
	return engine.Network{}, f.err
}

var _ engine.Engine = (*fakeEngine)(nil)

func testServer(f *fakeEngine) *Server {
	return New(f, Config{
		AllowedHosts:   []string{"127.0.0.1:9191", "localhost:9191"},
		AllowedOrigins: []string{"http://127.0.0.1:9191"},
	})
}

// req builds a request with a valid Host so it clears the browser guard unless
// a test overrides it.
func req(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Host = "127.0.0.1:9191"
	return r
}

func TestListContainersHappyPath(t *testing.T) {
	f := &fakeEngine{
		status:     engine.SystemStatus{APIServerRunning: true, CLIVersion: "1.0.0"},
		containers: []engine.Container{{ID: "web"}, {ID: "api"}},
	}
	rec := httptest.NewRecorder()
	testServer(f).ServeHTTP(rec, req("GET", "/api/containers"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []engine.Container
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d containers, want 2", len(got))
	}
}

func TestBootstrapGateBlocksWhenDaemonDown(t *testing.T) {
	f := &fakeEngine{status: engine.SystemStatus{APIServerRunning: false, Detail: "XPC connection error"}}
	rec := httptest.NewRecorder()
	testServer(f).ServeHTTP(rec, req("GET", "/api/containers"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (blocked state)", rec.Code)
	}
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Kind != string(engine.ErrDaemonDown) {
		t.Errorf("kind = %q, want daemon_down", env.Error.Kind)
	}
}

func TestStatusEndpointNotGated(t *testing.T) {
	// Even when the runtime is down, /status must answer (the banner reads it).
	f := &fakeEngine{status: engine.SystemStatus{APIServerRunning: false}}
	rec := httptest.NewRecorder()
	testServer(f).ServeHTTP(rec, req("GET", "/api/system/status"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestInspectNotFound(t *testing.T) {
	f := &fakeEngine{
		status:     engine.SystemStatus{APIServerRunning: true},
		containers: []engine.Container{{ID: "web"}},
	}
	rec := httptest.NewRecorder()
	testServer(f).ServeHTTP(rec, req("GET", "/api/containers/ghost"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRebindingHostRejected(t *testing.T) {
	f := &fakeEngine{status: engine.SystemStatus{APIServerRunning: true}}
	r := httptest.NewRequest("GET", "/api/containers", nil)
	r.Host = "evil.example.com" // attacker-controlled name pointed at 127.0.0.1
	rec := httptest.NewRecorder()
	testServer(f).ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (host not allowed)", rec.Code)
	}
}

func TestForeignOriginRejected(t *testing.T) {
	f := &fakeEngine{status: engine.SystemStatus{APIServerRunning: true}}
	r := req("GET", "/api/containers")
	r.Header.Set("Origin", "https://evil.example.com") // CSRF attempt
	rec := httptest.NewRecorder()
	testServer(f).ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (origin not allowed)", rec.Code)
	}
}
