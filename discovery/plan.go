// Package discovery computes stack service-name resolution via /etc/hosts.
//
// Captures (porthole-discovery-capture) ruled out every cleaner mechanism: the
// `container` CLI has no `--add-host`, its embedded DNS returns NXDOMAIN for peer
// names even with a created domain, and there is no `--hostname` flag. The ONLY
// way one stack member resolves another by name is to write the peer's current IP
// into the member's /etc/hosts. And /etc/hosts is regenerated to localhost+self on
// EVERY start (not just on IP-change), while IPs churn across restarts — so the
// full peer set must be re-injected on every start.
//
// This file is the PURE, I/O-free core: it computes the exact bytes to write. No
// exec, no runtime, no hub, no timers — all of that is the controller's job
// (controller.go), which depends only on small local interfaces. Keeping the
// gnarly idempotence logic pure makes it exhaustively table-testable.
package discovery

import (
	"fmt"
	"sort"
	"strings"
)

// Marker lines bracket Porthole's managed region of a container's /etc/hosts. The
// begin line carries the stack name for humans; strip matches the begin PREFIX
// (any stack) so a re-injection always removes the prior block wholesale.
const (
	beginPrefix = "# >>> porthole-managed"
	endMarker   = "# <<<"
)

func beginMarker(stack string) string {
	return fmt.Sprintf("%s (stack: %s)", beginPrefix, stack)
}

// Member is one stack member as discovery sees it: its container id (which for a
// named stack container equals the namespaced name), the bare logical service
// name, the namespaced name, and its current dedicated IPv4 ("" when it has none
// yet — i.e. not running). IP is always learned live and never cached: a stale IP
// is worse than a missing one.
type Member struct {
	ID         string // container id == namespaced name for stack members
	Service    string // bare logical name, e.g. "api"
	Namespaced string // <stack>-<service>, e.g. "shop-api"
	IP         string // dedicated IPv4 (no CIDR); "" if no IP yet
}

// Injection is a desired write: the managed block to merge into MemberID's
// /etc/hosts.
type Injection struct {
	MemberID string
	Block    string
}

// computeHostsBlock renders the managed block for ONE member: a marked region
// with one line per supplied peer that currently has an IP, mapping that IP to
// both the bare service name and the namespaced name. Callers pass only the peers
// (self already excluded). Peers without an IP (not yet running) are skipped.
// Lines are sorted so the output is stable — the basis of idempotence.
func computeHostsBlock(stack string, peers []Member) string {
	lines := make([]string, 0, len(peers))
	for _, m := range peers {
		if m.IP == "" {
			continue // no IP yet — can't write a hosts line for it
		}
		entry := m.IP + "  " + m.Service
		if m.Namespaced != "" && m.Namespaced != m.Service {
			entry += "  " + m.Namespaced
		}
		lines = append(lines, entry)
	}
	sort.Strings(lines)

	var b strings.Builder
	b.WriteString(beginMarker(stack))
	b.WriteByte('\n')
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	b.WriteString(endMarker)
	return b.String()
}

// mergeHosts strips any existing porthole-managed block from `existing` and, when
// `block` is non-empty, appends it — returning the full file. All non-managed
// (user/system) lines are preserved exactly and in order. A "" block strips only
// (used to turn discovery off). Idempotent: merging the same block twice yields
// the same file as once.
func mergeHosts(existing, block string) string {
	stripped := strings.TrimRight(stripManaged(existing), "\n")
	if block == "" {
		if stripped == "" {
			return ""
		}
		return stripped + "\n"
	}
	if stripped == "" {
		return block + "\n"
	}
	return stripped + "\n" + block + "\n"
}

// stripManaged removes the lines from a begin-marker through the next end-marker
// (inclusive), leaving every other line untouched. Tolerates a missing end marker
// (strips to EOF) so a half-written prior block can't survive.
func stripManaged(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	skipping := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if !skipping {
			if strings.HasPrefix(t, beginPrefix) {
				skipping = true
				continue
			}
			out = append(out, ln)
			continue
		}
		// inside a managed block: drop lines until (and including) the end marker
		if t == endMarker || strings.HasPrefix(t, endMarker) {
			skipping = false
		}
	}
	return strings.Join(out, "\n")
}

// planInjections computes the desired managed block for EVERY running member of a
// stack — the convergent full desired state, a pure function of the current
// member set. Each running member (one with an IP, so we can write to it) is given
// a block of all OTHER members that have an IP (self excluded). Members without an
// IP yet are skipped as targets and omitted from peer blocks until they get one,
// so a partial set during convergence is handled, not errored.
//
// startedID is the member whose start triggered this pass; it only orders the
// result (the trigger first, then by id) so the member a user is waiting on
// converges first. The desired blocks themselves do not depend on it — which is
// what makes repeated passes idempotent and what lets the SAME function serve a
// stop event (just call it with the remaining members).
func planInjections(stack string, members []Member, startedID string) []Injection {
	out := make([]Injection, 0, len(members))
	for _, target := range members {
		if target.IP == "" {
			continue // not running yet — nothing to write to
		}
		peers := make([]Member, 0, len(members))
		for _, m := range members {
			if m.ID != target.ID {
				peers = append(peers, m)
			}
		}
		out = append(out, Injection{MemberID: target.ID, Block: computeHostsBlock(stack, peers)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].MemberID == startedID {
			return out[j].MemberID != startedID
		}
		if out[j].MemberID == startedID {
			return false
		}
		return out[i].MemberID < out[j].MemberID
	})
	return out
}
