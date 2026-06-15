package stacks

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/idlock"
)

// recEngine is a recording fake of the stacks Engine. It mutates an in-memory
// container list so idempotency/lock behaviour can be exercised.
type recEngine struct {
	mu         sync.Mutex
	containers []engine.Container
	networks   []engine.Network
	runDelay   time.Duration

	runCalls    []engine.RunSpec
	started     []string
	stopped     []string
	deleted     []string
	createdNets []string
	removedNets []string
}

func (e *recEngine) ListContainers(context.Context, bool) ([]engine.Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]engine.Container(nil), e.containers...), nil
}

func (e *recEngine) ListNetworks(context.Context) ([]engine.Network, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]engine.Network(nil), e.networks...), nil
}

func (e *recEngine) CreateNetwork(_ context.Context, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.createdNets = append(e.createdNets, name)
	n := engine.Network{ID: name}
	n.Configuration.Name = name
	e.networks = append(e.networks, n)
	return nil
}

func (e *recEngine) RemoveNetwork(_ context.Context, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.removedNets = append(e.removedNets, name)
	out := e.networks[:0]
	for _, n := range e.networks {
		if n.Configuration.Name != name {
			out = append(out, n)
		}
	}
	e.networks = out
	return nil
}

func (e *recEngine) RunContainer(_ context.Context, spec engine.RunSpec) (string, error) {
	if e.runDelay > 0 {
		time.Sleep(e.runDelay)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runCalls = append(e.runCalls, spec)
	c := engine.Container{ID: spec.Name}
	c.Configuration.Labels = spec.Labels
	c.Configuration.Image.Reference = spec.Image
	c.Status.State = engine.StateRunning
	c.Status.Networks = []engine.NetworkStatus{{Network: spec.Network, IPv4Address: "192.168.65.9/24"}}
	e.containers = append(e.containers, c)
	return spec.Name, nil
}

func (e *recEngine) StartContainer(_ context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.started = append(e.started, id)
	for i := range e.containers {
		if e.containers[i].ID == id {
			e.containers[i].Status.State = engine.StateRunning
		}
	}
	return nil
}

func (e *recEngine) StopContainer(_ context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopped = append(e.stopped, id)
	for i := range e.containers {
		if e.containers[i].ID == id {
			e.containers[i].Status.State = engine.StateStopped
		}
	}
	return nil
}

func (e *recEngine) DeleteContainer(_ context.Context, id string, _ bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.deleted = append(e.deleted, id)
	out := e.containers[:0]
	for _, c := range e.containers {
		if c.ID != id {
			out = append(out, c)
		}
	}
	e.containers = out
	return nil
}

var _ Engine = (*recEngine)(nil)

func upStack() Stack {
	return Stack{
		Name: "shop",
		Services: []Service{
			{Name: "api", Image: "node", Restart: RestartAlways, Environment: map[string]string{"PORT": "3000"}},
			{Name: "web", Image: "nginx", DependsOn: []string{"api"},
				Ports: []PortMapping{{HostPort: 8080, ContainerPort: 80, Proto: "tcp"}}},
		},
	}
}

func TestUpCreatesMembersInOrder(t *testing.T) {
	eng := &recEngine{}
	ex := NewExecutor(eng, idlock.New())
	res, err := ex.Up(context.Background(), upStack())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "up" {
		t.Errorf("status = %q, want up", res.Status)
	}
	if len(eng.runCalls) != 2 {
		t.Fatalf("want 2 creates, got %d", len(eng.runCalls))
	}
	// Dependency order: api before web.
	if eng.runCalls[0].Name != "shop-api" || eng.runCalls[1].Name != "shop-web" {
		t.Errorf("create order = %s, %s", eng.runCalls[0].Name, eng.runCalls[1].Name)
	}
	// Network created first, members attached to it.
	if len(eng.createdNets) != 1 || eng.createdNets[0] != "shop" {
		t.Errorf("created nets = %v", eng.createdNets)
	}
	for _, rc := range eng.runCalls {
		if rc.Network != "shop" {
			t.Errorf("%s network = %q", rc.Name, rc.Network)
		}
		if rc.Labels[LabelStack] != "shop" {
			t.Errorf("%s missing stack label: %v", rc.Name, rc.Labels)
		}
		if rc.Labels[LabelService] == "" {
			t.Errorf("%s missing service label", rc.Name)
		}
	}
	// restart label mirrored to supervision for api (always), absent for web.
	api := eng.runCalls[0]
	if api.Labels[LabelRestart] != RestartAlways {
		t.Errorf("api restart label = %q", api.Labels[LabelRestart])
	}
	web := eng.runCalls[1]
	if _, has := web.Labels[LabelRestart]; has {
		t.Errorf("web should have no restart label: %v", web.Labels)
	}
	if api.Env["PORT"] != "3000" {
		t.Errorf("api env not passed: %v", api.Env)
	}
	if len(web.Ports) != 1 || web.Ports[0].HostPort != 8080 {
		t.Errorf("web ports = %+v", web.Ports)
	}
}

func TestUpStartsStoppedMember(t *testing.T) {
	eng := &recEngine{}
	// api already exists but is stopped; web absent.
	stopped := memberContainer("shop", "api", engine.StateStopped, "node")
	eng.containers = []engine.Container{stopped}
	ex := NewExecutor(eng, idlock.New())
	res, err := ex.Up(context.Background(), upStack())
	if err != nil {
		t.Fatal(err)
	}
	if len(eng.started) != 1 || eng.started[0] != "shop-api" {
		t.Errorf("started = %v, want [shop-api]", eng.started)
	}
	if len(eng.runCalls) != 1 || eng.runCalls[0].Name != "shop-web" {
		t.Errorf("runCalls = %v, want one create of shop-web", eng.runCalls)
	}
	if res.Status != "up" {
		t.Errorf("status = %q", res.Status)
	}
}

func TestUpIsIdempotent(t *testing.T) {
	eng := &recEngine{}
	ex := NewExecutor(eng, idlock.New())
	if _, err := ex.Up(context.Background(), upStack()); err != nil {
		t.Fatal(err)
	}
	first := len(eng.runCalls)
	// Second up: everything matches → all noop, no new creates/starts.
	res, err := ex.Up(context.Background(), upStack())
	if err != nil {
		t.Fatal(err)
	}
	if len(eng.runCalls) != first {
		t.Errorf("idempotent up created more: %d -> %d", first, len(eng.runCalls))
	}
	if len(res.Applied) != 0 {
		t.Errorf("second up applied %v, want none", res.Applied)
	}
}

func TestUpDoesNotApplyRecreateOrOrphan(t *testing.T) {
	eng := &recEngine{}
	// web is running but drifted (old image); "cache" is a running orphan.
	eng.containers = []engine.Container{
		memberContainer("shop", "api", engine.StateRunning, "node"),
		memberContainer("shop", "web", engine.StateRunning, "nginx:OLD"),
		memberContainer("shop", "cache", engine.StateRunning, "redis"),
	}
	ex := NewExecutor(eng, idlock.New())
	res, err := ex.Up(context.Background(), upStack())
	if err != nil {
		t.Fatal(err)
	}
	// NOTHING destructive applied: no deletes, no recreate-driven runs.
	if len(eng.deleted) != 0 {
		t.Errorf("destructive delete ran: %v", eng.deleted)
	}
	if len(eng.runCalls) != 0 {
		t.Errorf("recreate must not run a new container: %v", eng.runCalls)
	}
	// But the plan SHOWS the drift.
	web, _ := actionFor(res.Plan, "web")
	cache, _ := actionFor(res.Plan, "cache")
	if web.Action != ActionRecreate {
		t.Errorf("web plan = %s, want recreate (detected)", web.Action)
	}
	if cache.Action != ActionOrphan {
		t.Errorf("cache plan = %s, want orphan (detected)", cache.Action)
	}
}

func TestDownRemovesMembersKeepsVolumesRemovesNetwork(t *testing.T) {
	eng := &recEngine{}
	ex := NewExecutor(eng, idlock.New())
	stack := upStack()
	if _, err := ex.Up(context.Background(), stack); err != nil {
		t.Fatal(err)
	}
	res, err := ex.Down(context.Background(), stack)
	if err != nil {
		t.Fatal(err)
	}
	if len(eng.deleted) != 2 {
		t.Fatalf("want 2 deletes, got %v", eng.deleted)
	}
	// Reverse dependency order: web removed before api.
	if eng.deleted[0] != "shop-web" || eng.deleted[1] != "shop-api" {
		t.Errorf("teardown order = %v, want [shop-web shop-api]", eng.deleted)
	}
	if len(res.Removed) != 2 {
		t.Errorf("removed = %v", res.Removed)
	}
	// Network removed; volumes never touched (there is no volume-delete path).
	if len(eng.removedNets) != 1 || eng.removedNets[0] != "shop" {
		t.Errorf("removed nets = %v", eng.removedNets)
	}
}

func TestDownRemovesOrphanMembers(t *testing.T) {
	eng := &recEngine{}
	eng.containers = []engine.Container{
		memberContainer("shop", "api", engine.StateRunning, "node"),
		memberContainer("shop", "ghost", engine.StateRunning, "x"),
	}
	ex := NewExecutor(eng, idlock.New())
	if _, err := ex.Down(context.Background(), upStack()); err != nil {
		t.Fatal(err)
	}
	deleted := map[string]bool{}
	for _, d := range eng.deleted {
		deleted[d] = true
	}
	if !deleted["shop-api"] || !deleted["shop-ghost"] {
		t.Errorf("down should remove all labelled members incl orphan: %v", eng.deleted)
	}
}

func TestPerStackLockSerializesConcurrentUps(t *testing.T) {
	eng := &recEngine{runDelay: 40 * time.Millisecond}
	ex := NewExecutor(eng, idlock.New())
	stack := upStack()

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = ex.Up(context.Background(), stack)
		}()
	}
	wg.Wait()

	// With the per-stack lock, the second Up sees the first's created members
	// and noops — exactly one create per service, no double-create.
	if len(eng.runCalls) != 2 {
		t.Errorf("concurrent ups created %d containers, want 2 (lock failed)", len(eng.runCalls))
	}
}

func TestRestartStopsThenStarts(t *testing.T) {
	eng := &recEngine{}
	ex := NewExecutor(eng, idlock.New())
	stack := upStack()
	if _, err := ex.Up(context.Background(), stack); err != nil {
		t.Fatal(err)
	}
	res, err := ex.Restart(context.Background(), stack)
	if err != nil {
		t.Fatal(err)
	}
	if len(eng.stopped) != 2 || len(eng.started) != 2 {
		t.Fatalf("restart: stopped=%v started=%v", eng.stopped, eng.started)
	}
	// Stop reverse (web, api); start forward (api, web).
	if eng.stopped[0] != "shop-web" || eng.stopped[1] != "shop-api" {
		t.Errorf("stop order = %v", eng.stopped)
	}
	if eng.started[0] != "shop-api" || eng.started[1] != "shop-web" {
		t.Errorf("start order = %v", eng.started)
	}
	if res.Status != "up" {
		t.Errorf("status = %q", res.Status)
	}
}

func TestEnsureNetworkSkipsExisting(t *testing.T) {
	eng := &recEngine{}
	existing := engine.Network{ID: "shop"}
	existing.Configuration.Name = "shop"
	eng.networks = []engine.Network{existing}
	ex := NewExecutor(eng, idlock.New())
	if _, err := ex.Up(context.Background(), upStack()); err != nil {
		t.Fatal(err)
	}
	if len(eng.createdNets) != 0 {
		t.Errorf("network already existed; should not re-create: %v", eng.createdNets)
	}
}
