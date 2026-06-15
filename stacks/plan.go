package stacks

import (
	"fmt"
	"sort"
	"strings"

	"github.com/porthole/porthole/engine"
)

// Membership labels (spec §5, §11.2): the file is desired state, these labels
// prove membership, and `container ls` is observed truth. Re-discover members
// from labels every reconcile — never trust the DB alone.
const (
	LabelStack     = "porthole.stack"
	LabelService   = "porthole.service"
	LabelRestart   = "porthole.restart"   // mirrors supervision's restart label
	LabelDiscovery = "porthole.discovery" // "on" → inject peers' /etc/hosts (Phase 8)
)

// ActionKind is one per-service action in a reconcile Plan.
type ActionKind string

const (
	ActionCreate   ActionKind = "create"   // declared, no container — SAFE, applied in v1
	ActionStart    ActionKind = "start"    // declared, present, stopped — SAFE, applied in v1
	ActionNoop     ActionKind = "noop"     // declared, present, running, config matches
	ActionRecreate ActionKind = "recreate" // config differs — DETECTED ONLY in v1 (destructive, v2)
	ActionOrphan   ActionKind = "orphan"   // present, not declared — DETECTED ONLY in v1 (destructive, v2)
)

// Destructive reports whether applying this action would stop/delete a container.
// v1 NEVER applies destructive actions — it records them so the UI can show drift.
func (k ActionKind) Destructive() bool { return k == ActionRecreate || k == ActionOrphan }

// ServiceAction is one entry in a Plan.
type ServiceAction struct {
	Service     string     `json:"service"`
	Action      ActionKind `json:"action"`
	ContainerID string     `json:"containerId,omitempty"` // for existing containers
	Diff        []string   `json:"diff,omitempty"`        // for recreate: what changed
}

// Plan is the reconcile diff between a desired Stack and the observed containers.
type Plan struct {
	Stack   string          `json:"stack"`
	Actions []ServiceAction `json:"actions"`
}

// Safe returns only the non-destructive actions v1 will actually apply
// (create/start). Recreate/orphan are deliberately excluded — they are in the
// Plan for the UI, never executed in v1.
func (p Plan) Safe() []ServiceAction {
	var out []ServiceAction
	for _, a := range p.Actions {
		if !a.Action.Destructive() {
			out = append(out, a)
		}
	}
	return out
}

// memberOf returns the value of the porthole.service label if the container
// belongs to the named stack, plus whether it is a member.
func memberOf(c engine.Container, stack string) (string, bool) {
	if c.Configuration.Labels[LabelStack] != stack {
		return "", false
	}
	return c.Configuration.Labels[LabelService], true
}

// Plan computes the reconcile diff. It is PURE: given the desired Stack and the
// full observed container slice, it returns the per-service Plan. It filters
// observed down to this stack's members (by the porthole.stack label) and groups
// them by porthole.service. The heart of drift detection — recreate and orphan
// are FLAGGED, never turned into destructive actions (v1, spec §6/§10).
func PlanReconcile(desired Stack, observed []engine.Container) Plan {
	plan := Plan{Stack: desired.Name}

	// Group this stack's observed members by service name.
	members := map[string]engine.Container{}
	for _, c := range observed {
		svc, ok := memberOf(c, desired.Name)
		if !ok || svc == "" {
			continue
		}
		// If two containers claim the same service, keep a running one if present
		// so config-match compares against the live container.
		if existing, dup := members[svc]; dup && existing.IsRunning() && !c.IsRunning() {
			continue
		}
		members[svc] = c
	}

	declared := map[string]bool{}
	for _, svc := range desired.Services {
		declared[svc.Name] = true
		c, present := members[svc.Name]
		switch {
		case !present:
			plan.Actions = append(plan.Actions, ServiceAction{Service: svc.Name, Action: ActionCreate})
		case !c.IsRunning():
			plan.Actions = append(plan.Actions, ServiceAction{Service: svc.Name, Action: ActionStart, ContainerID: c.ID})
		default:
			if diff := configDiff(svc, c); len(diff) > 0 {
				plan.Actions = append(plan.Actions, ServiceAction{
					Service: svc.Name, Action: ActionRecreate, ContainerID: c.ID, Diff: diff,
				})
			} else {
				plan.Actions = append(plan.Actions, ServiceAction{Service: svc.Name, Action: ActionNoop, ContainerID: c.ID})
			}
		}
	}

	// Orphans: members whose service is no longer declared.
	var orphanSvcs []string
	for svc := range members {
		if !declared[svc] {
			orphanSvcs = append(orphanSvcs, svc)
		}
	}
	sort.Strings(orphanSvcs)
	for _, svc := range orphanSvcs {
		plan.Actions = append(plan.Actions, ServiceAction{Service: svc, Action: ActionOrphan, ContainerID: members[svc].ID})
	}

	return plan
}

// configDiff compares a declared Service against an observed container for the
// fields v1 can read, returning human descriptions of every difference.
//
// What counts as "changed" (documented, deliberately conservative):
//   - image: the configured image reference differs (string compare, as-declared).
//   - ports: the set of host:container/proto published ports differs.
//   - env: any declared KEY=VALUE is absent or different in the container's env
//     (we only flag declared keys; extra container env — injected defaults — is
//     ignored so we don't false-positive on runtime additions).
func configDiff(svc Service, c engine.Container) []string {
	var diff []string

	if svc.Image != "" && normalizeImage(svc.Image) != normalizeImage(c.Configuration.Image.Reference) {
		diff = append(diff, fmt.Sprintf("image: %q -> %q", c.Configuration.Image.Reference, svc.Image))
	}

	if d := portsDiff(svc.Ports, c.Configuration.PublishedPorts); d != "" {
		diff = append(diff, d)
	}

	if d := envDiff(svc.Environment, c.Configuration.InitProcess.Environment); len(d) > 0 {
		diff = append(diff, d...)
	}

	return diff
}

// normalizeImage canonicalizes an image reference for comparison: a reference
// with no tag and no digest gets the implicit ":latest" the runtime adds when it
// stores it (confirmed live — `alpine` is stored as `alpine:latest`). A digest
// reference (contains "@") is left as-is.
func normalizeImage(ref string) string {
	if ref == "" || strings.Contains(ref, "@") {
		return ref
	}
	// A tag exists only if there's a ':' AFTER the last '/' (a ':' before that is
	// part of a registry host:port, not a tag).
	lastSlash := strings.LastIndexByte(ref, '/')
	if strings.IndexByte(ref[lastSlash+1:], ':') >= 0 {
		return ref
	}
	return ref + ":latest"
}

func portKey(host, cont int, proto string) string {
	if proto == "" {
		proto = "tcp"
	}
	return fmt.Sprintf("%d:%d/%s", host, cont, proto)
}

func portsDiff(declared []PortMapping, observed []engine.PublishedPort) string {
	want := map[string]bool{}
	for _, p := range declared {
		want[portKey(p.HostPort, p.ContainerPort, p.Proto)] = true
	}
	got := map[string]bool{}
	for _, p := range observed {
		got[portKey(p.HostPort, p.ContainerPort, p.Proto)] = true
	}
	if len(want) != len(got) {
		return fmt.Sprintf("ports: %s -> %s", keyset(got), keyset(want))
	}
	for k := range want {
		if !got[k] {
			return fmt.Sprintf("ports: %s -> %s", keyset(got), keyset(want))
		}
	}
	return ""
}

func keyset(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "[" + strings.Join(keys, " ") + "]"
}

func envDiff(declared map[string]string, observed []string) []string {
	got := map[string]string{}
	for _, kv := range observed {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	var keys []string
	for k := range declared {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var diff []string
	for _, k := range keys {
		if cur, ok := got[k]; !ok || cur != declared[k] {
			diff = append(diff, fmt.Sprintf("env %s: %q -> %q", k, got[k], declared[k]))
		}
	}
	return diff
}

// Status derives a stack's status from its observed members (spec §8).
//   - "up": every declared service has a running member.
//   - "degraded": some but not all declared services are running.
//   - "down": no declared service is running.
func Status(desired Stack, observed []engine.Container) string {
	running := map[string]bool{}
	for _, c := range observed {
		svc, ok := memberOf(c, desired.Name)
		if ok && c.IsRunning() {
			running[svc] = true
		}
	}
	if len(desired.Services) == 0 {
		return "down"
	}
	count := 0
	for _, svc := range desired.Services {
		if running[svc.Name] {
			count++
		}
	}
	switch {
	case count == len(desired.Services):
		return "up"
	case count == 0:
		return "down"
	default:
		return "degraded"
	}
}

// Order returns the service names in dependency order (a service appears after
// everything it depends_on). Assumes an acyclic graph (Parse validated it); a
// residual cycle falls back to sorted order so the executor still makes progress.
func Order(desired Stack) []string {
	graph := map[string][]string{}
	names := make([]string, 0, len(desired.Services))
	for _, s := range desired.Services {
		names = append(names, s.Name)
		graph[s.Name] = append([]string(nil), s.DependsOn...)
	}
	sort.Strings(names)

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var out []string
	var visit func(n string) bool
	visit = func(n string) bool {
		color[n] = gray
		deps := append([]string(nil), graph[n]...)
		sort.Strings(deps)
		for _, d := range deps {
			if _, known := graph[d]; !known {
				continue
			}
			if color[d] == gray {
				return false // cycle
			}
			if color[d] == white && !visit(d) {
				return false
			}
		}
		color[n] = black
		out = append(out, n)
		return true
	}
	for _, n := range names {
		if color[n] == white {
			if !visit(n) {
				// Cycle fallback: deterministic sorted order.
				return names
			}
		}
	}
	return out
}
