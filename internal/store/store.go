// Package store is the SQLite-backed persistence layer for Gridwell.
//
// Single-tenant: there is no user/auth model. The store owns one root grid
// (looked up via the `system` table) and exposes spatial CRUD over a tree
// of grids and tiles.
//
// All mutating methods run inside a single transaction so refcount and tree
// invariants stay consistent under concurrent callers. The store publishes
// events to subscribers when grids change so the server's Subscribe stream
// can fan them out.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Sentinel errors. Callers should use errors.Is to test for them.
var (
	ErrNotFound            = errors.New("not found")
	ErrOverlap             = errors.New("footprint overlaps an existing node")
	ErrLocality            = errors.New("affected node is outside the framed view")
	ErrInvalidPath         = errors.New("descent path is invalid")
	ErrInvalidArgument     = errors.New("invalid argument")
	ErrUnsupportedMime     = errors.New("unsupported mime type")
	ErrChromiumUnavailable = errors.New("chromium unavailable")
	ErrNotURLTile          = errors.New("not a URL tile")
)

// Store wraps a SQLite database. It is safe for concurrent use.
type Store struct {
	db        *sql.DB
	now       func() time.Time // overridden in tests
	newID     func() string    // overridden in tests
	mu        sync.Mutex       // protects subscriber list
	subs      map[*subscriber]struct{}
	urlDriver URLDriver // set via SetURLDriver; nil means Chromium-unavailable
}

const systemKeyRootGridID = "root_grid_id"

// Open opens a SQLite database at the given path, applies the schema, and
// bootstraps the root grid if it doesn't already exist. Use ":memory:" for
// in-test stores.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	// SQLite is single-writer at the file level; one connection eliminates
	// contention and gives deterministic transaction interleaving.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(Schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	s := &Store{
		db:    db,
		now:   time.Now,
		newID: newUUID,
		subs:  map[*subscriber]struct{}{},
	}
	if err := s.bootstrapRoot(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("bootstrap root: %w", err)
	}
	return s, nil
}

// bootstrapRoot inserts the initial root grid if none exists. Idempotent.
func (s *Store) bootstrapRoot(ctx context.Context) error {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM system WHERE key = ?`, systemKeyRootGridID).Scan(&v)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		now := s.now().Unix()
		objID := s.newID()
		res, err := tx.ExecContext(ctx,
			`INSERT INTO grids (object_id, refcount, created_at) VALUES (?, 1, ?)`,
			objID, now)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO system (key, value) VALUES (?, ?)`,
			systemKeyRootGridID, strconv.FormatInt(id, 10))
		return err
	})
}

// RootGridID returns the current root grid id.
func (s *Store) RootGridID(ctx context.Context) (int64, error) {
	return rootGridID(ctx, s.db)
}

func rootGridID(ctx context.Context, q queryRower) (int64, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM system WHERE key = ?`, systemKeyRootGridID).Scan(&v)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

func setRootGridID(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE system SET value = ? WHERE key = ?`,
		strconv.FormatInt(id, 10), systemKeyRootGridID)
	return err
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying *sql.DB. Tests may use it; production code should
// not.
func (s *Store) DB() *sql.DB {
	return s.db
}

// SetClock overrides the time source. Used by tests.
func (s *Store) SetClock(now func() time.Time) {
	s.now = now
}

// SetIDGenerator overrides UUID generation. Used by tests for deterministic
// object_ids.
func (s *Store) SetIDGenerator(f func() string) {
	s.newID = f
}

// SetURLDriver installs the URL-tile driver. Without one, all URL-tile
// runtime operations (WakeURL, CaptureURL, URLStream open) return
// ErrChromiumUnavailable. The driver is typically Chromium-backed in
// production and a fake in tests.
func (s *Store) SetURLDriver(d URLDriver) {
	s.urlDriver = d
}

// withTx runs fn inside a transaction.
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// queryRower is satisfied by both *sql.DB and *sql.Tx.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
