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

// bootstrapRoot inserts the initial root grid if none exists. Framing is not
// seeded: a NULL root_zoom already means never visited, and the client
// substitutes the calibrated default until the user positions the view.
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
		res, err := tx.ExecContext(ctx,
			`INSERT INTO grids (created_at, updated_at) VALUES (?, ?)`,
			now, now)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		seeds := []struct{ k, v string }{
			{systemKeyRootGridID, strconv.FormatInt(id, 10)},
			{systemKeyPluginUUID, s.newID()},
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
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM system WHERE key = ?`, systemKeyScratchGridID).Scan(&v)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err := s.withTx(ctx, func(tx *sql.Tx) error {
		// Re-check inside the transaction: the single writer connection
		// serializes them, so a concurrent caller that got there first is
		// visible here and its id is returned rather than a second grid made.
		if e := tx.QueryRowContext(ctx, `SELECT value FROM system WHERE key = ?`, systemKeyScratchGridID).Scan(&v); e == nil {
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		now := s.now().Unix()
		res, e := tx.ExecContext(ctx,
			`INSERT INTO grids (created_at, updated_at) VALUES (?, ?)`,
			now, now)
		if e != nil {
			return e
		}
		id, e := res.LastInsertId()
		if e != nil {
			return e
		}
		v = strconv.FormatInt(id, 10)
		_, e = tx.ExecContext(ctx, `INSERT INTO system (key, value) VALUES (?, ?)`, systemKeyScratchGridID, v)
		return e
	}); err != nil {
		return "", err
	}
	return v, nil
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
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM system WHERE key = ?`, systemKeyPluginUUID).Scan(&v)
	return v, err
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
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM system WHERE key = ?`, systemKeyRootGridID).Scan(&v)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
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
