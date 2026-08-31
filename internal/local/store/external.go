package store

// The node's memory of a plugin's entries: they live as rows in the same
// grids and tiles tables as home, under the plugin's namespace, where ns is
// the plugin id and home is ns = ''. A plugin answers from its source in
// stable string keys; the node mints the ids, keeps the user's arrangement and
// framing, and retires keys as tombstones, so an id is never reused and a
// retired key stays retired. One table and one column vocabulary for placement
// and framing, whichever namespace a tile belongs to.
//
// Identity: ids are AUTOINCREMENT and never reused. A retired key's row stays,
// so a dangling reference stays interpretable, and a recreated key mints a
// fresh id, which a partial unique index over live rows only enforces. Plugin
// rows are unversioned, version 0 on the wire, and emit no store events: the
// plugin's own listing is the truth.
//
// These rows are the node's own facts, and they are the whole of what the
// durable file keeps about a plugin's entries: the id it minted for a key,
// where the user put it, how it is framed, and the tombstone of a key that
// went away. What the source last said — listings, bodies, previews — is cache
// and lives in cache.db, in internal/sourcecache.
//
// A row exists only once the user has made a durable fact about an entry —
// moved it, resized it, framed it, or pointed a reference at it. Listing
// writes nothing: Overlay is a read-only JOIN of the plugin's entries with
// whatever rows exist, and an entry with no row is answered at a DERIVED
// placement, by the same algorithm Mint stores. So a dark source costs only
// the rows' worth: the adapter overlays an empty non-authoritative listing
// and the TOUCHED rows answer, unchanged, stamped stale — an untouched entry
// has no row and is simply absent until the source speaks again. What the
// source last said in full is the cache's job, one layer up.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/josephburnett/gridwell/api/rpc"
)

// Namespace is one plugin's view of the store.
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

// ExtTile is one joined entry: the plugin's content facts over the user's
// stored arrangement. ID is the minted row id, or 0 when the entry has no
// row yet and X/Y/W/H are therefore DERIVED — the caller names such a tile by
// its key rather than by a row id.
type ExtTile struct {
	ID          int64
	Key         string
	Kind        string
	Label       string
	X, Y, W, H  int64
	ViewCx      float64
	ViewCy      float64
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

// Overlay joins one plugin listing with the stored arrangement and WRITES
// NOTHING. It is the one join: minted rows contribute their id, placement and
// framing; the listing contributes every content fact (label, kind, url,
// serves-page), so a renamed file needs no writeback; and an entry with no row
// is answered at a placement derived here, by the same algorithm Mint stores
// when the entry is first touched. Rows the listing does not mention — a dark
// source, or a non-authoritative pass — follow at the end, answering from
// their stored snapshot, which is what makes a touched tile survive an outage.
//
// gridID may be 0: a context nobody has touched has no grid row, so there is
// nothing to overlay and every entry is derived. The order is the listing's,
// then the unmatched rows by id.
func (n *Namespace) Overlay(gridID int64, entries []Entry) ([]ExtTile, error) {
	rows := map[string]ExtTile{}
	var stored []ExtTile
	if gridID != 0 {
		var err error
		stored, err = n.tiles(gridID)
		if err != nil {
			return nil, err
		}
		for _, r := range stored {
			rows[r.Key] = r
		}
	}
	// Occupancy: the full footprints of every minted row. A derived
	// placement flows around what the user has arranged; a row never moves
	// because its neighbours changed.
	occupied := map[[2]int64]bool{}
	for _, r := range stored {
		occupyRect(occupied, r.X, r.Y, r.W, r.H)
	}
	var cur cursor
	matched := map[string]bool{}
	out := make([]ExtTile, 0, len(entries)+len(stored))
	for _, e := range entries {
		if r, ok := rows[e.Key]; ok {
			matched[e.Key] = true
			// The row owns identity, placement and framing; the listing owns
			// the content facts, read fresh every time.
			r.Kind, r.Label = entryKind(e), e.Label
			out = append(out, r)
			continue
		}
		x, y, w, h := derivePlacement(occupied, &cur, e.Hint)
		out = append(out, ExtTile{Key: e.Key, Kind: entryKind(e), Label: e.Label,
			X: x, Y: y, W: w, H: h})
	}
	for _, r := range stored {
		if !matched[r.Key] {
			out = append(out, r)
		}
	}
	return out, nil
}

// entryKind is the kind an entry answers with; "" reads as text, the one
// default, applied where the join and the mint both see it.
func entryKind(e Entry) string {
	if e.Kind == "" {
		return "text"
	}
	return e.Kind
}

// derivePlacement is the auto-place rule, and the ONLY one: a hint seeds a
// first placement, otherwise the entry takes the next free cell in reading
// order. Overlay derives with it and Mint stores exactly what Overlay derived,
// so touching a tile never moves it.
func derivePlacement(occupied map[[2]int64]bool, cur *cursor, hint *Hint) (x, y, w, h int64) {
	w, h = 1, 1
	if hint != nil {
		x, y, w, h = hint.X, hint.Y, hint.W, hint.H
		if w < 1 {
			w = 1
		}
		if h < 1 {
			h = 1
		}
		occupyRect(occupied, x, y, w, h)
		return x, y, w, h
	}
	x, y = nextEmptyCell(occupied, DefaultGridWidth, cur)
	return x, y, 1, 1
}

// Mint writes the row an entry has earned: the id, the placement it was
// already being answered at, and a snapshot of the content facts for the
// outage case. It is the one INSERT, called once per entry, by the one caller
// that decides a durable fact has been made (pluginhost.Adapter.mint). An
// entry that already has a live row returns that row's id and writes nothing,
// so minting is idempotent.
func (n *Namespace) Mint(gridID int64, e Entry, childGridID int64, x, y, w, h int64) (int64, error) {
	if id, ok, err := n.LiveTileID(gridID, e.Key); err != nil || ok {
		return id, err
	}
	kind := entryKind(e)
	var child, url any
	if childGridID != 0 {
		child = childGridID
	}
	if kind == "url" {
		url = e.URL
	}
	now := n.s.now().UnixNano()
	res, err := n.s.db.Exec(`INSERT INTO tiles (version, grid_id, kind, x, y, w, h,
		child_grid_id, url_string, alt_text, created_at, updated_at, ns, key)
		VALUES (0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		gridID, kind, x, y, w, h, child, url, e.Label, now, now, n.ns, e.Key)
	if err != nil {
		return 0, fmt.Errorf("store: mint %q: %w", e.Key, err)
	}
	return res.LastInsertId()
}

// Refresh updates the CONTENT SNAPSHOT a row keeps — the kind, the label, the
// address — to what the listing just said. The snapshot is not what a listed
// entry reads by (Overlay takes those facts from the entry itself); it is only
// what the row answers with when the source cannot be reached, so this keeps
// the outage answer current with the last thing the source actually said.
//
// It writes only where a value genuinely differs, so a steady listing writes
// nothing at all and a renamed file costs exactly one UPDATE. child_grid_id is
// deliberately not refreshed: it is a stored REFERENCE, and re-pointing one
// because a listing came back differently is how a link starts naming
// something the user never linked.
func (n *Namespace) Refresh(gridID int64, entries []Entry) error {
	if gridID == 0 || len(entries) == 0 {
		return nil
	}
	stored, err := n.tiles(gridID)
	if err != nil {
		return err
	}
	byKey := map[string]ExtTile{}
	for _, r := range stored {
		byKey[r.Key] = r
	}
	now := n.s.now().UnixNano()
	for _, e := range entries {
		r, ok := byKey[e.Key]
		if !ok {
			continue
		}
		kind := entryKind(e)
		if r.Kind == kind && r.Label == e.Label {
			continue
		}
		if _, err := n.s.db.Exec(`UPDATE tiles SET kind = ?, alt_text = ?, updated_at = ?
			WHERE id = ? AND ns = ? AND tombstoned = 0`, kind, e.Label, now, r.ID, n.ns); err != nil {
			return fmt.Errorf("store: refresh %q: %w", e.Key, err)
		}
	}
	return nil
}

// Sweep retires the rows of a grid whose keys an AUTHORITATIVE listing did not
// mention: the source says they are gone, so the ids they minted retire and
// never come back. Only rows are swept — an untouched entry has nothing to
// sweep, it simply stops appearing. Nothing is written when nothing vanished.
func (n *Namespace) Sweep(gridID int64, present map[string]bool) error {
	rows, err := n.tiles(gridID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if present[r.Key] {
			continue
		}
		if err := n.Retire(r.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// LookupContext resolves a context key to its grid row id WITHOUT minting one:
// the read half of ContextID, for the join, which must not write.
func (n *Namespace) LookupContext(key string) (int64, bool, error) {
	var id int64
	err := n.s.db.QueryRow(`SELECT id FROM grids WHERE ns = ? AND context_key = ?`, n.ns, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// LiveTileID resolves (grid, plugin key) to the live row minted for it, if
// there is one. It is TileKey's inverse and the join's canonicalization: an
// entry with a row is named by that row's id, never by its key.
func (n *Namespace) LiveTileID(gridID int64, key string) (int64, bool, error) {
	if gridID == 0 {
		return 0, false, nil
	}
	var id int64
	err := n.s.db.QueryRow(`SELECT id FROM tiles WHERE ns = ? AND grid_id = ? AND key = ? AND tombstoned = 0`,
		n.ns, gridID, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// tiles lists the live rows of a grid, by id.
func (n *Namespace) tiles(gridID int64) ([]ExtTile, error) {
	rows, err := n.s.db.Query(`SELECT id, key, kind, alt_text, x, y, w, h, view_cx, view_cy, view_zoom,
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
			&t.ViewCx, &t.ViewCy, &t.ViewZoom,
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

// SetFraming persists framing into this namespace's memory — the ONE
// writer of the one shape (framing.go), tile or root. Exactly one of
// tileID and rootGridID is non-zero.
func (n *Namespace) SetFraming(tileID, rootGridID int64, f rpc.Framing) error {
	k, err := updateFraming(context.Background(), n.s.db, n.ns, tileID, rootGridID, f, n.s.now().UnixNano())
	if err != nil {
		return err
	}
	if k == 0 {
		return ErrNotFound
	}
	return nil
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

// RootFraming reads a ROOT grid's framing — the row that owns it when
// there is no doorway tile. ok=false = never visited (no row value, or a
// zero zoom: the one convention framing.go documents).
func (n *Namespace) RootFraming(gridID int64) (f rpc.Framing, ok bool, err error) {
	var ncx, ncy, nzoom sql.NullFloat64
	err = n.s.db.QueryRow(`SELECT root_cx, root_cy, root_zoom FROM grids WHERE id = ? AND ns = ?`, gridID, n.ns).
		Scan(&ncx, &ncy, &nzoom)
	if errors.Is(err, sql.ErrNoRows) {
		return rpc.Framing{}, false, ErrNotFound
	}
	if err != nil {
		return rpc.Framing{}, false, err
	}
	if !nzoom.Valid || nzoom.Float64 <= 0 {
		return rpc.Framing{}, false, nil
	}
	return rpc.Framing{Cx: ncx.Float64, Cy: ncy.Float64, Zoom: nzoom.Float64}, true, nil
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
	place [4]int64, view rpc.Framing, text [4]int64, textMode string, contentZoom float64) (int64, error) {
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
		view_cx, view_cy, view_zoom, child_grid_id, text_x, text_y, text_w, text_h, text_mode, content_zoom,
		url_string, alt_text, created_at, updated_at, ns, key, tombstoned)
		VALUES (0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		gridID, kind, place[0], place[1], place[2], place[3],
		view.Cx, view.Cy, view.Zoom, child, text[0], text[1], text[2], text[3], mode, contentZoom,
		u, label, now, now, ns, key, tomb)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
