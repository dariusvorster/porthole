package engine

import (
	"encoding/json"
	"testing"
	"time"
)

// All fixtures below are VERBATIM output captured from container v1.0.0.
// They exist to prove the structs in types.go match reality, and to fail loudly
// if a future container release changes the shape.

const fxVersion = `[{"appName":"container","buildType":"release","commit":"ee848e3ebfd7c73b04dd419683be54fb450b8779","version":"1.0.0"},{"appName":"container-apiserver","buildType":"release","commit":"ee848e3ebfd7c73b04dd419683be54fb450b8779","version":"container-apiserver version 1.0.0 (build: release, commit: ee848e3)"}]`

const fxDF = `{"containers":{"active":1,"reclaimable":405454848,"sizeInBytes":860344320,"total":2},"images":{"active":2,"reclaimable":240816128,"sizeInBytes":833818624,"total":3},"volumes":{"active":0,"reclaimable":0,"sizeInBytes":0,"total":0}}`

const fxList = `[{"configuration":{"capAdd":[],"capDrop":[],"creationDate":"2026-06-10T21:04:28Z","dns":{"nameservers":[],"options":[],"searchDomains":[]},"id":"api","image":{"descriptor":{"digest":"sha256:fb4c","mediaType":"application/vnd.oci.image.index.v1+json","size":7665},"reference":"docker.io/library/node:20-alpine"},"initProcess":{"arguments":["node"],"environment":["PATH=/usr/bin","NODE_VERSION=20.20.2"],"executable":"docker-entrypoint.sh","rlimits":[],"supplementalGroups":[],"terminal":false,"user":{"id":{"gid":0,"uid":0}},"workingDirectory":"/"},"labels":{},"mounts":[],"networks":[{"network":"default","options":{"hostname":"api","mtu":1280}}],"platform":{"architecture":"arm64","os":"linux"},"publishedPorts":[],"publishedSockets":[],"readOnly":false,"resources":{"cpuOverhead":1,"cpus":4,"memoryInBytes":1073741824},"rosetta":false,"runtimeHandler":"container-runtime-linux","ssh":false,"sysctls":{},"useInit":false,"virtualization":false},"id":"api","status":{"networks":[],"startedDate":"2026-06-10T21:04:29Z","state":"stopped"}},{"configuration":{"capAdd":[],"capDrop":[],"creationDate":"2026-06-10T21:00:10Z","dns":{"nameservers":[],"options":[],"searchDomains":[]},"id":"web","image":{"descriptor":{"digest":"sha256:5aca","mediaType":"application/vnd.oci.image.index.v1+json","size":10229},"reference":"docker.io/library/nginx:latest"},"initProcess":{"arguments":["nginx","-g","daemon off;"],"environment":["NGINX_VERSION=1.31.1"],"executable":"/docker-entrypoint.sh","rlimits":[],"supplementalGroups":[],"terminal":false,"user":{"id":{"gid":0,"uid":0}},"workingDirectory":"/"},"labels":{},"mounts":[],"networks":[{"network":"default","options":{"hostname":"web","mtu":1280}}],"platform":{"architecture":"arm64","os":"linux"},"publishedPorts":[],"publishedSockets":[],"readOnly":false,"resources":{"cpuOverhead":1,"cpus":4,"memoryInBytes":1073741824},"rosetta":false,"runtimeHandler":"container-runtime-linux","ssh":false,"stopSignal":"SIGQUIT","sysctls":{},"useInit":false,"virtualization":false},"id":"web","status":{"networks":[{"hostname":"web","ipv4Address":"192.168.64.2/24","ipv4Gateway":"192.168.64.1","ipv6Address":"fdd3:983f:10f0:f8e1:f819:95ff:fe74:8b99/64","macAddress":"fa:19:95:74:8b:99","mtu":1280,"network":"default"}],"startedDate":"2026-06-10T21:00:13Z","state":"running"}}]`

const fxNetwork = `[{"configuration":{"creationDate":"2026-06-10T20:50:12Z","labels":{"com.apple.container.resource.role":"builtin"},"mode":"nat","name":"default","options":{},"plugin":"container-network-vmnet"},"id":"default","status":{"ipv4Gateway":"192.168.64.1","ipv4Subnet":"192.168.64.0/24","ipv6Subnet":"fdd3:983f:10f0:f8e1::/64"}}]`

const fxStats = `[{"blockReadBytes":16711680,"blockWriteBytes":8192,"cpuUsageUsec":24006,"id":"web","memoryLimitBytes":1073741824,"memoryUsageBytes":23023616,"networkRxBytes":29056,"networkTxBytes":602,"numProcesses":6}]`

const fxError = "Error: Unknown option '--bogusflag'\nUsage: container list [--all] [--format <format>] [--quiet] [--debug]\n  See 'container list --help' for more information."

// Verbatim stderr captured after `container system stop`, then `container ls`.
// This is the sample the bootstrap banner depends on.
const fxDaemonDown = "Error: internalError: \"failed to list containers\" (cause: \"interrupted: \"XPC connection error: Connection invalid\"\")\nEnsure container system service has been started with `container system start`."

func TestVersionArrayAndCLIExtraction(t *testing.T) {
	var v []VersionEntry
	if err := json.Unmarshal([]byte(fxVersion), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(v) != 2 {
		t.Fatalf("want 2 version entries, got %d", len(v))
	}
	if got := CLIVersion(v); got != "1.0.0" {
		t.Errorf("CLIVersion = %q, want 1.0.0", got)
	}
}

func TestDiskUsage(t *testing.T) {
	var d DiskUsage
	if err := json.Unmarshal([]byte(fxDF), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Containers.Total != 2 || d.Containers.Active != 1 {
		t.Errorf("containers = %+v, want total 2 active 1", d.Containers)
	}
	if d.Images.SizeInBytes != 833818624 {
		t.Errorf("images size = %d", d.Images.SizeInBytes)
	}
}

func TestListShapeAndIPOnlyWhenRunning(t *testing.T) {
	var cs []Container
	if err := json.Unmarshal([]byte(fxList), &cs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cs) != 2 {
		t.Fatalf("want 2 containers, got %d", len(cs))
	}
	byID := map[string]Container{}
	for _, c := range cs {
		byID[c.ID] = c
	}

	web, api := byID["web"], byID["api"]

	if !web.IsRunning() {
		t.Error("web should be running")
	}
	if got := web.PrimaryIPv4(); got != "192.168.64.2" {
		t.Errorf("web IP = %q, want 192.168.64.2 (CIDR stripped)", got)
	}
	if web.Configuration.StopSignal != "SIGQUIT" {
		t.Errorf("web stopSignal = %q, want SIGQUIT", web.Configuration.StopSignal)
	}

	// The load-bearing finding: a stopped container has no runtime network,
	// hence no IP. Code that assumes an IP is always present would break here.
	if api.IsRunning() {
		t.Error("api should be stopped")
	}
	if got := api.PrimaryIPv4(); got != "" {
		t.Errorf("api IP = %q, want empty (stopped containers carry no IP)", got)
	}
	if len(api.Status.Networks) != 0 {
		t.Errorf("api status.networks = %d, want 0", len(api.Status.Networks))
	}
	// api omits stopSignal entirely — the omitempty/optional handling must hold.
	if api.Configuration.StopSignal != "" {
		t.Errorf("api stopSignal = %q, want empty", api.Configuration.StopSignal)
	}
	// Membership is still known for a stopped container via configuration.networks.
	if len(api.Configuration.Networks) != 1 || api.Configuration.Networks[0].Network != "default" {
		t.Errorf("api should still declare membership of 'default'")
	}
}

func TestNetworkShape(t *testing.T) {
	var ns []Network
	if err := json.Unmarshal([]byte(fxNetwork), &ns); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(ns) != 1 {
		t.Fatalf("want 1 network, got %d", len(ns))
	}
	n := ns[0]
	if n.Configuration.Mode != "nat" || n.Configuration.Plugin != "container-network-vmnet" {
		t.Errorf("network config = %+v", n.Configuration)
	}
	if n.Status.IPv4Subnet != "192.168.64.0/24" {
		t.Errorf("subnet = %q", n.Status.IPv4Subnet)
	}
	if n.Configuration.Labels["com.apple.container.resource.role"] != "builtin" {
		t.Errorf("missing builtin role label")
	}
}

func TestStatsCumulativeCPU(t *testing.T) {
	var s []Stats
	if err := json.Unmarshal([]byte(fxStats), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(s) != 1 {
		t.Fatalf("want 1 stats row, got %d", len(s))
	}
	cur := s[0]
	if cur.CPUUsageUsec != 24006 {
		t.Errorf("cpuUsageUsec = %d", cur.CPUUsageUsec)
	}
	// Memory% is a single-sample ratio: 23023616 / 1073741824 ~= 2.14%.
	if mp := cur.MemoryPercent(); mp < 2.0 || mp > 2.3 {
		t.Errorf("MemoryPercent = %.3f, want ~2.14", mp)
	}
	// CPU% needs two samples; demonstrate the delta math with a synthetic next
	// sample 1s later having used another 40,000us across 4 cores -> 1%.
	next := cur
	next.CPUUsageUsec = cur.CPUUsageUsec + 40000
	if pct := CPUPercent(cur, next, time.Second, 4); pct < 0.9 || pct > 1.1 {
		t.Errorf("CPUPercent = %.3f, want ~1.0", pct)
	}
}

func TestErrorClassification(t *testing.T) {
	kind, msg := classify(fxError)
	if kind != ErrUnknownOption {
		t.Errorf("kind = %q, want unknown_option", kind)
	}
	if msg != "Unknown option '--bogusflag'" {
		t.Errorf("msg = %q", msg)
	}
}

func TestDaemonDownClassification(t *testing.T) {
	kind, _ := classify(fxDaemonDown)
	if kind != ErrDaemonDown {
		t.Fatalf("kind = %q, want daemon_down — the bootstrap banner depends on this", kind)
	}
}
