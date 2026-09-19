package store

// The node's memory of a plugin's entries: rows in the same grids and tiles
// tables as home, under the plugin's namespace, where ns is the plugin id and
// home is ns = ''. A plugin answers from its source in stable string keys; the
// node mints the ids, keeps the user's arrangement and framing, and retires
// keys as tombstones. Ids are AUTOINCREMENT and never reused; a retired key's
// row stays, so a dangling reference stays interpretable, and a recreated key
// mints a fresh id, which a partial unique index over live rows enforces.
// Plugin rows are unversioned and emit no store events: the plugin's listing
// is the truth.
//
// These rows are the whole of what the durable file keeps about a plugin's
// entries. What a source last answered is cache: a connection's answers live
// in cache.db (internal/sourcecache) and a plugin's own memory of its source
// in its state_dir.
//
// A row exists only once the user has made a durable fact about an entry.
// Listing mints nothing: Overlay is a read-only join, and an entry with no
// row is answered at a placement derived by the same algorithm Mint stores;
// Refresh and Sweep write only to rows that exist. So a dark source answers
// from the touched rows, unchanged and stamped stale, and an untouched entry
// is simply absent until the source speaks again.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
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

// SQL exposes the store's one database handle for the node's other tables.
// SQLite is single-writer per file and this handle runs one connection, so a
// second handle on the same file would meet an instant SQLITE_BUSY.
func (s *Store) SQL() *sql.DB { return s.db }

// ExtTile is one joined entry: the node's row, or a derived placement when
// the entry has none, under the plugin's key. Tile holds the row's stored
// columns as the wire record they are, scanned through the one column
// descriptor (columns.go), and the listing's content facts are laid over it
// by the caller. ID is the minted row id, 0 when the entry has no row and the
// placement is derived; ChildGridID is the minted grid of a well's child
// context, 0 for a leaf. The caller names every such tile by its key either
// way; see pluginhost.tileAddr.
type ExtTile struct {
	ID          int64
	Key         string
	ChildGridID int64
	*gridwellv1.Tile
}

// ContextID resolves a context key to its grid id, minting on first sight.
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

// TileKey resolves a minted tile id to its (grid id, plugin key). A retired
// row still resolves, so a dangling reference stays interpretable; the caller
// decides what retirement means.
func (n *Namespace) TileKey(tileID int64) (gridID int64, key string, tombstoned bool, err error) {
	var tomb int64
	err = n.s.db.QueryRow(`SELECT grid_id, key, tombstoned FROM tiles WHERE id = ? AND ns = ?`, tileID, n.ns).
		Scan(&gridID, &key, &tomb)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, ErrNotFound
	}
	return gridID, key, tomb != 0, err
}

// Overlay joins one plugin listing with the stored arrangement and writes
// nothing. Minted rows contribute their id, placement and framing; the listing
// contributes every content fact, so a renamed file needs no writeback; and an
// entry with no row is answered at a placement derived here, by the algorithm
// Mint stores when the entry is first touched. Rows the listing does not
// mention follow at the end, answering from their stored snapshot, which is
// what makes a touched tile survive an outage. gridID may be 0: a context
// nobody has touched has no grid row, so every entry is derived.
func (n *Namespace) Overlay(gridID int64, entries []*pluginv1.Entry) ([]ExtTile, error) {
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
	// A derived placement flows around what the user has arranged; a row
	// never moves because its neighbours changed.
	occupied := map[[2]int64]bool{}
	for _, r := range stored {
		occupyRect(occupied, r.X, r.Y, r.W, r.H)
	}
	var cur cursor
	matched := map[string]bool{}
	out := make([]ExtTile, 0, len(entries)+len(stored))
	for _, e := range entries {
		// Every entry takes a slot in the flow, minted or not, and a minted
		// row then overrides its own slot. If a minted entry gave up its
		// slot, every entry after it would shift by a cell the moment the
		// user dragged it, and dragging one tile would rearrange the room.
		x, y, w, h := derivePlacement(occupied, &cur, e.PlacementHint)
		if r, ok := rows[e.Key]; ok {
			matched[e.Key] = true
			// The row owns identity, placement and framing; the listing owns
			// the content facts.
			r.Kind, r.AltText = entryKind(e), e.Label
			out = append(out, r)
			continue
		}
		out = append(out, ExtTile{Key: e.Key, Tile: &gridwellv1.Tile{
			Kind: entryKind(e), AltText: e.Label, X: x, Y: y, W: w, H: h}})
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
func entryKind(e *pluginv1.Entry) string {
	if e.Kind == "" {
		return "text"
	}
	return e.Kind
}

// derivePlacement seeds a first placement from the hint, else takes the next
// free cell by the one auto-place rule (autoplace.go). Overlay derives with it
// and Mint stores what Overlay derived, so touching a tile never moves it.
func derivePlacement(occupied map[[2]int64]bool, cur *cursor, hint *pluginv1.PlacementHint) (x, y, w, h int64) {
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
	x, y = nextFreeRect(occupied, cur, 1, 1)
	return x, y, 1, 1
}

// Mint writes the row an entry has earned: the id, the placement it was
// already being answered at, and a snapshot of the content facts for the
// outage case. It is the one INSERT, called by pluginhost.Adapter.mint when a
// durable fact has been made. An entry that already has a live row returns
// that row's id and writes nothing.
func (n *Namespace) Mint(gridID int64, e *pluginv1.Entry, childGridID int64, x, y, w, h int64) (int64, error) {
	if id, ok, err := n.LiveTileID(gridID, e.Key); err != nil || ok {
		return id, err
	}
	kind := entryKind(e)
	var child, url any
	if childGridID != 0 {
		child = childGridID
	}
	if kind == "url" {
		url = e.UrlString
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

// Refresh updates the content snapshot a row keeps to what the listing just
// said. The snapshot is only what the row answers with when the source cannot
// be reached, since Overlay takes a listed entry's facts from the entry
// itself. It writes only where a value differs, so a steady listing writes
// nothing. child_grid_id is deliberately not refreshed: it is a stored
// reference, and re-pointing one because a listing came back differently is
// how a link starts naming something the user never linked.
func (n *Namespace) Refresh(gridID int64, entries []*pluginv1.Entry) error {
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
		if r.Kind == kind && r.AltText == e.Label {
			continue
		}
		if _, err := n.s.db.Exec(`UPDATE tiles SET kind = ?, alt_text = ?, updated_at = ?
			WHERE id = ? AND ns = ? AND tombstoned = 0`, kind, e.Label, now, r.ID, n.ns); err != nil {
			return fmt.Errorf("store: refresh %q: %w", e.Key, err)
		}
	}
	return nil
}

// Sweep retires the rows of a grid whose keys an authoritative listing did not
// mention: the source says they are gone, so the ids they minted retire and
// never come back. An untouched entry has nothing to sweep.
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

// LookupContext is ContextID without the mint, for the join, which must not
// write.
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

// LiveTileID is TileKey's inverse: an entry with a row is named by that row's
// id, never by its key.
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

// tiles lists the live rows of a grid, by id, the stored columns through the
// one descriptor and the key beside them.
func (n *Namespace) tiles(gridID int64) ([]ExtTile, error) {
	rows, err := n.s.db.Query(`SELECT `+tileColumns+`, key FROM tiles
		WHERE ns = ? AND grid_id = ? AND tombstoned = 0 ORDER BY id`, n.ns, gridID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExtTile
	for rows.Next() {
		t := &gridwellv1.Tile{}
		var key string
		if err := rows.Scan(append(scanDests(tilesColumns, t), &key)...); err != nil {
			return nil, err
		}
		id, err := strconv.ParseInt(t.Id, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("store: row id %q: %w", t.Id, err)
		}
		// A leaf's child_grid_id is NULL, which the descriptor reads as "".
		var child int64
		if t.ChildGridId != "" {
			if child, err = strconv.ParseInt(t.ChildGridId, 10, 64); err != nil {
				return nil, fmt.Errorf("store: row %d child grid %q: %w", id, t.ChildGridId, err)
			}
		}
		out = append(out, ExtTile{ID: id, Key: key, ChildGridID: child, Tile: t})
	}
	return out, rows.Err()
}

// exec runs a single-row UPDATE on a live row of this namespace, mapping zero
// rows to ErrNotFound: a retired row refuses mutation.
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

// Place is the placement writeback; the grid never changes.
func (n *Namespace) Place(tileID, x, y, w, h int64) error {
	return n.exec(`x = ?, y = ?, w = ?, h = ?`, tileID, x, y, w, h)
}

// SetFraming persists framing into this namespace's memory, the one writer of
// the one shape (framing.go). Exactly one of tileID and rootGridID is set.
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

// Retire tombstones one tile row: the delete-gesture path.
func (n *Namespace) Retire(tileID int64) error {
	return n.exec(`tombstoned = 1`, tileID)
}

// RootFraming reads a root grid's framing, the row that owns it when there is
// no doorway tile. ok=false means never visited: no value, or a zero zoom, the
// convention framing.go documents.
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
