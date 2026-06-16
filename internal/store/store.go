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

	"github.com/josephburnett/gridwell/internal/rpc"

	_ "modernc.org/sqlite"
)

// Sentinel errors. Callers should use errors.Is to test for them.
var (
	ErrNotFound        = errors.New("not found")
	ErrOverlap         = errors.New("footprint overlaps an existing tile")
	ErrInvalidPath     = errors.New("descent path is invalid")
	ErrInvalidArgument = errors.New("invalid argument")
	ErrNotURLTile      = errors.New("not a URL tile")
	ErrNotTextTile     = errors.New("not a text tile")
	ErrNotWellTile     = errors.New("not a well tile")
	ErrNotShellTile    = errors.New("not a shell tile")
	ErrVersionConflict = errors.New("version mismatch")
)

// Store wraps a SQLite database. It is safe for concurrent use.
type Store struct {
	db         *sql.DB
	now        func() time.Time // overridden in tests
	newID      func() string    // overridden in tests
	mu         sync.Mutex       // protects subscriber list
	subs       map[*subscriber]struct{}
	fsReader   FSReader
	procReader ProcReader
	procRoot   string // path to /proc (overridden by tests)
	host       HostActor
}

const (
	systemKeyRootGridID = "root_grid_id"
	systemKeyRootViewCx = "root_view_cx"
	systemKeyRootViewCy = "root_view_cy"
	systemKeyRootZoom   = "root_zoom"
)

// Open opens a SQLite database at the given path, applies the schema, and
// bootstraps the root grid if it doesn't already exist. Use ":memory:" for
// in-test stores.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	// SQLite is single-writer at the file level; one connection eliminates
	// contention and gives deterministic transaction interleaving. It also
	// keeps the attached cache database (below) bound to this one connection.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(pragmas); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply pragmas: %w", err)
	}
	if _, err := db.Exec(systemDDL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply system schema: %w", err)
	}
	if _, err := db.Exec(tablesDDL("")); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	// Attach the ephemeral cache database beside the durable main one. The
	// main file stays independently copyable as an archive; projected host
	// state (fs/proc grids + their arrangement) lives in the cache file.
	if err := attachCache(context.Background(), db, path); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{
		db:         db,
		now:        time.Now,
		newID:      newUUID,
		subs:       map[*subscriber]struct{}{},
		fsReader:   realFSReader{},
		procReader: realProcReader{},
		procRoot:   "/proc",
		host:       realHostActor{},
	}
	if err := s.bootstrapRoot(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("bootstrap root: %w", err)
	}
	if err := s.applyMigrations(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	// Re-resolve durable exit wells against the (possibly freshly-created)
	// cache database, so the canvas opens correctly even when the cache file
	// is absent — the archival case.
	if err := s.rebindExitWells(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("rebind exit wells: %w", err)
	}
	return s, nil
}

// bootstrapRoot inserts the initial root grid if none exists and seeds the
// root viewport keys. Idempotent.
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
			`INSERT INTO grids (object_id, created_at, updated_at) VALUES (?, ?, ?)`,
			objID, now, now)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		seeds := []struct{ k, v string }{
			{systemKeyRootGridID, strconv.FormatInt(id, 10)},
			{systemKeyRootViewCx, "0"},
			{systemKeyRootViewCy, "0"},
			{systemKeyRootZoom, "1"},
		}
		for _, kv := range seeds {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO system (key, value) VALUES (?, ?)`,
				kv.k, kv.v); err != nil {
				return err
			}
		}
		return nil
	})
}

// RootGridID returns the current root grid id.
func (s *Store) RootGridID(ctx context.Context) (int64, error) {
	return rootGridID(ctx, s.db)
}

// RootView returns the persisted root viewport: center (cx, cy) and zoom.
func (s *Store) RootView(ctx context.Context) (cx, cy, zoom float64, err error) {
	cx, err = readFloatKey(ctx, s.db, systemKeyRootViewCx)
	if err != nil {
		return 0, 0, 0, err
	}
	cy, err = readFloatKey(ctx, s.db, systemKeyRootViewCy)
	if err != nil {
		return 0, 0, 0, err
	}
	zoom, err = readFloatKey(ctx, s.db, systemKeyRootZoom)
	if err != nil {
		return 0, 0, 0, err
	}
	return cx, cy, zoom, nil
}

// SetRootView writes the root framing to the system KV table and emits a
// GridChanged for the current root grid.
func (s *Store) SetRootView(ctx context.Context, req *rpc.SetRootViewRequest) error {
	var rootID int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		rootID, err = rootGridID(ctx, tx)
		if err != nil {
			return err
		}
		writes := []struct {
			k string
			v string
		}{
			{systemKeyRootViewCx, strconv.FormatFloat(req.Cx, 'f', -1, 64)},
			{systemKeyRootViewCy, strconv.FormatFloat(req.Cy, 'f', -1, 64)},
			{systemKeyRootZoom, strconv.FormatFloat(req.Zoom, 'f', -1, 64)},
		}
		for _, w := range writes {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO system (key, value) VALUES (?, ?)
				 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
				w.k, w.v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publish(rpc.Event{Kind: rpc.EventGridChanged, GridChanged: &rpc.GridChanged{GridID: rootID}})
	return nil
}

func rootGridID(ctx context.Context, q gridReader) (int64, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM system WHERE key = ?`, systemKeyRootGridID).Scan(&v)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

func readFloatKey(ctx context.Context, q gridReader, key string) (float64, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM system WHERE key = ?`, key).Scan(&v)
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(v, 64)
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
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

// withMutation runs fn in a transaction. fn appends to the provided events
// slice; on commit, withMutation publishes them in order.
func (s *Store) withMutation(ctx context.Context, fn func(tx *sql.Tx, events *[]rpc.Event) error) error {
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error { return fn(tx, &events) })
	if err != nil {
		return err
	}
	for _, ev := range events {
		s.publish(ev)
	}
	return nil
}
