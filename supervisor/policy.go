package supervisor

import "time"

// Action is the supervision decision for one container on one cycle.
type Action string

const (
	ActionNone    Action = "none"    // leave it alone
	ActionRestart Action = "restart" // issue a start (through the C1 per-id lock, in P3)
	ActionGiveUp  Action = "give-up" // exhausted attempts; terminal until manual reset
)

// SupState is the mutable per-container supervision runtime state. It is carried
// between cycles by the loop (P3); decide is otherwise pure.
type SupState struct {
	Attempts     int       // consecutive restart attempts (drives backoff)
	BackoffUntil time.Time // no restart before this instant
	LastRestart  time.Time // when the last restart was issued
	GaveUp       bool      // terminal until a manual reset
}

// Observed is what the reconcile loop saw this cycle.
type Observed struct {
	Running   bool
	StartedAt time.Time // status.startedDate; meaningful only while Running
}

// Config tunes backoff and giving-up. Zero value is not useful — use DefaultConfig.
type Config struct {
	BaseBackoff     time.Duration // first backoff after a restart
	MaxBackoff      time.Duration // cap
	MaxAttempts     int           // consecutive restarts before give-up
	StabilityWindow time.Duration // running this long resets the attempt counter
}

func DefaultConfig() Config {
	return Config{
		BaseBackoff:     1 * time.Second,
		MaxBackoff:      60 * time.Second,
		MaxAttempts:     10,
		StabilityWindow: 30 * time.Second,
	}
}

// backoffFor returns the wait before the *next* restart given the attempt count
// just made: 1→base, 2→2×base, 3→4×base, … capped at MaxBackoff.
func backoffFor(attempts int, cfg Config) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := cfg.BaseBackoff
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= cfg.MaxBackoff {
			return cfg.MaxBackoff
		}
	}
	return d
}

// decide is the pure restart state machine. Given the policy, the mediated
// desired state, what was observed, the carried SupState, and the clock, it
// returns the action to take and the next SupState. No I/O — fully unit-testable
// with an injected `now` (like engine.CPUPercent).
//
// Rules (spec §3, §6):
//   - A running container is never restarted; once it has been up past the
//     stability window, its attempt counter (and give-up) reset.
//   - `no` never restarts.
//   - desiredState==stopped blocks restart (a mediated stop, or unless-stopped
//     intent). `always` and `unless-stopped` are otherwise identical here — the
//     who-set-desired distinction lives at the mutation layer, not in this pure fn.
//   - Backoff gates restarts; MaxAttempts consecutive restarts → give-up.
func decide(policy RestartPolicy, desired DesiredState, obs Observed, sup SupState, cfg Config, now time.Time) (Action, SupState) {
	// Running: never restart. Reset the counter once it has proven stable.
	if obs.Running {
		next := sup
		if !obs.StartedAt.IsZero() && now.Sub(obs.StartedAt) >= cfg.StabilityWindow {
			next.Attempts = 0
			next.BackoffUntil = time.Time{}
			next.GaveUp = false
		}
		return ActionNone, next
	}

	// Stopped from here down.
	if policy == RestartNo {
		return ActionNone, sup
	}
	// A stop the user owns (mediated) — or unless-stopped intent — stays stopped.
	if desired == DesiredStopped {
		return ActionNone, sup
	}
	// A stopped, restart-eligible container: defer to the shared ceiling/backoff
	// decision used by both crash recovery and health-triggered restarts.
	return decideUnhealthy(sup, cfg, now)
}

// decideUnhealthy is the ONE place that owns the restart-attempt ceiling: the
// "Attempts >= MaxAttempts → giveUp" rule and the backoff gate. Both crash
// recovery (decide, on a stopped container) and health-triggered restarts
// (runProbe, on a sustained-unhealthy running container) call it, so there is a
// single implementation of the budget. Pure; clock injected.
func decideUnhealthy(sup SupState, cfg Config, now time.Time) (Action, SupState) {
	if sup.GaveUp {
		return ActionGiveUp, sup // terminal, idempotent
	}
	if sup.Attempts >= cfg.MaxAttempts {
		next := sup
		next.GaveUp = true
		return ActionGiveUp, next
	}
	if now.Before(sup.BackoffUntil) {
		return ActionNone, sup // still backing off
	}
	return ActionRestart, sup.afterRestart(cfg, now)
}

// afterRestart advances the state after issuing a restart: count it and arm the
// next backoff.
func (s SupState) afterRestart(cfg Config, now time.Time) SupState {
	n := s
	n.Attempts++
	n.LastRestart = now
	n.BackoffUntil = now.Add(backoffFor(n.Attempts, cfg))
	return n
}
