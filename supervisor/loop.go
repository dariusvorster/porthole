package supervisor

import (
	"context"
	"time"

	"github.com/porthole/porthole/engine"
)

// SupervisionEvent is the per-container SSE payload (event name "supervision").
type SupervisionEvent struct {
	ID           string        `json:"id"`
	Policy       RestartPolicy `json:"policy"`
	DesiredState DesiredState  `json:"desiredState"`
	RestartCount int           `json:"restartCount"` // transient backoff-attempt counter (resets on stabilization)
	RestartTotal int           `json:"restartTotal"` // cumulative lifetime supervision restarts (persisted, PF3)
	BackoffUntil *time.Time    `json:"backoffUntil,omitempty"`
	GaveUp       bool          `json:"gaveUp"`
	Health       *HealthInfo   `json:"health,omitempty"`       // runtime probe state
	HealthConfig *HealthConfig `json:"healthConfig,omitempty"` // configured probe (for the form)
}

// HealthConfig is the configured probe carried on the event so the inspector's
// health form can pre-fill on reload (the runtime state alone can't do that).
type HealthConfig struct {
	Type     string `json:"type"`
	Port     int    `json:"port"`
	Path     string `json:"path,omitempty"`
	Interval int    `json:"interval,omitempty"`
}

const restartLabel = "porthole.restart"

// OnCycle runs one supervision pass over the current container list. Registered
// via Hub.SetOnCycle so it shares the reconcile poll (no second `ls`). Per
// policied container it runs the restart decision (crash recovery) and schedules
// a health probe if due; a supervision event is emitted with a restart.
func (s *Supervisor) OnCycle(containers []engine.Container) {
	now := s.nowFn()
	for _, c := range containers {
		pol, ok := s.policyFor(c)
		if !ok {
			continue
		}

		if pol.Restart == RestartAlways || pol.Restart == RestartUnlessStopped {
			s.mu.Lock()
			prev := s.state[c.ID]
			s.mu.Unlock()
			obs := Observed{Running: c.IsRunning(), StartedAt: c.Status.StartedDate}
			action, next := decide(pol.Restart, s.desired(c.ID), obs, prev, s.cfg, now)
			s.mu.Lock()
			s.state[c.ID] = next
			s.mu.Unlock()
			if action == ActionRestart {
				s.doRestart(c.ID)
			}
		}

		// Health probing is independent of restart policy. maybeProbe initializes
		// state synchronously and runs the probe (if due) in a goroutine.
		if pol.Health != nil && pol.Health.Type != "" {
			s.maybeProbe(c, *pol.Health, now)
		}

		// Emit the current supervision state every cycle so a freshly-connected
		// client (and the node badges) stay current without polling. A probe that
		// changes health mid-interval also emits promptly from runProbe.
		s.emitFor(c.ID)
	}
}

// Reconcile is the staggered boot pass: start always/unless-stopped containers
// found stopped (unless a mediated stop recorded desired=stopped). Serial + gap
// avoids a thundering herd on the apiserver (spec §8 gap 2).
func (s *Supervisor) Reconcile(ctx context.Context) error {
	cs, err := s.eng.ListContainers(ctx, true)
	if err != nil {
		return err
	}
	for _, c := range cs {
		pol, ok := s.policyFor(c)
		if !ok || (pol.Restart != RestartAlways && pol.Restart != RestartUnlessStopped) {
			continue
		}
		if c.IsRunning() || s.desired(c.ID) == DesiredStopped {
			continue
		}
		s.logger.Printf("supervisor: boot-restart %s (policy %s)", c.ID, pol.Restart)
		s.doRestart(c.ID)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.bootGap):
		}
	}
	return nil
}

// doRestart starts a stopped container through the shared per-id lock.
func (s *Supervisor) doRestart(id string) {
	unlock := s.locks.Lock(id)
	defer unlock()
	if err := s.eng.StartContainer(context.Background(), id); err != nil {
		s.logger.Printf("supervisor: restart %s failed: %v", id, err)
		return
	}
	_ = s.store.BumpRestartCount(id) // cumulative lifetime count (PF3)
}

// doFullRestart stops then starts a (running) container — used for a
// health-triggered restart — atomically under the per-id lock.
func (s *Supervisor) doFullRestart(id string) {
	unlock := s.locks.Lock(id)
	defer unlock()
	ctx := context.Background()
	if err := s.eng.StopContainer(ctx, id); err != nil {
		s.logger.Printf("supervisor: health-restart stop %s failed: %v", id, err)
		return
	}
	if err := s.eng.StartContainer(ctx, id); err != nil {
		s.logger.Printf("supervisor: health-restart start %s failed: %v", id, err)
		return
	}
	_ = s.store.BumpRestartCount(id) // cumulative lifetime count (PF3)
}

// policyFor resolves a container's full policy: the DB wins; otherwise valid
// porthole.restart / porthole.health.* labels fill the gap and are seeded into
// the DB (union on discovery — never trust the DB alone, spec §4).
func (s *Supervisor) policyFor(c engine.Container) (Policy, bool) {
	if p, ok, _ := s.store.GetPolicy(c.ID); ok && (p.Restart != "" || p.Health != nil) {
		return p, true
	}
	derived := Policy{ContainerID: c.ID}
	has := false
	switch lbl := RestartPolicy(c.Configuration.Labels[restartLabel]); lbl {
	case RestartAlways, RestartUnlessStopped, RestartNo:
		derived.Restart = lbl
		has = true
	}
	if hp := healthFromLabels(c.Configuration.Labels); hp != nil {
		derived.Health = hp
		has = true
	}
	if !has {
		return Policy{}, false
	}
	derived.UpdatedAt = s.nowFn()
	_ = s.store.SetPolicy(derived)
	return derived, true
}

// ApplyPolicy persists a policy (from the REST endpoint) and emits an immediate
// supervision event so the change shows without waiting a full cycle. The label
// mirror is intentionally NOT written here — labels are immutable post-create, so
// a running container is store-only; the porthole.restart mirror is written only
// when a container is created through Porthole (a future run dialog).
func (s *Supervisor) ApplyPolicy(p Policy) error {
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = s.nowFn()
	}
	if err := s.store.SetPolicy(p); err != nil {
		return err
	}
	s.emitFor(p.ContainerID)
	return nil
}

// OnRemoved prunes a removed container's rows + in-memory state so a reused name
// never inherits stale policy (the name-reuse footgun). Wired to the hub's
// container.removed via Hub.SetOnRemoved.
func (s *Supervisor) OnRemoved(id string) {
	_ = s.store.DeletePolicy(id)
	_ = s.store.DeleteDesired(id)
	_ = s.store.DeleteRestartCount(id)
	s.mu.Lock()
	delete(s.state, id)
	delete(s.health, id)
	s.mu.Unlock()
}

func (s *Supervisor) desired(id string) DesiredState {
	if d, ok, _ := s.store.GetDesired(id); ok {
		return d
	}
	return DesiredRunning
}

// emitFor builds and broadcasts the combined supervision event for a container,
// reading the latest restart state + health under the lock.
func (s *Supervisor) emitFor(id string) {
	if s.bus == nil {
		return
	}
	p, _, _ := s.store.GetPolicy(id)
	desired := s.desired(id)

	s.mu.Lock()
	st := s.state[id]
	var health *HealthInfo
	if hr := s.health[id]; hr != nil && hr.State != "" {
		health = &HealthInfo{State: hr.State, Failures: hr.Failures}
		if !hr.LastProbe.IsZero() {
			lp := hr.LastProbe
			health.LastProbe = &lp
		}
	}
	s.mu.Unlock()

	total, _ := s.store.GetRestartCount(id)
	ev := SupervisionEvent{
		ID:           id,
		Policy:       p.Restart,
		DesiredState: desired,
		RestartCount: st.Attempts,
		RestartTotal: total,
		GaveUp:       st.GaveUp,
		Health:       health,
	}
	if !st.BackoffUntil.IsZero() {
		bu := st.BackoffUntil
		ev.BackoffUntil = &bu
	}
	if p.Health != nil && p.Health.Type != "" {
		ev.HealthConfig = &HealthConfig{
			Type:     p.Health.Type,
			Port:     p.Health.Port,
			Path:     p.Health.Path,
			Interval: p.Health.Interval,
		}
	}
	s.bus.Emit("supervision", ev)
}
