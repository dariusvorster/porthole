package stacks

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/porthole/porthole/engine"
	"github.com/porthole/porthole/idlock"
)

func execWithStore(eng Engine, store SpecStore) *Executor {
	ex := NewExecutor(eng, idlock.New())
	ex.specs = store
	return ex
}

// findRunByImage returns the first RunContainer spec with the given image.
func findRunByImage(specs []engine.RunSpec, image string) (engine.RunSpec, bool) {
	for _, s := range specs {
		if s.Image == image {
			return s, true
		}
	}
	return engine.RunSpec{}, false
}

func TestSpecStore_RoundTripPreservesAllFields(t *testing.T) {
	s := testSQLiteStore(t)
	want := engine.RunSpec{
		Name: "shop-api", Image: "nginx:1.0",
		Entrypoint: "/custom-init", ShmSize: "64m", Tmpfs: []string{"/run", "/tmp"},
		Env: map[string]string{"FOO": "bar"}, CapAdd: []string{"NET_RAW"},
	}
	if err := s.SaveSpec("shop-api", want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetSpec("shop-api")
	if err != nil || !ok {
		t.Fatalf("GetSpec ok=%v err=%v", ok, err)
	}
	// the three fields inspect-reconstruction would lose:
	if got.Entrypoint != "/custom-init" || got.ShmSize != "64m" || len(got.Tmpfs) != 2 {
		t.Errorf("lossy round-trip: %+v", got)
	}
	if _, ok, _ := s.GetSpec("absent"); ok {
		t.Error("absent spec should report ok=false")
	}
}

func TestSpecStore_MigrationOnExistingDB(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	// Simulate a pre-Phase-10b DB: only the stacks table exists.
	if _, err := db.Exec(`CREATE TABLE stacks (name TEXT PRIMARY KEY, compose_yaml TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	s, err := NewSQLiteStore(db) // migration must add container_specs cleanly
	if err != nil {
		t.Fatalf("migration failed on existing DB: %v", err)
	}
	if err := s.SaveSpec("x", engine.RunSpec{Name: "x", Image: "nginx"}); err != nil {
		t.Errorf("spec table not usable after migration: %v", err)
	}
}

func TestUpPersistsSpec(t *testing.T) {
	eng := newRemEngine()
	store := NewMemStore()
	ex := execWithStore(eng, store)
	if _, err := ex.Up(context.Background(), oneSvcStack("nginx:1.0")); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.GetSpec("shop-api"); !ok {
		t.Error("Up should persist the member's RunSpec")
	}
}

func TestRollback_PrefersStoredSpec_ByptePerfect(t *testing.T) {
	eng := newRemEngine()
	eng.seed(member1("shop-api", "api", "nginx:1.0")) // drifted, running
	store := NewMemStore()
	// the authoritative spec carries fields inspect can't recover:
	store.SaveSpec("shop-api", engine.RunSpec{
		Name: "shop-api", Image: "nginx:1.0",
		Entrypoint: "/custom-init", ShmSize: "64m", Tmpfs: []string{"/run"},
	})
	ex := execWithStore(eng, store)
	eng.failImage["nginx:bogus"] = true // new create fails → rollback

	res, _ := ex.Remediate(context.Background(), oneSvcStack("nginx:bogus"))
	if len(res.Applied) != 1 || res.Applied[0].Outcome != OutcomeRolledBack {
		t.Fatalf("expected rolled_back, got %+v", res.Applied)
	}
	// the rollback re-created from the STORED spec — all three fields present.
	roll, ok := findRunByImage(eng.runSpecs, "nginx:1.0")
	if !ok {
		t.Fatal("rollback did not re-create the previous image")
	}
	if roll.Entrypoint != "/custom-init" || roll.ShmSize != "64m" || len(roll.Tmpfs) != 1 {
		t.Errorf("byte-perfect rollback lost fields: %+v", roll)
	}
}

func TestRollback_FallsBackToInspect_WhenNoStoredSpec(t *testing.T) {
	eng := newRemEngine()
	eng.seed(member1("shop-api", "api", "nginx:1.0"))
	ex := NewExecutor(eng, idlock.New()) // no spec store → reconstruction fallback
	eng.failImage["nginx:bogus"] = true

	res, _ := ex.Remediate(context.Background(), oneSvcStack("nginx:bogus"))
	if len(res.Applied) != 1 || res.Applied[0].Outcome != OutcomeRolledBack {
		t.Fatalf("fallback must still roll back, got %+v", res.Applied)
	}
	// reconstruction can't recover entrypoint/shm/tmpfs — proves it took that path.
	roll, ok := findRunByImage(eng.runSpecs, "nginx:1.0")
	if !ok {
		t.Fatal("rollback did not re-create the previous image")
	}
	if roll.Entrypoint != "" || roll.ShmSize != "" || len(roll.Tmpfs) != 0 {
		t.Errorf("expected reconstruction (no entrypoint/shm/tmpfs), got %+v", roll)
	}
}
