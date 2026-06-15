package logbuf

import (
	"strings"
	"testing"
)

func TestLineSplitterCompleteAndPartial(t *testing.T) {
	s := NewLineSplitter(8192)

	if got := s.Write([]byte("alpha\nbeta\n")); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("two complete lines: %v", got)
	}
	// Partial held across writes — no emit until the newline.
	if got := s.Write([]byte("gam")); len(got) != 0 {
		t.Fatalf("partial should not emit: %v", got)
	}
	if got := s.Write([]byte("ma\n")); len(got) != 1 || got[0] != "gamma" {
		t.Fatalf("partial completed: %v", got)
	}
	// Multiple newlines + trailing partial in one chunk.
	got := s.Write([]byte("one\ntwo\nthr"))
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("multi-line chunk: %v", got)
	}
	if got := s.Write([]byte("ee\n")); len(got) != 1 || got[0] != "three" {
		t.Fatalf("final partial: %v", got)
	}
}

func TestLineSplitterTruncatesLongLine(t *testing.T) {
	s := NewLineSplitter(5)
	// A 10-char line over a 5-char cap → truncated to 5 + marker; rest dropped.
	got := s.Write([]byte("0123456789\nnext\n"))
	if len(got) != 2 {
		t.Fatalf("want 2 lines, got %v", got)
	}
	if got[0] != "01234"+truncSuffix {
		t.Errorf("truncated line = %q", got[0])
	}
	if got[1] != "next" {
		t.Errorf("resync after over-cap line failed: %q", got[1])
	}
}

func TestRingDropsOldestWithCoalescedCount(t *testing.T) {
	r := NewRing(3)
	for i := 0; i < 5; i++ {
		r.Push(string(rune('a' + i))) // a b c d e → ring keeps c d e, drops a,b
	}
	lines, dropped := r.Pull()
	if strings.Join(lines, "") != "cde" {
		t.Errorf("ring contents = %v, want [c d e]", lines)
	}
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2 (coalesced)", dropped)
	}
	// Pull again with no new drops → zero.
	r.Push("f")
	lines, dropped = r.Pull()
	if len(lines) != 1 || lines[0] != "f" || dropped != 0 {
		t.Errorf("second pull = %v dropped %d, want [f] 0", lines, dropped)
	}
}

func TestRingPullResets(t *testing.T) {
	r := NewRing(10)
	r.PushAll([]string{"x", "y"})
	if lines, _ := r.Pull(); len(lines) != 2 {
		t.Fatalf("first pull should drain 2")
	}
	if lines, dropped := r.Pull(); len(lines) != 0 || dropped != 0 {
		t.Errorf("empty pull = %v dropped %d", lines, dropped)
	}
}
