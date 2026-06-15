package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/porthole/porthole/engine"
)

// fakeEngine is a programmable Engine whose return values can be swapped between
// pollOnce calls to simulate state changes.
type fakeEngine struct {
	containers []engine.Container
	stats      []engine.Stats
	listErr    error
}

func (f *fakeEngine) SystemVersion(context.Context) ([]engine.VersionEntry, error) {
	return nil, nil
}
func (f *fakeEngine) SystemStatus(context.Context) (engine.SystemStatus, error) {
	return engine.SystemStatus{APIServerRunning: true}, nil
}
func (f *fakeEngine) DiskUsage(context.Context) (engine.DiskUsage, error) {
	return engine.DiskUsage{}, nil
}
func (f *fakeEngine) ListContainers(context.Context, bool) ([]engine.Container, error) {
	return f.containers, f.listErr
}
func (f *fakeEngine) InspectContainer(context.Context, string) (engine.Container, error) {
	return engine.Container{}, nil
}
func (f *fakeEngine) Stats(context.Context, ...string) ([]engine.Stats, error) {
	return f.stats, nil
}
func (f *fakeEngine) StartContainer(context.Context, string) error        { return nil }
func (f *fakeEngine) StopContainer(context.Context, string) error         { return nil }
func (f *fakeEngine) KillContainer(context.Context, string, string) error { return nil }
func (f *fakeEngine) DeleteContainer(context.Context, string, bool) error { return nil }
func (f *fakeEngine) Logs(context.Context, string, engine.LogOpts) (io.ReadCloser, error) {
	return nil, nil
}
func (f *fakeEngine) Exec(context.Context, string, []string, engine.WinSize) (engine.ExecSession, error) {
	return nil, nil
}
func (f *fakeEngine) ListNetworks(context.Context) ([]engine.Network, error) { return nil, nil }
func (f *fakeEngine) InspectNetwork(context.Context, string) (engine.Network, error) {
	return engine.Network{}, nil
}

var _ engine.Engine = (*fakeEngine)(nil)

func running(id string, cpus int) engine.Container {
	c := engine.Container{ID: id}
	c.Configuration.ID = id
	c.Configuration.Resources.CPUs = cpus
	c.Status.State = engine.StateRunning
	return c
}

// drain reads all currently-buffered events from a subscriber without blocking.
func drain(ch <-chan Event) []Event {
	var out []Event
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

func names(evs []Event) map[string]int {
	m := map[string]int{}
	for _, e := range evs {
		m[e.Name]++
	}
	return m
}

func TestSubscribeSeedsSnapshot(t *testing.T) {
	h := NewHub(&fakeEngine{}, time.Second, time.Second)
	_, ch := h.Subscribe()
	evs := drain(ch)
	if len(evs) != 1 || evs[0].Name != "snapshot" {
		t.Fatalf("first event should be snapshot, got %+v", names(evs))
	}
}

func TestDiffEmitsUpsertThenRemove(t *testing.T) {
	f := &fakeEngine{containers: []engine.Container{running("web", 4), running("api", 4)}}
	h := NewHub(f, time.Second, time.Second)
	_, ch := h.Subscribe()
	drain(ch) // discard initial snapshot

	ctx := context.Background()
	h.pollOnce(ctx)
	got := names(drain(ch))
	if got["container.upserted"] != 2 {
		t.Fatalf("first poll should upsert 2 containers, got %+v", got)
	}

	// Second poll, no changes → no container events (fingerprint match).
	h.pollOnce(ctx)
	got = names(drain(ch))
	if got["container.upserted"] != 0 || got["container.removed"] != 0 {
		t.Errorf("unchanged poll should emit no container deltas, got %+v", got)
	}

	// Remove api, change web's state → one removed + one upsert.
	web := running("web", 4)
	web.Status.State = "stopped"
	f.containers = []engine.Container{web}
	h.pollOnce(ctx)
	got = names(drain(ch))
	if got["container.removed"] != 1 {
		t.Errorf("want 1 removal (api), got %+v", got)
	}
	if got["container.upserted"] != 1 {
		t.Errorf("want 1 upsert (web changed state), got %+v", got)
	}
}

func TestStatsCPUAcrossTwoSamples(t *testing.T) {
	f := &fakeEngine{
		containers: []engine.Container{running("web", 4)},
		stats:      []engine.Stats{{ID: "web", CPUUsageUsec: 1_000_000, MemoryLimitBytes: 100, MemoryUsageBytes: 25}},
	}
	h := NewHub(f, time.Second, time.Second)

	// Deterministic clock: each nowFn() call advances exactly 1s, so the wall
	// time between the two stats samples is 1s.
	var tick int64
	h.nowFn = func() time.Time { tick++; return time.Unix(tick, 0) }

	_, ch := h.Subscribe()
	drain(ch)
	ctx := context.Background()

	// First poll: no previous sample → CPU 0, but memory% is immediate.
	h.pollOnce(ctx)
	s1 := lastStats(t, drain(ch))
	if s1.CPUPercent != 0 {
		t.Errorf("first sample CPU should be 0 (no prior), got %.2f", s1.CPUPercent)
	}
	if s1.MemoryPercent != 25 {
		t.Errorf("memory%% = %.2f, want 25", s1.MemoryPercent)
	}

	// Second poll: +2,000,000us over 1s wall across 4 cores = 50%.
	f.stats = []engine.Stats{{ID: "web", CPUUsageUsec: 3_000_000, MemoryLimitBytes: 100, MemoryUsageBytes: 25}}
	h.pollOnce(ctx)
	s2 := lastStats(t, drain(ch))
	if s2.CPUPercent < 49 || s2.CPUPercent > 51 {
		t.Errorf("CPU%% = %.2f, want ~50", s2.CPUPercent)
	}
}

func TestDaemonDownEmitsSystemEvent(t *testing.T) {
	f := &fakeEngine{listErr: &engine.CLIError{Kind: engine.ErrDaemonDown, Message: "down"}}
	h := NewHub(f, time.Second, time.Second)
	_, ch := h.Subscribe()
	drain(ch)

	h.pollOnce(context.Background())
	evs := drain(ch)
	if len(evs) != 1 || evs[0].Name != "system" {
		t.Fatalf("want a single system event, got %+v", names(evs))
	}
	var se SystemEvent
	if err := json.Unmarshal(evs[0].Data, &se); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if se.APIServerRunning {
		t.Error("system event should report apiServerRunning=false")
	}

	// A second down poll should NOT re-emit (degraded edge-triggered).
	h.pollOnce(context.Background())
	if evs := drain(ch); len(evs) != 0 {
		t.Errorf("daemon-down should be edge-triggered, got %+v", names(evs))
	}
}

func lastStats(t *testing.T, evs []Event) StatsSample {
	t.Helper()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Name == "stats" {
			var se StatsEvent
			if err := json.Unmarshal(evs[i].Data, &se); err != nil {
				t.Fatalf("decode stats: %v", err)
			}
			if len(se.Samples) == 0 {
				t.Fatal("stats event had no samples")
			}
			return se.Samples[0]
		}
	}
	t.Fatal("no stats event found")
	return StatsSample{}
}
