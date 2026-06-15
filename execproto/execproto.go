// Package execproto is the pure, I/O-free core of the exec WebSocket protocol:
// classifying an inbound frame into terminal data or a resize control, and
// framing the outbound terminal-end notice. No WebSocket or PTY imports — this is
// the one unit-testable piece (spec §9.8); the plumbing needs integration tests.
//
// Frame convention (spec §3): binary frames = raw terminal bytes (both
// directions); text frames = JSON control, currently only {"type":"resize",...}.
package execproto

import (
	"encoding/json"
	"errors"
	"fmt"
)

// maxDim caps a resize dimension — a sane terminal is well under this, and it
// guards against absurd/overflowing values.
const maxDim = 10000

// Resize is a terminal resize control.
type Resize struct {
	Cols uint16
	Rows uint16
}

// Inbound is a parsed inbound frame: exactly one of Data or Resize is set.
type Inbound struct {
	Data   []byte  // non-nil for a terminal-data (binary) frame
	Resize *Resize // non-nil for a resize control (text) frame
}

type control struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// ParseInbound classifies a frame. Binary frames are raw terminal data; text
// frames must be a valid control JSON. Malformed JSON, unknown control type, and
// non-positive/oversized dimensions are errors (never a panic).
func ParseInbound(isText bool, payload []byte) (Inbound, error) {
	if !isText {
		return Inbound{Data: payload}, nil
	}
	var c control
	if err := json.Unmarshal(payload, &c); err != nil {
		return Inbound{}, fmt.Errorf("exec control: bad JSON: %w", err)
	}
	switch c.Type {
	case "resize":
		if c.Cols <= 0 || c.Rows <= 0 {
			return Inbound{}, errors.New("exec control: resize dims must be positive")
		}
		if c.Cols > maxDim || c.Rows > maxDim {
			return Inbound{}, fmt.Errorf("exec control: resize dims exceed %d", maxDim)
		}
		return Inbound{Resize: &Resize{Cols: uint16(c.Cols), Rows: uint16(c.Rows)}}, nil
	case "":
		return Inbound{}, errors.New("exec control: missing type")
	default:
		return Inbound{}, fmt.Errorf("exec control: unknown type %q", c.Type)
	}
}

// ExitNotice is the text/JSON control frame sent to the client when the session
// ends (the shell exited or the container stopped), so the UI can show a terminal
// "— session ended —" banner.
func ExitNotice() []byte {
	b, _ := json.Marshal(map[string]string{"type": "exit"})
	return b
}
