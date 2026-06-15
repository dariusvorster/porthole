// Package supervisor adds restart policies and health checks on top of the
// read+write console. `container` itself has neither; portholed, a long-running
// launchd agent, supplies them. This file is the persistent state layer only —
// the decision logic (P2) and loop (P3) build on it.
//
// Settled constraints (see docs/porthole-supervision-spec.md §9):
//   - restart policies: no / always / unless-stopped (NO on-failure — the runtime
//     exposes no exit code).
//   - policy truth is this store, keyed by container id; a porthole.restart label
//     is a durable mirror written at create-through-Porthole.
package supervisor

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo → single-binary build)
)

// RestartPolicy is the durable restart intent. on-failure is intentionally absent.
type RestartPolicy string

const (
	RestartNo            RestartPolicy = "no"
	RestartAlways        RestartPolicy = "always"
	RestartUnlessStopped RestartPolicy = "unless-stopped"
)

// DesiredState is the mutable runtime intent, set by mediated start/stop.
type DesiredState string

const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
)

// HealthPolicy configures an HTTP/TCP probe. nil Policy.Health means no probe.
// Exec-based health is deferred (no exec path yet — spec §5).
type HealthPolicy struct {
	Type        string `json:"type"`                  // "http" | "tcp"
	Port        int    `json:"port"`                  // container port to probe
	Path        string `json:"path,omitempty"`        // http only
	Interval    int    `json:"interval,omitempty"`    // seconds (default 30)
	Timeout     int    `json:"timeout,omitempty"`     // seconds (default 5)
	Retries     int    `json:"retries,omitempty"`     // consecutive fails -> unhealthy (default 3)
	StartPeriod int    `json:"startPeriod,omitempty"` // grace seconds (default 10)
	OnUnhealthy string `json:"onUnhealthy,omitempty"` // "restart" | "" (informational)
}

// Policy is the per-container supervision config. Keyed by container id (== name
// for named containers, a UUID for unnamed — both stable; spec §9.4).
type Policy struct {
	ContainerID string        `json:"containerId"`
	Restart     RestartPolicy `json:"restart"`
	Health      *HealthPolicy `json:"health,omitempty"`
	UpdatedAt   time.Time     `json:"updatedAt"`
}

// Store persists policies and desired-state. Two homes for two distinct things:
// policy is durable intent; desired-state is the running/stopped intent that
// changes on every mediated start/stop.
type Store interface {
	GetPolicy(id string) (Policy, bool, error)
	SetPolicy(p Policy) error
	ListPolicies() ([]Policy, error)

	GetDesired(id string) (DesiredState, bool, error)
	SetDesired(id string, d DesiredState) error

	// DeletePolicy / DeleteDesired remove a container's rows (called on
	// container.removed so a reused name never inherits stale policy).
	DeletePolicy(id string) error
	DeleteDesired(id string) error

	// Cumulative lifetime supervision-restart count, persisted (PF3 / F7). This is
	// distinct from the transient in-memory backoff-attempt counter: it is bumped
	// once per successful supervision restart and survives stabilization + restarts
	// of portholed, so the "restarted N times" badge doesn't vanish.
	BumpRestartCount(id string) error
	GetRestartCount(id string) (int, error)
	DeleteRestartCount(id string) error

	Close() error
}

// DefaultDBPath returns ~/Library/Application Support/porthole/porthole.db on
// macOS (os.UserConfigDir is Application Support there).
func DefaultDBPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "porthole", "porthole.db"), nil
}

// SQLiteStore is the on-disk Store backed by the pure-Go sqlite driver.
type SQLiteStore struct {
	db *sql.DB
}

var _ Store = (*SQLiteStore)(nil)

// OpenSQLite opens (creating dirs + schema as needed) the db at path.
func OpenSQLite(path string) (*SQLiteStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // sqlite: serialize writers, avoid "database is locked"
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA busy_timeout=5000;
		CREATE TABLE IF NOT EXISTS policy (
			container_id TEXT PRIMARY KEY,
			restart      TEXT NOT NULL,
			health_json  TEXT NOT NULL DEFAULT '',
			updated_at   TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS desired_state (
			container_id TEXT PRIMARY KEY,
			desired      TEXT NOT NULL,
			updated_at   TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS restart_count (
			container_id TEXT PRIMARY KEY,
			count        INTEGER NOT NULL
		);
	`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) BumpRestartCount(id string) error {
	_, err := s.db.Exec(`
		INSERT INTO restart_count (container_id, count) VALUES (?, 1)
		ON CONFLICT(container_id) DO UPDATE SET count = count + 1
	`, id)
	return err
}

func (s *SQLiteStore) GetRestartCount(id string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count FROM restart_count WHERE container_id = ?`, id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

func (s *SQLiteStore) DeleteRestartCount(id string) error {
	_, err := s.db.Exec(`DELETE FROM restart_count WHERE container_id = ?`, id)
	return err
}

// DB exposes the underlying handle so other subsystems (e.g. the Stacks store)
// can share the single connection pool / WAL writer instead of opening a second
// handle to the same file.
func (s *SQLiteStore) DB() *sql.DB { return s.db }

func encodeHealth(h *HealthPolicy) (string, error) {
	if h == nil {
		return "", nil
	}
	b, err := json.Marshal(h)
	return string(b), err
}

func decodeHealth(s string) (*HealthPolicy, error) {
	if s == "" {
		return nil, nil
	}
	var h HealthPolicy
	if err := json.Unmarshal([]byte(s), &h); err != nil {
		return nil, err
	}
	return &h, nil
}

func (s *SQLiteStore) GetPolicy(id string) (Policy, bool, error) {
	var restart, healthJSON, updated string
	err := s.db.QueryRow(
		`SELECT restart, health_json, updated_at FROM policy WHERE container_id = ?`, id,
	).Scan(&restart, &healthJSON, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Policy{}, false, nil
	}
	if err != nil {
		return Policy{}, false, err
	}
	health, err := decodeHealth(healthJSON)
	if err != nil {
		return Policy{}, false, err
	}
	ts, _ := time.Parse(time.RFC3339, updated)
	return Policy{ContainerID: id, Restart: RestartPolicy(restart), Health: health, UpdatedAt: ts}, true, nil
}

func (s *SQLiteStore) SetPolicy(p Policy) error {
	healthJSON, err := encodeHealth(p.Health)
	if err != nil {
		return err
	}
	if p.UpdatedAt.IsZero() {
		// Caller didn't stamp it; leave as zero so callers control the clock.
	}
	updated := p.UpdatedAt.Format(time.RFC3339)
	_, err = s.db.Exec(`
		INSERT INTO policy (container_id, restart, health_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(container_id) DO UPDATE SET
			restart=excluded.restart, health_json=excluded.health_json, updated_at=excluded.updated_at
	`, p.ContainerID, string(p.Restart), healthJSON, updated)
	return err
}

func (s *SQLiteStore) ListPolicies() ([]Policy, error) {
	rows, err := s.db.Query(`SELECT container_id, restart, health_json, updated_at FROM policy`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Policy
	for rows.Next() {
		var id, restart, healthJSON, updated string
		if err := rows.Scan(&id, &restart, &healthJSON, &updated); err != nil {
			return nil, err
		}
		health, err := decodeHealth(healthJSON)
		if err != nil {
			return nil, err
		}
		ts, _ := time.Parse(time.RFC3339, updated)
		out = append(out, Policy{ContainerID: id, Restart: RestartPolicy(restart), Health: health, UpdatedAt: ts})
	}
	return out, rows.Err()
}

func (s *SQLiteStore) DeletePolicy(id string) error {
	_, err := s.db.Exec(`DELETE FROM policy WHERE container_id = ?`, id)
	return err
}

func (s *SQLiteStore) DeleteDesired(id string) error {
	_, err := s.db.Exec(`DELETE FROM desired_state WHERE container_id = ?`, id)
	return err
}

func (s *SQLiteStore) GetDesired(id string) (DesiredState, bool, error) {
	var desired string
	err := s.db.QueryRow(`SELECT desired FROM desired_state WHERE container_id = ?`, id).Scan(&desired)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return DesiredState(desired), true, nil
}

func (s *SQLiteStore) SetDesired(id string, d DesiredState) error {
	_, err := s.db.Exec(`
		INSERT INTO desired_state (container_id, desired, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(container_id) DO UPDATE SET desired=excluded.desired, updated_at=excluded.updated_at
	`, id, string(d), time.Now().UTC().Format(time.RFC3339))
	return err
}
