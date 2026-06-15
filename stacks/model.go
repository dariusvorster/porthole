// Package stacks implements compose-style multi-container orchestration on top
// of the verified console + supervision (Phase 4). v1 is deliberately
// NON-DESTRUCTIVE: it imports/validates a docker-compose subset, brings stacks
// up/down (keeping volumes), and DETECTS drift (recreate/orphan) without ever
// applying the destructive parts — those are v2 (see docs/porthole-stacks-spec.md
// §6, §10).
//
// This file is the pure domain model + the compose-subset parser/validator
// (ST1). It has NO I/O and imports neither the engine nor a store — it is a
// testable core, like supervisor.decide / logbuf.Ring / execproto.ParseInbound.
package stacks

// Stack is the internal model a compose file parses into. It is intentionally
// smaller than compose: only the fields v1 maps onto `container run`.
type Stack struct {
	Name     string
	Services []Service // sorted by Name for deterministic plans/output
	Volumes  []string  // declared top-level named volumes
	Networks []string  // declared top-level networks (v1 uses one per-stack net; see spec §9.2)

	// Discovery is a transient flag set by the Manager from the stored Record
	// before an Up — Parse never sets it (compose has no such field). When true,
	// members get the porthole.discovery=on label so service discovery (Phase 8)
	// injects peers' /etc/hosts.
	Discovery bool
}

// Service is one declared service. Restart maps to a supervision label; the rest
// map to `container run` flags (env/ports/volumes/labels).
type Service struct {
	Name        string
	Image       string
	Command     []string
	Environment map[string]string
	EnvFile     []string
	Ports       []PortMapping
	Volumes     []VolumeMount
	Labels      map[string]string
	DependsOn   []string
	Restart     string // "" | no | always | unless-stopped (on-failure rejected)
}

// PortMapping is a published port: HostPort:ContainerPort[/Proto]. Proto
// defaults to "tcp".
type PortMapping struct {
	HostPort      int
	ContainerPort int
	Proto         string
}

// VolumeMount is `source:target` — source is a named volume (v1) or a host path;
// target is the in-container path.
type VolumeMount struct {
	Source string
	Target string
}

// ValidationReport is the result of parsing a compose file. It is what the
// validate endpoint returns and what the import UI shows BEFORE saving. A file
// that uses unsupported keys parses to a report that lists them explicitly — it
// is never silently dropped (spec §3, §11.8).
type ValidationReport struct {
	Valid    bool          `json:"valid"`
	Rejected []RejectedKey `json:"rejected"` // unsupported keys, explicitly named
	Errors   []string      `json:"errors"`   // hard failures (cycle, bad restart, malformed)
	Warnings []string      `json:"warnings"` // unknown-but-ignored keys
	Notes    []string      `json:"notes"`    // applied defaults, informational
}

// RejectedKey is one unsupported compose key with the reason it cannot be honored.
type RejectedKey struct {
	Path   string `json:"path"`   // e.g. "services.web.build"
	Reason string `json:"reason"` // why it is rejected
}

// Supported restart policies — on-failure is intentionally absent (the runtime
// exposes no exit code; spec/supervision §9).
const (
	RestartNo            = "no"
	RestartAlways        = "always"
	RestartUnlessStopped = "unless-stopped"
)
