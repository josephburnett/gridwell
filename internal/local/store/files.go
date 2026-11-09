package store

// The files' rows: rows in the same grids and tiles tables as home, keyed by
// the path they are about (key; home's rows have key ''). The files answer in
// paths; the node mints the ids, keeps arrangement and framing, and retires
// paths as tombstones. Ids are AUTOINCREMENT and never
// reused, and a recreated key mints a fresh id (a partial unique index over
// live rows). These rows emit no store events, and a row's version moves
// only on a typed name.
//
// A row exists only once the user has made a durable fact about an entry.
// Listing mints nothing: Overlay is a read-only join that derives an
// untouched entry's placement by the algorithm Mint stores, and Refresh and
// Sweep write only to rows that exist. An entry with a LinkTarget is a link
// row, its link_target_id the target's namespace-relative address, which the
// router qualifies on the way out. A row's typed name is the user's, as on
// home's rows: alt_text with alt_user latched, which no listing overwrites.
// A file's url row is a page, which has no address, so its url_string is ''.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Entry is one thing a directory lists: its path and the content facts the
// disk owns. LinkTarget is the address of the thing it links to, "" when it
// owns its content; Child is a door's grid key.
type Entry struct {
	Key, Label, Kind string
	ServesPage       bool
	LinkTarget       string
	Child            string
}

// FileRows is the files' view of the store.
type FileRows struct{ s *Store }

// Files is the files' rows.
func (s *Store) Files() *FileRows { return &FileRows{s: s} }

// side is which of a table's two kinds of row a query keeps to: home's,
// whose key is ”, or the files', whose key is a path.
type side bool

const (
	home  side = false
	files side = true
)

// of is the predicate on col, a key column, that keeps a query to the side.
func (on side) of(col string) string {
	if on {
		return col + " != ''"
	}
	return col + " = ''"
}

// FileTile is one joined entry: the node's row, or a derived placement when
// the entry has none. ID is the minted row id, 0 when derived; ChildGridID is
// a door's minted child grid, 0 for a leaf.
type FileTile struct {
	ID          int64
	Key         string
	ChildGridID int64
	*gridwellv1.Tile
}

// GridOf resolves a grid key to its grid id, minting on first sight; two
// first sights at once mint one row and both read it.
func (n *FileRows) GridOf(key string) (int64, error) {
	id, ok, err := n.LookupGrid(key)
	if err != nil || ok {
		return id, err
	}
	now := n.s.now().Unix()
	err = n.write("GridOf", "c/"+key, func(ctx context.Context, tx *sql.Tx) error {
		id, err = mintKeyGrid(ctx, tx, key, now)
		return err
	})
	return id, err
}

// GridKey resolves a minted grid id back to the source's grid key.
func (n *FileRows) GridKey(gridID int64) (string, error) {
	var key string
	err := n.s.db.QueryRow(`SELECT key FROM grids WHERE id = ? AND key != ''`, gridID).Scan(&key)
	return key, notFound(err)
}

// TileKey resolves a minted tile id to its (grid id, source key). A retired
// row still resolves, so a dangling link stays interpretable; the caller
// decides what retirement means.
func (n *FileRows) TileKey(tileID int64) (gridID int64, key string, tombstoned bool, err error) {
	var tomb int64
	err = n.s.db.QueryRow(`SELECT grid_id, key, tombstoned FROM tiles WHERE id = ? AND key != ''`, tileID).
		Scan(&gridID, &key, &tomb)
	return gridID, key, tomb != 0, notFound(err)
}

// Over is home's rows on a directory's grid, the Gridwell tiles placed over
// it, as home answers them: each named by its digits and its grid by the
// directory's id. gridID 0, a grid never touched, holds none.
func (n *FileRows) Over(gridID int64) ([]*gridwellv1.Tile, error) {
	if gridID == 0 {
		return nil, nil
	}
	return n.s.loadTilesInGrid(context.Background(), n.s.db, gridID)
}

// Overlay joins one listing with the stored arrangement and writes
// nothing. Rows contribute id, placement, framing and a typed name; the listing contributes every other content fact. Rows the listing does not mention follow at the end from
// their stored snapshot, which is what makes a touched tile survive an outage.
// gridID 0 means an untouched grid: every entry is derived.
func (n *FileRows) Overlay(gridID int64, entries []Entry) ([]FileTile, error) {
	rows := map[string]FileTile{}
	var stored []FileTile
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
	// A derived placement flows around what the user has arranged, a
	// Gridwell tile placed over the directory among it; a row never moves
	// because its neighbours changed.
	occupied := map[[2]int64]bool{}
	for _, r := range stored {
		occupyRect(occupied, r.X, r.Y, r.W, r.H)
	}
	over, err := n.Over(gridID)
	if err != nil {
		return nil, err
	}
	for _, t := range over {
		occupyRect(occupied, t.X, t.Y, t.W, t.H)
	}
	var cur cursor
	matched := map[string]bool{}
	out := make([]FileTile, 0, len(entries)+len(stored))
	for _, e := range entries {
		// Every entry takes a slot in the flow, minted or not, so touching
		// or dragging one tile never shifts the rest by a cell. An entry's
		// own row is its slot, not an obstacle to it.
		r, ok := rows[e.Key]
		if ok {
			vacateRect(occupied, r.X, r.Y, r.W, r.H)
		}
		x, y := nextFreeRect(occupied, &cur, 1, 1)
		if ok {
			occupyRect(occupied, r.X, r.Y, r.W, r.H)
			matched[e.Key] = true
			r.Kind, r.LinkTargetId = entryKind(e), e.LinkTarget
			if !r.Named {
				r.AltText = e.Label
			}
			if rpc.LeafLink(r.Tile) {
				r.UrlString, r.ServesPage, r.PreviewBlobId = "", false, 0
			}
			out = append(out, r)
			continue
		}
		out = append(out, FileTile{Key: e.Key, Tile: &gridwellv1.Tile{Kind: entryKind(e), AltText: e.Label,
			LinkTargetId: e.LinkTarget, X: x, Y: y, W: 1, H: 1}})
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
		return rpc.KindText
	}
	return e.Kind
}

// entrySnapshot is what a row keeps of its entry's content facts, presented
// when its source does not list it. Mint writes it and Refresh keeps it. A
// link holds only its target: the rest are the target's.
type entrySnapshot struct {
	kind string
	url  sql.NullString // '' on a url row, NULL on every other kind and on a link
	page bool
	link sql.NullString
}

func snapshotOf(e Entry) entrySnapshot {
	s := entrySnapshot{kind: entryKind(e)}
	if t := e.LinkTarget; t != "" {
		s.link = sql.NullString{String: t, Valid: true}
		return s
	}
	if rpc.KindOf(s.kind).Content == rpc.ContentAddress {
		s.url = sql.NullString{Valid: true}
		s.page = e.ServesPage
	}
	return s
}

// Mint writes the row an entry has earned: the id, the placement it was
// already answered at, and a content snapshot. It is the one INSERT; an entry
// with a live row, one a mint racing it made included, returns that id and
// writes nothing.
func (n *FileRows) Mint(gridID int64, e Entry, childGridID int64, x, y, w, h int64) (int64, error) {
	if id, ok, err := n.LiveTileID(gridID, e.Key); err != nil || ok {
		return id, err
	}
	s := snapshotOf(e)
	var child any
	if childGridID != 0 {
		child = childGridID
	}
	now := n.s.now().Unix()
	var id int64
	err := n.write("Mint", "g/"+strconv.FormatInt(gridID, 10)+"/"+e.Key, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tiles (version, grid_id, kind, x, y, w, h,
			child_grid_id, url_string, alt_text, serves_page, link_target_id, created_at, updated_at, key)
			VALUES (0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			gridID, s.kind, x, y, w, h, child, s.url, e.Label, s.page, s.link, now, now, e.Key); err != nil {
			return fmt.Errorf("store: mint %q: %w", e.Key, err)
		}
		return tx.QueryRowContext(ctx, `SELECT id FROM tiles WHERE grid_id = ? AND key = ? AND tombstoned = 0`,
			gridID, e.Key).Scan(&id)
	})
	return id, err
}

// Refresh updates a row's content snapshot to what the listing just said,
// writing only where a value differs. A door's child_grid_id is
// deliberately not refreshed: re-pointing a stored link because a listing
// changed is how a link starts naming something the user never linked. A
// link target is different, being the source's own content fact rather than
// the user's, and an entry that becomes a link or another kind converts its
// row in place, keeping its id, placement and framing (convert): a row that
// stops being a door gives up its grid, and one that becomes a door takes
// its entry's, since the tiles CHECK lets only a door hold one.
func (n *FileRows) Refresh(gridID int64, entries []Entry) error {
	if gridID == 0 || len(entries) == 0 {
		return nil
	}
	stored, err := n.tiles(gridID)
	if err != nil {
		return err
	}
	byKey := map[string]FileTile{}
	for _, r := range stored {
		byKey[r.Key] = r
	}
	for _, e := range entries {
		r, ok := byKey[e.Key]
		if !ok {
			continue
		}
		s := snapshotOf(e)
		if r.Kind == s.kind && (r.Named || r.AltText == e.Label) && r.UrlString == s.url.String &&
			r.ServesPage == s.page && r.LinkTargetId == s.link.String {
			continue
		}
		child := sql.NullInt64{Int64: r.ChildGridID, Valid: r.ChildGridID != 0}
		switch {
		case !rpc.IsDoorKind(s.kind) || s.link.Valid:
			child = sql.NullInt64{}
		case !child.Valid:
			if e.Child == "" {
				return fmt.Errorf("store: refresh %q: a door names no grid", e.Key)
			}
			id, err := n.GridOf(e.Child)
			if err != nil {
				return err
			}
			child = sql.NullInt64{Int64: id, Valid: true}
		}
		if err := n.convert(r, e.Label, s, child); err != nil {
			return fmt.Errorf("store: refresh %q: %w", e.Key, err)
		}
	}
	return nil
}

// convert writes a row's new snapshot and child, the grid only a door holds.
// A row that becomes a link gives up what the tiles CHECK lets only an owner
// hold, its screenshot and its text mode, since a link's preview is its
// target's. A typed name stays.
func (n *FileRows) convert(r FileTile, label string, s entrySnapshot, child sql.NullInt64) error {
	return n.write("Refresh", tileRow(r.ID), func(ctx context.Context, tx *sql.Tx) error {
		if s.link.Valid {
			if _, err := tx.ExecContext(ctx, `UPDATE tiles SET preview_blob_id = NULL, text_mode = NULL
				WHERE id = ? AND key != '' AND tombstoned = 0`, r.ID); err != nil {
				return err
			}
			if r.PreviewBlobId != 0 {
				if err := n.s.decBlobRefcount(ctx, tx, r.PreviewBlobId); err != nil {
					return err
				}
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE tiles SET kind = ?,
			alt_text = CASE WHEN alt_user = 1 THEN alt_text ELSE ? END, url_string = ?, serves_page = ?,
			link_target_id = ?, child_grid_id = ?, updated_at = ? WHERE id = ? AND key != '' AND tombstoned = 0`,
			s.kind, label, s.url, s.page, s.link, child, n.s.now().Unix(), r.ID)
		return err
	})
}

// Sweep retires the rows of a grid whose keys an authoritative listing did not
// mention: the source says they are gone, so the ids they minted retire and
// never come back. An untouched entry has nothing to sweep.
func (n *FileRows) Sweep(gridID int64, present map[string]bool) error {
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

// LookupGrid is GridOf without the mint, for the join, which must not
// write.
func (n *FileRows) LookupGrid(key string) (int64, bool, error) {
	return n.lookupID(`SELECT id FROM grids WHERE key = ? AND key != ''`, key)
}

// LiveTileID is TileKey's inverse: an entry with a row is named by that row's
// id, never by its key.
func (n *FileRows) LiveTileID(gridID int64, key string) (int64, bool, error) {
	if gridID == 0 {
		return 0, false, nil
	}
	return n.lookupID(`SELECT id FROM tiles WHERE grid_id = ? AND key = ? AND key != '' AND tombstoned = 0`, gridID, key)
}

// lookupID answers the id query finds, ok false when it finds none.
func (n *FileRows) lookupID(query string, args ...any) (id int64, ok bool, err error) {
	err = n.s.db.QueryRow(query, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// tiles lists the live rows of a grid, by id, the stored columns through the
// one descriptor and the key beside them.
func (n *FileRows) tiles(gridID int64) ([]FileTile, error) {
	rows, err := n.s.db.Query(`SELECT `+tileColumns+`, key FROM tiles
		WHERE key != '' AND grid_id = ? AND tombstoned = 0 ORDER BY id`, gridID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(rows *sql.Rows) (FileTile, error) {
		var key string
		t, err := scanTile(rows, &key)
		if err != nil {
			return FileTile{}, err
		}
		id, err := strconv.ParseInt(t.Id, 10, 64)
		if err != nil {
			return FileTile{}, fmt.Errorf("store: row id %q: %w", t.Id, err)
		}
		// A leaf's child_grid_id is NULL, which the descriptor reads as "".
		var child int64
		if t.ChildGridId != "" {
			if child, err = strconv.ParseInt(t.ChildGridId, 10, 64); err != nil {
				return FileTile{}, fmt.Errorf("store: row %d child grid %q: %w", id, t.ChildGridId, err)
			}
		}
		return FileTile{ID: id, Key: key, ChildGridID: child, Tile: t}, nil
	})
}

// write runs one write to this namespace's rows through the store's funnel
// (withMutation). A source row publishes no store event, so row names what it
// touched for the record: an id as rpc.EventKey spells it, or, before the
// mint, its grid and key.
func (n *FileRows) write(verb, row string, fn func(ctx context.Context, tx *sql.Tx) error) error {
	ctx := context.Background()
	return n.s.withMutation(ctx, verb, func(tx *sql.Tx, _ *[]*gridwellv1.Event) error {
		return fn(ctx, tx)
	}, "~:"+row)
}

func tileRow(id int64) string { return "t/" + strconv.FormatInt(id, 10) }

// rowWrite is write for one existing row of table that carries no claim
// (store.withRowWrite): changed is false when it landed where the row already
// was, and the caller, which announces source rows itself, then says nothing.
func (n *FileRows) rowWrite(verb, table string, id int64, row string, fn func(ctx context.Context, tx *sql.Tx) error) (changed bool, err error) {
	ctx := context.Background()
	return n.s.withRowWrite(ctx, verb, table, id, func(tx *sql.Tx, _ *[]*gridwellv1.Event) error {
		return fn(ctx, tx)
	}, "~:"+row)
}

// exec runs a single-row UPDATE on a live row of this namespace, mapping zero
// rows to ErrNotFound: a retired row refuses mutation.
func (n *FileRows) exec(verb, set string, tileID int64, args ...any) (changed bool, err error) {
	args = append(args, n.s.now().Unix(), tileID)
	return n.rowWrite(verb, "tiles", tileID, tileRow(tileID), func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE tiles SET `+set+`, updated_at = ? WHERE id = ? AND key != '' AND tombstoned = 0`, args...)
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
	})
}

// Move is the placement writeback, claiming the footprint the writer saw.
// gridID 0 keeps the row's grid and key; another is a move by the source
// and the row leaves the old selection.
func (n *FileRows) Move(tileID, gridID int64, key string, x, y, w, h int64, claim *gridwellv1.Placement) (changed bool, err error) {
	return n.rowWrite("MoveTile", "tiles", tileID, tileRow(tileID), func(ctx context.Context, tx *sql.Tx) error {
		cur := &gridwellv1.Tile{Id: strconv.FormatInt(tileID, 10)}
		from, was := int64(0), ""
		if err := liveRow(ctx, tx, files, tileID, "x, y, w, h, grid_id, key", &cur.X, &cur.Y, &cur.W, &cur.H, &from, &was); err != nil {
			return err
		}
		if gridID == 0 {
			gridID, key = from, was
		}
		if gridID == from && key == was && cur.X == x && cur.Y == y && cur.W == w && cur.H == h {
			return nil
		}
		if err := ClaimPlacement(cur, claim, false); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tiles SET grid_id = ?, key = ?, `+placementSet+`, updated_at = ? WHERE id = ?`,
			gridID, key, x, y, w, h, n.s.now().Unix(), tileID); err != nil {
			return err
		}
		if gridID != from {
			_, _, err = leaveSelection(ctx, tx, from, tileID)
		}
		return err
	})
}

// Rename is the user's name for an entry, kept on its row and never told to
// the source, as home's RenameTile keeps one; a text tile's name is its
// source's and is refused, since its presentation reads it.
func (n *FileRows) Rename(tileID, version int64, name string) (changed bool, err error) {
	return n.userContent("RenameTile", tileID, version, func(t *gridwellv1.Tile) (string, any, error) {
		if !rpc.KindOf(t.Kind).TypedName {
			return "", nil, fmt.Errorf("%w: a text tile's name is its source's; its presentation reads it", ErrInvalidArgument)
		}
		if t.Named && t.AltText == name {
			return "", nil, nil
		}
		return `alt_text = ?, alt_user = 1`, name, nil
	})
}

// userContent is the one write of a fact the user owns on an entry's row: it
// claims the version, and decide answers the latching SET and its value from
// the row as claimed, "" for a write that lands where the row already is.
// A write bumps the version.
func (n *FileRows) userContent(verb string, tileID, version int64, decide func(*gridwellv1.Tile) (string, any, error)) (changed bool, err error) {
	err = n.write(verb, tileRow(tileID), func(ctx context.Context, tx *sql.Tx) error {
		var t gridwellv1.Tile
		if err := liveRow(ctx, tx, files, tileID, tileColumns, scanDests(tilesColumns, &t)...); err != nil {
			return err
		}
		if t.Version != version {
			return fmt.Errorf("%w: tile %d at version %d, claimed %d", ErrVersionConflict, tileID, t.Version, version)
		}
		set, v, err := decide(&t)
		if err != nil || set == "" {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tiles SET `+set+` WHERE id = ?`, v, tileID); err != nil {
			return err
		}
		changed = true
		return n.s.bumpTileVersion(ctx, tx, tileID)
	})
	return changed && err == nil, err
}

// SetFraming persists framing into this namespace's rows, the one writer of
// the one shape (framing.go). Exactly one of tileID and rootGridID is set.
func (n *FileRows) SetFraming(tileID, rootGridID int64, f rpc.Framing) (changed bool, err error) {
	table, id, row := "tiles", tileID, tileRow(tileID)
	if tileID == 0 {
		table, id, row = "grids", rootGridID, "f/"+strconv.FormatInt(rootGridID, 10)
	}
	return n.rowWrite("SetFraming", table, id, row, func(ctx context.Context, tx *sql.Tx) error {
		k, err := updateFraming(ctx, tx, files, tileID, rootGridID, f, n.s.now().Unix())
		if err != nil {
			return err
		}
		if k == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// SetTextScroll persists a text tile's scroll.
func (n *FileRows) SetTextScroll(tileID, tx, ty, tw, th int64, mode string) (changed bool, err error) {
	var m any
	if mode != "" {
		m = mode
	}
	return n.exec("SetTextScroll", textScrollSet+textModeSet, tileID, tx, ty, tw, th, m)
}

// SetContentZoom persists the per-tile content scale.
func (n *FileRows) SetContentZoom(tileID int64, zoom rpc.ContentZoom) (changed bool, err error) {
	return n.exec("SetContentZoom", contentZoomSet, tileID, zoom.Float())
}

// Retire tombstones one tile row: the delete-gesture path. The row stays so a
// stale link stays interpretable, its screenshot is released, and it
// leaves its grid's selection.
func (n *FileRows) Retire(tileID int64) error {
	return n.write("Retire", tileRow(tileID), func(ctx context.Context, tx *sql.Tx) error {
		var preview sql.NullInt64
		var gridID int64
		if err := liveRow(ctx, tx, files, tileID, "preview_blob_id, grid_id", &preview, &gridID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tiles SET tombstoned = 1, preview_blob_id = NULL WHERE id = ?`, tileID); err != nil {
			return err
		}
		if _, _, err := leaveSelection(ctx, tx, gridID, tileID); err != nil {
			return err
		}
		if preview.Valid {
			return n.s.decBlobRefcount(ctx, tx, preview.Int64)
		}
		return nil
	})
}

// RootFraming reads a root grid's framing, the row that owns it when there is
// no door tile.
func (n *FileRows) RootFraming(gridID int64) (rpc.View, error) { return n.s.rootFraming(gridID) }

// rootFraming is the framing a grid row keeps for itself, home's root or a
// directory's, NotFound when never framed.
func (s *Store) rootFraming(gridID int64) (rpc.View, error) {
	var ncx, ncy, nzoom sql.NullFloat64
	err := s.db.QueryRow(`SELECT root_cx, root_cy, root_zoom FROM grids WHERE id = ?`, gridID).
		Scan(&ncx, &ncy, &nzoom)
	if err != nil || !ncx.Valid || !ncy.Valid || !nzoom.Valid {
		return rpc.View{}, notFound(err)
	}
	return rpc.ViewOf(ncx.Float64, ncy.Float64, nzoom.Float64), nil
}
