// Package logbuf holds the pure, I/O-free core of log streaming: a LineSplitter
// (raw bytes → complete lines, with long-line truncation) and a Ring (last-N
// lines with drop-oldest + a coalesced dropped count). The OOM guard lives here
// (spec §4 / §8.9). No process spawning, no HTTP — fully unit-testable.
package logbuf

import (
	"bytes"
	"sync"
)

// truncSuffix is appended to a line that exceeded the per-line cap.
const truncSuffix = " …(truncated)"

// LineSplitter accumulates raw bytes and emits only COMPLETE lines (holding a
// trailing partial until its newline). A line longer than maxLine is truncated
// with a marker, and the rest of that line (until the next newline) is dropped.
// Not safe for concurrent use — one producer feeds it.
type LineSplitter struct {
	maxLine   int
	partial   []byte
	truncated bool // current line hit the cap
	skipping  bool // dropping the rest of an over-cap line until newline
}

func NewLineSplitter(maxLine int) *LineSplitter {
	if maxLine <= 0 {
		maxLine = 8192
	}
	return &LineSplitter{maxLine: maxLine}
}

// Write consumes a chunk and returns any complete lines it produced.
func (s *LineSplitter) Write(p []byte) []string {
	var out []string
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.appendPartial(p)
			break
		}
		s.appendPartial(p[:i])
		out = append(out, s.complete())
		p = p[i+1:]
	}
	return out
}

func (s *LineSplitter) appendPartial(chunk []byte) {
	if s.skipping {
		return // over-cap line: discard until newline
	}
	room := s.maxLine - len(s.partial)
	if len(chunk) <= room {
		s.partial = append(s.partial, chunk...)
		return
	}
	s.partial = append(s.partial, chunk[:room]...)
	s.truncated = true
	s.skipping = true
}

func (s *LineSplitter) complete() string {
	line := string(s.partial)
	if s.truncated {
		line += truncSuffix
	}
	s.partial = s.partial[:0]
	s.truncated = false
	s.skipping = false
	return line
}

// Ring is a fixed-capacity line buffer: pushing past cap drops the oldest line
// and counts it. Pull drains the buffered lines and reports how many were dropped
// since the previous Pull (so the consumer can show one coalesced marker rather
// than a per-drop event). Safe for concurrent producer/consumer.
type Ring struct {
	mu         sync.Mutex
	cap        int
	lines      []string
	dropped    int // cumulative
	lastPulled int // dropped count at the previous Pull
}

func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = 1000
	}
	return &Ring{cap: capacity}
}

func (r *Ring) Push(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) >= r.cap {
		r.lines = r.lines[1:]
		r.dropped++
	}
	r.lines = append(r.lines, line)
}

func (r *Ring) PushAll(lines []string) {
	for _, l := range lines {
		r.Push(l)
	}
}

// Pull returns the buffered lines and the number dropped since the last Pull,
// clearing the buffer.
func (r *Ring) Pull() (lines []string, droppedSinceLast int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lines = r.lines
	r.lines = nil
	droppedSinceLast = r.dropped - r.lastPulled
	r.lastPulled = r.dropped
	return
}
