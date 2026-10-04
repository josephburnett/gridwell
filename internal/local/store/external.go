package store

// The node's memory of a plugin's entries: rows in the same grids and tiles
// tables as home, under ns = the plugin id (home is ns = ''). The plugin
// answers in stable string keys; the node mints the ids, keeps arrangement and
// framing, and retires keys as tombstones. Ids are AUTOINCREMENT and never
// reused, and a recreated key mints a fresh id (a partial unique index over
// live rows). Plugin rows are unversioned and emit no store events.
//
// A row exists only once the user has made a durable fact about an entry.
// Listing mints nothing: Overlay is a read-only join that derives an
// untouched entry's placement by the algorithm Mint stores, and Refresh and
// Sweep write only to rows that exist. An entry with a link_target is a link
// row, its link_target_id the target's namespace-relative address
// (rpc.EntryTileID), which the router qualifies on the way out.

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

// SQL exposes the store's one database handle for the node's other tables: a
// second handle on the same file would meet an instant SQLITE_BUSY.
func (s *Store) SQL() *sql.DB { return s.db }

// ExtTile is one joined entry: the node's row, or a derived placement when
// the entry has none. ID is the minted row id, 0 when derived; ChildGridID is
// a well's minted child grid, 0 for a leaf. The caller names every such tile
// by its key either way; see rpc.EntryTileID.
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
// nothing. Rows contribute id, placement and framing; the listing contributes
// every content fact. Rows the listing does not mention follow at the end from
// their stored snapshot, which is what makes a touched tile survive an outage.
// gridID 0 means an untouched context: every entry is derived.
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
		// Every entry takes a slot in the flow, minted or not, so touching
		// or dragging one tile never shifts the rest by a cell. An entry's
		// own row is its slot, not an obstacle to it.
		r, ok := rows[e.Key]
		if ok {
			vacateRect(occupied, r.X, r.Y, r.W, r.H)
		}
		x, y, w, h := derivePlacement(occupied, &cur, e.PlacementHint)
		if ok {
			occupyRect(occupied, r.X, r.Y, r.W, r.H)
			matched[e.Key] = true
			r.Kind, r.AltText, r.LinkTargetId = entryKind(e), e.Label, linkTargetOf(e)
			if r.LinkTargetId != "" {
				r.UrlString, r.ServesPage, r.PreviewBlobId = "", false, 0
			}
			out = append(out, r)
			continue
		}
		out = append(out, ExtTile{Key: e.Key, Tile: &gridwellv1.Tile{
			Kind: entryKind(e), AltText: e.Label, LinkTargetId: linkTargetOf(e), X: x, Y: y, W: w, H: h}})
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

// linkTargetOf is the address of the entry e links to, "" when e owns its
// content.
func linkTargetOf(e *pluginv1.Entry) string {
	if lt := e.GetLinkTarget(); lt != nil {
		return rpc.EntryTileID(lt.Context, lt.Key)
	}
	return ""
}

// entrySnapshot is what a row keeps of its entry's content facts, presented
// when its source does not list it. Mint writes it and Refresh keeps it. A
// link holds only its target: the rest are the target's.
type entrySnapshot struct {
	kind string
	url  sql.NullString // a url row's address, NULL on every other kind and on a link
	page bool
	link sql.NullString
}

func snapshotOf(e *pluginv1.Entry) entrySnapshot {
	s := entrySnapshot{kind: entryKind(e)}
	if t := linkTargetOf(e); t != "" {
		s.link = sql.NullString{String: t, Valid: true}
		return s
	}
	if s.kind == "url" {
		s.url = sql.NullString{String: e.UrlString, Valid: true}
		s.page = e.ServesPage
	}
	return s
}

// derivePlacement places a hinted entry at the first free rect of the hint's
// size at or below the hint, in the hint's own column, so a hint is a
// preference that never lands on an occupied cell and a calendar's column
// stays its column. Any other entry takes the next free cell from the
// listing's cursor by the one auto-place rule (autoplace.go). Overlay derives
// with it and Mint stores what Overlay derived, so touching a tile never
// moves it.
func derivePlacement(occupied map[[2]int64]bool, cur *cursor, hint *pluginv1.PlacementHint) (x, y, w, h int64) {
	if hint != nil {
		x, y, w, h = hint.X, hint.Y, max(hint.W, 1), max(hint.H, 1)
		for !rectFree(occupied, x, y, w, h) {
			y++
		}
		occupyRect(occupied, x, y, w, h)
		return x, y, w, h
	}
	x, y = nextFreeRect(occupied, cur, 1, 1)
	return x, y, 1, 1
}

// Mint writes the row an entry has earned: the id, the placement it was
// already answered at, and a content snapshot. It is the one INSERT; an entry
// with a live row returns that id and writes nothing.
func (n *Namespace) Mint(gridID int64, e *pluginv1.Entry, childGridID int64, x, y, w, h int64) (int64, error) {
	if id, ok, err := n.LiveTileID(gridID, e.Key); err != nil || ok {
		return id, err
	}
	s := snapshotOf(e)
	var child any
	if childGridID != 0 {
		child = childGridID
	}
	now := n.s.now().UnixNano()
	res, err := n.s.db.Exec(`INSERT INTO tiles (version, grid_id, kind, x, y, w, h,
		child_grid_id, url_string, alt_text, serves_page, link_target_id, created_at, updated_at, ns, key)
		VALUES (0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		gridID, s.kind, x, y, w, h, child, s.url, e.Label, s.page, s.link, now, now, n.ns, e.Key)
	if err != nil {
		return 0, fmt.Errorf("store: mint %q: %w", e.Key, err)
	}
	return res.LastInsertId()
}

// Refresh updates a row's content snapshot to what the listing just said,
// writing only where a value differs. child_grid_id is deliberately not
// refreshed: re-pointing a stored reference because a listing changed is how
// a link starts naming something the user never linked. A link target is
// different, being the source's own content fact rather than the user's, and
// an entry that becomes a link converts its row in place, keeping its id,
// placement and framing (convert).
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
	for _, e := range entries {
		r, ok := byKey[e.Key]
		if !ok {
			continue
		}
		s := snapshotOf(e)
		if r.Kind == s.kind && r.AltText == e.Label && r.UrlString == s.url.String && r.ServesPage == s.page &&
			r.LinkTargetId == s.link.String {
			continue
		}
		if err := n.convert(r, e.Label, s); err != nil {
			return fmt.Errorf("store: refresh %q: %w", e.Key, err)
		}
	}
	return nil
}

// convert writes a row's new snapshot. A row that becomes a link gives up
// what the tiles CHECK lets only an owner hold, its screenshot and its text
// mode, since a link's face is its target's.
func (n *Namespace) convert(r ExtTile, label string, s entrySnapshot) error {
	ctx := context.Background()
	return n.s.withMutation(ctx, "Refresh", func(tx *sql.Tx, _ *[]*gridwellv1.Event) error {
		if s.link.Valid {
			if _, err := tx.ExecContext(ctx, `UPDATE tiles SET preview_blob_id = NULL, text_mode = NULL
				WHERE id = ? AND ns = ? AND tombstoned = 0`, r.ID, n.ns); err != nil {
				return err
			}
			if r.PreviewBlobId != 0 {
				if err := n.s.decBlobRefcount(ctx, tx, r.PreviewBlobId); err != nil {
					return err
				}
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE tiles SET kind = ?, alt_text = ?, url_string = ?, serves_page = ?,
			link_target_id = ?, updated_at = ? WHERE id = ? AND ns = ? AND tombstoned = 0`,
			s.kind, label, s.url, s.page, s.link, n.s.now().UnixNano(), r.ID, n.ns)
		return err
	})
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
	return collect(rows, func(rows *sql.Rows) (ExtTile, error) {
		var key string
		t, err := scanTile(rows, &key)
		if err != nil {
			return ExtTile{}, err
		}
		id, err := strconv.ParseInt(t.Id, 10, 64)
		if err != nil {
			return ExtTile{}, fmt.Errorf("store: row id %q: %w", t.Id, err)
		}
		// A leaf's child_grid_id is NULL, which the descriptor reads as "".
		var child int64
		if t.ChildGridId != "" {
			if child, err = strconv.ParseInt(t.ChildGridId, 10, 64); err != nil {
				return ExtTile{}, fmt.Errorf("store: row %d child grid %q: %w", id, t.ChildGridId, err)
			}
		}
		return ExtTile{ID: id, Key: key, ChildGridID: child, Tile: t}, nil
	})
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
	return n.exec(placementSet, tileID, x, y, w, h)
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
	return n.exec(textViewSet+textModeSet, tileID, tx, ty, tw, th, m)
}

// SetContentZoom persists the per-tile content scale.
func (n *Namespace) SetContentZoom(tileID int64, zoom rpc.ContentZoom) error {
	return n.exec(contentZoomSet, tileID, zoom.Float())
}

// Retire tombstones one tile row: the delete-gesture path. The row stays so a
// stale reference stays interpretable, and its screenshot is released.
func (n *Namespace) Retire(tileID int64) error {
	ctx := context.Background()
	return n.s.withMutation(ctx, "Retire", func(tx *sql.Tx, _ *[]*gridwellv1.Event) error {
		var preview sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT preview_blob_id FROM tiles WHERE id = ? AND ns = ? AND tombstoned = 0`,
			tileID, n.ns).Scan(&preview)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tiles SET tombstoned = 1, preview_blob_id = NULL WHERE id = ?`, tileID); err != nil {
			return err
		}
		if preview.Valid {
			return n.s.decBlobRefcount(ctx, tx, preview.Int64)
		}
		return nil
	})
}

// RootFraming reads a root grid's framing, the row that owns it when there is
// no doorway tile.
func (n *Namespace) RootFraming(gridID int64) (rpc.View, error) {
	var ncx, ncy, nzoom sql.NullFloat64
	err := n.s.db.QueryRow(`SELECT root_cx, root_cy, root_zoom FROM grids WHERE id = ? AND ns = ?`, gridID, n.ns).
		Scan(&ncx, &ncy, &nzoom)
	if errors.Is(err, sql.ErrNoRows) {
		return rpc.View{}, ErrNotFound
	}
	if err != nil || !ncx.Valid || !ncy.Valid || !nzoom.Valid {
		return rpc.View{}, err
	}
	return rpc.ViewOf(ncx.Float64, ncy.Float64, nzoom.Float64), nil
}
