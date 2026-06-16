package stacks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/idlock"
)

// ErrStackInvalid is returned when an operation needs a valid compose file but
// the stored one no longer parses cleanly (e.g. the user imported it, then a
// later edit broke it). Callers map this to a 4xx, not a 500.
var ErrStackInvalid = errors.New("stored stack file is invalid")

// Manager is the API-level façade over the store + executor. The HTTP layer
// depends only on this, so transport code stays thin and all stack logic
// (validate / import / status / plan / up / down) lives here. (Named Manager,
// not Service, to avoid colliding with the compose Service type.)
type Manager struct {
	store Store
	exec  *Executor
	eng   Engine
}

// NewManager wires a Manager. locks is the SHARED per-container KeyedMutex so
// stack mutations serialize with user actions + supervision on the same id.
func NewManager(store Store, eng Engine, locks *idlock.KeyedMutex) *Manager {
	return &Manager{store: store, exec: NewExecutor(eng, locks), eng: eng}
}

// Member is one container belonging to a stack, summarised for the UI. The IP is
// present only while running (spec: status.networks is empty when stopped).
type Member struct {
	Service string `json:"service"`
	ID      string `json:"id"`
	State   string `json:"state"`
	IP      string `json:"ip,omitempty"`
	Image   string `json:"image"`
}

// StackView is a stored stack plus its live status + members — the GET shape.
type StackView struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Status    string    `json:"status"`    // up | degraded | down | unknown
	Valid     bool      `json:"valid"`     // does the stored file still parse cleanly?
	Discovery bool      `json:"discovery"` // service-discovery opt-in (Phase 8)
	Services  []string  `json:"services"`
	Members   []Member  `json:"members"`
}

// StackEvent is the SSE `stack` payload broadcast after a mutation.
type StackEvent struct {
	Stack   string   `json:"stack"`
	Status  string   `json:"status"`
	Members []Member `json:"members"`
}

// Validate parses a compose document and returns the report — no side effects.
func (s *Manager) Validate(name string, yaml []byte) ValidationReport {
	_, rep := Parse(name, yaml)
	return rep
}

// Import validates a compose document and, if valid, stores it. Returns the
// report and whether it was stored (invalid files are reported, never stored).
func (s *Manager) Import(name string, yaml []byte) (ValidationReport, bool, error) {
	_, rep := Parse(name, yaml)
	if !rep.Valid {
		return rep, false, nil
	}
	rec := Record{Name: name, ComposeYAML: string(yaml)}
	// Preserve the discovery toggle across a re-import (it's a separate decision
	// from editing the compose file).
	if existing, ok, _ := s.store.GetStack(name); ok {
		rec.Discovery = existing.Discovery
	}
	if err := s.store.SaveStack(rec); err != nil {
		return rep, false, err
	}
	return rep, true, nil
}

// SetDiscovery flips a stack's service-discovery opt-in (Phase 8). The DB is
// working truth; the controller picks the change up on the next reconcile cycle
// (injecting peers when turned on, stripping the managed block when turned off).
// ok=false if the stack is unknown.
func (s *Manager) SetDiscovery(name string, on bool) (bool, error) {
	rec, ok, err := s.store.GetStack(name)
	if err != nil || !ok {
		return ok, err
	}
	rec.Discovery = on
	return true, s.store.SaveStack(rec)
}

// DiscoveryFlags returns name→discovery for every stored stack. The discovery
// controller's caller consults this so the DB stays authoritative (a stored
// record overrides a stale create-time label); unknown stacks fall back to the
// label. Best-effort: an empty map on a store error (controller leaves hosts as-is).
func (s *Manager) DiscoveryFlags() map[string]bool {
	recs, err := s.store.ListStacks()
	if err != nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(recs))
	for _, r := range recs {
		out[r.Name] = r.Discovery
	}
	return out
}

// List returns every stored stack with its live status + members (best-effort:
// if the runtime can't be read, status is "unknown").
func (s *Manager) List(ctx context.Context) ([]StackView, error) {
	recs, err := s.store.ListStacks()
	if err != nil {
		return nil, err
	}
	observed, obsErr := s.eng.ListContainers(ctx, true)
	out := make([]StackView, 0, len(recs))
	for _, r := range recs {
		out = append(out, s.viewFor(r, observed, obsErr == nil))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns one stored stack's view, or ok=false if absent.
func (s *Manager) Get(ctx context.Context, name string) (StackView, bool, error) {
	r, ok, err := s.store.GetStack(name)
	if err != nil || !ok {
		return StackView{}, ok, err
	}
	observed, obsErr := s.eng.ListContainers(ctx, true)
	return s.viewFor(r, observed, obsErr == nil), true, nil
}

func (s *Manager) viewFor(r Record, observed []engine.Container, obsOK bool) StackView {
	stack, rep := Parse(r.Name, []byte(r.ComposeYAML))
	v := StackView{Name: r.Name, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Valid: rep.Valid, Discovery: r.Discovery}
	for _, svc := range stack.Services {
		v.Services = append(v.Services, svc.Name)
	}
	if !obsOK {
		v.Status = "unknown"
		return v
	}
	v.Status = Status(stack, observed)
	v.Members = membersView(r.Name, observed)
	return v
}

// Plan computes the reconcile diff for a stored stack (dry run). ok=false if the
// stack is unknown; ErrStackInvalid if the stored file no longer parses.
func (s *Manager) Plan(ctx context.Context, name string) (Plan, bool, error) {
	stack, ok, err := s.loadValid(name)
	if err != nil || !ok {
		return Plan{}, ok, err
	}
	p, err := s.exec.Plan(ctx, stack)
	return p, true, err
}

// Up / Down / Restart load the stored stack and apply through the executor.
func (s *Manager) Up(ctx context.Context, name string) (UpResult, bool, error) {
	stack, ok, err := s.loadValidWithDiscovery(name)
	if err != nil || !ok {
		return UpResult{}, ok, err
	}
	res, err := s.exec.Up(ctx, stack)
	return res, true, err
}

func (s *Manager) Down(ctx context.Context, name string) (DownResult, bool, error) {
	stack, ok, err := s.loadValid(name)
	if err != nil || !ok {
		return DownResult{}, ok, err
	}
	res, err := s.exec.Down(ctx, stack)
	return res, true, err
}

func (s *Manager) Restart(ctx context.Context, name string) (UpResult, bool, error) {
	stack, ok, err := s.loadValid(name)
	if err != nil || !ok {
		return UpResult{}, ok, err
	}
	res, err := s.exec.Restart(ctx, stack)
	return res, true, err
}

// Remediate APPLIES the drifted-service recreates the planner flags (Phase 10,
// destructive). Loads with the discovery flag so re-created members carry the
// porthole.discovery label. ok=false if the stack is unknown.
func (s *Manager) Remediate(ctx context.Context, name string) (RemediateResult, bool, error) {
	stack, ok, err := s.loadValidWithDiscovery(name)
	if err != nil || !ok {
		return RemediateResult{}, ok, err
	}
	res, err := s.exec.Remediate(ctx, stack)
	return res, true, err
}

// Delete removes the stored stack definition. It does NOT touch running
// containers — the caller must `down` first to tear them down (spec/ST4).
func (s *Manager) Delete(name string) (bool, error) {
	_, ok, err := s.store.GetStack(name)
	if err != nil || !ok {
		return ok, err
	}
	return true, s.store.DeleteStack(name)
}

// StatusEvent builds the SSE payload for a stack after a mutation.
func (s *Manager) StatusEvent(ctx context.Context, name string) StackEvent {
	observed, _ := s.eng.ListContainers(ctx, true)
	ev := StackEvent{Stack: name, Status: "down", Members: membersView(name, observed)}
	if r, ok, _ := s.store.GetStack(name); ok {
		stack, _ := Parse(r.Name, []byte(r.ComposeYAML))
		ev.Status = Status(stack, observed)
	}
	return ev
}

// loadValid loads a stored stack and parses it, returning ErrStackInvalid if the
// file no longer validates (so callers don't apply a broken plan).
func (s *Manager) loadValid(name string) (Stack, bool, error) {
	r, ok, err := s.store.GetStack(name)
	if err != nil || !ok {
		return Stack{}, ok, err
	}
	stack, rep := Parse(r.Name, []byte(r.ComposeYAML))
	if !rep.Valid {
		return Stack{}, true, fmt.Errorf("%w: %v", ErrStackInvalid, rep.Errors)
	}
	return stack, true, nil
}

// loadValidWithDiscovery is loadValid plus the stored discovery flag stamped onto
// the parsed Stack, so Up labels new members for service discovery. Only Up needs
// it — restart/down don't recreate members, so their labels are untouched.
func (s *Manager) loadValidWithDiscovery(name string) (Stack, bool, error) {
	r, ok, err := s.store.GetStack(name)
	if err != nil || !ok {
		return Stack{}, ok, err
	}
	stack, rep := Parse(r.Name, []byte(r.ComposeYAML))
	if !rep.Valid {
		return Stack{}, true, fmt.Errorf("%w: %v", ErrStackInvalid, rep.Errors)
	}
	stack.Discovery = r.Discovery
	return stack, true, nil
}

// membersView lists a stack's labelled members (declared + orphan), sorted by
// service, re-discovered from the observed containers (never the DB).
func membersView(stackName string, observed []engine.Container) []Member {
	var out []Member
	for _, c := range observed {
		svc, ok := memberOf(c, stackName)
		if !ok || svc == "" {
			continue
		}
		out = append(out, Member{
			Service: svc,
			ID:      c.ID,
			State:   c.Status.State,
			IP:      c.PrimaryIPv4(),
			Image:   c.Configuration.Image.Reference,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}
