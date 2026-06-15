package stacks

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	s, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func runStoreContract(t *testing.T, s Store) {
	if _, ok, err := s.GetStack("nope"); err != nil || ok {
		t.Fatalf("missing get: ok=%v err=%v", ok, err)
	}
	if err := s.SaveStack(Record{Name: "shop", ComposeYAML: "services: {}"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetStack("shop")
	if err != nil || !ok {
		t.Fatalf("get after save: ok=%v err=%v", ok, err)
	}
	if got.ComposeYAML != "services: {}" {
		t.Errorf("yaml = %q", got.ComposeYAML)
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at not stamped")
	}
	created := got.CreatedAt

	// Update preserves created_at, refreshes updated_at.
	time.Sleep(1100 * time.Millisecond)
	if err := s.SaveStack(Record{Name: "shop", ComposeYAML: "services: {web: {}}"}); err != nil {
		t.Fatal(err)
	}
	got2, _, _ := s.GetStack("shop")
	if got2.ComposeYAML != "services: {web: {}}" {
		t.Errorf("update yaml = %q", got2.ComposeYAML)
	}
	if !got2.CreatedAt.Equal(created) {
		t.Errorf("created_at changed on update: %v -> %v", created, got2.CreatedAt)
	}

	if err := s.SaveStack(Record{Name: "other", ComposeYAML: "x"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListStacks()
	if err != nil || len(list) != 2 {
		t.Fatalf("list: n=%d err=%v", len(list), err)
	}

	if err := s.DeleteStack("shop"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetStack("shop"); ok {
		t.Error("shop should be deleted")
	}
}

func TestSQLiteStoreContract(t *testing.T) { runStoreContract(t, testSQLiteStore(t)) }
func TestMemStoreContract(t *testing.T)    { runStoreContract(t, NewMemStore()) }
