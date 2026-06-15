// Package resources is the pure, I/O-free core of Phase 6: given the live lists
// (images, volumes, networks, containers), it annotates each with in-use /
// anonymous / protected status and computes prune previews. It makes no engine
// calls — the HTTP layer wraps it around the engine reads — so it is exhaustively
// table-tested like the Stacks planner and supervisor.decide.
package resources

import (
	"regexp"
	"sort"
	"strings"

	"github.com/porthole/porthole/engine"
)

// roleLabel marks runtime-builtin (protected) networks — the discriminator for
// non-removable networks (resources spec §8.4b).
const roleLabel = "com.apple.container.resource.role"

// uuidRe matches the bare-UUID name the runtime gives anonymous volumes
// (resources spec §8.5) — NOT an `anon-` prefix.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// AnnotatedImage adds advisory in-use info. The runtime never refuses an image
// delete (§8.3b), so InUseByRunning drives a WARNING, not a block.
type AnnotatedImage struct {
	engine.Image
	InUseByRunning []string `json:"inUseByRunning"`
}

// AnnotatedVolume adds the ENFORCED in-use status (any container, running or
// stopped) plus the anonymous-orphan flag (UUID-shaped name + zero references).
type AnnotatedVolume struct {
	engine.Volume
	InUse     bool     `json:"inUse"`
	UsedBy    []string `json:"usedBy"`
	Anonymous bool     `json:"anonymous"`
}

// AnnotatedNetwork adds protection + membership.
type AnnotatedNetwork struct {
	engine.Network
	Protected   bool     `json:"protected"`
	Members     []string `json:"members"`
	MemberCount int      `json:"memberCount"`
}

// Annotated is the full annotated bundle the resources view renders.
type Annotated struct {
	Images   []AnnotatedImage   `json:"images"`
	Volumes  []AnnotatedVolume  `json:"volumes"`
	Networks []AnnotatedNetwork `json:"networks"`
}

// PrunePlan is the dry-run preview: exactly which items a prune WOULD remove.
type PrunePlan struct {
	Kind  string   `json:"kind"`
	Items []string `json:"items"`
}

// normalizeRef gives an untagged/undigested ref the implicit :latest the runtime
// adds, so an image ref and a container's image ref compare equal.
func normalizeRef(ref string) string {
	if ref == "" || strings.Contains(ref, "@") {
		return ref
	}
	last := strings.LastIndexByte(ref, '/')
	if strings.IndexByte(ref[last+1:], ':') >= 0 {
		return ref
	}
	return ref + ":latest"
}

// Annotate cross-references the container list against images/volumes/networks.
// Rules differ by resource (§5/§8.3): image in-use = running only (advisory);
// volume in-use = any container (enforced); network membership from declared
// attachments.
func Annotate(images []engine.Image, volumes []engine.Volume, networks []engine.Network, containers []engine.Container) Annotated {
	var a Annotated

	for _, img := range images {
		var running []string
		for _, c := range containers {
			if c.IsRunning() && normalizeRef(c.Configuration.Image.Reference) == normalizeRef(img.Reference) {
				running = append(running, c.ID)
			}
		}
		sort.Strings(running)
		a.Images = append(a.Images, AnnotatedImage{Image: img, InUseByRunning: running})
	}

	for _, v := range volumes {
		var usedBy []string
		for _, c := range containers {
			for _, m := range c.Configuration.Mounts {
				if m.VolumeName() == v.Name {
					usedBy = append(usedBy, c.ID)
					break
				}
			}
		}
		sort.Strings(usedBy)
		inUse := len(usedBy) > 0
		a.Volumes = append(a.Volumes, AnnotatedVolume{
			Volume:    v,
			InUse:     inUse,
			UsedBy:    usedBy,
			Anonymous: isUUID(v.Name) && !inUse, // orphaned anonymous = the disk-leak case
		})
	}

	for _, n := range networks {
		var members []string
		for _, c := range containers {
			for _, att := range c.Configuration.Networks {
				if att.Network == n.Configuration.Name {
					members = append(members, c.ID)
					break
				}
			}
		}
		sort.Strings(members)
		a.Networks = append(a.Networks, AnnotatedNetwork{
			Network:     n,
			Protected:   n.Configuration.Labels[roleLabel] == "builtin",
			Members:     members,
			MemberCount: len(members),
		})
	}

	return a
}

// isUUID reports whether s is a bare UUID (the anonymous-volume name shape).
func isUUID(s string) bool { return uuidRe.MatchString(s) }

// PrunePreview computes exactly what a prune of `kind` would remove, BEFORE
// running it — the safety mechanism (§6). For images, all=true previews every
// unreferenced image; all=false previews dangling only. The plan lists names so
// the UI can show (and the user can veto) even named volumes that would go.
func PrunePreview(kind string, a Annotated, containers []engine.Container, all bool) PrunePlan {
	plan := PrunePlan{Kind: kind}
	switch kind {
	case "images":
		for _, img := range a.Images {
			if all {
				if len(img.InUseByRunning) == 0 {
					plan.Items = append(plan.Items, img.Reference)
				}
			} else if isDangling(img.Image) {
				plan.Items = append(plan.Items, img.Reference)
			}
		}
	case "volumes":
		// volume prune removes ALL unreferenced — including named ones (over-reach
		// the preview surfaces).
		for _, v := range a.Volumes {
			if !v.InUse {
				plan.Items = append(plan.Items, v.Name)
			}
		}
	case "networks":
		for _, n := range a.Networks {
			if !n.Protected && n.MemberCount == 0 {
				plan.Items = append(plan.Items, n.Configuration.Name)
			}
		}
	case "containers":
		for _, c := range containers {
			if !c.IsRunning() {
				plan.Items = append(plan.Items, c.ID)
			}
		}
	}
	sort.Strings(plan.Items)
	return plan
}

// isDangling reports whether an image looks dangling (untagged). The runtime has
// no explicit marker (§8.2); a `<none>` reference is the conventional signal.
func isDangling(img engine.Image) bool {
	return img.Reference == "" || strings.Contains(img.Reference, "<none>")
}
