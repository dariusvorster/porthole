package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/execproto"
)

const execMaxSessions = 10

// handleExec upgrades to a WebSocket and runs an interactive PTY session.
//
// SECURITY (spec §5): this is a browser-reachable interactive (root) shell. The
// upgrade is an ordinary HTTP GET, so it already flows through the same
// securityHeaders→browserGuard chain as every REST call — a foreign Origin is
// rejected 403 BEFORE this handler runs (verified in exec_test.go). We therefore
// set InsecureSkipVerify on Accept: the browserGuard is the single, authoritative
// Origin/Host check; the library's redundant check would only risk false
// rejections (127.0.0.1 vs localhost).
func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Running-state gate: no process to attach to on a stopped container.
	c, err := s.eng.InspectContainer(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if !c.IsRunning() {
		writeJSON(w, http.StatusConflict, errorEnvelope{Error: errorBody{
			Kind: string(engine.ErrNotRunning), Message: "container is not running",
		}})
		return
	}

	// Concurrent-session cap. Exec is NOT deduped — each open is its own shell.
	if atomic.AddInt32(&s.execActive, 1) > execMaxSessions {
		atomic.AddInt32(&s.execActive, -1)
		writeJSON(w, http.StatusTooManyRequests, errorEnvelope{Error: errorBody{
			Kind: "too_many_streams", Message: "too many exec sessions",
		}})
		return
	}
	defer atomic.AddInt32(&s.execActive, -1)

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return // Accept already wrote the error response
	}

	var cmd []string
	if c := r.URL.Query().Get("cmd"); c != "" {
		cmd = []string{c}
	}
	initial := engine.WinSize{
		Cols: atou16(r.URL.Query().Get("cols")),
		Rows: atou16(r.URL.Query().Get("rows")),
	}
	s.runExecSession(r.Context(), conn, id, cmd, initial)
}

func (s *Server) runExecSession(parent context.Context, conn *websocket.Conn, id string, cmd []string, initial engine.WinSize) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	sess, err := s.eng.Exec(ctx, id, cmd, initial)
	if err != nil {
		conn.Close(websocket.StatusInternalError, "exec failed")
		return
	}
	defer sess.Close() // reaps BOTH child + PTY (idempotent)

	// Read pump: WS → PTY. Binary = stdin bytes; text = JSON control (resize).
	go func() {
		defer cancel()
		for {
			mt, data, rerr := conn.Read(ctx)
			if rerr != nil {
				return
			}
			in, perr := execproto.ParseInbound(mt == websocket.MessageText, data)
			if perr != nil {
				continue // ignore malformed control frames
			}
			if in.Resize != nil {
				_ = sess.Resize(in.Resize.Cols, in.Resize.Rows)
				continue
			}
			if _, werr := sess.Write(in.Data); werr != nil {
				return
			}
		}
	}()

	// Ping keepalive: detect a half-open (dead-tab) connection and reap.
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Write pump: PTY → WS (this goroutine). Ends on PTY EOF — the shell exited
	// or the container stopped (rc 137, the natural-exit teardown, §8.3).
	buf := make([]byte, 32*1024)
	for {
		n, rerr := sess.Read(buf)
		if n > 0 {
			if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
				break
			}
		}
		if rerr != nil {
			break
		}
	}

	// Notify the client the session ended (UI shows "— session ended —"), close.
	notifyCtx, ncancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = conn.Write(notifyCtx, websocket.MessageText, execproto.ExitNotice())
	ncancel()
	conn.Close(websocket.StatusNormalClosure, "session ended")
}

func atou16(s string) uint16 {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 65535 {
		return 0
	}
	return uint16(n)
}
