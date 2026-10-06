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
	"strings"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/eventhub"
	"github.com/josephburnett/gridwell/internal/trace"

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

// Open opens a SQLite database at path, applies the schema, and mints the
// singleton grids. ":memory:" is the in-test store.
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
	// Migrate before bootstrapping: bootstrap writes through the current
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
	if err := s.bootstrap(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("bootstrap: %w", err)
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

// singletonGridKeys names every grid the node holds exactly one of. Open mints
// each absent one, so a read only ever finds them.
var singletonGridKeys = []string{systemKeyRootGridID, systemKeyScratchGridID, systemKeyTrashGridID}

// singletonGrid returns the grid a system key names. Absent is a store Open
// did not bootstrap, never a cue to mint.
func singletonGrid(ctx context.Context, q gridReader, key string) (int64, error) {
	v, ok, err := systemValue(ctx, q, key)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("store: no %s singleton: %w", key, sql.ErrNoRows)
	}
	return strconv.ParseInt(v, 10, 64)
}

// bootstrap mints, in one transaction, every singleton grid the file lacks,
// keyed by the same system row a read finds it by, so an existing home keeps
// its ids. The plugin_uuid mint rides with the root's, never alone: see
// PluginUUID. Framing is not seeded: NULL already means never visited.
func (s *Store) bootstrap(ctx context.Context) error {
	var absent []string
	for _, key := range singletonGridKeys {
		_, ok, err := systemValue(ctx, s.db, key)
		if err != nil {
			return err
		}
		if !ok {
			absent = append(absent, key)
		}
	}
	if len(absent) == 0 {
		return nil
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, key := range absent {
			id, err := insertGrid(ctx, tx, s.now().Unix())
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO system (key, value) VALUES (?, ?)`,
				key, strconv.FormatInt(id, 10)); err != nil {
				return err
			}
			if key == systemKeyRootGridID {
				if _, err := tx.ExecContext(ctx, `INSERT INTO system (key, value) VALUES (?, ?)`,
					systemKeyPluginUUID, s.newID()); err != nil {
					return err
				}
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

// ScratchGridID returns the id of this store's scratch grid. It holds url tiles visited by descending into a url without
// placing one on a visible grid. It never renders, yet it persists, so it
// doubles as the visited-url history that feeds autocomplete and a deep link
// into it still resolves. The id is stored once in system metadata.
func (s *Store) ScratchGridID(ctx context.Context) (string, error) {
	id, err := singletonGrid(ctx, s.db, systemKeyScratchGridID)
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
// context's root keeps.
func (s *Store) RootFraming(ctx context.Context) (rpc.View, error) {
	rootID, err := rootGridID(ctx, s.db)
	if err != nil {
		return rpc.View{}, err
	}
	return s.Namespace("").RootFraming(rootID)
}

// GridFraming is the same fact for any grid of home, by its decimal id. Home
// declares its trashcan as a menu entry, and an entry carries the framing of
// the grid behind it exactly as a root does, so the two read one column set.
func (s *Store) GridFraming(gridID string) (rpc.View, error) {
	id, err := parseID(gridID)
	if err != nil {
		return rpc.View{}, err
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
	f, err := rpc.FramingOf(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	var out *gridwellv1.Tile
	err = s.withTileWrite(ctx, "SetFraming", tileID, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		n, err := s.loadForWrite(ctx, tx, tileID, "", nil)
		if err != nil {
			return err
		}
		if !isWellKind(n.Kind) {
			return ErrNotWellTile
		}
		if _, err := updateFraming(ctx, tx, "", tileID, 0, f, s.now().Unix()); err != nil {
			return err
		}
		out, err = s.emitTileChanged(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// setRootFraming is SetFraming's root arm: the write lands on the grid row and
// announces the framing itself, because a root has no tile to change and its
// listing did not change.
func (s *Store) setRootFraming(ctx context.Context, req *gridwellv1.SetFramingRequest) error {
	gridID, err := parseID(req.RootGridId)
	if err != nil {
		return fmt.Errorf("%w: invalid root_grid_id", ErrInvalidArgument)
	}
	f, err := rpc.FramingOf(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	return s.withMutation(ctx, "SetFraming/root", func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		n, err := updateFraming(ctx, tx, "", 0, gridID, f, s.now().Unix())
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		*events = append(*events, rpc.FramingEvent(req.RootGridId, f))
		return nil
	})
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

// readSnapshot is how a read of more than one statement sees one state of the
// database. Outside it, a write can land between a row and the blob it names
// and release that blob, so the read answers NotFound for a tile that exists.
func (s *Store) readSnapshot(ctx context.Context, fn func(*sql.Tx) error) error {
	return s.withTx(ctx, fn)
}

// withMutation runs fn in a transaction and, on commit, publishes the events
// fn appended, in order. Every store write goes through it, so it is also
// where a write says it happened: verb is the caller's, and what it touched
// comes from the events it is about to publish rather than a second reading.
func (s *Store) withMutation(ctx context.Context, verb string, fn func(tx *sql.Tx, events *[]*gridwellv1.Event) error) error {
	var events []*gridwellv1.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error { return fn(tx, &events) })
	if errors.Is(err, errUnchanged) {
		trace.Emit("store", "write", verb+" unchanged", nil)
		return nil
	}
	if err != nil {
		trace.Emit("store", "write", verb+" error: "+err.Error(), nil)
		return err
	}
	trace.Emit("store", "write", verb, touched(events))
	for _, ev := range events {
		s.publish(ev)
	}
	return nil
}

// errUnchanged rolls back a tile write that left its row as it found it.
var errUnchanged = errors.New("store: write changed nothing")

// withTileWrite is withMutation for a write without a claim to one existing
// tile: framing, layout, a capture. One that lands where the row already is
// rolls back, so it neither stamps updated_at nor tells a subscriber, because
// every open view would re-read and re-settle against a change that is not
// one. The row is compared whole, columns off the wire included.
func (s *Store) withTileWrite(ctx context.Context, verb string, tileID int64, fn func(tx *sql.Tx, events *[]*gridwellv1.Event) error) error {
	return s.withMutation(ctx, verb, func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		before, err := tileImage(ctx, tx, tileID)
		if err != nil {
			return err
		}
		if err := fn(tx, events); err != nil {
			return err
		}
		after, err := tileImage(ctx, tx, tileID)
		if err != nil {
			return err
		}
		if after == before {
			return errUnchanged
		}
		return nil
	})
}

// tileImage is a tile row as one comparable value, every column but
// updated_at. A missing row is the empty image, so the write's own load
// reports it.
func tileImage(ctx context.Context, tx *sql.Tx, tileID int64) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT * FROM tiles WHERE id = ?`, tileID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", rows.Err()
	}
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", err
	}
	var b strings.Builder
	for i, c := range cols {
		if c != "updated_at" {
			fmt.Fprintf(&b, "%s=%#v;", c, vals[i])
		}
	}
	return b.String(), rows.Err()
}

// touched names the entities a mutation changed, and for a single-tile write
// its kind and the version it left behind.
func touched(events []*gridwellv1.Event) map[string]string {
	if len(events) == 0 {
		return nil
	}
	keys := make([]string, 0, len(events))
	for _, ev := range events {
		keys = append(keys, rpc.EventKey(ev))
	}
	kv := map[string]string{"keys": strings.Join(keys, " ")}
	if len(events) == 1 {
		if t := events[0].GetTileChanged().GetTile(); t != nil {
			kv["kind"], kv["v"] = t.Kind, strconv.FormatInt(t.Version, 10)
		}
	}
	return kv
}
