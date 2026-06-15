// Package engine contains Porthole's domain models and the Engine abstraction.
//
// Every type in this file was derived from REAL output captured from
// `container` v1.0.0 (commit ee848e3), not from documentation. Fields marked
// UNCONFIRMED had no data in the captured sample and need a follow-up capture
// before they are trusted — see contract_test.go and the Phase 0 notes.
package engine

import (
	"encoding/json"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Containers
//
// IMPORTANT: `container ls --all --format json` and `container inspect <id>`
// return the IDENTICAL object shape. `ls` returns a JSON array of Container;
// `inspect` returns a JSON array with a single Container. One model serves
// both, which means the reconcile loop can read almost everything it needs
// from a single `ls --all` call without a per-container inspect.
// ---------------------------------------------------------------------------

// Container is one entry from `container ls --all` / `container inspect`.
type Container struct {
	ID            string          `json:"id"`
	Configuration ContainerConfig `json:"configuration"`
	Status        ContainerStatus `json:"status"`
}

// ContainerConfig is the declared desired-state of a container.
type ContainerConfig struct {
	ID               string            `json:"id"`
	CreationDate     time.Time         `json:"creationDate"`
	Image            ImageRef          `json:"image"`
	InitProcess      InitProcess       `json:"initProcess"`
	Networks         []NetworkAttach   `json:"networks"` // membership (name), NOT the assigned IP
	Platform         Platform          `json:"platform"`
	Resources        Resources         `json:"resources"`
	Labels           map[string]string `json:"labels"` // present + writable — basis for porthole.restart / porthole.stack
	DNS              DNSConfig         `json:"dns"`
	CapAdd           []string          `json:"capAdd"`
	CapDrop          []string          `json:"capDrop"`
	Sysctls          map[string]string `json:"sysctls"`
	RuntimeHandler   string            `json:"runtimeHandler"`
	StopSignal       string            `json:"stopSignal,omitempty"` // optional: present on nginx, absent on node
	Rosetta          bool              `json:"rosetta"`
	ReadOnly         bool              `json:"readOnly"`
	SSH              bool              `json:"ssh"`
	UseInit          bool              `json:"useInit"`
	Virtualization   bool              `json:"virtualization"`
	Mounts           []Mount           `json:"mounts"`           // UNCONFIRMED shape (empty in sample)
	PublishedPorts   []PublishedPort   `json:"publishedPorts"`   // UNCONFIRMED shape (empty in sample)
	PublishedSockets []json.RawMessage `json:"publishedSockets"` // UNCONFIRMED shape (empty in sample)
}

// ContainerStatus is the live observed state.
type ContainerStatus struct {
	State       string          `json:"state"` // observed: "running", "stopped" (treat as open set)
	StartedDate time.Time       `json:"startedDate"`
	Networks    []NetworkStatus `json:"networks"` // POPULATED ONLY WHEN RUNNING — stopped containers have []
}

// NetworkStatus is the runtime network assignment. The dedicated per-container
// IP lives here as a CIDR string, and only exists while the container runs.
type NetworkStatus struct {
	Network     string `json:"network"`
	Hostname    string `json:"hostname"`
	IPv4Address string `json:"ipv4Address"` // CIDR form, e.g. "192.168.64.2/24"
	IPv4Gateway string `json:"ipv4Gateway"`
	IPv6Address string `json:"ipv6Address"`
	MACAddress  string `json:"macAddress"`
	MTU         int    `json:"mtu"`
}

type ImageRef struct {
	Reference  string          `json:"reference"`
	Descriptor ImageDescriptor `json:"descriptor"`
}

type ImageDescriptor struct {
	Digest    string `json:"digest"`
	MediaType string `json:"mediaType"`
	Size      int64  `json:"size"`
}

type InitProcess struct {
	Arguments          []string          `json:"arguments"`
	Environment        []string          `json:"environment"`
	Executable         string            `json:"executable"`
	WorkingDirectory   string            `json:"workingDirectory"`
	Terminal           bool              `json:"terminal"`
	User               User              `json:"user"`
	Rlimits            []json.RawMessage `json:"rlimits"`            // UNCONFIRMED shape
	SupplementalGroups []json.RawMessage `json:"supplementalGroups"` // UNCONFIRMED shape
}

type User struct {
	ID UserID `json:"id"`
}

type UserID struct {
	UID int `json:"uid"`
	GID int `json:"gid"`
}

// NetworkAttach is a container's declared membership of a network.
type NetworkAttach struct {
	Network string               `json:"network"`
	Options NetworkAttachOptions `json:"options"`
}

type NetworkAttachOptions struct {
	Hostname string `json:"hostname"`
	MTU      int    `json:"mtu"`
}

type Platform struct {
	Architecture string `json:"architecture"` // "arm64"
	OS           string `json:"os"`           // "linux"
}

type Resources struct {
	CPUs          int   `json:"cpus"`
	CPUOverhead   int   `json:"cpuOverhead"`
	MemoryInBytes int64 `json:"memoryInBytes"`
}

type DNSConfig struct {
	Nameservers   []string `json:"nameservers"`
	Options       []string `json:"options"`
	SearchDomains []string `json:"searchDomains"`
}

// Mount is confirmed against `container inspect` of a container run with
// `-v name:/path` (Stacks live capture). The runtime returns `type` as a TAGGED
// UNION object, not a string — a named volume is:
//
//	{"destination":"/data","options":[],
//	 "source":".../volumes/<name>/volume.img",
//	 "type":{"volume":{"name":"<name>","format":"ext4","cache":{...},"sync":{...}}}}
//
// A host-path bind mount is confirmed too (create spec §8.5) — `type` is
// `{"virtiofs":{}}` with the host path in `source`. tmpfs is still UNCONFIRMED.
type Mount struct {
	Source      string    `json:"source,omitempty"`
	Destination string    `json:"destination,omitempty"`
	Options     []string  `json:"options,omitempty"`
	Type        MountType `json:"type"`
}

// MountType is the tagged-union `mounts[].type`. Two variants are confirmed: a
// named volume (`{"volume":{name,…}}`) and a host-path bind mount
// (`{"virtiofs":{}}`, create spec §8.5). The host path of a bind lives in
// Mount.Source. Other kinds (tmpfs) add sibling keys, left nil until captured.
type MountType struct {
	Volume   *VolumeMount   `json:"volume,omitempty"`
	Virtiofs *VirtiofsMount `json:"virtiofs,omitempty"`
}

// VolumeMount is the named-volume payload of a MountType. Extra fields (cache,
// sync) are intentionally not modelled — only name + format are load-bearing.
type VolumeMount struct {
	Name   string `json:"name"`
	Format string `json:"format,omitempty"`
}

// VirtiofsMount is the host-path bind-mount payload. The captured shape was an
// empty object — the host path is carried by Mount.Source — so this is a marker.
type VirtiofsMount struct{}

// VolumeName returns the named volume backing this mount, or "" for a bind/
// tmpfs mount (or an unconfirmed variant).
func (m Mount) VolumeName() string {
	if m.Type.Volume != nil {
		return m.Type.Volume.Name
	}
	return ""
}

// IsBind reports whether this is a host-path bind mount (virtiofs variant).
func (m Mount) IsBind() bool { return m.Type.Virtiofs != nil }

// HostPath returns the host path of a bind mount (Mount.Source), or "" if this
// is not a bind mount.
func (m Mount) HostPath() string {
	if m.IsBind() {
		return m.Source
	}
	return ""
}

// PublishedPort is confirmed against real `container inspect` output of a
// container run with `-p 8080:80`:
//
//	{"containerPort":80,"count":1,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}
type PublishedPort struct {
	HostAddress   string `json:"hostAddress,omitempty"`
	HostPort      int    `json:"hostPort,omitempty"`
	ContainerPort int    `json:"containerPort,omitempty"`
	Proto         string `json:"proto,omitempty"`
	Count         int    `json:"count,omitempty"`
}

// ---------------------------------------------------------------------------
// Networks  (`container network ls` / `container network inspect`)
// Same array-of-object shape for both, like containers.
// ---------------------------------------------------------------------------

type Network struct {
	ID            string         `json:"id"`
	Configuration NetworkConfig  `json:"configuration"`
	Status        NetworkRuntime `json:"status"`
}

type NetworkConfig struct {
	Name         string            `json:"name"`
	CreationDate time.Time         `json:"creationDate"`
	Mode         string            `json:"mode"`   // "nat"
	Plugin       string            `json:"plugin"` // "container-network-vmnet"
	Labels       map[string]string `json:"labels"`
	Options      map[string]string `json:"options"`
}

type NetworkRuntime struct {
	IPv4Gateway string `json:"ipv4Gateway"`
	IPv4Subnet  string `json:"ipv4Subnet"` // "192.168.64.0/24"
	IPv6Subnet  string `json:"ipv6Subnet"`
}

// ---------------------------------------------------------------------------
// System: version, disk usage, stats
// ---------------------------------------------------------------------------

// VersionEntry — note `container system version` returns an ARRAY of these,
// one for the CLI ("container") and one for the apiserver. The apiserver's
// Version field is a messy human string; the CLI's is clean semver. Use
// CLIVersion() rather than reading element [0] blindly.
type VersionEntry struct {
	AppName   string `json:"appName"`
	BuildType string `json:"buildType"`
	Commit    string `json:"commit"`
	Version   string `json:"version"`
}

// DiskUsage from `container system df --format json`.
type DiskUsage struct {
	Containers DiskCategory `json:"containers"`
	Images     DiskCategory `json:"images"`
	Volumes    DiskCategory `json:"volumes"`
}

type DiskCategory struct {
	Active      int   `json:"active"`
	Total       int   `json:"total"`
	SizeInBytes int64 `json:"sizeInBytes"`
	Reclaimable int64 `json:"reclaimable"`
}

// Stats from `container stats --no-stream --format json <id>` (an array).
//
// CRITICAL: CPUUsageUsec is CUMULATIVE CPU time in microseconds, NOT a
// percentage. To render a CPU% (as the fleet sparklines do) you must sample
// twice and divide the delta by the wall-clock delta and core count — see
// CPUPercent below. Memory% is a straight ratio and needs only one sample.
type Stats struct {
	ID               string `json:"id"`
	CPUUsageUsec     int64  `json:"cpuUsageUsec"`
	MemoryUsageBytes int64  `json:"memoryUsageBytes"`
	MemoryLimitBytes int64  `json:"memoryLimitBytes"`
	BlockReadBytes   int64  `json:"blockReadBytes"`
	BlockWriteBytes  int64  `json:"blockWriteBytes"`
	NetworkRxBytes   int64  `json:"networkRxBytes"`
	NetworkTxBytes   int64  `json:"networkTxBytes"`
	NumProcesses     int    `json:"numProcesses"`
}

// ---------------------------------------------------------------------------
// Derived helpers — keep presentation logic off the raw wire types.
// ---------------------------------------------------------------------------

const (
	StateRunning = "running"
	StateStopped = "stopped"
)

// IsRunning reports whether the container is in the running state.
func (c Container) IsRunning() bool { return c.Status.State == StateRunning }

// PrimaryIPv4 returns the container's dedicated IPv4 address without the CIDR
// suffix, or "" if the container is not running (stopped containers carry no
// runtime network assignment). This is the value the fleet/topology views show.
func (c Container) PrimaryIPv4() string {
	if len(c.Status.Networks) == 0 {
		return ""
	}
	addr := c.Status.Networks[0].IPv4Address
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[:i]
	}
	return addr
}

// Uptime returns how long the container has been running, or 0 if not running.
func (c Container) Uptime(now time.Time) time.Duration {
	if !c.IsRunning() || c.Status.StartedDate.IsZero() {
		return 0
	}
	return now.Sub(c.Status.StartedDate)
}

// CLIVersion extracts the clean CLI semver from the version array, ignoring
// the apiserver's free-form string. Returns "" if not found.
func CLIVersion(entries []VersionEntry) string {
	for _, e := range entries {
		if e.AppName == "container" {
			return e.Version
		}
	}
	return ""
}

// MemoryPercent returns memory utilisation in the range [0,100].
func (s Stats) MemoryPercent() float64 {
	if s.MemoryLimitBytes == 0 {
		return 0
	}
	return float64(s.MemoryUsageBytes) / float64(s.MemoryLimitBytes) * 100
}

// CPUPercent computes CPU utilisation between two stats samples. Because
// CPUUsageUsec is cumulative, a single sample cannot yield a percentage.
// `wall` is the real time elapsed between the two samples; `cores` is the
// container's allocated CPU count (Configuration.Resources.CPUs). The result
// is clamped to [0, 100*cores] and normalised to [0,100] of total capacity.
func CPUPercent(prev, cur Stats, wall time.Duration, cores int) float64 {
	if wall <= 0 || cores <= 0 {
		return 0
	}
	deltaUsec := float64(cur.CPUUsageUsec - prev.CPUUsageUsec)
	if deltaUsec < 0 {
		return 0
	}
	wallUsec := float64(wall.Microseconds())
	pct := (deltaUsec / (wallUsec * float64(cores))) * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}
