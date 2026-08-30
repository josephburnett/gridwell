package store

// The node's memory of EXTERNALS (docs/one-node.md §2.6): every content
// plugin's entries live as rows in the same grids/tiles tables as home,
// under the plugin's namespace (ns = the plugin id; home is ns = ''). A
// plugin answers from its source in stable string KEYS; the node mints
// the ids, keeps the user's arrangement and framing, and retires keys
// (tombstones — an id is never reused, a retired key stays retired). One
// table, one column vocabulary for placement and framing, whichever
// namespace a tile belongs to.
//
// Identity rules (v2 tenet 7): ids are AUTOINCREMENT, never reused; a
// retired key's row stays (a dangling reference stays interpretable) and
// a recreated key mints a fresh id — enforced by a partial unique index
// on LIVE rows only. Plugin rows are unversioned (version 0 on the wire)
// and emit no store events: the plugin's own listing is the truth.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Namespace is one external's view of the store.
type Namespace struct {
	s  *Store
	ns string
}

// Namespace returns the external memory under ns (a plugin id).
func (s *Store) Namespace(ns string) *Namespace {
	return &Namespace{s: s, ns: ns}
}

// SQL exposes the store's one database handle for the node's other
// tables (the transport's connections): SQLite is single-writer per
// file and this handle runs one connection, so a second handle on the
// same file would meet an instant SQLITE_BUSY.
func (s *Store) SQL() *sql.DB { return s.db }

// Entry is one plugin listing row, as the engine needs it — a mirror of
// the plugin Entry without a proto dependency.
type Entry struct {
	Key          string
	Kind         string
	Label        string
	ChildContext string // wells: the context key this entry opens into
	URL          string // url entries: the address (the row's url_string)
	// Hint, when non-nil, seeds the entry's FIRST placement only.
	Hint *Hint
}

// Hint is a plugin's suggested first placement.
type Hint struct{ X, Y, W, H int64 }

// ExtTile is one merged row: the plugin's content facts joined with the
// user's stored arrangement.
type ExtTile struct {
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

// DefaultGridWidth is the auto-place wrap width for first-sighted
// entries with no hint.
const DefaultGridWidth int64 = 8

// ContextID resolves a context key to its minted grid id, minting on
// first sight.
func (n *Namespace) ContextID(key string) (int64, error) {
	var id int64
	err := n.s.db.QueryRow(`SELECT id FROM grids WHERE ns = ? AND context_key = ?`, n.ns, key).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	now := n.s.now().UnixNano()
	res, err := n.s.db.Exec(`INSERT INTO grids (version, created_at, updated_at, ns, context_key)
		VALUES (0, ?, ?, ?, ?)`, now, now, n.ns, key)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ContextKey resolves a minted grid id back to the plugin's context key.
func (n *Namespace) ContextKey(gridID int64) (string, error) {
	var key string
	err := n.s.db.QueryRow(`SELECT context_key FROM grids WHERE id = ? AND ns = ?`, gridID, n.ns).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return key, err
}

// RetiredKeys returns the keys with a tombstoned row in the grid. The
// adapter's cache filter reads it: a remembered (cached) entry whose key
// was retired must not re-enter the merge — a retired key stays retired.
func (n *Namespace) RetiredKeys(gridID int64) (map[string]bool, error) {
	rows, err := n.s.db.Query(`SELECT DISTINCT key FROM tiles WHERE ns = ? AND grid_id = ? AND tombstoned = 1`, n.ns, gridID)
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
func (n *Namespace) TileKey(tileID int64) (gridID int64, key string, tombstoned bool, err error) {
	var tomb int64
	err = n.s.db.QueryRow(`SELECT grid_id, key, tombstoned FROM tiles WHERE id = ? AND ns = ?`, tileID, n.ns).
		Scan(&gridID, &key, &tomb)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, ErrNotFound
	}
	return gridID, key, tomb != 0, err
}

// Merge joins one plugin listing with the stored arrangement,
// transactionally: known live keys keep their rows (content facts —
// label, url, child — refresh); new keys mint ids and take their first
// placement (hint, else first free cell); and when the listing is
// authoritative, live keys absent from it retire. The returned tiles are
// ordered by id (stable output).
func (n *Namespace) Merge(gridID int64, entries []Entry, authoritative bool) ([]ExtTile, error) {
	// Child contexts mint OUTSIDE the transaction (the store runs one
	// connection; a nested Exec would self-deadlock) and before the rows
	// that reference them — a well row needs its child grid to exist.
	child := map[string]int64{}
	for _, e := range entries {
		if e.Kind == "well" && e.ChildContext != "" {
			cgid, err := n.ContextID(e.ChildContext)
			if err != nil {
				return nil, err
			}
			child[e.Key] = cgid
		}
	}
	tx, err := n.s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	type live struct {
		id         int64
		x, y, w, h int64
	}
	rows, err := tx.Query(`SELECT id, key, x, y, w, h FROM tiles
		WHERE ns = ? AND grid_id = ? AND tombstoned = 0`, n.ns, gridID)
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
	now := n.s.now().UnixNano()

	present := map[string]bool{}
	for _, e := range entries {
		present[e.Key] = true
		kind := e.Kind
		if kind == "" {
			kind = "text"
		}
		var childID, url any
		if kind == "well" {
			cg, ok := child[e.Key]
			if !ok {
				return nil, fmt.Errorf("store: well entry %q declares no child context", e.Key)
			}
			childID = cg
		}
		if kind == "url" {
			url = e.URL
		}
		if l, ok := liveByKey[e.Key]; ok {
			// Arranged already; the user's placement stands. The
			// plugin's facts refresh (a renamed file, a moved link).
			if _, err := tx.Exec(`UPDATE tiles SET kind = ?, alt_text = ?, child_grid_id = ?, url_string = ?, updated_at = ?
				WHERE id = ?`, kind, e.Label, childID, url, now, l.id); err != nil {
				return nil, fmt.Errorf("store: refresh %q: %w", e.Key, err)
			}
			continue
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
		if _, err := tx.Exec(`INSERT INTO tiles (version, grid_id, kind, x, y, w, h,
			child_grid_id, url_string, alt_text, created_at, updated_at, ns, key)
			VALUES (0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			gridID, kind, x, y, w, h, childID, url, e.Label, now, now, n.ns, e.Key); err != nil {
			return nil, fmt.Errorf("store: mint %q: %w", e.Key, err)
		}
	}

	if authoritative {
		for key, l := range liveByKey {
			if !present[key] {
				if _, err := tx.Exec(`UPDATE tiles SET tombstoned = 1, updated_at = ? WHERE id = ?`, now, l.id); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return n.tiles(gridID)
}

// tiles lists the live rows of a grid, by id.
func (n *Namespace) tiles(gridID int64) ([]ExtTile, error) {
	rows, err := n.s.db.Query(`SELECT id, key, kind, alt_text, x, y, w, h, view_x, view_y, view_zoom,
		text_x, text_y, text_w, text_h, COALESCE(text_mode, ''), content_zoom, COALESCE(child_grid_id, 0)
		FROM tiles WHERE ns = ? AND grid_id = ? AND tombstoned = 0 ORDER BY id`, n.ns, gridID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExtTile
	for rows.Next() {
		var t ExtTile
		if err := rows.Scan(&t.ID, &t.Key, &t.Kind, &t.Label, &t.X, &t.Y, &t.W, &t.H,
			&t.ViewX, &t.ViewY, &t.ViewZoom,
			&t.TextX, &t.TextY, &t.TextW, &t.TextH, &t.TextMode, &t.ContentZoom, &t.ChildGridID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// exec runs a single-row UPDATE on a LIVE row of this namespace, mapping
// zero rows to ErrNotFound (a retired row refuses mutation).
func (n *Namespace) exec(set string, tileID int64, args ...any) error {
	args = append(args, tileID, n.ns)
	res, err := n.s.db.Exec(`UPDATE tiles SET `+set+` WHERE id = ? AND ns = ? AND tombstoned = 0`, args...)
	if err != nil {
		return err
	}
	k, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if k == 0 {
		return ErrNotFound
	}
	return nil
}

// Place is the placement writeback: (x, y, w, h), same grid always.
// Unversioned.
func (n *Namespace) Place(tileID, x, y, w, h int64) error {
	return n.exec(`x = ?, y = ?, w = ?, h = ?`, tileID, x, y, w, h)
}

// SetWellView persists a well's preview framing — the descent target and
// ascent return value.
func (n *Namespace) SetWellView(tileID, vx, vy int64, vz float64) error {
	return n.exec(`view_x = ?, view_y = ?, view_zoom = ?`, tileID, vx, vy, vz)
}

// SetTextView persists a text tile's framed window.
func (n *Namespace) SetTextView(tileID, tx, ty, tw, th int64, mode string) error {
	var m any
	if mode != "" {
		m = mode
	}
	return n.exec(`text_x = ?, text_y = ?, text_w = ?, text_h = ?, text_mode = ?`, tileID, tx, ty, tw, th, m)
}

// SetContentZoom persists the per-tile content scale.
func (n *Namespace) SetContentZoom(tileID int64, zoom float64) error {
	return n.exec(`content_zoom = ?`, tileID, zoom)
}

// Retire tombstones one tile row directly — the delete-gesture path.
func (n *Namespace) Retire(tileID int64) error {
	return n.exec(`tombstoned = 1`, tileID)
}

// RootView reads a context's persisted root viewport; ok=false = never set.
func (n *Namespace) RootView(gridID int64) (cx, cy, zoom float64, ok bool, err error) {
	var ncx, ncy, nzoom sql.NullFloat64
	err = n.s.db.QueryRow(`SELECT root_cx, root_cy, root_zoom FROM grids WHERE id = ? AND ns = ?`, gridID, n.ns).
		Scan(&ncx, &ncy, &nzoom)
	if errors.Is(err, sql.ErrNoRows) {
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
func (n *Namespace) SetRootView(gridID int64, cx, cy, zoom float64) error {
	res, err := n.s.db.Exec(`UPDATE grids SET root_cx = ?, root_cy = ?, root_zoom = ? WHERE id = ? AND ns = ?`,
		cx, cy, zoom, gridID, n.ns)
	if err != nil {
		return err
	}
	if k, _ := res.RowsAffected(); k == 0 {
		return ErrNotFound
	}
	return nil
}

// CacheListing remembers a context's last good listing — an opaque blob
// the caller (the plugin adapter) serializes; the store never
// interprets it. Disposable in principle, durable in practice: it is the
// offline answer.
func (n *Namespace) CacheListing(gridID int64, blob []byte, authoritative bool) error {
	auth := 0
	if authoritative {
		auth = 1
	}
	_, err := n.s.db.Exec(`INSERT INTO listings (grid_id, entries, authoritative) VALUES (?, ?, ?)
		ON CONFLICT(grid_id) DO UPDATE SET entries = excluded.entries, authoritative = excluded.authoritative`,
		gridID, blob, auth)
	return err
}

// CachedListing returns the remembered listing, ok=false when none.
func (n *Namespace) CachedListing(gridID int64) (blob []byte, authoritative, ok bool, err error) {
	var auth int64
	err = n.s.db.QueryRow(`SELECT entries, authoritative FROM listings WHERE grid_id = ?`, gridID).
		Scan(&blob, &auth)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	return blob, auth != 0, true, nil
}

// ── auto-place ───────────────────────────────────────────────────────────────

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

// InsertExternalRow is the converter's door (internal/node.Convert): one
// external row with its full remembered state, minted fresh. Tombstoned
// rows are inserted tombstoned — a retired key stays retired.
func (s *Store) InsertExternalRow(ctx context.Context, ns string, gridID int64, key, kind, label string, childGridID int64, url string, tombstoned bool,
	place [4]int64, view [2]int64, viewZoom float64, text [4]int64, textMode string, contentZoom float64) (int64, error) {
	now := s.now().UnixNano()
	var child, u, mode any
	if childGridID != 0 {
		child = childGridID
	}
	if kind == "url" {
		u = url
	}
	if textMode != "" {
		mode = textMode
	}
	tomb := 0
	if tombstoned {
		tomb = 1
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO tiles (version, grid_id, kind, x, y, w, h,
		view_x, view_y, view_zoom, child_grid_id, text_x, text_y, text_w, text_h, text_mode, content_zoom,
		url_string, alt_text, created_at, updated_at, ns, key, tombstoned)
		VALUES (0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		gridID, kind, place[0], place[1], place[2], place[3],
		view[0], view[1], viewZoom, child, text[0], text[1], text[2], text[3], mode, contentZoom,
		u, label, now, now, ns, key, tomb)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
