package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/porthole/porthole/reconcile"
)

// handleStream is the SSE endpoint. The browser opens one EventSource here and
// receives: an initial `snapshot`, then `container.upserted` / `container.removed`
// / `stats` / `df` / `system` deltas as the reconcile loop observes them.
//
// It is intentionally NOT behind the bootstrap gate — the stream itself reports
// the daemon-down state via a `system` event, which is how the UI keeps the
// banner live without polling a separate endpoint.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // disable proxy buffering if one is ever in front
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	id, ch := s.hub.Subscribe()
	defer s.hub.Unsubscribe(id)

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return // hub dropped us (slow consumer) — client will reconnect
			}
			writeSSE(w, e)
			flusher.Flush()
		case <-keepalive.C:
			io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// writeSSE frames one event. JSON payloads from the hub are single-line, so a
// single `data:` line is sufficient.
func writeSSE(w io.Writer, e reconcile.Event) {
	fmt.Fprintf(w, "event: %s\n", e.Name)
	fmt.Fprintf(w, "data: %s\n\n", e.Data)
}
