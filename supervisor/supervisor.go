package supervisor

import (
	"io"
	"log"
	"sync"
	"time"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/idlock"
)

// Broadcaster emits a named event to SSE subscribers (satisfied by reconcile.Hub).
type Broadcaster interface {
	Emit(name string, payload any)
}

// Supervisor enforces restart policies + health on top of the engine. It shares
// the reconcile poll (via Hub.SetOnCycle) and the C1 per-id mutation lock.
type Supervisor struct {
	store  Store
	eng    engine.Engine
	bus    Broadcaster
	locks  *idlock.KeyedMutex
	cfg    Config
	logger *log.Logger
	nowFn  func() time.Time

	mu      sync.Mutex
	state   map[string]SupState
	health  map[string]*healthRuntime
	bootGap time.Duration // stagger between boot-time restarts
}

// New constructs a Supervisor. A nil logger discards; a nil locks gets a fresh
// keyed mutex; a zero cfg uses DefaultConfig. bus may be nil (no SSE emission).
func New(store Store, eng engine.Engine, bus Broadcaster, locks *idlock.KeyedMutex, cfg Config, logger *log.Logger) *Supervisor {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	if locks == nil {
		locks = idlock.New()
	}
	if cfg == (Config{}) {
		cfg = DefaultConfig()
	}
	return &Supervisor{
		store:   store,
		eng:     eng,
		bus:     bus,
		locks:   locks,
		cfg:     cfg,
		logger:  logger,
		nowFn:   time.Now,
		state:   map[string]SupState{},
		health:  map[string]*healthRuntime{},
		bootGap: 250 * time.Millisecond,
	}
}

// Store exposes the underlying store (used by the policy REST endpoint in P5).
func (s *Supervisor) Store() Store { return s.store }

// RecordStart / RecordStop capture mediated intent from the HTTP mutation layer:
// a stop the user made through Porthole sets desired=stopped, so supervision will
// not restart it (the unless-stopped / mediated-stop semantics, spec §3.1).
func (s *Supervisor) RecordStart(id string) { _ = s.store.SetDesired(id, DesiredRunning) }
func (s *Supervisor) RecordStop(id string)  { _ = s.store.SetDesired(id, DesiredStopped) }
