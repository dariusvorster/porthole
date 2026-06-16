package stacks

import (
	"context"
	"errors"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/idlock"
)

// Engine is the focused slice of the runtime the executor needs. It is a subset
// of engine.Engine plus the create/network verbs (interface segregation — the
// executor depends only on what it uses, and *engine.CLIEngine satisfies it).
type Engine interface {
	ListContainers(ctx context.Context, all bool) ([]engine.Container, error)
	ListNetworks(ctx context.Context) ([]engine.Network, error)
	CreateNetwork(ctx context.Context, name string) error
	RemoveNetwork(ctx context.Context, name string) error
	RunContainer(ctx context.Context, spec engine.RunSpec) (string, error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string) error
	DeleteContainer(ctx context.Context, id string, force bool) error
}

// SpecStore persists the authoritative RunSpec per container name (Phase 10b) so
// drift-remediation rollback is byte-perfect rather than reconstructed from
// `inspect`. nil → no persistence; rollback falls back to reconstruction (so this
// is purely additive — never worse than before).
type SpecStore interface {
	SaveSpec(name string, spec engine.RunSpec) error
	GetSpec(name string) (engine.RunSpec, bool, error)
	DeleteSpec(name string) error
}

// Executor applies stack plans through the engine. It applies the safe actions
// (create/start) and — for destructive drift remediation (Phase 10) — recreate
// with rollback. Orphan is detected only.
type Executor struct {
	eng        Engine
	locks      *idlock.KeyedMutex // per-container, shared with user mutations + supervisor (spec §6)
	stackLocks *idlock.KeyedMutex // per-stack, so two ups on one stack serialize (spec §8)
	specs      SpecStore          // optional: persisted RunSpecs for byte-perfect rollback
}

// NewExecutor wires the executor. locks is the SHARED per-container KeyedMutex so
// a stack mutation, a user action, and a supervision restart on the same
// container can never race; if nil, a private one is created.
func NewExecutor(eng Engine, locks *idlock.KeyedMutex) *Executor {
	if locks == nil {
		locks = idlock.New()
	}
	return &Executor{eng: eng, locks: locks, stackLocks: idlock.New()}
}

// Failure records a single per-service action that errored during apply.
type Failure struct {
	Service string     `json:"service"`
	Action  ActionKind `json:"action"`
	Error   string     `json:"error"`
}

// UpResult is the outcome of an Up: the plan that was computed, the safe actions
// applied, the derived status, and any per-service failures (no rollback).
type UpResult struct {
	Stack    string          `json:"stack"`
	Plan     Plan            `json:"plan"`
	Applied  []ServiceAction `json:"applied"`
	Status   string          `json:"status"`
	Failures []Failure       `json:"failures,omitempty"`
}

// Plan computes (but does not apply) the reconcile diff for a stack — the
// read-only dry run behind POST /api/stacks/{name}/plan. It re-discovers members
// from `container ls` (labels), never the DB.
func (e *Executor) Plan(ctx context.Context, stack Stack) (Plan, error) {
	observed, err := e.eng.ListContainers(ctx, true)
	if err != nil {
		return Plan{}, err
	}
	return PlanReconcile(stack, observed), nil
}

// Up brings a stack up: ensure its network, then apply the SAFE plan actions
// (create/start) in dependency order. Recreate/orphan are left in the plan,
// unapplied. Partial failure → no rollback; the result carries the failures and
// the derived (likely degraded) status. Idempotent: re-running noops the members
// that already match.
func (e *Executor) Up(ctx context.Context, stack Stack) (UpResult, error) {
	unlock := e.stackLocks.Lock(stack.Name)
	defer unlock()

	if err := e.ensureNetwork(ctx, stack.Name); err != nil {
		return UpResult{}, err
	}

	observed, err := e.eng.ListContainers(ctx, true)
	if err != nil {
		return UpResult{}, err
	}
	plan := PlanReconcile(stack, observed)

	// Index the safe actions by service, then apply in dependency order.
	safe := map[string]ServiceAction{}
	for _, a := range plan.Safe() {
		safe[a.Service] = a
	}
	svcByName := map[string]Service{}
	for _, s := range stack.Services {
		svcByName[s.Name] = s
	}

	result := UpResult{Stack: stack.Name, Plan: plan}
	for _, name := range Order(stack) {
		action, ok := safe[name]
		if !ok || action.Action == ActionNoop {
			continue
		}
		if err := e.applyOne(ctx, stack, svcByName[name], action); err != nil {
			result.Failures = append(result.Failures, Failure{Service: name, Action: action.Action, Error: err.Error()})
			continue // partial-failure: keep going, never rollback (spec §8)
		}
		result.Applied = append(result.Applied, action)
	}

	// Derive status from the post-apply observation.
	after, err := e.eng.ListContainers(ctx, true)
	if err != nil {
		return result, err
	}
	result.Status = Status(stack, after)
	return result, nil
}

// applyOne performs a single safe action under the per-container lock.
func (e *Executor) applyOne(ctx context.Context, stack Stack, svc Service, action ServiceAction) error {
	switch action.Action {
	case ActionCreate:
		name := containerName(stack.Name, svc.Name)
		unlock := e.locks.Lock(name)
		defer unlock()
		spec := runSpecFor(stack, svc)
		if _, err := e.eng.RunContainer(ctx, spec); err != nil {
			return err
		}
		e.saveSpec(name, spec) // authoritative spec for byte-perfect rollback (Phase 10b)
		return nil
	case ActionStart:
		unlock := e.locks.Lock(action.ContainerID)
		defer unlock()
		return e.eng.StartContainer(ctx, action.ContainerID)
	default:
		return nil // noop / destructive (never reached — destructive excluded by Safe)
	}
}

// DownResult is the outcome of a Down.
type DownResult struct {
	Stack    string    `json:"stack"`
	Removed  []string  `json:"removed"` // service names torn down
	Failures []Failure `json:"failures,omitempty"`
}

// Down stops and removes a stack's members in reverse dependency order, then
// best-effort removes the stack network. Named volumes are KEPT — v1 never
// deletes a volume (down --keep-volumes is the only behaviour; spec §8). All
// labelled members are removed, including orphans (a full teardown the user
// asked for).
func (e *Executor) Down(ctx context.Context, stack Stack) (DownResult, error) {
	unlock := e.stackLocks.Lock(stack.Name)
	defer unlock()

	members, err := e.members(ctx, stack.Name)
	if err != nil {
		return DownResult{}, err
	}

	result := DownResult{Stack: stack.Name}
	for _, svc := range teardownOrder(stack, members) {
		c, ok := members[svc]
		if !ok {
			continue
		}
		if err := e.removeOne(ctx, c.ID); err != nil {
			result.Failures = append(result.Failures, Failure{Service: svc, Action: "remove", Error: err.Error()})
			continue
		}
		result.Removed = append(result.Removed, svc)
	}

	// Best-effort network teardown — ignore "still in use" / "not found".
	_ = e.eng.RemoveNetwork(ctx, stack.Name)
	return result, nil
}

func (e *Executor) removeOne(ctx context.Context, id string) error {
	unlock := e.locks.Lock(id)
	defer unlock()
	// force=true: stop+remove in one step (a running member must come down).
	if err := e.eng.DeleteContainer(ctx, id, true); err != nil {
		return err
	}
	e.deleteSpec(id) // a removed container's stored spec is stale — drop it
	return nil
}

// Restart stops then starts each declared member (no recreate). Stop in reverse
// dependency order, start in forward order, all under per-container locks.
func (e *Executor) Restart(ctx context.Context, stack Stack) (UpResult, error) {
	unlock := e.stackLocks.Lock(stack.Name)
	defer unlock()

	members, err := e.members(ctx, stack.Name)
	if err != nil {
		return UpResult{}, err
	}

	order := Order(stack)
	result := UpResult{Stack: stack.Name}

	// Stop in reverse order.
	for i := len(order) - 1; i >= 0; i-- {
		c, ok := members[order[i]]
		if !ok {
			continue
		}
		unlockC := e.locks.Lock(c.ID)
		if err := e.eng.StopContainer(ctx, c.ID); err != nil {
			result.Failures = append(result.Failures, Failure{Service: order[i], Action: "stop", Error: err.Error()})
		}
		unlockC()
	}
	// Start in forward order.
	for _, svc := range order {
		c, ok := members[svc]
		if !ok {
			continue
		}
		unlockC := e.locks.Lock(c.ID)
		if err := e.eng.StartContainer(ctx, c.ID); err != nil {
			result.Failures = append(result.Failures, Failure{Service: svc, Action: "start", Error: err.Error()})
		} else {
			result.Applied = append(result.Applied, ServiceAction{Service: svc, Action: ActionStart, ContainerID: c.ID})
		}
		unlockC()
	}

	after, err := e.eng.ListContainers(ctx, true)
	if err != nil {
		return result, err
	}
	result.Status = Status(stack, after)
	return result, nil
}

// ensureNetwork creates the per-stack network if it does not already exist.
// Tolerates a name-conflict race (treats it as already-present).
func (e *Executor) ensureNetwork(ctx context.Context, name string) error {
	nets, err := e.eng.ListNetworks(ctx)
	if err != nil {
		return err
	}
	for _, n := range nets {
		if n.Configuration.Name == name {
			return nil
		}
	}
	err = e.eng.CreateNetwork(ctx, name)
	var ce *engine.CLIError
	if errors.As(err, &ce) && ce.Kind == engine.ErrNameConflict {
		return nil
	}
	return err
}

// members re-discovers this stack's members from `container ls` labels (never the
// DB), keyed by service name. A running container wins a duplicate service.
func (e *Executor) members(ctx context.Context, stackName string) (map[string]engine.Container, error) {
	observed, err := e.eng.ListContainers(ctx, true)
	if err != nil {
		return nil, err
	}
	out := map[string]engine.Container{}
	for _, c := range observed {
		svc, ok := memberOf(c, stackName)
		if !ok || svc == "" {
			continue
		}
		if existing, dup := out[svc]; dup && existing.IsRunning() && !c.IsRunning() {
			continue
		}
		out[svc] = c
	}
	return out, nil
}

// teardownOrder is reverse dependency order for declared services, with any
// undeclared (orphan) members appended so a full teardown removes everything.
func teardownOrder(stack Stack, members map[string]engine.Container) []string {
	order := Order(stack)
	declared := map[string]bool{}
	out := make([]string, 0, len(members))
	for i := len(order) - 1; i >= 0; i-- {
		declared[order[i]] = true
		out = append(out, order[i])
	}
	var orphans []string
	for svc := range members {
		if !declared[svc] {
			orphans = append(orphans, svc)
		}
	}
	// Deterministic order for orphans.
	for i := 0; i < len(orphans); i++ {
		for j := i + 1; j < len(orphans); j++ {
			if orphans[j] < orphans[i] {
				orphans[i], orphans[j] = orphans[j], orphans[i]
			}
		}
	}
	return append(out, orphans...)
}

func containerName(stack, svc string) string { return stack + "-" + svc }

// runSpecFor builds the engine RunSpec for a service: namespaced name, the
// per-stack network, membership labels, and the restart label (mirrored to
// supervision). porthole.* labels always win over user labels.
func runSpecFor(stack Stack, svc Service) engine.RunSpec {
	labels := map[string]string{}
	for k, v := range svc.Labels {
		labels[k] = v
	}
	labels[LabelStack] = stack.Name
	labels[LabelService] = svc.Name
	if svc.Restart != "" && svc.Restart != RestartNo {
		labels[LabelRestart] = svc.Restart
	}
	if stack.Discovery {
		labels[LabelDiscovery] = "on" // durable backup; the DB record is working truth
	}

	spec := engine.RunSpec{
		Name:    containerName(stack.Name, svc.Name),
		Image:   svc.Image,
		Command: svc.Command,
		Network: stack.Name,
		Env:     svc.Environment,
		Labels:  labels,
	}
	for _, p := range svc.Ports {
		spec.Ports = append(spec.Ports, engine.RunPort{HostPort: p.HostPort, ContainerPort: p.ContainerPort, Proto: p.Proto})
	}
	for _, v := range svc.Volumes {
		spec.Volumes = append(spec.Volumes, engine.RunVolume{Source: v.Source, Target: v.Target})
	}
	return spec
}
