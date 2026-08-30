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
	"time"

	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/eventhub"

	_ "modernc.org/sqlite"
)

// Sentinel errors. The vocabulary is owned by api/gwerr, where every
// transport and every plugin classifies from one table. These aliases are
// the store's own call-site names, and errors.Is identity is preserved.
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
	hub   *eventhub.Hub[rpc.Event]
	// pluginID is the plugin identity, injected after verification by
	// SetPluginID. "" is a bare test store, and PluginUUID then falls back
	// to the bootstrap mint.
	pluginID string
}

const (
	systemKeyRootGridID    = "root_grid_id"
	systemKeyPluginUUID    = "plugin_uuid"
	systemKeyScratchGridID = "scratch_grid_id"
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
		hub:   eventhub.New(eventKey),
	}
	// Migrate before bootstrapping. bootstrapRoot is a write through the
	// current column set, and an old file does not have that shape until the
	// chain has run: a genuine v1 file's grids still carries the NOT NULL
	// object_id v10 removed, so a pre-migration insert fails its constraint.
	// Schema first, then writes.
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
	// Guard against an out-of-contract shape user_version alone cannot catch,
	// such as an unstamped DB the fast path stamped as v1 without checking
	// columns. Fail loudly here rather than let a later insert hit an
	// orphaned constraint.
	if err := s.verifySchema(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// bootstrapRoot inserts the initial root grid if none exists. It is
// idempotent. The root's framing is not seeded here: it lives on the grid row
// itself, in root_cx/cy/zoom, and a NULL zoom already means never visited, so
// the client substitutes the calibrated default until the user positions the
// view.
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

// parseID converts a string tile/grid ID to int64 for SQL binding.
//
// Error-mapping convention at call sites: reads map a garbage id to
// ErrNotFound, because an id that cannot exist behaves like one that does
// not; mutations map it to ErrInvalidArgument, because the caller asserted
// an identity in a write and the assertion itself is malformed. The asymmetry
// is deliberate.
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
// first use. The scratch grid holds ephemeral url tiles: pages visited by
// descending into a url — a link in a shell, a click on the menu url swatch —
// without ever placing a tile on a visible grid. It is never a plugin root
// and is never mounted, so it never renders, yet it persists, so it doubles
// as the durable visited-url history that feeds url autocomplete, and a deep
// link to one of its tiles still resolves. Idempotent: the id is stored once
// in system metadata and returned verbatim thereafter.
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
		// serializes transactions, so a concurrent caller that created it
		// first is committed and visible here, and its id is returned rather
		// than a second grid made.
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

// SchemaVersion returns the on-disk schema generation this binary
// materializes, stamped into the SQLite header as user_version. It is exposed
// so the Info handshake reports the real version instead of a literal that
// could drift from the stored one.
func (s *Store) SchemaVersion() int { return schemaVersion }

// SetPluginID injects the plugin identity: the config id the binary verified
// against its DB at spawn, through pluginmeta.Verify. The system.plugin_uuid
// row is a second, independently minted identity nothing outside this store
// ever sees; qualified references — layout blobs, every wire id — carry the
// config id, so a comparison against the mint could never match. Identity
// flows in from the one verified source, and the mint survives only as the
// fallback for bare test stores that never had a config.
func (s *Store) SetPluginID(id string) { s.pluginID = id }

// PluginUUID returns the plugin identity this store's qualified ids carry:
// the injected config id, see SetPluginID, or else the bootstrap-minted
// system.plugin_uuid for a bare test store.
func (s *Store) PluginUUID(ctx context.Context) (string, error) {
	if s.pluginID != "" {
		return s.pluginID, nil
	}
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM system WHERE key = ?`, systemKeyPluginUUID).Scan(&v)
	return v, err
}

// RootFraming returns home's root framing: the same fact, in the same three
// columns a plugin context's root keeps, on home's root grid row in the empty
// namespace. ok=false means never visited.
func (s *Store) RootFraming(ctx context.Context) (f rpc.Framing, ok bool, err error) {
	rootID, err := rootGridID(ctx, s.db)
	if err != nil {
		return rpc.Framing{}, false, err
	}
	return s.Namespace("").RootFraming(rootID)
}

// SetFraming is the one framing writer: "how this grid looked when I left it
// through this doorway" — a float center in the grid's own coordinates plus
// the pane-size-independent zoom — onto the row that owns it. Exactly one
// target is set: req.TileID names a doorway tile (a well, interior or exit,
// or a well link, since framing is per-doorway and a link keeps its own), and
// req.RootGridID names a root grid, which has no doorway to carry it.
//
// Framing is not a content edit: it carries no version claim and does not
// bump the tile version. It is an in-place write to the owning row, and
// clones are already independent, so there is nothing to fork.
func (s *Store) SetFraming(ctx context.Context, req *rpc.SetFramingRequest) (*rpc.Tile, error) {
	if req.RootGridID != "" {
		return nil, s.setRootFraming(ctx, req)
	}
	tileID, err := parseID(req.TileID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.loadForWrite(ctx, tx, tileID, "", nil)
		if err != nil {
			return err
		}
		if !isWellKind(n.Kind) {
			return ErrNotWellTile
		}
		if _, err := updateFraming(ctx, tx, "", tileID, 0, req.Framing, s.now().Unix()); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// setRootFraming is SetFraming's root arm: the write lands on the grid row
// and announces itself as a grid change, because a root has no tile to
// change.
func (s *Store) setRootFraming(ctx context.Context, req *rpc.SetFramingRequest) error {
	gridID, err := parseID(req.RootGridID)
	if err != nil {
		return fmt.Errorf("%w: invalid root_grid_id", ErrInvalidArgument)
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		n, err := updateFraming(ctx, tx, "", 0, gridID, req.Framing, s.now().Unix())
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
	s.publish(rpc.Event{Kind: rpc.EventGridChanged, GridChanged: &rpc.GridChanged{GridID: req.RootGridID}})
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
