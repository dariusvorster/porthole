// Package reconcile turns the pull-only `container` CLI into a push stream.
//
// `container` has no event API, so the Hub polls `ls --all` (plus df and stats)
// on a cadence, diffs each poll against the last snapshot keyed by container ID,
// and broadcasts only the deltas to subscribers (the SSE handler). This is the
// load-bearing loop from spec §8 gap 1.
//
// Two cadences: fast while at least one view is subscribed, slow when idle.
// Stats are special — cpuUsageUsec is cumulative, so a CPU% needs two samples;
// the Hub retains the previous Stats per container to compute the rate.
package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/porthole/porthole/engine"
)

// Event is one server-sent event: a name and a pre-marshalled JSON payload.
type Event struct {
	Name string
	Data []byte
}

func newEvent(name string, payload any) Event {
	b, _ := json.Marshal(payload)
	return Event{Name: name, Data: b}
}

// Event payloads. Names: snapshot, container.upserted, container.removed, stats,
// df, system.
type SnapshotEvent struct {
	Containers       []engine.Container `json:"containers"`
	DiskUsage        engine.DiskUsage   `json:"diskUsage"`
	APIServerRunning bool               `json:"apiServerRunning"`
}

type ContainerEvent struct {
	Container engine.Container `json:"container"`
}

type RemovedEvent struct {
	ID string `json:"id"`
}

type SystemEvent struct {
	APIServerRunning bool   `json:"apiServerRunning"`
	Detail           string `json:"detail,omitempty"`
}

type StatsSample struct {
	ID               string  `json:"id"`
	CPUPercent       float64 `json:"cpuPercent"`
	MemoryPercent    float64 `json:"memoryPercent"`
	MemoryUsageBytes int64   `json:"memoryUsageBytes"`
	NumProcesses     int     `json:"numProcesses"`
}

type StatsEvent struct {
	Samples []StatsSample `json:"samples"`
}

// Hub fans engine deltas out to subscribers.
type Hub struct {
	eng        engine.Engine
	fast, slow time.Duration
	nowFn      func() time.Time

	mu          sync.Mutex
	subs        map[int]chan Event
	nextID      int
	snapshot    map[string]engine.Container
	fingerprint map[string]string
	df          engine.DiskUsage
	degraded    bool
	prevStats   map[string]engine.Stats
	lastStatsAt time.Time

	onCycle   func([]engine.Container) // supervision hook; set before Run
	onRemoved func(string)            // supervision prune hook; set before Run
}

// NewHub builds a Hub. fast/slow default to 2s/30s when zero.
func NewHub(eng engine.Engine, fast, slow time.Duration) *Hub {
	if fast <= 0 {
		fast = 2 * time.Second
	}
	if slow <= 0 {
		slow = 30 * time.Second
	}
	return &Hub{
		eng:         eng,
		fast:        fast,
		slow:        slow,
		nowFn:       time.Now,
		subs:        map[int]chan Event{},
		snapshot:    map[string]engine.Container{},
		fingerprint: map[string]string{},
	}
}

// SetOnCycle registers a callback invoked after each successful poll with the
// current container list. It runs outside the Hub lock (it may issue engine
// restarts and call Emit). Set it before Run; not safe to change concurrently.
func (h *Hub) SetOnCycle(fn func([]engine.Container)) { h.onCycle = fn }

// SetOnRemoved registers a callback invoked (outside the lock) for each container
// id that disappeared this cycle — the supervisor uses it to prune policy rows.
func (h *Hub) SetOnRemoved(fn func(string)) { h.onRemoved = fn }

// WatchStopped returns a channel that is CLOSED when the container is observed to
// have left the running set (stopped or removed). The logs endpoint relies on
// this because `container logs -f` does not exit when the container stops (spec
// §7.4) — passive wait-for-exit would hang. The watcher unsubscribes when ctx is
// cancelled. If the container is already not-running, the channel closes ~at once.
func (h *Hub) WatchStopped(ctx context.Context, id string) <-chan struct{} {
	stopped := make(chan struct{})
	subID, ch := h.Subscribe()
	go func() {
		defer h.Unsubscribe(subID)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return // hub dropped this subscriber; ctx/endpoint handle teardown
				}
				if containerStopped(ev, id) {
					close(stopped)
					return
				}
			}
		}
	}()
	return stopped
}

// containerStopped reports whether event ev indicates id is no longer running.
func containerStopped(ev Event, id string) bool {
	switch ev.Name {
	case "snapshot":
		var s SnapshotEvent
		if json.Unmarshal(ev.Data, &s) != nil {
			return false
		}
		for _, c := range s.Containers {
			if c.ID == id {
				return !c.IsRunning()
			}
		}
		return true // absent from the snapshot → gone
	case "container.upserted":
		var e ContainerEvent
		if json.Unmarshal(ev.Data, &e) != nil {
			return false
		}
		return e.Container.ID == id && !e.Container.IsRunning()
	case "container.removed":
		var e RemovedEvent
		if json.Unmarshal(ev.Data, &e) != nil {
			return false
		}
		return e.ID == id
	}
	return false
}

// Emit broadcasts a custom named event to all subscribers — used by the
// supervisor for `supervision` events. Safe to call from the onCycle callback.
func (h *Hub) Emit(name string, payload any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.broadcastLocked(newEvent(name, payload))
}

// Run drives the poll loop until ctx is cancelled. Cadence is fast while there
// is at least one subscriber, slow when idle (spec §8: 2s focused / backoff idle).
func (h *Hub) Run(ctx context.Context) {
	for {
		h.pollOnce(ctx)
		interval := h.slow
		if h.subCount() > 0 {
			interval = h.fast
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// Subscribe registers a new client and immediately seeds it with a full
// snapshot, so a freshly-connected view starts consistent before deltas arrive.
func (h *Hub) Subscribe() (int, <-chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.nextID
	h.nextID++
	ch := make(chan Event, 64)
	ch <- h.snapshotEventLocked() // buffered + fresh: never blocks
	h.subs[id] = ch
	return id, ch
}

// Unsubscribe removes a client. Idempotent and safe if the Hub already dropped
// the subscriber as a slow consumer.
func (h *Hub) Unsubscribe(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.subs[id]; ok {
		close(ch)
		delete(h.subs, id)
	}
}

func (h *Hub) subCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *Hub) snapshotEventLocked() Event {
	cs := make([]engine.Container, 0, len(h.snapshot))
	for _, c := range h.snapshot {
		cs = append(cs, c)
	}
	return newEvent("snapshot", SnapshotEvent{
		Containers:       cs,
		DiskUsage:        h.df,
		APIServerRunning: !h.degraded,
	})
}

// broadcastLocked sends to every subscriber without blocking. A subscriber whose
// buffer is full is treated as a dead/slow consumer: its channel is closed and
// dropped, which ends its SSE response and lets the browser's EventSource
// auto-reconnect and pull a fresh snapshot. Must be called with h.mu held.
func (h *Hub) broadcastLocked(e Event) {
	for id, ch := range h.subs {
		select {
		case ch <- e:
		default:
			close(ch)
			delete(h.subs, id)
		}
	}
}

// pollOnce performs one reconcile cycle: engine reads happen without the lock;
// diffing, state mutation, and broadcasting happen under it (broadcasts are
// non-blocking, so holding the lock is cheap).
func (h *Hub) pollOnce(ctx context.Context) {
	cs, err := h.eng.ListContainers(ctx, true)

	var (
		df     engine.DiskUsage
		haveDF bool
		ss     []engine.Stats
	)
	if err == nil {
		if d, e := h.eng.DiskUsage(ctx); e == nil {
			df, haveDF = d, true
		}
		if ids := runningIDs(cs); len(ids) > 0 {
			if x, e := h.eng.Stats(ctx, ids...); e == nil {
				ss = x
			}
		}
	}

	h.mu.Lock()

	if err != nil {
		if isDaemonDown(err) && !h.degraded {
			h.degraded = true
			h.broadcastLocked(newEvent("system", SystemEvent{APIServerRunning: false, Detail: detail(err)}))
		}
		// Transient (non-daemon-down) errors are swallowed: keep the last good
		// snapshot rather than nuking the UI on a single flaky poll.
		h.mu.Unlock()
		return
	}

	// Recovery: announce the runtime is back, then force every container to
	// re-emit as an upsert by clearing fingerprints.
	if h.degraded {
		h.degraded = false
		h.broadcastLocked(newEvent("system", SystemEvent{APIServerRunning: true}))
		h.fingerprint = map[string]string{}
	}

	cur := make(map[string]engine.Container, len(cs))
	newFP := make(map[string]string, len(cs))
	for _, c := range cs {
		cur[c.ID] = c
		fp := fingerprint(c)
		newFP[c.ID] = fp
		if h.fingerprint[c.ID] != fp {
			h.broadcastLocked(newEvent("container.upserted", ContainerEvent{Container: c}))
		}
	}
	var removed []string
	for id := range h.fingerprint {
		if _, ok := cur[id]; !ok {
			removed = append(removed, id)
			h.broadcastLocked(newEvent("container.removed", RemovedEvent{ID: id}))
		}
	}
	h.snapshot = cur
	h.fingerprint = newFP

	if haveDF {
		h.df = df
		h.broadcastLocked(newEvent("df", df))
	}

	h.diffStatsLocked(cur, ss)

	h.mu.Unlock()

	// Supervision runs AFTER the lock is released: it issues engine restarts under
	// the per-id lock and may call back into Emit — neither may happen under h.mu.
	if h.onRemoved != nil {
		for _, id := range removed {
			h.onRemoved(id)
		}
	}
	if h.onCycle != nil {
		h.onCycle(cs)
	}
}

// diffStatsLocked computes CPU% (needs the previous sample) and memory% for each
// running container and broadcasts a stats event. Must hold h.mu.
func (h *Hub) diffStatsLocked(cur map[string]engine.Container, ss []engine.Stats) {
	if len(ss) == 0 {
		h.prevStats = nil
		h.lastStatsAt = time.Time{}
		return
	}
	now := h.nowFn()
	wall := now.Sub(h.lastStatsAt)
	samples := make([]StatsSample, 0, len(ss))
	for _, s := range ss {
		var cpu float64
		if prev, ok := h.prevStats[s.ID]; ok && !h.lastStatsAt.IsZero() {
			cpu = engine.CPUPercent(prev, s, wall, cur[s.ID].Configuration.Resources.CPUs)
		}
		samples = append(samples, StatsSample{
			ID:               s.ID,
			CPUPercent:       cpu,
			MemoryPercent:    s.MemoryPercent(),
			MemoryUsageBytes: s.MemoryUsageBytes,
			NumProcesses:     s.NumProcesses,
		})
	}
	np := make(map[string]engine.Stats, len(ss))
	for _, s := range ss {
		np[s.ID] = s
	}
	h.prevStats = np
	h.lastStatsAt = now
	h.broadcastLocked(newEvent("stats", StatsEvent{Samples: samples}))
}

// --- helpers -------------------------------------------------------------

func runningIDs(cs []engine.Container) []string {
	var ids []string
	for _, c := range cs {
		if c.IsRunning() {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

func fingerprint(c engine.Container) string {
	b, _ := json.Marshal(c)
	return string(b)
}

func isDaemonDown(err error) bool {
	var ce *engine.CLIError
	return errors.As(err, &ce) && ce.Kind == engine.ErrDaemonDown
}

func detail(err error) string {
	var ce *engine.CLIError
	if errors.As(err, &ce) {
		return ce.Message
	}
	return err.Error()
}
