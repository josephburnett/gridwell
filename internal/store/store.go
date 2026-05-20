// Package store is the SQLite-backed persistence layer for Gridwell.
//
// All mutating methods run inside a single transaction so refcount and tree
// invariants stay consistent under concurrent callers. The store also
// publishes events to subscribers when grids change so the server's Subscribe
// stream can fan them out to clients.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Sentinel errors. Callers should use errors.Is to test for them.
var (
	ErrNotFound            = errors.New("not found")
	ErrPermissionDenied    = errors.New("permission denied")
	ErrOverlap             = errors.New("footprint overlaps an existing node")
	ErrLocality            = errors.New("affected node is outside the framed view")
	ErrInvalidPath         = errors.New("descent path is invalid")
	ErrInvalidArgument     = errors.New("invalid argument")
	ErrUnsupportedMime     = errors.New("unsupported mime type")
	ErrSessionExpired      = errors.New("session expired")
	ErrChromiumUnavailable = errors.New("chromium unavailable")
	ErrNotURLTile          = errors.New("not a URL tile")
)

// Store wraps a SQLite database. It is safe for concurrent use.
//
// SQLite serializes writers anyway, so the store does not add a process-wide
// lock; readers can run concurrently with each other.
type Store struct {
	db         *sql.DB
	now        func() time.Time // overridden in tests
	newID      func() string    // overridden in tests
	hashParams *passwordHashParams
	mu         sync.Mutex // protects subscriber list
	subs       map[*subscriber]struct{}
	urlDriver  URLDriver // set via SetURLDriver; nil means Chromium-unavailable
}

// passwordHashParams is a thin wrapper so we don't import auth into the
// public API of the store. Nil means "use defaults".
type passwordHashParams struct {
	Time    uint32
	Memory  uint32
	Threads uint8
	KeyLen  uint32
	SaltLen uint32
}

// Open opens a SQLite database at the given path and applies the schema. Use
// ":memory:" for in-test stores. Connection pool is capped at 1 because
// SQLite write-locks the file and we want predictable transaction ordering.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	// SQLite is single-writer at the file level; multiple Go connections
	// would just contend on the file lock. One connection eliminates that
	// contention and gives us deterministic transaction interleaving.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(Schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	return &Store{
		db:    db,
		now:   time.Now,
		newID: newUUID,
		subs:  map[*subscriber]struct{}{},
	}, nil
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

// UseFastHashing switches password hashing to a much cheaper parameter set,
// suitable only for tests. This must not be called in production: the
// resulting hashes are not safe for storage.
func (s *Store) UseFastHashing() {
	s.hashParams = &passwordHashParams{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 16, SaltLen: 8}
}

// SetURLDriver installs the URL-tile driver. Without one, all URL-tile
// runtime operations (WakeURL, CaptureURL, URLStream open) return
// ErrChromiumUnavailable. The driver is typically Chromium-backed in
// production and a fake in tests.
func (s *Store) SetURLDriver(d URLDriver) {
	s.urlDriver = d
}

// withTx runs fn inside a transaction. Commit on success, rollback on error.
// fn returns its own value via the closure (Go cannot return generics from
// a method here without forcing a type parameter on Store).
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
