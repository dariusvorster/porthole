package supervisor

import (
	"path/filepath"
	"testing"
	"time"
)

// storeFactory builds a fresh Store and a cleanup. Both impls run the same suite.
type storeFactory struct {
	name string
	open func(t *testing.T) Store
}

func factories() []storeFactory {
	return []storeFactory{
		{"mem", func(*testing.T) Store { return NewMemStore() }},
		{"sqlite", func(t *testing.T) Store {
			path := filepath.Join(t.TempDir(), "porthole.db")
			s, err := OpenSQLite(path)
			if err != nil {
				t.Fatalf("OpenSQLite: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		}},
	}
}

func TestStorePolicyRoundTrip(t *testing.T) {
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			s := f.open(t)

			// Missing → found=false, no error.
			if _, ok, err := s.GetPolicy("web"); err != nil || ok {
				t.Fatalf("GetPolicy(missing) = ok %v, err %v; want false, nil", ok, err)
			}

			now := time.Now().UTC().Truncate(time.Second)
			p := Policy{
				ContainerID: "web",
				Restart:     RestartAlways,
				Health:      &HealthPolicy{Type: "http", Port: 80, Path: "/", Interval: 30, Retries: 3, OnUnhealthy: "restart"},
				UpdatedAt:   now,
			}
			if err := s.SetPolicy(p); err != nil {
				t.Fatalf("SetPolicy: %v", err)
			}

			got, ok, err := s.GetPolicy("web")
			if err != nil || !ok {
				t.Fatalf("GetPolicy = ok %v, err %v; want true, nil", ok, err)
			}
			if got.Restart != RestartAlways {
				t.Errorf("restart = %q, want always", got.Restart)
			}
			if got.Health == nil || got.Health.Type != "http" || got.Health.Port != 80 || got.Health.OnUnhealthy != "restart" {
				t.Errorf("health round-trip wrong: %+v", got.Health)
			}
			if !got.UpdatedAt.Equal(now) {
				t.Errorf("updatedAt = %v, want %v", got.UpdatedAt, now)
			}

			// Update in place (no health this time).
			if err := s.SetPolicy(Policy{ContainerID: "web", Restart: RestartUnlessStopped, UpdatedAt: now}); err != nil {
				t.Fatalf("SetPolicy update: %v", err)
			}
			got, _, _ = s.GetPolicy("web")
			if got.Restart != RestartUnlessStopped {
				t.Errorf("after update restart = %q, want unless-stopped", got.Restart)
			}
			if got.Health != nil {
				t.Errorf("health should be nil after clearing, got %+v", got.Health)
			}

			// A second policy, then ListPolicies returns both.
			if err := s.SetPolicy(Policy{ContainerID: "api", Restart: RestartNo, UpdatedAt: now}); err != nil {
				t.Fatalf("SetPolicy api: %v", err)
			}
			list, err := s.ListPolicies()
			if err != nil {
				t.Fatalf("ListPolicies: %v", err)
			}
			if len(list) != 2 {
				t.Errorf("ListPolicies len = %d, want 2", len(list))
			}
		})
	}
}

func TestStoreDesiredRoundTrip(t *testing.T) {
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			s := f.open(t)

			if _, ok, err := s.GetDesired("web"); err != nil || ok {
				t.Fatalf("GetDesired(missing) = ok %v, err %v; want false, nil", ok, err)
			}
			if err := s.SetDesired("web", DesiredStopped); err != nil {
				t.Fatalf("SetDesired: %v", err)
			}
			got, ok, err := s.GetDesired("web")
			if err != nil || !ok {
				t.Fatalf("GetDesired = ok %v, err %v; want true, nil", ok, err)
			}
			if got != DesiredStopped {
				t.Errorf("desired = %q, want stopped", got)
			}
			// Flip it.
			if err := s.SetDesired("web", DesiredRunning); err != nil {
				t.Fatalf("SetDesired flip: %v", err)
			}
			if got, _, _ := s.GetDesired("web"); got != DesiredRunning {
				t.Errorf("desired after flip = %q, want running", got)
			}
		})
	}
}

func TestSQLitePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "porthole.db")
	now := time.Now().UTC().Truncate(time.Second)

	s1, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("open1: %v", err)
	}
	if err := s1.SetPolicy(Policy{ContainerID: "web", Restart: RestartAlways, UpdatedAt: now}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s1.SetDesired("web", DesiredStopped); err != nil {
		t.Fatalf("setDesired: %v", err)
	}
	_ = s1.Close()

	s2, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("open2: %v", err)
	}
	defer s2.Close()
	p, ok, _ := s2.GetPolicy("web")
	if !ok || p.Restart != RestartAlways {
		t.Errorf("policy not persisted across reopen: ok=%v %+v", ok, p)
	}
	if d, ok, _ := s2.GetDesired("web"); !ok || d != DesiredStopped {
		t.Errorf("desired not persisted across reopen: ok=%v %q", ok, d)
	}
}
