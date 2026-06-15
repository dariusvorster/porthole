package stacks

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// rejectedServiceKeys are compose service-level keys v1 cannot honor. Their
// presence makes the stack invalid (un-appliable) and is reported, never dropped.
var rejectedServiceKeys = map[string]string{
	"build":    "image builds are not supported — supply a prebuilt image: reference",
	"profiles": "compose profiles are not supported",
	"deploy":   "swarm/deploy directives are not supported",
	"extends":  "extends is not supported",
	"configs":  "configs are not supported",
	"secrets":  "secrets are not supported",
}

// knownServiceKeys are the service-level keys v1 maps onto `container run`.
var knownServiceKeys = map[string]bool{
	"image": true, "command": true, "environment": true, "env_file": true,
	"ports": true, "volumes": true, "labels": true, "depends_on": true,
	"restart": true, "container_name": true,
}

// knownTopKeys are the top-level compose keys v1 understands.
var knownTopKeys = map[string]bool{
	"services": true, "volumes": true, "networks": true, "version": true, "name": true,
}

var rejectedTopKeys = map[string]string{
	"configs": "top-level configs are not supported",
	"secrets": "top-level secrets are not supported",
}

// Parse turns a docker-compose document into a Stack + a ValidationReport. It is
// pure (no I/O). It never returns a Go error: an unparseable document or any
// validation failure is reported via the ValidationReport (Valid=false), so the
// caller can always show the user a result. The Stack is best-effort populated
// even when invalid, so the UI can preview what WOULD run.
func Parse(name string, data []byte) (Stack, ValidationReport) {
	rep := ValidationReport{Valid: true}
	stack := Stack{Name: name}

	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		rep.addError(fmt.Sprintf("yaml: %v", err))
		return stack, rep
	}
	if len(doc) == 0 {
		rep.addError("empty compose document")
		return stack, rep
	}

	// Top-level keys: classify supported / rejected / unknown.
	for _, key := range sortedNodeKeys(doc) {
		switch {
		case knownTopKeys[key]:
		case rejectedTopKeys[key] != "":
			rep.reject(key, rejectedTopKeys[key])
		default:
			rep.warn(fmt.Sprintf("ignoring unknown top-level key %q", key))
		}
	}

	// A "name:" in the file overrides the supplied name only if one wasn't given.
	if stack.Name == "" {
		if n, ok := doc["name"]; ok {
			_ = n.Decode(&stack.Name)
		}
	}

	// Top-level volumes / networks: accept the declared names (map or list form).
	if vn, ok := doc["volumes"]; ok {
		stack.Volumes = declaredNames(vn)
	}
	if nn, ok := doc["networks"]; ok {
		stack.Networks = declaredNames(nn)
	}

	servicesNode, ok := doc["services"]
	if !ok {
		rep.addError("no services declared")
		return stack, rep
	}
	var rawServices map[string]yaml.Node
	if err := servicesNode.Decode(&rawServices); err != nil {
		rep.addError(fmt.Sprintf("services: %v", err))
		return stack, rep
	}
	if len(rawServices) == 0 {
		rep.addError("no services declared")
		return stack, rep
	}

	declared := map[string]bool{}
	for _, svcName := range sortedNodeKeys(rawServices) {
		declared[svcName] = true
	}

	for _, svcName := range sortedNodeKeys(rawServices) {
		svc := parseService(svcName, rawServices[svcName], &rep)
		stack.Services = append(stack.Services, svc)
	}

	// Cross-service validation: depends_on references + acyclicity.
	validateDependsOn(stack.Services, declared, &rep)

	return stack, rep
}

// parseService decodes one service node, classifying its keys and decoding the
// supported fields with the flexible (map-or-list) compose forms.
func parseService(name string, node yaml.Node, rep *ValidationReport) Service {
	svc := Service{Name: name, Environment: map[string]string{}, Labels: map[string]string{}}

	var raw map[string]yaml.Node
	if err := node.Decode(&raw); err != nil {
		rep.addError(fmt.Sprintf("services.%s: %v", name, err))
		return svc
	}

	for _, key := range sortedNodeKeys(raw) {
		switch {
		case knownServiceKeys[key]:
		case rejectedServiceKeys[key] != "":
			rep.reject("services."+name+"."+key, rejectedServiceKeys[key])
		default:
			rep.warn(fmt.Sprintf("ignoring unknown key services.%s.%s", name, key))
		}
	}

	if n, ok := raw["image"]; ok {
		_ = n.Decode(&svc.Image)
	}
	if svc.Image == "" {
		rep.addError(fmt.Sprintf("services.%s: image is required (v1 does not build)", name))
	}
	if n, ok := raw["container_name"]; ok {
		var cn string
		_ = n.Decode(&cn)
		rep.note(fmt.Sprintf("services.%s.container_name ignored — names are namespaced <stack>-<service>", name))
	}
	if n, ok := raw["command"]; ok {
		cmd, err := commandOrList(n)
		if err != nil {
			rep.addError(fmt.Sprintf("services.%s.command: %v", name, err))
		}
		svc.Command = cmd
	}
	if n, ok := raw["environment"]; ok {
		env, err := kvMapOrList(n)
		if err != nil {
			rep.addError(fmt.Sprintf("services.%s.environment: %v", name, err))
		}
		svc.Environment = env
	}
	if n, ok := raw["env_file"]; ok {
		ef, err := stringOrList(n)
		if err != nil {
			rep.addError(fmt.Sprintf("services.%s.env_file: %v", name, err))
		}
		svc.EnvFile = ef
	}
	if n, ok := raw["labels"]; ok {
		lbl, err := kvMapOrList(n)
		if err != nil {
			rep.addError(fmt.Sprintf("services.%s.labels: %v", name, err))
		}
		svc.Labels = lbl
	}
	if n, ok := raw["depends_on"]; ok {
		deps, err := dependsOn(n)
		if err != nil {
			rep.addError(fmt.Sprintf("services.%s.depends_on: %v", name, err))
		}
		svc.DependsOn = deps
	}
	if n, ok := raw["ports"]; ok {
		ports, err := parsePorts(n)
		if err != nil {
			rep.addError(fmt.Sprintf("services.%s.ports: %v", name, err))
		}
		svc.Ports = ports
	}
	if n, ok := raw["volumes"]; ok {
		vols, err := parseVolumes(n)
		if err != nil {
			rep.addError(fmt.Sprintf("services.%s.volumes: %v", name, err))
		}
		svc.Volumes = vols
	}
	if n, ok := raw["restart"]; ok {
		var r string
		_ = n.Decode(&r)
		svc.Restart = r
		switch r {
		case RestartNo, RestartAlways, RestartUnlessStopped, "":
		case "on-failure":
			rep.addError(fmt.Sprintf("services.%s.restart: on-failure is not supported (the runtime exposes no exit code); use always or unless-stopped", name))
		default:
			rep.addError(fmt.Sprintf("services.%s.restart: %q is not a valid policy (no|always|unless-stopped)", name, r))
		}
	}

	return svc
}

// validateDependsOn checks every depends_on target exists and the graph is acyclic.
func validateDependsOn(services []Service, declared map[string]bool, rep *ValidationReport) {
	graph := map[string][]string{}
	for _, s := range services {
		for _, dep := range s.DependsOn {
			if !declared[dep] {
				rep.addError(fmt.Sprintf("services.%s.depends_on: references unknown service %q", s.Name, dep))
				continue
			}
			graph[s.Name] = append(graph[s.Name], dep)
		}
	}
	if cycle := findCycle(services, graph); cycle != "" {
		rep.addError("depends_on cycle detected: " + cycle)
	}
}

// findCycle does a DFS for a back-edge, returning a human description of the
// first cycle found, or "" when the graph is acyclic.
func findCycle(services []Service, graph map[string][]string) string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var path []string
	var dfs func(n string) string
	dfs = func(n string) string {
		color[n] = gray
		path = append(path, n)
		for _, dep := range graph[n] {
			switch color[dep] {
			case gray:
				return strings.Join(append(path, dep), " -> ")
			case white:
				if c := dfs(dep); c != "" {
					return c
				}
			}
		}
		path = path[:len(path)-1]
		color[n] = black
		return ""
	}
	for _, s := range services {
		if color[s.Name] == white {
			if c := dfs(s.Name); c != "" {
				return c
			}
		}
	}
	return ""
}

// --- flexible compose value decoders -------------------------------------

// commandOrList decodes the two compose command forms: a scalar shell line
// ("sh -c \"echo hi\"") which is shell-tokenized (honoring quotes), OR an
// already-split sequence (["sh","-c","echo hi"]) taken verbatim.
func commandOrList(n yaml.Node) ([]string, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		var s string
		if err := n.Decode(&s); err != nil {
			return nil, err
		}
		if strings.TrimSpace(s) == "" {
			return nil, nil
		}
		return shellSplit(s)
	case yaml.SequenceNode:
		var out []string
		if err := n.Decode(&out); err != nil {
			return nil, err
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected a string or list")
	}
}

// stringOrList decodes a scalar as a SINGLE element (paths are not split) OR a
// sequence verbatim. Used for env_file, where a value is a path that may contain
// spaces and must never be tokenized.
func stringOrList(n yaml.Node) ([]string, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		var s string
		if err := n.Decode(&s); err != nil {
			return nil, err
		}
		if s == "" {
			return nil, nil
		}
		return []string{s}, nil
	case yaml.SequenceNode:
		var out []string
		if err := n.Decode(&out); err != nil {
			return nil, err
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected a string or list")
	}
}

// ShellSplit tokenizes a command line honoring quotes — exported for the create
// endpoint, which shell-splits a command string into argv using the same rules
// as the compose string command form (one implementation, one place for bugs).
func ShellSplit(s string) ([]string, error) { return shellSplit(s) }

// shellSplit tokenizes a command line honoring single and double quotes (POSIX
// word-splitting, the subset compose's string command form needs). Redirections
// and pipes are left as literal tokens — a string command is expected to be run
// via `sh -c "..."`, where the quoted argument keeps them intact.
func shellSplit(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inSingle, inDouble, hasToken := false, false, false
	for _, r := range s {
		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				cur.WriteRune(r)
			}
		case inDouble:
			if r == '"' {
				inDouble = false
			} else {
				cur.WriteRune(r)
			}
		case r == '\'':
			inSingle, hasToken = true, true
		case r == '"':
			inDouble, hasToken = true, true
		case r == ' ' || r == '\t':
			if hasToken {
				args = append(args, cur.String())
				cur.Reset()
				hasToken = false
			}
		default:
			cur.WriteRune(r)
			hasToken = true
		}
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("unbalanced quotes in command")
	}
	if hasToken {
		args = append(args, cur.String())
	}
	return args, nil
}

// kvMapOrList decodes the two compose forms: a map {K: V} or a list ["K=V","K"].
func kvMapOrList(n yaml.Node) (map[string]string, error) {
	out := map[string]string{}
	switch n.Kind {
	case yaml.MappingNode:
		var m map[string]string
		if err := n.Decode(&m); err != nil {
			// values may be non-string scalars (numbers/bools); decode loosely.
			var loose map[string]any
			if err2 := n.Decode(&loose); err2 != nil {
				return nil, err
			}
			for k, v := range loose {
				out[k] = fmt.Sprintf("%v", v)
			}
			return out, nil
		}
		return m, nil
	case yaml.SequenceNode:
		var items []string
		if err := n.Decode(&items); err != nil {
			return nil, err
		}
		for _, it := range items {
			k, v, found := strings.Cut(it, "=")
			if !found {
				out[k] = "" // "KEY" form = pass-through from host env (empty here)
				continue
			}
			out[k] = v
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected a map or list")
	}
}

// dependsOn decodes a list ["a","b"] or the long-form map {a: {condition: ...}}.
func dependsOn(n yaml.Node) ([]string, error) {
	switch n.Kind {
	case yaml.SequenceNode:
		var out []string
		if err := n.Decode(&out); err != nil {
			return nil, err
		}
		sort.Strings(out)
		return out, nil
	case yaml.MappingNode:
		var m map[string]yaml.Node
		if err := n.Decode(&m); err != nil {
			return nil, err
		}
		out := sortedNodeKeys(m)
		return out, nil
	default:
		return nil, fmt.Errorf("expected a list or map")
	}
}

// parsePorts decodes a sequence of "HOST:CONTAINER[/proto]" / "CONTAINER" / numbers.
func parsePorts(n yaml.Node) ([]PortMapping, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("expected a list of port mappings")
	}
	var items []yaml.Node
	if err := n.Decode(&items); err != nil {
		return nil, err
	}
	var out []PortMapping
	for _, item := range items {
		var s string
		if err := item.Decode(&s); err != nil {
			return nil, err
		}
		pm, err := parsePort(s)
		if err != nil {
			return nil, err
		}
		out = append(out, pm)
	}
	return out, nil
}

func parsePort(s string) (PortMapping, error) {
	proto := "tcp"
	if base, p, found := strings.Cut(s, "/"); found {
		s = base
		proto = strings.ToLower(p)
		if proto != "tcp" && proto != "udp" {
			return PortMapping{}, fmt.Errorf("invalid protocol %q in %q", proto, s)
		}
	}
	hostStr, contStr, found := strings.Cut(s, ":")
	if !found {
		// "CONTAINER" only — publish on the same host port.
		contStr = hostStr
	}
	host, err := strconv.Atoi(strings.TrimSpace(hostStr))
	if err != nil {
		return PortMapping{}, fmt.Errorf("invalid host port %q", hostStr)
	}
	cont, err := strconv.Atoi(strings.TrimSpace(contStr))
	if err != nil {
		return PortMapping{}, fmt.Errorf("invalid container port %q", contStr)
	}
	if host < 1 || host > 65535 || cont < 1 || cont > 65535 {
		return PortMapping{}, fmt.Errorf("port out of range in %q", s)
	}
	return PortMapping{HostPort: host, ContainerPort: cont, Proto: proto}, nil
}

// parseVolumes decodes a sequence of "SOURCE:TARGET" strings (named volume or path).
func parseVolumes(n yaml.Node) ([]VolumeMount, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("expected a list of volume mounts")
	}
	var items []string
	if err := n.Decode(&items); err != nil {
		return nil, err
	}
	var out []VolumeMount
	for _, it := range items {
		src, tgt, found := strings.Cut(it, ":")
		if !found || src == "" || tgt == "" {
			return nil, fmt.Errorf("invalid volume mount %q (want SOURCE:TARGET)", it)
		}
		out = append(out, VolumeMount{Source: src, Target: tgt})
	}
	return out, nil
}

// declaredNames returns the keys of a map node or the items of a list node — the
// two forms compose accepts for top-level volumes:/networks:.
func declaredNames(n yaml.Node) []string {
	switch n.Kind {
	case yaml.MappingNode:
		var m map[string]yaml.Node
		if err := n.Decode(&m); err == nil {
			return sortedNodeKeys(m)
		}
	case yaml.SequenceNode:
		var out []string
		if err := n.Decode(&out); err == nil {
			sort.Strings(out)
			return out
		}
	}
	return nil
}

// sortedNodeKeys returns a map's keys in sorted order for deterministic output.
func sortedNodeKeys(m map[string]yaml.Node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- ValidationReport mutators -------------------------------------------

func (r *ValidationReport) addError(msg string) { r.Errors = append(r.Errors, msg); r.Valid = false }
func (r *ValidationReport) reject(path, reason string) {
	r.Rejected = append(r.Rejected, RejectedKey{Path: path, Reason: reason})
	r.Valid = false
}
func (r *ValidationReport) warn(msg string) { r.Warnings = append(r.Warnings, msg) }
func (r *ValidationReport) note(msg string) { r.Notes = append(r.Notes, msg) }
