package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/porthole/porthole/engine"
)

type fakeWatcher struct{ ch chan struct{} }

func (w *fakeWatcher) WatchStopped(ctx context.Context, _ string) <-chan struct{} { return w.ch }

type sseFrame struct{ event, data string }

func readFrames(r io.Reader) <-chan sseFrame {
	out := make(chan sseFrame)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		var ev, data string
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				if ev != "" || data != "" {
					out <- sseFrame{ev, data}
					ev, data = "", ""
				}
				continue
			}
			if s, ok := strings.CutPrefix(line, "event: "); ok {
				ev = s
			}
			if s, ok := strings.CutPrefix(line, "data: "); ok {
				data = s
			}
		}
	}()
	return out
}

func nextFrame(t *testing.T, frames <-chan sseFrame) sseFrame {
	t.Helper()
	select {
	case f, ok := <-frames:
		if !ok {
			t.Fatal("frame stream closed unexpectedly")
		}
		return f
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SSE frame")
		return sseFrame{}
	}
}

func logsServer(t *testing.T, f *fakeEngine, w LogWatcher) *httptest.Server {
	t.Helper()
	srv := New(f, Config{
		AllowedHosts:   []string{"127.0.0.1:9191", "localhost:9191"},
		AllowedOrigins: []string{"http://127.0.0.1:9191"},
		LogWatcher:     w,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func openLogs(t *testing.T, ts *httptest.Server) (*http.Response, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/containers/web/logs", nil)
	req.Host = "127.0.0.1:9191" // pass the browser guard regardless of test port
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET logs: %v", err)
	}
	return resp, cancel
}

func decodeLines(t *testing.T, data string) []string {
	t.Helper()
	var v logLines
	if err := json.Unmarshal([]byte(data), &v); err != nil {
		t.Fatalf("decode lines: %v (%q)", err, data)
	}
	return v.Lines
}

func TestLogsHistoryThenLiveThenStop(t *testing.T) {
	pr, pw := io.Pipe()
	f := &fakeEngine{
		status:     engine.SystemStatus{APIServerRunning: true},
		containers: []engine.Container{{ID: "web", Status: engine.ContainerStatus{State: "running"}}},
		logHistory: "h1\nh2\n",
		logFollow:  func() io.ReadCloser { return pr },
	}
	watcher := &fakeWatcher{ch: make(chan struct{})}
	ts := logsServer(t, f, watcher)

	resp, cancel := openLogs(t, ts)
	defer cancel()
	defer resp.Body.Close()
	frames := readFrames(resp.Body)

	// 1. history first
	if h := nextFrame(t, frames); h.event != "history" || strings.Join(decodeLines(t, h.data), ",") != "h1,h2" {
		t.Fatalf("first frame = %+v, want history h1,h2", h)
	}
	// 2. a live line appended after history
	go func() { _, _ = pw.Write([]byte("live-1\n")) }()
	if l := nextFrame(t, frames); l.event != "log" || strings.Join(decodeLines(t, l.data), ",") != "live-1" {
		t.Fatalf("live frame = %+v, want log live-1", l)
	}
	// 3. container stops → terminal event, stream ends
	close(watcher.ch)
	var sawStopped bool
	for f := range frames {
		if f.event == "stopped" {
			sawStopped = true
			break
		}
	}
	if !sawStopped {
		t.Fatal("never received terminal 'stopped' event after container stop")
	}
}

func TestLogsStoppedOnOpen(t *testing.T) {
	f := &fakeEngine{
		status:     engine.SystemStatus{APIServerRunning: true},
		containers: []engine.Container{{ID: "web", Status: engine.ContainerStatus{State: "stopped"}}},
		logHistory: "old-1\nold-2\n",
	}
	ts := logsServer(t, f, &fakeWatcher{ch: make(chan struct{})})

	resp, cancel := openLogs(t, ts)
	defer cancel()
	defer resp.Body.Close()
	frames := readFrames(resp.Body)

	if h := nextFrame(t, frames); h.event != "history" || strings.Join(decodeLines(t, h.data), ",") != "old-1,old-2" {
		t.Fatalf("history = %+v", h)
	}
	if s := nextFrame(t, frames); s.event != "stopped" {
		t.Fatalf("expected stopped terminal, got %+v", s)
	}
	if got := f.followSpawns; got != 0 {
		t.Errorf("followSpawns = %d, want 0 (no follower for a stopped container)", got)
	}
}

func TestLogsFanoutOneChildPerContainer(t *testing.T) {
	f := &fakeEngine{logFollow: func() io.ReadCloser { pr, _ := io.Pipe(); return pr }}
	reg := newLogRegistry()
	w := &fakeWatcher{ch: make(chan struct{})}

	sub1, st1, err1 := reg.subscribe("web", f, w)
	sub2, st2, err2 := reg.subscribe("web", f, w)
	if err1 != nil || err2 != nil {
		t.Fatalf("subscribe errs: %v %v", err1, err2)
	}
	if st1 != st2 {
		t.Errorf("two subscribers to same id should share one stream")
	}
	if got := f.followSpawns; got != 1 {
		t.Errorf("followSpawns = %d, want 1 (one child fanned out)", got)
	}
	reg.unsubscribe("web", sub1)
	reg.unsubscribe("web", sub2)
	reg.mu.Lock()
	_, present := reg.streams["web"]
	reg.mu.Unlock()
	if present {
		t.Errorf("stream not torn down after last subscriber left")
	}
}

func TestLogsConcurrentCap(t *testing.T) {
	f := &fakeEngine{logFollow: func() io.ReadCloser { pr, _ := io.Pipe(); return pr }}
	reg := newLogRegistry()
	reg.max = 1
	w := &fakeWatcher{ch: make(chan struct{})}

	sub1, _, err1 := reg.subscribe("a", f, w)
	if err1 != nil {
		t.Fatalf("first subscribe: %v", err1)
	}
	if _, _, err2 := reg.subscribe("b", f, w); err2 != errTooManyStreams {
		t.Fatalf("over-cap subscribe err = %v, want errTooManyStreams", err2)
	}
	reg.unsubscribe("a", sub1)
}
