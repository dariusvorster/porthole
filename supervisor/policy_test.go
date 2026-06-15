package supervisor

import (
	"testing"
	"time"
)

// t0 is a fixed fake "now"; all times are relative to it (clock injected).
var t0 = time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

func TestDecide(t *testing.T) {
	cfg := DefaultConfig()
	running := func(since time.Duration) Observed { return Observed{Running: true, StartedAt: t0.Add(-since)} }
	stopped := Observed{Running: false}

	cases := []struct {
		name     string
		policy   RestartPolicy
		desired  DesiredState
		obs      Observed
		sup      SupState
		now      time.Time
		want     Action
		wantNext func(SupState) bool // optional assertion on next state
	}{
		{
			name: "no policy, stopped → none",
			policy: RestartNo, obs: stopped, now: t0, want: ActionNone,
		},
		{
			name: "always, stopped, desired running → restart (counts, arms backoff)",
			policy: RestartAlways, desired: DesiredRunning, obs: stopped, now: t0, want: ActionRestart,
			wantNext: func(s SupState) bool {
				return s.Attempts == 1 && s.LastRestart.Equal(t0) && s.BackoffUntil.Equal(t0.Add(1*time.Second))
			},
		},
		{
			name: "always, stopped, desired stopped → none (mediated stop)",
			policy: RestartAlways, desired: DesiredStopped, obs: stopped, now: t0, want: ActionNone,
		},
		{
			name: "unless-stopped, stopped, desired running → restart (same as always)",
			policy: RestartUnlessStopped, desired: DesiredRunning, obs: stopped, now: t0, want: ActionRestart,
		},
		{
			name: "unless-stopped, stopped, desired stopped → none",
			policy: RestartUnlessStopped, desired: DesiredStopped, obs: stopped, now: t0, want: ActionNone,
		},
		{
			name: "always, RUNNING → never restart",
			policy: RestartAlways, desired: DesiredRunning, obs: running(5 * time.Second), now: t0, want: ActionNone,
		},
		{
			name: "always, stopped, within backoff → none",
			policy: RestartAlways, desired: DesiredRunning, obs: stopped,
			sup: SupState{Attempts: 2, BackoffUntil: t0.Add(2 * time.Second)}, now: t0, want: ActionNone,
		},
		{
			name: "always, stopped, backoff elapsed → restart (attempts 2→3)",
			policy: RestartAlways, desired: DesiredRunning, obs: stopped,
			sup: SupState{Attempts: 2, BackoffUntil: t0.Add(-1 * time.Second)}, now: t0, want: ActionRestart,
			wantNext: func(s SupState) bool { return s.Attempts == 3 && s.BackoffUntil.Equal(t0.Add(4*time.Second)) },
		},
		{
			name: "always, stopped, attempts at max → give up (terminal set)",
			policy: RestartAlways, desired: DesiredRunning, obs: stopped,
			sup: SupState{Attempts: cfg.MaxAttempts}, now: t0, want: ActionGiveUp,
			wantNext: func(s SupState) bool { return s.GaveUp },
		},
		{
			name: "already gave up, stopped → give up (stays terminal)",
			policy: RestartAlways, desired: DesiredRunning, obs: stopped,
			sup: SupState{GaveUp: true, Attempts: 4}, now: t0, want: ActionGiveUp,
			wantNext: func(s SupState) bool { return s.GaveUp && s.Attempts == 4 },
		},
		{
			name: "running past stability window → none, counter + giveUp reset",
			policy: RestartAlways, desired: DesiredRunning, obs: running(31 * time.Second),
			sup: SupState{Attempts: 5, GaveUp: true, BackoffUntil: t0.Add(10 * time.Second)}, now: t0, want: ActionNone,
			wantNext: func(s SupState) bool { return s.Attempts == 0 && !s.GaveUp && s.BackoffUntil.IsZero() },
		},
		{
			name: "running but not yet stable → none, counter preserved",
			policy: RestartAlways, desired: DesiredRunning, obs: running(10 * time.Second),
			sup: SupState{Attempts: 3}, now: t0, want: ActionNone,
			wantNext: func(s SupState) bool { return s.Attempts == 3 },
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, next := decide(c.policy, c.desired, c.obs, c.sup, cfg, c.now)
			if got != c.want {
				t.Fatalf("action = %q, want %q", got, c.want)
			}
			if c.wantNext != nil && !c.wantNext(next) {
				t.Errorf("next SupState assertion failed: %+v", next)
			}
		})
	}
}

func TestDecideUnhealthy(t *testing.T) {
	cfg := DefaultConfig() // MaxAttempts 10, base 1s

	cases := []struct {
		name  string
		sup   SupState
		now   time.Time
		want  Action
		check func(SupState) bool
	}{
		{
			name: "below ceiling, no backoff → restart (counts, arms backoff)",
			sup:  SupState{Attempts: 2}, now: t0, want: ActionRestart,
			check: func(s SupState) bool { return s.Attempts == 3 && s.BackoffUntil.Equal(t0.Add(4*time.Second)) },
		},
		{
			name: "within backoff → none (no change)",
			sup:  SupState{Attempts: 2, BackoffUntil: t0.Add(2 * time.Second)}, now: t0, want: ActionNone,
			check: func(s SupState) bool { return s.Attempts == 2 && !s.GaveUp },
		},
		{
			// The exact case the consolidated sweep caught: a health-restart that
			// climbs to MaxAttempts must flip to the terminal give-up state.
			name: "at ceiling → give up (sets terminal)",
			sup:  SupState{Attempts: cfg.MaxAttempts}, now: t0, want: ActionGiveUp,
			check: func(s SupState) bool { return s.GaveUp },
		},
		{
			name: "already gave up → give up (idempotent)",
			sup:  SupState{GaveUp: true, Attempts: 4}, now: t0, want: ActionGiveUp,
			check: func(s SupState) bool { return s.GaveUp && s.Attempts == 4 },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			act, next := decideUnhealthy(c.sup, cfg, c.now)
			if act != c.want {
				t.Fatalf("action = %q, want %q", act, c.want)
			}
			if c.check != nil && !c.check(next) {
				t.Errorf("next state assertion failed: %+v", next)
			}
		})
	}
}

func TestBackoffProgressionAndCap(t *testing.T) {
	cfg := DefaultConfig() // base 1s, cap 60s
	want := map[int]time.Duration{
		1: 1 * time.Second,
		2: 2 * time.Second,
		3: 4 * time.Second,
		4: 8 * time.Second,
		5: 16 * time.Second,
		6: 32 * time.Second,
		7: 60 * time.Second, // 64 capped
		8: 60 * time.Second,
		20: 60 * time.Second,
	}
	for attempts, exp := range want {
		if got := backoffFor(attempts, cfg); got != exp {
			t.Errorf("backoffFor(%d) = %v, want %v", attempts, got, exp)
		}
	}
}

// TestDecideFullCrashLoop walks a container from first crash to give-up, then a
// manual recovery that resets the counter — the end-to-end state path.
func TestDecideFullCrashLoop(t *testing.T) {
	cfg := DefaultConfig()
	var sup SupState
	now := t0
	restarts := 0

	for i := 0; i < 100; i++ { // bounded; should give up well before
		act, next := decide(RestartAlways, DesiredRunning, Observed{Running: false}, sup, cfg, now)
		sup = next
		if act == ActionRestart {
			restarts++
			now = sup.BackoffUntil // jump the clock to exactly when the next restart is allowed
			continue
		}
		if act == ActionGiveUp {
			break
		}
		t.Fatalf("unexpected action %q at iter %d", act, i)
	}
	if restarts != cfg.MaxAttempts {
		t.Fatalf("restarts = %d, want %d before give-up", restarts, cfg.MaxAttempts)
	}
	if !sup.GaveUp {
		t.Fatalf("expected GaveUp after exhausting attempts")
	}

	// Manual recovery: container comes back and stays up past the stability window.
	act, next := decide(RestartAlways, DesiredRunning, Observed{Running: true, StartedAt: now.Add(-cfg.StabilityWindow)}, sup, cfg, now)
	if act != ActionNone {
		t.Errorf("recovered container action = %q, want none", act)
	}
	if next.Attempts != 0 || next.GaveUp {
		t.Errorf("recovery did not reset state: %+v", next)
	}
}
