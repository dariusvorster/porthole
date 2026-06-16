package stacks

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/porthole/porthole/engine"
)

// Record is a stored stack: its name (PRIMARY KEY) and the raw compose YAML that
// is its desired state. The DB holds the file; labels prove membership; `ls` is
// observed truth (spec §5) — so a Record is re-parsed on read, never trusted as
// the live shape.
type Record struct {
	Name        string    `json:"name"`
	ComposeYAML string    `json:"composeYaml"`
	Discovery   bool      `json:"discovery"` // service discovery opt-in (Phase 8); DB is working truth
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Store persists stack definitions. Reuses the supervision SQLite database (a
// separate `stacks` table) in production; MemStore backs tests.
//
// It also persists the authoritative RunSpec Porthole builds at create time,
// keyed by container name (Phase 10b), so drift-remediation rollback is byte-
// perfect rather than reconstructed from `inspect`. A container with no stored
// spec falls back to reconstruction — so this is purely additive.
type Store interface {
	SaveStack(r Record) error
	GetStack(name string) (Record, bool, error)
	ListStacks() ([]Record, error)
	DeleteStack(name string) error

	SaveSpec(name string, spec engine.RunSpec) error
	GetSpec(name string) (engine.RunSpec, bool, error)
	DeleteSpec(name string) error
}

// SQLiteStore is the on-disk Store. It shares the *sql.DB opened by the
// supervisor so there is a single connection pool / WAL writer.
type SQLiteStore struct {
	db *sql.DB
}

var _ Store = (*SQLiteStore)(nil)

// NewSQLiteStore creates the stacks table on an existing database handle.
func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS stacks (
			name         TEXT PRIMARY KEY,
			compose_yaml TEXT NOT NULL,
			created_at   TEXT NOT NULL,
			updated_at   TEXT NOT NULL
		);
	`); err != nil {
		return nil, err
	}
	// Migration: add the discovery column to a pre-Phase-8 table. CREATE IF NOT
	// EXISTS won't add columns, so ALTER and ignore the "duplicate column" error.
	if _, err := db.Exec(`ALTER TABLE stacks ADD COLUMN discovery INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return nil, err
	}
	// Phase 10b: the per-container authoritative RunSpec for byte-perfect rollback.
	// A new table, so a plain CREATE IF NOT EXISTS migrates an existing DB cleanly.
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS container_specs (
			name       TEXT PRIMARY KEY,
			spec       TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
	`); err != nil {
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

// SaveSpec stores (upserts) the authoritative RunSpec for a container name.
func (s *SQLiteStore) SaveSpec(name string, spec engine.RunSpec) error {
	b, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO container_specs (name, spec, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET spec = excluded.spec, updated_at = excluded.updated_at
	`, name, string(b), time.Now().UTC().Format(time.RFC3339))
	return err
}

// GetSpec returns the stored RunSpec for a container name, ok=false if absent.
func (s *SQLiteStore) GetSpec(name string) (engine.RunSpec, bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT spec FROM container_specs WHERE name = ?`, name).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.RunSpec{}, false, nil
	}
	if err != nil {
		return engine.RunSpec{}, false, err
	}
	var spec engine.RunSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return engine.RunSpec{}, false, err
	}
	return spec, true, nil
}

func (s *SQLiteStore) DeleteSpec(name string) error {
	_, err := s.db.Exec(`DELETE FROM container_specs WHERE name = ?`, name)
	return err
}

func (s *SQLiteStore) SaveStack(r Record) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = time.Now().UTC()
	}
	// Preserve the original created_at on update; only refresh updated_at.
	_, err := s.db.Exec(`
		INSERT INTO stacks (name, compose_yaml, discovery, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			compose_yaml = excluded.compose_yaml,
			discovery    = excluded.discovery,
			updated_at   = excluded.updated_at
	`, r.Name, r.ComposeYAML, boolToInt(r.Discovery), r.CreatedAt.Format(time.RFC3339), r.UpdatedAt.Format(time.RFC3339))
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *SQLiteStore) GetStack(name string) (Record, bool, error) {
	var yaml, created, updated string
	var disc int
	err := s.db.QueryRow(
		`SELECT compose_yaml, discovery, created_at, updated_at FROM stacks WHERE name = ?`, name,
	).Scan(&yaml, &disc, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	c, _ := time.Parse(time.RFC3339, created)
	u, _ := time.Parse(time.RFC3339, updated)
	return Record{Name: name, ComposeYAML: yaml, Discovery: disc != 0, CreatedAt: c, UpdatedAt: u}, true, nil
}

func (s *SQLiteStore) ListStacks() ([]Record, error) {
	rows, err := s.db.Query(`SELECT name, compose_yaml, discovery, created_at, updated_at FROM stacks ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var name, yaml, created, updated string
		var disc int
		if err := rows.Scan(&name, &yaml, &disc, &created, &updated); err != nil {
			return nil, err
		}
		c, _ := time.Parse(time.RFC3339, created)
		u, _ := time.Parse(time.RFC3339, updated)
		out = append(out, Record{Name: name, ComposeYAML: yaml, Discovery: disc != 0, CreatedAt: c, UpdatedAt: u})
	}
	return out, rows.Err()
}

func (s *SQLiteStore) DeleteStack(name string) error {
	_, err := s.db.Exec(`DELETE FROM stacks WHERE name = ?`, name)
	return err
}

// MemStore is an in-memory Store for tests.
type MemStore struct {
	mu    sync.Mutex
	m     map[string]Record
	specs map[string]engine.RunSpec
}

var _ Store = (*MemStore)(nil)

func NewMemStore() *MemStore {
	return &MemStore{m: map[string]Record{}, specs: map[string]engine.RunSpec{}}
}

func (s *MemStore) SaveSpec(name string, spec engine.RunSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.specs[name] = spec
	return nil
}

func (s *MemStore) GetSpec(name string) (engine.RunSpec, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	spec, ok := s.specs[name]
	return spec, ok, nil
}

func (s *MemStore) DeleteSpec(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.specs, name)
	return nil
}

func (s *MemStore) SaveStack(r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.m[r.Name]; ok {
		r.CreatedAt = existing.CreatedAt // preserve original creation time
	} else if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = time.Now().UTC()
	}
	s.m[r.Name] = r
	return nil
}

func (s *MemStore) GetStack(name string) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.m[name]
	return r, ok, nil
}

func (s *MemStore) ListStacks() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.m))
	for _, r := range s.m {
		out = append(out, r)
	}
	return out, nil
}

func (s *MemStore) DeleteStack(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, name)
	return nil
}
