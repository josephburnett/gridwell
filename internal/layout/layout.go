// Package layout is the node's presentation engine (docs/v2-design.md
// §4.1) — THE seam of the v2 design, so it is a pure, headless package:
// given a plugin's listing (stable keys, no ids, no placement) and the
// external's memory DB, it resolves keys to minted numeric ids, overlays
// the user's stored arrangement, places first-sighted entries (the
// plugin's hint, else the first free cell), and retires entries an
// authoritative listing no longer contains.
//
// Identity rules (tenet 7):
//   - ids are AUTOINCREMENT, never reused;
//   - a retired (tombstoned) key stays retired — a file deleted and
//     recreated under the same name is a NEW thing with a fresh id
//     (exactly the legacy fs reconcile's delete-then-reinsert identity),
//     enforced by a partial unique index on LIVE rows only;
//   - placement/framing writes are unversioned (plugin tiles serve
//     version 0 on the wire — carried over verbatim from the retired
//     legacy fs/proc plugins so the migration changed nothing).
package layout

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/josephburnett/gridwell/internal/dbformat"
	_ "modernc.org/sqlite"
)

// DefaultGridWidth is the auto-place wrap width — the value the legacy
// fs/proc reconcilers used (their autoGridWidth).
const DefaultGridWidth = 8

// On-disk format identity (internal/dbformat). The memory DB is
// durable-but-forgettable (tenet 5): losing it dangles links and resets
// arrangement, but it still carries the additive-only promise — it is
// never deleted to absorb a schema change.
const (
	memApplicationID = 0x4757786d // "GWxm" — gridwell external memory
	memSchemaVersion = 1
)

var memMigrations = []dbformat.Migration{}

const schemaTemplate = `
CREATE TABLE IF NOT EXISTS meta (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS contexts (
    grid_id   INTEGER PRIMARY KEY AUTOINCREMENT,
    key       TEXT NOT NULL UNIQUE,
    -- The context's persisted root viewport. NULL = never set.
    root_cx   REAL,
    root_cy   REAL,
    root_zoom REAL
);
CREATE TABLE IF NOT EXISTS idmap (
    tile_id    INTEGER PRIMARY KEY AUTOINCREMENT,
    grid_id    INTEGER NOT NULL REFERENCES contexts(grid_id),
    key        TEXT NOT NULL,
    tombstoned INTEGER NOT NULL DEFAULT 0
);
-- Key uniqueness holds among LIVE rows only: a retired key's row stays
-- (ids are never reused, and a dangling reference stays interpretable),
-- and a recreated key mints a fresh id.
CREATE UNIQUE INDEX IF NOT EXISTS idmap_live_key
    ON idmap (grid_id, key) WHERE tombstoned = 0;
-- The read-through listing cache (tenet 6): the last good answer per
-- context, serialized by the caller (the adapter owns the entry shape).
-- Disposable rows in a durable file: dropping them loses only the
-- offline answer, never ids or arrangement.
CREATE TABLE IF NOT EXISTS cache_listings (
    grid_id       INTEGER PRIMARY KEY REFERENCES contexts(grid_id),
    entries       BLOB NOT NULL,
    authoritative INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS layout (
    tile_id      INTEGER PRIMARY KEY REFERENCES idmap(tile_id),
    x            INTEGER NOT NULL DEFAULT 0,
    y            INTEGER NOT NULL DEFAULT 0,
    w            INTEGER NOT NULL DEFAULT 1,
    h            INTEGER NOT NULL DEFAULT 1,
    view_x       INTEGER NOT NULL DEFAULT 0,
    view_y       INTEGER NOT NULL DEFAULT 0,
    view_zoom    REAL    NOT NULL DEFAULT 1.0,
    text_x       INTEGER NOT NULL DEFAULT 0,
    text_y       INTEGER NOT NULL DEFAULT 0,
    text_w       INTEGER NOT NULL DEFAULT 0,
    text_h       INTEGER NOT NULL DEFAULT 0,
    text_mode    TEXT    NOT NULL DEFAULT '',
    content_zoom REAL    NOT NULL DEFAULT 0
);`

// ErrNotFound is the missing/retired-row verdict.
var ErrNotFound = errors.New("layout: not found")

// DB is one external's memory store. Single writer: the node.
type DB struct {
	db *sql.DB
}

// Open opens (or creates) the memory DB at path.
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("layout: open %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("layout: pragmas: %w", err)
	}
	if _, err := db.Exec(schemaTemplate); err != nil {
		db.Close()
		return nil, fmt.Errorf("layout: schema: %w", err)
	}
	if err := dbformat.EnsureVersion(context.Background(), db, memApplicationID, memSchemaVersion, memMigrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("layout: %s: %w", path, err)
	}
	return &DB{db: db}, nil
}

// Close closes the underlying handle.
func (d *DB) Close() error { return d.db.Close() }

// OpenVerified opens (or creates) the memory DB and fuses the identity
// check (the pluginmeta lesson, applied node-side): a fresh file is
// stamped with the external's uuid and kind; an existing file must match
// both, so a memory DB can never be served under the wrong id — the
// key→id map inside it is what stored references resolve through.
func OpenVerified(path, uuid, kind string) (*DB, error) {
	d, err := Open(path)
	if err != nil {
		return nil, err
	}
	get := func(k string) (string, error) {
		var v string
		err := d.db.QueryRow(`SELECT v FROM meta WHERE k = ?`, k).Scan(&v)
		if err == sql.ErrNoRows {
			return "", nil
		}
		return v, err
	}
	gotUUID, err := get("uuid")
	if err != nil {
		d.Close()
		return nil, err
	}
	gotKind, err := get("kind")
	if err != nil {
		d.Close()
		return nil, err
	}
	if gotUUID == "" {
		if _, err := d.db.Exec(`INSERT INTO meta (k, v) VALUES ('uuid', ?), ('kind', ?)`, uuid, kind); err != nil {
			d.Close()
			return nil, err
		}
		return d, nil
	}
	if gotUUID != uuid || gotKind != kind {
		d.Close()
		return nil, fmt.Errorf("layout: %s belongs to %s/%s, not %s/%s — refusing to serve another external's memory",
			path, gotUUID, gotKind, uuid, kind)
	}
	return d, nil
}

// Entry is one plugin listing row, as the engine needs it — a mirror
// of the Plugin Entry without a proto dependency (this package
// stays pure).
type Entry struct {
	Key          string
	Kind         string
	Label        string
	ChildContext string // wells: the context key this entry opens into
	// Hint, when non-nil, seeds the entry's FIRST placement only.
	Hint *Hint
}

// Hint is a plugin's suggested first placement.
type Hint struct{ X, Y, W, H int64 }

// Tile is one merged row: the plugin's content facts joined with the
// user's stored arrangement.
type Tile struct {
	ID          int64
	Key         string
	Kind        string
	Label       string
	X, Y, W, H  int64
	ViewX       int64
	ViewY       int64
	ViewZoom    float64
	TextX       int64
	TextY       int64
	TextW       int64
	TextH       int64
	TextMode    string
	ContentZoom float64
	// ChildGridID is the minted grid id of the entry's child context;
	// 0 for leaves.
	ChildGridID int64
}

// ContextID resolves a context key to its minted grid id, minting on
// first sight.
func (d *DB) ContextID(key string) (int64, error) {
	var id int64
	err := d.db.QueryRow(`SELECT grid_id FROM contexts WHERE key = ?`, key).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := d.db.Exec(`INSERT INTO contexts (key) VALUES (?)`, key)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ContextKey resolves a minted grid id back to the plugin's context key.
func (d *DB) ContextKey(gridID int64) (string, error) {
	var key string
	err := d.db.QueryRow(`SELECT key FROM contexts WHERE grid_id = ?`, gridID).Scan(&key)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	return key, err
}

// RetiredKeys returns the keys with a tombstoned row in the grid. The
// adapter's cache filter reads it: a remembered (cached) entry whose key
// was retired must not re-enter the merge — a retired key stays retired,
// and re-minting it burns identity the converter's sequences protect.
func (d *DB) RetiredKeys(gridID int64) (map[string]bool, error) {
	rows, err := d.db.Query(`SELECT DISTINCT key FROM idmap WHERE grid_id = ? AND tombstoned = 1`, gridID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// TileKey resolves a minted tile id to its (grid id, plugin key).
// Retired rows still resolve — a dangling reference stays interpretable;
// the caller decides what retirement means (reads: gone).
func (d *DB) TileKey(tileID int64) (gridID int64, key string, tombstoned bool, err error) {
	var tomb int64
	err = d.db.QueryRow(`SELECT grid_id, key, tombstoned FROM idmap WHERE tile_id = ?`, tileID).
		Scan(&gridID, &key, &tomb)
	if err == sql.ErrNoRows {
		return 0, "", false, ErrNotFound
	}
	return gridID, key, tomb != 0, err
}

// Merge joins one plugin listing with the stored arrangement,
// transactionally: known live keys keep their rows; new keys mint ids
// and take their first placement (hint, else first free cell); and when
// the listing is authoritative, live keys absent from it retire. The
// returned tiles are ordered by id (stable output, the legacy rule).
func (d *DB) Merge(gridID int64, entries []Entry, authoritative bool) ([]Tile, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Live rows for this grid: key → placement.
	type live struct {
		id         int64
		x, y, w, h int64
	}
	rows, err := tx.Query(`SELECT i.tile_id, i.key, l.x, l.y, l.w, l.h
		FROM idmap i JOIN layout l ON l.tile_id = i.tile_id
		WHERE i.grid_id = ? AND i.tombstoned = 0`, gridID)
	if err != nil {
		return nil, err
	}
	liveByKey := map[string]live{}
	for rows.Next() {
		var l live
		var key string
		if err := rows.Scan(&l.id, &key, &l.x, &l.y, &l.w, &l.h); err != nil {
			rows.Close()
			return nil, err
		}
		liveByKey[key] = l
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Occupancy: full footprints of every live row (the #265 rule).
	occupied := map[[2]int64]bool{}
	for _, l := range liveByKey {
		occupyRect(occupied, l.x, l.y, l.w, l.h)
	}
	var cur cursor

	present := map[string]bool{}
	for _, e := range entries {
		present[e.Key] = true
		if _, ok := liveByKey[e.Key]; ok {
			continue // arranged already; the user's placement stands
		}
		x, y, w, h := int64(0), int64(0), int64(1), int64(1)
		if e.Hint != nil {
			x, y, w, h = e.Hint.X, e.Hint.Y, e.Hint.W, e.Hint.H
			if w < 1 {
				w = 1
			}
			if h < 1 {
				h = 1
			}
			occupyRect(occupied, x, y, w, h)
		} else {
			x, y = nextEmptyCell(occupied, DefaultGridWidth, &cur)
		}
		res, err := tx.Exec(`INSERT INTO idmap (grid_id, key) VALUES (?, ?)`, gridID, e.Key)
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO layout (tile_id, x, y, w, h) VALUES (?, ?, ?, ?, ?)`,
			id, x, y, w, h); err != nil {
			return nil, err
		}
	}

	if authoritative {
		for key, l := range liveByKey {
			if !present[key] {
				if _, err := tx.Exec(`UPDATE idmap SET tombstoned = 1 WHERE tile_id = ?`, l.id); err != nil {
					return nil, err
				}
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.tilesFor(gridID, entries)
}

// tilesFor joins the (post-merge) live rows with the listing's content
// facts and mints child-context grids.
func (d *DB) tilesFor(gridID int64, entries []Entry) ([]Tile, error) {
	byKey := map[string]Entry{}
	for _, e := range entries {
		byKey[e.Key] = e
	}
	rows, err := d.db.Query(`SELECT i.tile_id, i.key,
		l.x, l.y, l.w, l.h, l.view_x, l.view_y, l.view_zoom,
		l.text_x, l.text_y, l.text_w, l.text_h, l.text_mode, l.content_zoom
		FROM idmap i JOIN layout l ON l.tile_id = i.tile_id
		WHERE i.grid_id = ? AND i.tombstoned = 0 ORDER BY i.tile_id`, gridID)
	if err != nil {
		return nil, err
	}
	// Drain the iterator BEFORE minting child contexts: the DB runs one
	// connection (single-writer discipline), and an open rows cursor
	// holds it — an Exec inside the loop would self-deadlock.
	var out []Tile
	for rows.Next() {
		var t Tile
		if err := rows.Scan(&t.ID, &t.Key, &t.X, &t.Y, &t.W, &t.H,
			&t.ViewX, &t.ViewY, &t.ViewZoom,
			&t.TextX, &t.TextY, &t.TextW, &t.TextH, &t.TextMode, &t.ContentZoom); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		e, ok := byKey[out[i].Key]
		if !ok {
			// A remembered row the listing didn't include this pass
			// (non-authoritative churn): serve it as it was last known.
			// The caller supplies content facts from its cache.
			continue
		}
		out[i].Kind, out[i].Label = e.Kind, e.Label
		if e.ChildContext != "" {
			cgid, err := d.ContextID(e.ChildContext)
			if err != nil {
				return nil, err
			}
			out[i].ChildGridID = cgid
		}
	}
	return out, nil
}

// exec runs a single-row UPDATE, mapping zero rows to ErrNotFound.
func (d *DB) exec(q string, args ...any) error {
	res, err := d.db.Exec(q, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// liveOnly guards a mutation against retired rows.
func (d *DB) liveOnly(tileID int64) error {
	_, _, tomb, err := d.TileKey(tileID)
	if err != nil {
		return err
	}
	if tomb {
		return ErrNotFound
	}
	return nil
}

// Place is the placement writeback: (x, y, w, h), same grid always
// (cross-grid placement never existed for plugin tiles). Unversioned.
func (d *DB) Place(tileID, x, y, w, h int64) error {
	if err := d.liveOnly(tileID); err != nil {
		return err
	}
	return d.exec(`UPDATE layout SET x = ?, y = ?, w = ?, h = ? WHERE tile_id = ?`, x, y, w, h, tileID)
}

// SetWellView persists a well's preview framing — the descent target and
// ascent return value.
func (d *DB) SetWellView(tileID, vx, vy int64, vz float64) error {
	if err := d.liveOnly(tileID); err != nil {
		return err
	}
	return d.exec(`UPDATE layout SET view_x = ?, view_y = ?, view_zoom = ? WHERE tile_id = ?`, vx, vy, vz, tileID)
}

// SetTextView persists a text tile's framed window — the durable home
// the legacy fs never had (#236's client special case retires on this).
func (d *DB) SetTextView(tileID, tx, ty, tw, th int64, mode string) error {
	if err := d.liveOnly(tileID); err != nil {
		return err
	}
	return d.exec(`UPDATE layout SET text_x = ?, text_y = ?, text_w = ?, text_h = ?, text_mode = ? WHERE tile_id = ?`,
		tx, ty, tw, th, mode, tileID)
}

// SetContentZoom persists the per-tile content scale.
func (d *DB) SetContentZoom(tileID int64, zoom float64) error {
	if err := d.liveOnly(tileID); err != nil {
		return err
	}
	return d.exec(`UPDATE layout SET content_zoom = ? WHERE tile_id = ?`, zoom, tileID)
}

// RootView reads a context's persisted root viewport; ok=false = never set.
func (d *DB) RootView(gridID int64) (cx, cy, zoom float64, ok bool, err error) {
	var ncx, ncy, nzoom sql.NullFloat64
	err = d.db.QueryRow(`SELECT root_cx, root_cy, root_zoom FROM contexts WHERE grid_id = ?`, gridID).
		Scan(&ncx, &ncy, &nzoom)
	if err == sql.ErrNoRows {
		return 0, 0, 0, false, ErrNotFound
	}
	if err != nil {
		return 0, 0, 0, false, err
	}
	if !nzoom.Valid {
		return 0, 0, 0, false, nil
	}
	return ncx.Float64, ncy.Float64, nzoom.Float64, true, nil
}

// SetRootView persists a context's root viewport. Framing-class.
func (d *DB) SetRootView(gridID int64, cx, cy, zoom float64) error {
	return d.exec(`UPDATE contexts SET root_cx = ?, root_cy = ?, root_zoom = ? WHERE grid_id = ?`,
		cx, cy, zoom, gridID)
}

// CacheListing remembers a context's last good listing — an opaque blob
// the caller (the plugin adapter) serializes; the engine never
// interprets it.
func (d *DB) CacheListing(gridID int64, blob []byte, authoritative bool) error {
	auth := 0
	if authoritative {
		auth = 1
	}
	_, err := d.db.Exec(`INSERT INTO cache_listings (grid_id, entries, authoritative) VALUES (?, ?, ?)
		ON CONFLICT(grid_id) DO UPDATE SET entries = excluded.entries, authoritative = excluded.authoritative`,
		gridID, blob, auth)
	return err
}

// CachedListing returns the remembered listing, ok=false when none.
func (d *DB) CachedListing(gridID int64) (blob []byte, authoritative, ok bool, err error) {
	var auth int64
	err = d.db.QueryRow(`SELECT entries, authoritative FROM cache_listings WHERE grid_id = ?`, gridID).
		Scan(&blob, &auth)
	if err == sql.ErrNoRows {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	return blob, auth != 0, true, nil
}

// Retire tombstones one tile row directly — the delete-gesture path
// (the plugin deletes the source; the row retires without waiting for
// the next authoritative listing).
func (d *DB) Retire(tileID int64) error {
	if err := d.liveOnly(tileID); err != nil {
		return err
	}
	return d.exec(`UPDATE idmap SET tombstoned = 1 WHERE tile_id = ?`, tileID)
}

// ── auto-place (the retired griddb semantics, carried verbatim) ─────────────

type cursor struct{ x, y int64 }

func nextEmptyCell(occupied map[[2]int64]bool, width int64, cur *cursor) (int64, int64) {
	cx, cy := cur.x, cur.y
	for {
		if !occupied[[2]int64{cx, cy}] {
			occupied[[2]int64{cx, cy}] = true
			cur.x, cur.y = cx, cy
			return cx, cy
		}
		cx++
		if cx >= width {
			cx = 0
			cy++
		}
	}
}

func occupyRect(occupied map[[2]int64]bool, x, y, w, h int64) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	for dx := int64(0); dx < w; dx++ {
		for dy := int64(0); dy < h; dy++ {
			occupied[[2]int64{x + dx, y + dy}] = true
		}
	}
}
