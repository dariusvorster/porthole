package stacks

import (
	"reflect"
	"regexp"
	"strings"

	"github.com/porthole/porthole/engine"
)

// recreate.go is the PURE planning side of destructive drift remediation
// (Phase 10): given the old and new RunSpecs for a drifted service, describe what
// recreating it entails — the new spec to apply and how each of the old
// container's volumes fares (preserved vs orphaned). No engine calls, no I/O — the
// confirm UX and the executor both read the same tested determination of "what
// recreating this destroys".

// VolumeKind classifies a volume for the recreate confirm.
type VolumeKind string

const (
	VolNamed     VolumeKind = "named"     // named volume — persists across recreate, re-attached
	VolBind      VolumeKind = "bind"      // host-path bind — data lives on the host, untouched
	VolAnonymous VolumeKind = "anonymous" // bare-UUID volume — ORPHANED on remove (warn)
)

// VolumePlan is one old-container volume's fate on recreate.
type VolumePlan struct {
	Source   string     `json:"source"`
	Target   string     `json:"target"`
	Kind     VolumeKind `json:"kind"`
	Orphaned bool       `json:"orphaned"` // true only for anonymous volumes
}

// RecreatePlan is the intent for recreating one service: the new spec to create
// and the classification of the OLD container's volumes (what the remove destroys
// or preserves). Noop is true when old and new are identical (defensive — the
// planner already gates this).
type RecreatePlan struct {
	New     engine.RunSpec `json:"-"`
	Volumes []VolumePlan   `json:"volumes"`
	Noop    bool           `json:"noop"`
}

// anon volumes surface as bare-UUID volume NAMES (capture DR-C: `-v /anon` →
// volume `fa9802fb-…`). A UUID-shaped source is therefore an anonymous volume.
var volUUIDRe = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// classifyVolume determines a volume's kind from its source: empty → anonymous;
// a path (/… or .…) → host-path bind; a UUID name → anonymous; otherwise named.
func classifyVolume(v engine.RunVolume) VolumeKind {
	switch {
	case v.Source == "":
		return VolAnonymous
	case strings.HasPrefix(v.Source, "/") || strings.HasPrefix(v.Source, "."):
		return VolBind
	case volUUIDRe.MatchString(v.Source):
		return VolAnonymous
	default:
		return VolNamed
	}
}

// planRecreate builds the RecreatePlan from the old and new specs. Volume fates
// are read from the OLD spec (that's the container being removed).
func planRecreate(old, new engine.RunSpec) RecreatePlan {
	return RecreatePlan{New: new, Volumes: classifyVolumes(old.Volumes), Noop: reflect.DeepEqual(old, new)}
}

// classifyVolumes classifies a spec's volumes (named/bind/anonymous + orphan).
func classifyVolumes(vols []engine.RunVolume) []VolumePlan {
	out := make([]VolumePlan, 0, len(vols))
	for _, v := range vols {
		k := classifyVolume(v)
		out = append(out, VolumePlan{Source: v.Source, Target: v.Target, Kind: k, Orphaned: k == VolAnonymous})
	}
	return out
}

// volumePlansFor classifies the volumes an OBSERVED container would have on a
// recreate — used by the planner so the confirm can name preserve/orphan before
// applying. Same reconstruction the executor's snapshot uses (one source of truth).
func volumePlansFor(c engine.Container) []VolumePlan {
	return classifyVolumes(specFromContainer(c).Volumes)
}
