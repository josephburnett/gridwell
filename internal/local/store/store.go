// Package store is the SQLite-backed persistence layer for Gridwell: one root
// grid, named in the `system` table, over a tree of grids and tiles. Every
// mutating method runs in one transaction, so refcount and tree invariants
// hold under concurrent callers, and publishes events for Subscribe to fan
// out.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/eventhub"

	_ "modernc.org/sqlite"
)

// Sentinel errors. api/gwerr owns the vocabulary; these are the store's
// call-site names, with errors.Is identity preserved.
var (
	ErrNotFound        = gwerr.ErrNotFound
	ErrOverlap         = gwerr.ErrOverlap
	ErrInvalidPath     = gwerr.ErrInvalidPath
	ErrInvalidArgument = gwerr.ErrInvalidArgument
	ErrNotURLTile      = gwerr.ErrNotURLTile
	ErrNotTextTile     = gwerr.ErrNotTextTile
	ErrNotWellTile     = gwerr.ErrNotWellTile
	ErrNotShellTile    = gwerr.ErrNotShellTile
	ErrNotPaneTile     = gwerr.ErrNotPaneTile
	ErrVersionConflict = gwerr.ErrVersionConflict
)

// Store wraps a SQLite database. It is safe for concurrent use.
type Store struct {
	db    *sql.DB
	now   func() time.Time // overridden in tests
	newID func() string    // overridden in tests
	hub   *eventhub.Hub[*gridwellv1.Event]
	// pluginID is injected by SetPluginID. "" is a bare test store, and
	// PluginUUID then falls back to the bootstrap mint.
	pluginID string
}

const (
	systemKeyRootGridID    = "root_grid_id"
	systemKeyPluginUUID    = "plugin_uuid"
	systemKeyScratchGridID = "scratch_grid_id"
)

// Open opens a SQLite database at path, applies the schema, and bootstraps the
// root grid. ":memory:" is the in-test store.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	// SQLite is single-writer at the file level; one connection eliminates
	// contention and gives deterministic transaction interleaving.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(pragmas); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply pragmas: %w", err)
	}
	if _, err := db.Exec(systemDDL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply system schema: %w", err)
	}
	if _, err := db.Exec(tablesDDL()); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	s := &Store{
		db:    db,
		now:   time.Now,
		newID: newUUID,
		hub:   eventhub.New(rpc.EventKey),
	}
	// Migrate before bootstrapping: bootstrapRoot writes through the current
	// column set, and a v1 file's grids still carries the NOT NULL object_id
	// v10 removed, so a pre-migration insert fails its constraint.
	if err := s.applyMigrations(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	if _, err := db.Exec(externalsIndexDDL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply externals indexes: %w", err)
	}
	if err := s.bootstrapRoot(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("bootstrap root: %w", err)
	}
	// user_version alone cannot catch an unstamped DB the fast path stamped
	// as v1 without checking columns. Fail here, not at a later insert.
	if err := s.verifySchema(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// systemValue reads one row of the system KV table. ok is false when the key
// is absent, which every caller answers for itself.
func systemValue(ctx context.Context, q gridReader, key string) (string, bool, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM system WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// singletonGrid returns the grid a system key names, minting it on first use.
// The root, scratch and trash grids are all this one shape.
func (s *Store) singletonGrid(ctx context.Context, key string) (int64, error) {
	v, ok, err := systemValue(ctx, s.db, key)
	if err != nil {
		return 0, err
	}
	if ok {
		return strconv.ParseInt(v, 10, 64)
	}
	var id int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		id, err = s.singletonGridTx(ctx, tx, key)
		return err
	})
	return id, err
}

// singletonGridTx reads or mints inside an existing transaction. The re-check
// here is the whole idempotence story: the single writer connection serializes
// transactions, so a caller that got there first is visible and its id is
// returned rather than a second grid made.
func (s *Store) singletonGridTx(ctx context.Context, tx *sql.Tx, key string) (int64, error) {
	v, ok, err := systemValue(ctx, tx, key)
	if err != nil {
		return 0, err
	}
	if ok {
		return strconv.ParseInt(v, 10, 64)
	}
	id, err := insertGrid(ctx, tx, s.now().Unix())
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO system (key, value) VALUES (?, ?)`,
		key, strconv.FormatInt(id, 10)); err != nil {
		return 0, err
	}
	return id, nil
}

// bootstrapRoot inserts the initial root grid if none exists. Framing is not
// seeded: a NULL root_zoom already means never visited, and the client
// substitutes the calibrated default until the user positions the view.
func (s *Store) bootstrapRoot(ctx context.Context) error {
	_, ok, err := systemValue(ctx, s.db, systemKeyRootGridID)
	if err != nil || ok {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := s.singletonGridTx(ctx, tx, systemKeyRootGridID); err != nil {
			return err
		}
		// The mint's identity is seeded with the root, never alone: see
		// PluginUUID.
		_, err := tx.ExecContext(ctx, `INSERT INTO system (key, value) VALUES (?, ?)`,
			systemKeyPluginUUID, s.newID())
		return err
	})
}

// parseID converts a string tile or grid id to int64 for SQL binding. Call
// sites map a garbage id to ErrNotFound on a read, because an id that cannot
// exist behaves like one that does not, and to ErrInvalidArgument on a write,
// because the caller asserted a malformed identity.
func parseID(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

// RootGridID returns the current root grid id as a decimal string.
func (s *Store) RootGridID(ctx context.Context) (string, error) {
	id, err := rootGridID(ctx, s.db)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(id, 10), nil
}

// ScratchGridID returns the id of this store's scratch grid, creating it on
// first use. It holds url tiles visited by descending into a url without
// placing one on a visible grid. It never renders, yet it persists, so it
// doubles as the visited-url history that feeds autocomplete and a deep link
// into it still resolves. The id is stored once in system metadata.
func (s *Store) ScratchGridID(ctx context.Context) (string, error) {
	id, err := s.singletonGrid(ctx, systemKeyScratchGridID)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(id, 10), nil
}

// SetPluginID injects the config id the binary verified against its DB at
// spawn. The system.plugin_uuid row is a second, independently minted identity
// nothing outside this store sees: qualified references carry the config id,
// so a comparison against the mint could never match. The mint survives only
// as the fallback for bare test stores that never had a config.
func (s *Store) SetPluginID(id string) { s.pluginID = id }

// PluginUUID returns the identity this store's qualified ids carry: the
// injected config id (see SetPluginID), or the bootstrap mint for a test store.
func (s *Store) PluginUUID(ctx context.Context) (string, error) {
	if s.pluginID != "" {
		return s.pluginID, nil
	}
	v, ok, err := systemValue(ctx, s.db, systemKeyPluginUUID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", sql.ErrNoRows
	}
	return v, nil
}

// RootFraming returns home's root framing, in the same three columns a plugin
// context's root keeps. ok=false means never visited.
func (s *Store) RootFraming(ctx context.Context) (f rpc.Framing, ok bool, err error) {
	rootID, err := rootGridID(ctx, s.db)
	if err != nil {
		return rpc.Framing{}, false, err
	}
	return s.Namespace("").RootFraming(rootID)
}

// GridFraming is the same fact for any grid of home, by its decimal id. Home
// declares its trashcan as a menu entry, and an entry carries the framing of
// the grid behind it exactly as a root does, so the two read one column set.
func (s *Store) GridFraming(gridID string) (f rpc.Framing, ok bool, err error) {
	id, err := parseID(gridID)
	if err != nil {
		return rpc.Framing{}, false, err
	}
	return s.Namespace("").RootFraming(id)
}

// SetFraming is the one framing writer: how this grid looked when the user
// left it through this doorway, a float center plus the pane-size-independent
// zoom, onto the row that owns it. Exactly one target is set: req.TileID names
// a doorway tile, a well link included, since framing is per-doorway;
// req.RootGridID names a root grid, which has no doorway to carry it. Framing
// is not a content edit: no version claim, no bump.
func (s *Store) SetFraming(ctx context.Context, req *gridwellv1.SetFramingRequest) (*gridwellv1.Tile, error) {
	if req.RootGridId != "" {
		return nil, s.setRootFraming(ctx, req)
	}
	tileID, err := parseID(req.TileId)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		n, err := s.loadForWrite(ctx, tx, tileID, "", nil)
		if err != nil {
			return err
		}
		if !isWellKind(n.Kind) {
			return ErrNotWellTile
		}
		if _, err := updateFraming(ctx, tx, "", tileID, 0, rpc.Framing{Cx: req.Cx, Cy: req.Cy, Zoom: req.Zoom}, s.now().Unix()); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// setRootFraming is SetFraming's root arm: the write lands on the grid row and
// announces a grid change, because a root has no tile to change.
func (s *Store) setRootFraming(ctx context.Context, req *gridwellv1.SetFramingRequest) error {
	gridID, err := parseID(req.RootGridId)
	if err != nil {
		return fmt.Errorf("%w: invalid root_grid_id", ErrInvalidArgument)
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		n, err := updateFraming(ctx, tx, "", 0, gridID, rpc.Framing{Cx: req.Cx, Cy: req.Cy, Zoom: req.Zoom}, s.now().Unix())
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publish(&gridwellv1.Event{Payload: &gridwellv1.Event_GridChanged{GridChanged: &gridwellv1.GridChanged{GridId: req.RootGridId}}})
	return nil
}

func rootGridID(ctx context.Context, q gridReader) (int64, error) {
	v, ok, err := systemValue(ctx, q, systemKeyRootGridID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, sql.ErrNoRows
	}
	return strconv.ParseInt(v, 10, 64)
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// collect drains rows into a slice and closes them before returning. Every
// caller needs that: the store runs on one connection, so a cursor still open
// blocks the queries and writes the collected rows drive.
func collect[T any](rows *sql.Rows, scan func(*sql.Rows) (T, error)) ([]T, error) {
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
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

// withMutation runs fn in a transaction and, on commit, publishes the events
// fn appended, in order.
func (s *Store) withMutation(ctx context.Context, fn func(tx *sql.Tx, events *[]*gridwellv1.Event) error) error {
	var events []*gridwellv1.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error { return fn(tx, &events) })
	if err != nil {
		return err
	}
	for _, ev := range events {
		s.publish(ev)
	}
	return nil
}
