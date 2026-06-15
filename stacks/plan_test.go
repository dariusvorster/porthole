package stacks

import (
	"reflect"
	"testing"

	"github.com/porthole/porthole/engine"
)

// memberContainer builds an observed container with stack/service membership labels.
func memberContainer(stack, svc, state, image string) engine.Container {
	c := engine.Container{ID: stack + "-" + svc}
	c.Configuration.Labels = map[string]string{LabelStack: stack, LabelService: svc}
	c.Configuration.Image.Reference = image
	c.Status.State = state
	if state == engine.StateRunning {
		c.Status.Networks = []engine.NetworkStatus{{Network: stack, IPv4Address: "192.168.65.2/24"}}
	}
	return c
}

func actionFor(p Plan, svc string) (ServiceAction, bool) {
	for _, a := range p.Actions {
		if a.Service == svc {
			return a, true
		}
	}
	return ServiceAction{}, false
}

func twoServiceStack() Stack {
	return Stack{
		Name: "shop",
		Services: []Service{
			{Name: "api", Image: "node"},
			{Name: "web", Image: "nginx", DependsOn: []string{"api"}},
		},
	}
}

func TestPlanCreateWhenAbsent(t *testing.T) {
	p := PlanReconcile(twoServiceStack(), nil)
	for _, svc := range []string{"api", "web"} {
		a, ok := actionFor(p, svc)
		if !ok || a.Action != ActionCreate {
			t.Errorf("%s: want create, got %+v", svc, a)
		}
	}
}

func TestPlanStartWhenStopped(t *testing.T) {
	observed := []engine.Container{
		memberContainer("shop", "api", engine.StateStopped, "node"),
		memberContainer("shop", "web", engine.StateStopped, "nginx"),
	}
	p := PlanReconcile(twoServiceStack(), observed)
	for _, svc := range []string{"api", "web"} {
		a, _ := actionFor(p, svc)
		if a.Action != ActionStart || a.ContainerID != "shop-"+svc {
			t.Errorf("%s: want start, got %+v", svc, a)
		}
	}
}

func TestPlanNoopWhenMatches(t *testing.T) {
	observed := []engine.Container{
		memberContainer("shop", "api", engine.StateRunning, "node"),
		memberContainer("shop", "web", engine.StateRunning, "nginx"),
	}
	p := PlanReconcile(twoServiceStack(), observed)
	for _, svc := range []string{"api", "web"} {
		a, _ := actionFor(p, svc)
		if a.Action != ActionNoop {
			t.Errorf("%s: want noop, got %+v", svc, a)
		}
	}
}

func TestPlanRecreateDetectedOnImageChange(t *testing.T) {
	observed := []engine.Container{
		memberContainer("shop", "api", engine.StateRunning, "node"),
		memberContainer("shop", "web", engine.StateRunning, "nginx:OLD"),
	}
	p := PlanReconcile(twoServiceStack(), observed)
	web, _ := actionFor(p, "web")
	if web.Action != ActionRecreate {
		t.Fatalf("web: want recreate, got %+v", web)
	}
	if !web.Action.Destructive() {
		t.Error("recreate must be classified destructive")
	}
	if len(web.Diff) == 0 {
		t.Error("recreate should carry a diff")
	}
	// CRITICAL: recreate must NOT appear in the safe (applied) set.
	for _, a := range p.Safe() {
		if a.Action == ActionRecreate {
			t.Error("recreate leaked into Safe() — would be applied in v1")
		}
	}
}

func TestPlanNoopWithImplicitLatestTag(t *testing.T) {
	// Declared "alpine"; runtime stores "alpine:latest" — must be noop, not recreate.
	stack := Stack{Name: "shop", Services: []Service{
		{Name: "web", Image: "docker.io/library/alpine"},
	}}
	c := memberContainer("shop", "web", engine.StateRunning, "docker.io/library/alpine:latest")
	p := PlanReconcile(stack, []engine.Container{c})
	web, _ := actionFor(p, "web")
	if web.Action != ActionNoop {
		t.Errorf("implicit :latest should be noop, got %s (diff=%v)", web.Action, web.Diff)
	}
}

func TestNormalizeImage(t *testing.T) {
	cases := map[string]string{
		"alpine":                       "alpine:latest",
		"docker.io/library/alpine":     "docker.io/library/alpine:latest",
		"docker.io/library/alpine:1.2": "docker.io/library/alpine:1.2",
		"registry:5000/img":            "registry:5000/img:latest", // host:port, no tag
		"registry:5000/img:v2":         "registry:5000/img:v2",
		"img@sha256:abc":               "img@sha256:abc", // digest left alone
	}
	for in, want := range cases {
		if got := normalizeImage(in); got != want {
			t.Errorf("normalizeImage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlanRecreateOnPortChange(t *testing.T) {
	stack := Stack{Name: "shop", Services: []Service{
		{Name: "web", Image: "nginx", Ports: []PortMapping{{HostPort: 8080, ContainerPort: 80, Proto: "tcp"}}},
	}}
	c := memberContainer("shop", "web", engine.StateRunning, "nginx")
	c.Configuration.PublishedPorts = []engine.PublishedPort{{HostPort: 9090, ContainerPort: 80, Proto: "tcp"}}
	p := PlanReconcile(stack, []engine.Container{c})
	web, _ := actionFor(p, "web")
	if web.Action != ActionRecreate {
		t.Errorf("port change should recreate, got %+v", web)
	}
}

func TestPlanRecreateOnEnvChange(t *testing.T) {
	stack := Stack{Name: "shop", Services: []Service{
		{Name: "web", Image: "nginx", Environment: map[string]string{"LEVEL": "debug"}},
	}}
	c := memberContainer("shop", "web", engine.StateRunning, "nginx")
	c.Configuration.InitProcess.Environment = []string{"LEVEL=info"}
	p := PlanReconcile(stack, []engine.Container{c})
	web, _ := actionFor(p, "web")
	if web.Action != ActionRecreate {
		t.Errorf("env change should recreate, got %+v", web)
	}
}

func TestPlanNoopIgnoresExtraContainerEnv(t *testing.T) {
	stack := Stack{Name: "shop", Services: []Service{
		{Name: "web", Image: "nginx", Environment: map[string]string{"LEVEL": "info"}},
	}}
	c := memberContainer("shop", "web", engine.StateRunning, "nginx")
	// Container has the declared var PLUS runtime-injected defaults — not drift.
	c.Configuration.InitProcess.Environment = []string{"LEVEL=info", "PATH=/usr/bin", "HOME=/root"}
	p := PlanReconcile(stack, []engine.Container{c})
	web, _ := actionFor(p, "web")
	if web.Action != ActionNoop {
		t.Errorf("extra container env should not be drift, got %+v", web)
	}
}

func TestPlanOrphanDetected(t *testing.T) {
	// "cache" runs and is labelled for the stack but is no longer declared.
	observed := []engine.Container{
		memberContainer("shop", "api", engine.StateRunning, "node"),
		memberContainer("shop", "web", engine.StateRunning, "nginx"),
		memberContainer("shop", "cache", engine.StateRunning, "redis"),
	}
	p := PlanReconcile(twoServiceStack(), observed)
	orphan, ok := actionFor(p, "cache")
	if !ok || orphan.Action != ActionOrphan {
		t.Fatalf("cache: want orphan, got %+v", orphan)
	}
	if !orphan.Action.Destructive() {
		t.Error("orphan must be classified destructive")
	}
	for _, a := range p.Safe() {
		if a.Action == ActionOrphan {
			t.Error("orphan leaked into Safe()")
		}
	}
}

func TestPlanIgnoresForeignContainers(t *testing.T) {
	// A container from another stack and a standalone one must be invisible.
	other := memberContainer("warehouse", "api", engine.StateRunning, "node")
	standalone := engine.Container{ID: "loose"}
	standalone.Status.State = engine.StateRunning
	p := PlanReconcile(twoServiceStack(), []engine.Container{other, standalone})
	for _, svc := range []string{"api", "web"} {
		a, _ := actionFor(p, svc)
		if a.Action != ActionCreate {
			t.Errorf("%s should be create (foreign ignored), got %+v", svc, a)
		}
	}
	if len(p.Actions) != 2 {
		t.Errorf("foreign containers should not add actions: %+v", p.Actions)
	}
}

func TestPlanMixedDrift(t *testing.T) {
	// api: running+match (noop); web: stopped (start); plus an orphan.
	stack := Stack{Name: "shop", Services: []Service{
		{Name: "api", Image: "node"},
		{Name: "web", Image: "nginx"},
	}}
	observed := []engine.Container{
		memberContainer("shop", "api", engine.StateRunning, "node"),
		memberContainer("shop", "web", engine.StateStopped, "nginx"),
		memberContainer("shop", "old", engine.StateRunning, "x"),
	}
	p := PlanReconcile(stack, observed)
	want := map[string]ActionKind{"api": ActionNoop, "web": ActionStart, "old": ActionOrphan}
	for svc, kind := range want {
		a, _ := actionFor(p, svc)
		if a.Action != kind {
			t.Errorf("%s: want %s, got %s", svc, kind, a.Action)
		}
	}
	// Only web (start) is safe to apply; api noop is also non-destructive.
	safeKinds := map[ActionKind]int{}
	for _, a := range p.Safe() {
		safeKinds[a.Action]++
	}
	if safeKinds[ActionRecreate] != 0 || safeKinds[ActionOrphan] != 0 {
		t.Errorf("destructive leaked into Safe(): %+v", p.Safe())
	}
}

func TestStatusDerivation(t *testing.T) {
	stack := twoServiceStack()
	up := []engine.Container{
		memberContainer("shop", "api", engine.StateRunning, "node"),
		memberContainer("shop", "web", engine.StateRunning, "nginx"),
	}
	if s := Status(stack, up); s != "up" {
		t.Errorf("want up, got %q", s)
	}
	degraded := []engine.Container{
		memberContainer("shop", "api", engine.StateRunning, "node"),
		memberContainer("shop", "web", engine.StateStopped, "nginx"),
	}
	if s := Status(stack, degraded); s != "degraded" {
		t.Errorf("want degraded, got %q", s)
	}
	down := []engine.Container{
		memberContainer("shop", "api", engine.StateStopped, "node"),
	}
	if s := Status(stack, down); s != "down" {
		t.Errorf("want down, got %q", s)
	}
	if s := Status(stack, nil); s != "down" {
		t.Errorf("no members: want down, got %q", s)
	}
}

func TestOrderRespectsDependsOn(t *testing.T) {
	// web depends_on api → api must come first.
	got := Order(twoServiceStack())
	want := []string{"api", "web"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestOrderChain(t *testing.T) {
	stack := Stack{Name: "s", Services: []Service{
		{Name: "c", DependsOn: []string{"b"}},
		{Name: "b", DependsOn: []string{"a"}},
		{Name: "a"},
	}}
	got := Order(stack)
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}
