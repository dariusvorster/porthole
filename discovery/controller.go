package discovery

import (
	"context"
	"log"
	"sync"
	"time"
)

// HostsRW is the slice of the engine the controller needs. *engine.CLIEngine
// satisfies it; declaring it here keeps the discovery package free of engine/
// runtime imports (the pure-core constraint).
type HostsRW interface {
	ReadHosts(ctx context.Context, id string) (string, error)
	WriteHosts(ctx context.Context, id, content string) error
}

// Locker is the shared per-id mutation lock. *idlock.KeyedMutex satisfies it.
// Sharing it means an injection never races a supervisor/lifecycle op on the same
// container.
type Locker interface {
	Lock(key string) func()
}

// Snapshot is the controller's view of one container for a reconcile cycle. The
// caller (main) adapts the runtime's container list into these each poll, which
// keeps this package import-pure and gives the controller everything it needs:
// stack membership, the discovery opt-in, the live IP, and running state.
type Snapshot struct {
	ID        string // container id == namespaced name for stack members
	Stack     string // porthole.stack ("" → not a stack member)
	Service   string // porthole.service (bare logical name)
	Discovery bool   // porthole.discovery == "on"
	IP        string // dedicated IPv4, "" when not running
	Running   bool
	Started   string // status.startedDate — changes on every (re)start; see reconcile
}

// Controller converges stack members' /etc/hosts so peers resolve by name. It is
// driven by the reconcile hub's existing per-cycle signal (OnCycle) — it adds NO
// polling loop of its own. Each pass computes the full desired state from the
// current set and writes only what changed, under the shared per-id lock, best-
// effort (a failure is retried on the next cycle because writes are idempotent).
type Controller struct {
	eng      HostsRW
	locks    Locker
	logger   *log.Logger
	debounce time.Duration

	reconcileMu sync.Mutex // serializes reconcile passes

	mu          sync.Mutex
	lastDesired map[string]string // memberID -> last block successfully applied
	lastStarted map[string]string // memberID -> startedDate at last apply (restart detector)
	timer       *time.Timer
	pending     []Snapshot
}

// NewController builds a Controller. debounce coalesces bursts of cycle signals
// (e.g. a crash-looping member); <=0 makes OnCycle run synchronously (tests).
func NewController(eng HostsRW, locks Locker, logger *log.Logger, debounce time.Duration) *Controller {
	if logger == nil {
		logger = log.Default()
	}
	return &Controller{
		eng:         eng,
		locks:       locks,
		logger:      logger,
		debounce:    debounce,
		lastDesired: map[string]string{},
		lastStarted: map[string]string{},
	}
}

// OnCycle receives the full container set once per reconcile poll. It coalesces
// rapid cycles within the debounce window and runs the convergence pass in the
// background, so it never blocks the hub.
func (c *Controller) OnCycle(ctx context.Context, snaps []Snapshot) {
	if c.debounce <= 0 {
		c.reconcile(ctx, snaps)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = snaps
	if c.timer == nil {
		c.timer = time.AfterFunc(c.debounce, func() {
			c.mu.Lock()
			s := c.pending
			c.timer = nil
			c.mu.Unlock()
			c.reconcile(ctx, s)
		})
	} else {
		c.timer.Reset(c.debounce)
	}
}

// reconcile is one convergence pass: compute the desired managed block for every
// running member of every discovery-enabled stack, apply the ones that changed,
// and strip members we previously managed that are no longer enabled.
func (c *Controller) reconcile(ctx context.Context, snaps []Snapshot) {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()

	byStack := map[string][]Member{}
	started := map[string]string{} // id -> startedDate, to detect restarts
	for _, s := range snaps {
		started[s.ID] = s.Started
		if s.Stack == "" || !s.Discovery {
			continue // not a discovery-enabled stack member
		}
		byStack[s.Stack] = append(byStack[s.Stack], Member{
			ID: s.ID, Service: s.Service, Namespaced: s.ID, IP: s.IP,
		})
	}

	desired := map[string]string{}
	for stack, members := range byStack {
		for _, inj := range planInjections(stack, members, "") {
			desired[inj.MemberID] = inj.Block
		}
	}

	// Apply blocks that changed OR whose member restarted since our last apply.
	// The restart check is load-bearing: /etc/hosts is wiped to self on every
	// start, so a prior injection does NOT survive a restart even when the desired
	// block is identical (e.g. peers kept their IPs). Never assume it survived.
	// A failed write leaves lastDesired untouched, so the next cycle re-applies —
	// never fatal, never blocks other members.
	for id, block := range desired {
		st := started[id]
		c.mu.Lock()
		prevBlock, ok := c.lastDesired[id]
		prevStart := c.lastStarted[id]
		c.mu.Unlock()
		if ok && prevBlock == block && prevStart == st {
			continue // unchanged and no restart — the prior write is still on disk
		}
		if err := c.applyOne(ctx, id, block); err != nil {
			c.logger.Printf("discovery: inject %s: %v (retry next cycle)", id, err)
			continue
		}
		c.mu.Lock()
		c.lastDesired[id] = block
		c.lastStarted[id] = st
		c.mu.Unlock()
	}

	// Members we used to manage but no longer should (discovery turned off, or the
	// member stopped). Strip the block from those still running+present; just forget
	// the ones that are gone/stopped (their /etc/hosts is wiped on their own start).
	c.mu.Lock()
	var stale []string
	for id := range c.lastDesired {
		if _, still := desired[id]; !still {
			stale = append(stale, id)
		}
	}
	c.mu.Unlock()
	for _, id := range stale {
		if !runningByID(snaps, id) {
			c.mu.Lock()
			delete(c.lastDesired, id)
			delete(c.lastStarted, id)
			c.mu.Unlock()
			continue
		}
		if err := c.applyOne(ctx, id, ""); err != nil {
			c.logger.Printf("discovery: strip %s: %v (retry next cycle)", id, err)
			continue
		}
		c.mu.Lock()
		delete(c.lastDesired, id)
		delete(c.lastStarted, id)
		c.mu.Unlock()
	}
}

// applyOne reads, merges, and (only if changed) writes one member's /etc/hosts,
// holding the shared per-id lock for the whole read-modify-write so it never races
// a supervisor restart on the same container. A "" block strips the managed block.
func (c *Controller) applyOne(ctx context.Context, id, block string) error {
	unlock := c.locks.Lock(id)
	defer unlock()
	existing, err := c.eng.ReadHosts(ctx, id)
	if err != nil {
		return err
	}
	merged := mergeHosts(existing, block)
	if merged == existing {
		return nil // already converged on disk
	}
	return c.eng.WriteHosts(ctx, id, merged)
}

func runningByID(snaps []Snapshot, id string) bool {
	for _, s := range snaps {
		if s.ID == id {
			return s.Running
		}
	}
	return false
}
