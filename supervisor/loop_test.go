package supervisor

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/idlock"
)

// supFakeEngine records StartContainer calls; other methods are inert.
type supFakeEngine struct {
	mu      sync.Mutex
	started []string
}

func (f *supFakeEngine) StartContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, id)
	return nil
}
func (f *supFakeEngine) startedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.started...)
}
func (f *supFakeEngine) SystemVersion(context.Context) ([]engine.VersionEntry, error) { return nil, nil }
func (f *supFakeEngine) SystemStatus(context.Context) (engine.SystemStatus, error) {
	return engine.SystemStatus{}, nil
}
func (f *supFakeEngine) DiskUsage(context.Context) (engine.DiskUsage, error) {
	return engine.DiskUsage{}, nil
}
func (f *supFakeEngine) ListContainers(context.Context, bool) ([]engine.Container, error) {
	return nil, nil
}
func (f *supFakeEngine) InspectContainer(context.Context, string) (engine.Container, error) {
	return engine.Container{}, nil
}
func (f *supFakeEngine) Stats(context.Context, ...string) ([]engine.Stats, error) { return nil, nil }
func (f *supFakeEngine) StopContainer(context.Context, string) error              { return nil }
func (f *supFakeEngine) KillContainer(context.Context, string, string) error      { return nil }
func (f *supFakeEngine) DeleteContainer(context.Context, string, bool) error      { return nil }
func (f *supFakeEngine) Logs(context.Context, string, engine.LogOpts) (io.ReadCloser, error) {
	return nil, nil
}
func (f *supFakeEngine) Exec(context.Context, string, []string, engine.WinSize) (engine.ExecSession, error) {
	return nil, nil
}
func (f *supFakeEngine) ListNetworks(context.Context) ([]engine.Network, error) { return nil, nil }
func (f *supFakeEngine) InspectNetwork(context.Context, string) (engine.Network, error) {
	return engine.Network{}, nil
}

var _ engine.Engine = (*supFakeEngine)(nil)

// capBus captures emitted supervision events.
type capBus struct {
	mu     sync.Mutex
	events []SupervisionEvent
}

func (b *capBus) Emit(_ string, payload any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ev, ok := payload.(SupervisionEvent); ok {
		b.events = append(b.events, ev)
	}
}
func (b *capBus) last() (SupervisionEvent, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.events) == 0 {
		return SupervisionEvent{}, false
	}
	return b.events[len(b.events)-1], true
}

func ctr(id, state string, labels map[string]string) engine.Container {
	c := engine.Container{ID: id}
	c.Configuration.Labels = labels
	c.Status.State = state
	if state == "running" {
		c.Status.StartedDate = time.Now()
	}
	return c
}

func newSup(t *testing.T, store Store, bus Broadcaster, eng engine.Engine) *Supervisor {
	t.Helper()
	return New(store, eng, bus, idlock.New(), DefaultConfig(), nil)
}

func TestOnCycleRestartsCrashedAlways(t *testing.T) {
	store := NewMemStore()
	_ = store.SetPolicy(Policy{ContainerID: "web", Restart: RestartAlways})
	eng := &supFakeEngine{}
	bus := &capBus{}
	s := newSup(t, store, bus, eng)

	s.OnCycle([]engine.Container{ctr("web", "stopped", nil)})

	if got := eng.startedIDs(); len(got) != 1 || got[0] != "web" {
		t.Fatalf("started = %v, want [web]", got)
	}
	ev, ok := bus.last()
	if !ok || ev.ID != "web" || ev.RestartCount != 1 || ev.Policy != RestartAlways {
		t.Fatalf("supervision event wrong: %+v ok=%v", ev, ok)
	}
}

// TestRestartTotalPersistsAcrossStabilization is the PF3/F7 fix: the cumulative
// lifetime restart count is bumped on a supervision restart, carried on the event
// as RestartTotal, and does NOT vanish once the container stabilizes (running).
func TestRestartTotalPersistsAcrossStabilization(t *testing.T) {
	store := NewMemStore()
	_ = store.SetPolicy(Policy{ContainerID: "web", Restart: RestartAlways})
	eng := &supFakeEngine{}
	bus := &capBus{}
	s := newSup(t, store, bus, eng)

	// Crash → restart → cumulative total = 1.
	s.OnCycle([]engine.Container{ctr("web", "stopped", nil)})
	if n, _ := store.GetRestartCount("web"); n != 1 {
		t.Fatalf("persisted restart total = %d, want 1", n)
	}
	if ev, _ := bus.last(); ev.RestartTotal != 1 {
		t.Errorf("event RestartTotal = %d, want 1", ev.RestartTotal)
	}

	// Stabilize (running) → no new restart; the cumulative total must STAY 1.
	s.OnCycle([]engine.Container{ctr("web", "running", nil)})
	if ev2, _ := bus.last(); ev2.RestartTotal != 1 {
		t.Errorf("after stabilization RestartTotal = %d, want 1 (must persist)", ev2.RestartTotal)
	}
	if n, _ := store.GetRestartCount("web"); n != 1 {
		t.Errorf("persisted count changed on stabilization: %d, want 1", n)
	}
}

func TestOnCycleHonorsMediatedStop(t *testing.T) {
	store := NewMemStore()
	_ = store.SetPolicy(Policy{ContainerID: "web", Restart: RestartUnlessStopped})
	_ = store.SetDesired("web", DesiredStopped) // user stopped it through Porthole
	eng := &supFakeEngine{}
	s := newSup(t, store, &capBus{}, eng)

	s.OnCycle([]engine.Container{ctr("web", "stopped", nil)})

	if got := eng.startedIDs(); len(got) != 0 {
		t.Fatalf("started = %v, want none (mediated stop must stay stopped)", got)
	}
}

func TestOnCycleNoPolicyIgnored(t *testing.T) {
	eng := &supFakeEngine{}
	s := newSup(t, NewMemStore(), &capBus{}, eng)
	s.OnCycle([]engine.Container{ctr("web", "stopped", nil)})
	if got := eng.startedIDs(); len(got) != 0 {
		t.Fatalf("started = %v, want none (no policy)", got)
	}
}

func TestOnCycleSeedsPolicyFromLabel(t *testing.T) {
	store := NewMemStore()
	eng := &supFakeEngine{}
	s := newSup(t, store, &capBus{}, eng)

	// Label-only policy, no DB row yet.
	s.OnCycle([]engine.Container{ctr("web", "stopped", map[string]string{"porthole.restart": "always"})})

	if got := eng.startedIDs(); len(got) != 1 || got[0] != "web" {
		t.Fatalf("started = %v, want [web] (label seeds policy)", got)
	}
	if p, ok, _ := store.GetPolicy("web"); !ok || p.Restart != RestartAlways {
		t.Errorf("policy not seeded from label: ok=%v %+v", ok, p)
	}
}

func TestOnCycleRunningNotRestarted(t *testing.T) {
	store := NewMemStore()
	_ = store.SetPolicy(Policy{ContainerID: "web", Restart: RestartAlways})
	eng := &supFakeEngine{}
	s := newSup(t, store, &capBus{}, eng)
	s.OnCycle([]engine.Container{ctr("web", "running", nil)})
	if got := eng.startedIDs(); len(got) != 0 {
		t.Fatalf("started = %v, want none (already running)", got)
	}
}

func TestApplyPolicyStoresAndEmits(t *testing.T) {
	store := NewMemStore()
	bus := &capBus{}
	s := newSup(t, store, bus, &supFakeEngine{})

	if err := s.ApplyPolicy(Policy{ContainerID: "web", Restart: RestartUnlessStopped}); err != nil {
		t.Fatalf("ApplyPolicy: %v", err)
	}
	if p, ok, _ := store.GetPolicy("web"); !ok || p.Restart != RestartUnlessStopped {
		t.Errorf("policy not stored: ok=%v %+v", ok, p)
	}
	if ev, ok := bus.last(); !ok || ev.ID != "web" || ev.Policy != RestartUnlessStopped {
		t.Errorf("no/ wrong emit: %+v ok=%v", ev, ok)
	}
}

func TestOnRemovedPrunesRows(t *testing.T) {
	store := NewMemStore()
	_ = store.SetPolicy(Policy{ContainerID: "web", Restart: RestartAlways})
	_ = store.SetDesired("web", DesiredStopped)
	s := newSup(t, store, &capBus{}, &supFakeEngine{})

	s.OnRemoved("web")

	if _, ok, _ := store.GetPolicy("web"); ok {
		t.Error("policy row not pruned after OnRemoved")
	}
	if _, ok, _ := store.GetDesired("web"); ok {
		t.Error("desired_state row not pruned after OnRemoved")
	}
}

func TestRecordIntent(t *testing.T) {
	store := NewMemStore()
	s := newSup(t, store, &capBus{}, &supFakeEngine{})

	s.RecordStop("web")
	if d, _, _ := store.GetDesired("web"); d != DesiredStopped {
		t.Errorf("after RecordStop desired = %q, want stopped", d)
	}
	s.RecordStart("web")
	if d, _, _ := store.GetDesired("web"); d != DesiredRunning {
		t.Errorf("after RecordStart desired = %q, want running", d)
	}
}
