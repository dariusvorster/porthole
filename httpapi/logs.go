package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/logbuf"
)

// LogWatcher signals when a container leaves the running set, so a follow stream
// can be torn down — `container logs -f` won't exit on its own (spec §7.4).
// *reconcile.Hub satisfies this.
type LogWatcher interface {
	WatchStopped(ctx context.Context, id string) <-chan struct{}
}

const (
	logHistoryN   = 200                    // last N lines on open
	logRingCap    = 1000                   // per-subscriber ring
	logMaxLine    = 8192                   // per-line cap (long-line truncation)
	logFlush      = 100 * time.Millisecond // coalesce flush
	logMaxStreams = 20                     // concurrent subscriber cap
)

// --- registry: one follower child per container, fanned out to subscribers ---

type logRegistry struct {
	mu      sync.Mutex
	streams map[string]*logStream
	active  int // total subscribers across all streams
	max     int
}

func newLogRegistry() *logRegistry {
	return &logRegistry{streams: map[string]*logStream{}, max: logMaxStreams}
}

type logStream struct {
	id       string
	splitter *logbuf.LineSplitter
	child    io.ReadCloser
	cancel   context.CancelFunc

	mu      sync.Mutex
	subs    map[*logSub]struct{}
	stopped bool
}

type logSub struct {
	ring *logbuf.Ring
	wake chan struct{} // buffered(1): "new data or state change"
}

func newLogSub() *logSub {
	return &logSub{ring: logbuf.NewRing(logRingCap), wake: make(chan struct{}, 1)}
}

func (s *logSub) wakeUp() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// subscribe returns a subscriber for id, creating (and starting) the shared
// follower child on first use and fanning out to it thereafter. Errors when the
// concurrent-stream cap is hit.
func (reg *logRegistry) subscribe(id string, eng engine.Engine, w LogWatcher) (*logSub, *logStream, error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.active >= reg.max {
		return nil, nil, errTooManyStreams
	}
	st := reg.streams[id]
	if st == nil {
		st = reg.startStream(id, eng, w)
		reg.streams[id] = st
	}
	sub := newLogSub()
	st.mu.Lock()
	st.subs[sub] = struct{}{}
	st.mu.Unlock()
	reg.active++
	return sub, st, nil
}

func (reg *logRegistry) unsubscribe(id string, sub *logSub) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	st := reg.streams[id]
	if st == nil {
		return
	}
	st.mu.Lock()
	delete(st.subs, sub)
	empty := len(st.subs) == 0
	st.mu.Unlock()
	reg.active--
	if empty {
		st.cancel() // kill the follower child; do not wait for logs -f to exit
		delete(reg.streams, id)
	}
}

// startStream spawns the single follower child and the stop-watcher. Caller holds
// reg.mu.
func (reg *logRegistry) startStream(id string, eng engine.Engine, w LogWatcher) *logStream {
	ctx, cancel := context.WithCancel(context.Background())
	st := &logStream{
		id:       id,
		splitter: logbuf.NewLineSplitter(logMaxLine),
		cancel:   cancel,
		subs:     map[*logSub]struct{}{},
	}
	child, err := eng.Logs(ctx, id, engine.LogOpts{Follow: true})
	if err != nil {
		st.markStopped()
		cancel()
		return st
	}
	st.child = child
	go st.readLoop()
	if w != nil {
		go func() {
			select {
			case <-ctx.Done():
			case <-w.WatchStopped(ctx, id):
				st.markStopped()
			}
		}()
	}
	return st
}

func (st *logStream) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := st.child.Read(buf)
		if n > 0 {
			if lines := st.splitter.Write(buf[:n]); len(lines) > 0 {
				st.broadcast(lines)
			}
		}
		if err != nil {
			// The follower only ends when we kill it (teardown) — `logs -f` does
			// not exit on container stop. The watcher owns the terminal event.
			return
		}
	}
}

func (st *logStream) broadcast(lines []string) {
	st.mu.Lock()
	subs := make([]*logSub, 0, len(st.subs))
	for s := range st.subs {
		subs = append(subs, s)
	}
	st.mu.Unlock()
	for _, s := range subs {
		s.ring.PushAll(lines)
		s.wakeUp()
	}
}

// markStopped flips the terminal flag, wakes subscribers (so their flush loops
// emit the terminal event), and force-kills the follower child.
func (st *logStream) markStopped() {
	st.mu.Lock()
	if st.stopped {
		st.mu.Unlock()
		return
	}
	st.stopped = true
	subs := make([]*logSub, 0, len(st.subs))
	for s := range st.subs {
		subs = append(subs, s)
	}
	st.mu.Unlock()
	for _, s := range subs {
		s.wakeUp()
	}
	st.cancel()
}

func (st *logStream) isStopped() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.stopped
}

// --- handler ---------------------------------------------------------------

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")

	// History first (works for running AND stopped containers — spec §7.6).
	if hist, err := s.eng.Logs(ctx, id, engine.LogOpts{Tail: logHistoryN}); err == nil {
		data, _ := io.ReadAll(hist)
		_ = hist.Close()
		writeLogEvent(w, "history", logLines{Lines: splitLines(data)})
	} else {
		writeLogEvent(w, "history", logLines{Lines: nil})
	}
	flusher.Flush()

	// Not running → history-only, terminal, no follower (never a hung spinner).
	if c, err := s.eng.InspectContainer(ctx, id); err != nil || !c.IsRunning() {
		writeLogEvent(w, "stopped", logTerminal{Reason: "not running"})
		flusher.Flush()
		return
	}

	sub, st, err := s.logs.subscribe(id, s.eng, s.logWatcher)
	if err != nil {
		writeLogEvent(w, "error", map[string]string{"kind": "too_many_streams", "message": err.Error()})
		flusher.Flush()
		return
	}
	defer s.logs.unsubscribe(id, sub)

	ticker := time.NewTicker(logFlush)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.wake:
		case <-ticker.C:
		}

		lines, dropped := sub.ring.Pull()
		if dropped > 0 {
			writeLogEvent(w, "dropped", map[string]int{"count": dropped})
		}
		if len(lines) > 0 {
			writeLogEvent(w, "log", logLines{Lines: lines})
		}
		if dropped > 0 || len(lines) > 0 {
			flusher.Flush()
		}

		if st.isStopped() {
			// Drain anything buffered, then the terminal event.
			if rest, _ := sub.ring.Pull(); len(rest) > 0 {
				writeLogEvent(w, "log", logLines{Lines: rest})
			}
			writeLogEvent(w, "stopped", logTerminal{Reason: "container stopped"})
			flusher.Flush()
			return
		}
	}
}

type logLines struct {
	Lines []string `json:"lines"`
}

type logTerminal struct {
	Reason string `json:"reason"`
}

func writeLogEvent(w io.Writer, name string, payload any) {
	b, _ := json.Marshal(payload)
	_, _ = w.Write([]byte("event: " + name + "\ndata: "))
	_, _ = w.Write(b)
	_, _ = w.Write([]byte("\n\n"))
}

// splitLines splits finite log output into lines, keeping a final newline-less
// line and dropping a trailing empty.
func splitLines(b []byte) []string {
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
