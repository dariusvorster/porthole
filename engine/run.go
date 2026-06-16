package engine

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// RunSpec describes a container to create via `container run -d`. It is the
// engine-level mapping target for a Stacks service (the stacks package builds
// one of these per service). Kept here because the engine is the only place that
// knows the CLI surface (`-e`, `-v`, `-p`, `--label`, `--network`).
type RunSpec struct {
	Name    string            // --name (namespaced <stack>-<service> by the caller)
	Image   string            // image reference (required)
	Command []string          // optional command + args after the image
	Env     map[string]string // -e KEY=VALUE
	EnvFile []string          // --env-file PATH
	Labels  map[string]string // --label KEY=VALUE
	Ports   []RunPort         // -p HOST:CONTAINER[/proto]
	Volumes []RunVolume       // -v SOURCE:TARGET (named volume or host-path bind)
	Network string            // --network NAME
	CPUs    int               // --cpus N (0 = unset)
	Memory  string            // --memory <n>m (MiB granularity; "" = unset)
	WorkDir string            // --workdir DIR
	User    string            // --user NAME|UID[:GID]

	// Richer create flags (Phase 9). Bools emit only when true; strings/slices
	// only when set. Health is intentionally NOT here — it's a supervision concern
	// (no --health-* flag exists), wired via porthole.health.* labels.
	Init       bool     // --init
	ReadOnly   bool     // --read-only
	Entrypoint string   // --entrypoint CMD
	CapAdd     []string // repeated --cap-add CAP
	CapDrop    []string // repeated --cap-drop CAP
	Tmpfs      []string // repeated --tmpfs PATH
	ShmSize    string   // --shm-size SIZE
}

// toArgs builds the `container run` argv from a RunSpec. progress=true adds
// `--progress plain` so the pull phases stream as plain lines (RunStream);
// map-valued flags emit in sorted key order for determinism.
func (spec RunSpec) toArgs(progress bool) []string {
	args := []string{"run", "-d"}
	if progress {
		args = append(args, "--progress", "plain")
	}
	if spec.Name != "" {
		args = append(args, "--name", spec.Name)
	}
	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	}
	if spec.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(spec.CPUs))
	}
	if spec.Memory != "" {
		args = append(args, "--memory", spec.Memory)
	}
	if spec.WorkDir != "" {
		args = append(args, "--workdir", spec.WorkDir)
	}
	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}
	if spec.Init {
		args = append(args, "--init")
	}
	if spec.ReadOnly {
		args = append(args, "--read-only")
	}
	if spec.Entrypoint != "" {
		args = append(args, "--entrypoint", spec.Entrypoint)
	}
	if spec.ShmSize != "" {
		args = append(args, "--shm-size", spec.ShmSize)
	}
	for _, c := range spec.CapAdd {
		args = append(args, "--cap-add", c)
	}
	for _, c := range spec.CapDrop {
		args = append(args, "--cap-drop", c)
	}
	for _, t := range spec.Tmpfs {
		args = append(args, "--tmpfs", t)
	}
	for _, k := range sortedKeys(spec.Labels) {
		args = append(args, "--label", k+"="+spec.Labels[k])
	}
	for _, k := range sortedKeys(spec.Env) {
		args = append(args, "-e", k+"="+spec.Env[k])
	}
	for _, f := range spec.EnvFile {
		args = append(args, "--env-file", f)
	}
	for _, p := range spec.Ports {
		args = append(args, "-p", formatPort(p))
	}
	for _, v := range spec.Volumes {
		args = append(args, "-v", v.Source+":"+v.Target)
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)
	return args
}

// RunPort is a published port for RunSpec.
type RunPort struct {
	HostPort      int
	ContainerPort int
	Proto         string // "tcp" (default) | "udp"
}

// RunVolume is a volume mount for RunSpec.
type RunVolume struct {
	Source string // named volume or host path
	Target string // in-container path
}

// RunContainer runs `container run -d ...` from a RunSpec and returns the new
// container id. For named containers id == name; the runtime also echoes the id
// on stdout (the last line, after any progress output), which we prefer.
//
// Map-valued flags (env, labels) are emitted in sorted key order so the command
// is deterministic — important for tests and for stable recreate-diffing later.
func (e *CLIEngine) RunContainer(ctx context.Context, spec RunSpec) (string, error) {
	out, err := e.run(ctx, spec.toArgs(false)...)
	if err != nil {
		return "", err
	}
	if id := lastNonEmptyLine(out); id != "" {
		return id, nil
	}
	return spec.Name, nil
}

// RunUpdate is one event from RunStream: a progress phase, the final created id,
// or a terminal error. Exactly one terminal (created|error) is sent, last.
type RunUpdate struct {
	Kind  string // "progress" | "created" | "error"
	Index int    // progress: N in [N/total]
	Total int    // progress: total phases
	Phase string // progress: phase text (e.g. "Fetching image")
	ID    string // created: the new container id
	Err   error  // error: a classified *CLIError
}

// RunStream runs `container run -d --progress plain …` and streams the pull/start
// phases as they arrive, then a terminal created/error. Because run AUTO-PULLS
// and BLOCKS on a not-present image (create spec §8.1), this is the create path:
// the same spawn-stream-reap discipline as logs. The child is tied to ctx — a
// client disconnect cancels ctx, which kills the child and ends the stream.
func (e *CLIEngine) RunStream(ctx context.Context, spec RunSpec) <-chan RunUpdate {
	ch := make(chan RunUpdate, 16)
	send := func(u RunUpdate) {
		select {
		case ch <- u:
		case <-ctx.Done():
		}
	}
	go func() {
		defer close(ch)
		args := spec.toArgs(true)
		cmd := exec.CommandContext(ctx, e.Bin, args...)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			send(RunUpdate{Kind: "error", Err: err})
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			send(RunUpdate{Kind: "error", Err: err})
			return
		}
		if err := cmd.Start(); err != nil {
			send(RunUpdate{Kind: "error", Err: err})
			return
		}

		var stderrBuf bytes.Buffer
		done := make(chan struct{})
		go func() {
			defer close(done)
			sc := bufio.NewScanner(stderr)
			sc.Buffer(make([]byte, 64*1024), 1024*1024)
			for sc.Scan() {
				line := sc.Text()
				stderrBuf.WriteString(line)
				stderrBuf.WriteByte('\n')
				if n, m, txt, ok := parsePhase(line); ok {
					send(RunUpdate{Kind: "progress", Index: n, Total: m, Phase: txt})
				}
			}
		}()

		out, _ := io.ReadAll(stdout)
		<-done
		if werr := cmd.Wait(); werr != nil {
			kind, msg := classify(stderrBuf.String())
			send(RunUpdate{Kind: "error", Err: &CLIError{Args: args, Kind: kind, Message: msg, Raw: stderrBuf.String()}})
			return
		}
		id := lastNonEmptyLine(out)
		if id == "" {
			id = spec.Name
		}
		send(RunUpdate{Kind: "created", ID: id})
	}()
	return ch
}

// parsePhase parses a `--progress plain` line: `[N/M] <text> [<elapsed>s]`. It
// returns the phase index/total and the text (with the trailing elapsed bracket
// stripped, byte/blob detail preserved). ok=false for non-phase lines.
func parsePhase(line string) (idx, total int, text string, ok bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "[") {
		return 0, 0, "", false
	}
	end := strings.IndexByte(line, ']')
	if end < 0 {
		return 0, 0, "", false
	}
	frac := line[1:end]
	slash := strings.IndexByte(frac, '/')
	if slash < 0 {
		return 0, 0, "", false
	}
	n, err1 := strconv.Atoi(strings.TrimSpace(frac[:slash]))
	m, err2 := strconv.Atoi(strings.TrimSpace(frac[slash+1:]))
	if err1 != nil || err2 != nil {
		return 0, 0, "", false
	}
	rest := strings.TrimSpace(line[end+1:])
	// Strip a trailing `[<elapsed>s]` bracket, keeping any blob/byte detail.
	if i := strings.LastIndexByte(rest, '['); i >= 0 && strings.HasSuffix(rest, "]") {
		rest = strings.TrimSpace(rest[:i])
	}
	return n, m, rest, true
}

// CreateNetwork creates a network (`container network create <name>`). The
// caller is expected to check existence first; a name_conflict is surfaced as a
// CLIError so the caller can treat it as already-present if it wishes.
func (e *CLIEngine) CreateNetwork(ctx context.Context, name string) error {
	_, err := e.run(ctx, "network", "create", name)
	return err
}

// NetworkSpec is the user-facing network-create surface (Phase 11) — exactly the
// flags `container network create` exposes (captured): name + optional subnet,
// IPv6 subnet, internal, labels, options. No driver/gateway/--ipv6 toggle exist.
type NetworkSpec struct {
	Name     string
	Subnet   string            // --subnet CIDR ("" → runtime auto-assigns)
	SubnetV6 string            // --subnet-v6 prefix
	Internal bool              // --internal (host-only)
	Labels   map[string]string // --label k=v (sorted)
	Options  map[string]string // --option k=v (sorted)
}

// networkCreateArgs builds the argv for a NetworkSpec. Set flags only; map-valued
// flags emit in sorted key order for determinism.
func networkCreateArgs(spec NetworkSpec) []string {
	args := []string{"network", "create"}
	if spec.Subnet != "" {
		args = append(args, "--subnet", spec.Subnet)
	}
	if spec.SubnetV6 != "" {
		args = append(args, "--subnet-v6", spec.SubnetV6)
	}
	if spec.Internal {
		args = append(args, "--internal")
	}
	for _, k := range sortedKeys(spec.Labels) {
		args = append(args, "--label", k+"="+spec.Labels[k])
	}
	for _, k := range sortedKeys(spec.Options) {
		args = append(args, "--option", k+"="+spec.Options[k])
	}
	args = append(args, spec.Name)
	return args
}

// NetworkCreate creates a network from a spec and returns the resulting Network
// (re-read via inspect, so the caller sees the auto-assigned subnet/gateway). A
// name collision surfaces as a name_conflict CLIError.
func (e *CLIEngine) NetworkCreate(ctx context.Context, spec NetworkSpec) (Network, error) {
	if _, err := e.run(ctx, networkCreateArgs(spec)...); err != nil {
		return Network{}, err
	}
	return e.InspectNetwork(ctx, spec.Name)
}

// RemoveNetwork deletes a network (`container network delete <name>`). Used on
// stack down (best-effort — a network still in use will error).
func (e *CLIEngine) RemoveNetwork(ctx context.Context, name string) error {
	_, err := e.run(ctx, "network", "delete", name)
	return err
}

func formatPort(p RunPort) string {
	proto := p.Proto
	s := strconv.Itoa(p.HostPort) + ":" + strconv.Itoa(p.ContainerPort)
	if proto != "" && proto != "tcp" {
		s += "/" + proto
	}
	return s
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func lastNonEmptyLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}
