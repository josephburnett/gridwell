package store

// The node's rows for a plugin's entries: rows in the same grids and tiles
// tables as home, under ns = the plugin id (home is ns = ''). The plugin
// answers in stable string keys; the node mints the ids, keeps arrangement and
// framing, and retires keys as tombstones. Ids are AUTOINCREMENT and never
// reused, and a recreated key mints a fresh id (a partial unique index over
// live rows). Plugin rows emit no store events, and a row's version moves
// only on a typed name.
//
// A row exists only once the user has made a durable fact about an entry.
// Listing mints nothing: Overlay is a read-only join that derives an
// untouched entry's placement by the algorithm Mint stores, and Refresh and
// Sweep write only to rows that exist. An entry with a link_target is a link
// row, its link_target_id the target's namespace-relative address
// (rpc.EntryTileID), which the router qualifies on the way out. A row's
// typed name is the user's, as on home's rows: alt_text with alt_user
// latched, which no listing overwrites; so is a url's landed address, with
// url_user latched.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Namespace is one plugin's view of the store.
type Namespace struct {
	s  *Store
	ns string
}

// Namespace returns the rows under ns (a plugin id).
func (s *Store) Namespace(ns string) *Namespace {
	return &Namespace{s: s, ns: ns}
}

// ID is the plugin id the rows are kept under.
func (n *Namespace) ID() string { return n.ns }

// ExtTile is one joined entry: the node's row, or a derived placement when
// the entry has none. ID is the minted row id, 0 when derived; ChildGridID is
// a door's minted child grid, 0 for a leaf. The caller names every such tile
// by its key either way; see rpc.EntryTileID. URLUser is the row's url_user.
type ExtTile struct {
	ID          int64
	Key         string
	ChildGridID int64
	URLUser     bool
	*gridwellv1.Tile
}

// GridOf resolves a grid key to its grid id, minting on first sight.
func (n *Namespace) GridOf(key string) (int64, error) {
	id, ok, err := n.LookupGrid(key)
	if err != nil || ok {
		return id, err
	}
	now := n.s.now().Unix()
	err = n.write("GridOf", "c/"+key, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO grids (version, created_at, updated_at, ns, context_key)
			VALUES (0, ?, ?, ?, ?)`, now, now, n.ns, key)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// GridKey resolves a minted grid id back to the plugin's grid key.
func (n *Namespace) GridKey(gridID int64) (string, error) {
	var key string
	err := n.s.db.QueryRow(`SELECT context_key FROM grids WHERE id = ? AND ns = ?`, gridID, n.ns).Scan(&key)
	return key, notFound(err)
}

// TileKey resolves a minted tile id to its (grid id, plugin key). A retired
// row still resolves, so a dangling link stays interpretable; the caller
// decides what retirement means.
func (n *Namespace) TileKey(tileID int64) (gridID int64, key string, tombstoned bool, err error) {
	var tomb int64
	err = n.s.db.QueryRow(`SELECT grid_id, key, tombstoned FROM tiles WHERE id = ? AND ns = ?`, tileID, n.ns).
		Scan(&gridID, &key, &tomb)
	return gridID, key, tomb != 0, notFound(err)
}

// Overlay joins one plugin listing with the stored arrangement and writes
// nothing. Rows contribute id, placement, framing, a typed name and a landed
// address; the listing contributes every other content fact. Rows the listing does not mention follow at the end from
// their stored snapshot, which is what makes a touched tile survive an outage.
// gridID 0 means an untouched grid: every entry is derived.
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
			r.Kind, r.LinkTargetId = entryKind(e), linkTargetOf(e)
			if !r.Named {
				r.AltText = e.Label
			}
			if !r.URLUser {
				r.UrlString = snapshotOf(e).url.String
			}
			if r.LinkTargetId != "" {
				r.UrlString, r.ServesPage, r.PreviewBlobId = "", false, 0
			}
			out = append(out, r)
			continue
		}
		out = append(out, ExtTile{Key: e.Key, Tile: &gridwellv1.Tile{Kind: entryKind(e), AltText: e.Label,
			UrlString: snapshotOf(e).url.String, LinkTargetId: linkTargetOf(e), X: x, Y: y, W: w, H: h}})
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
		return rpc.EntryTileID(lt.Grid, lt.Key)
	}
	return ""
}

// entrySnapshot is what a row keeps of its entry's content facts, presented
// when its source does not list it. Mint writes it and Refresh keeps it. A
// link holds only its target: the rest are the target's.
type entrySnapshot struct {
	kind string
	url  sql.NullString // a url row's address, NULL on every other kind and on a link
	user bool           // url is the user's landed address, url_user
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
// size at or below the hint (belowHint), so a calendar's column stays its
// column. Any other entry takes the next free cell from the
// listing's cursor, the first free cell (freeslot.go). Overlay derives
// with it and Mint stores what Overlay derived, so touching a tile never
// moves it.
func derivePlacement(occupied map[[2]int64]bool, cur *cursor, hint *pluginv1.PlacementHint) (x, y, w, h int64) {
	if hint != nil {
		x, w, h = hint.X, max(hint.W, 1), max(hint.H, 1)
		y = belowHint(occupied, x, hint.Y, w, h)
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
	now := n.s.now().Unix()
	var id int64
	err := n.write("Mint", "g/"+strconv.FormatInt(gridID, 10)+"/"+e.Key, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO tiles (version, grid_id, kind, x, y, w, h,
			child_grid_id, url_string, alt_text, serves_page, link_target_id, created_at, updated_at, ns, key)
			VALUES (0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			gridID, s.kind, x, y, w, h, child, s.url, e.Label, s.page, s.link, now, now, n.ns, e.Key)
		if err != nil {
			return fmt.Errorf("store: mint %q: %w", e.Key, err)
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// Refresh updates a row's content snapshot to what the listing just said,
// writing only where a value differs. child_grid_id is deliberately not
// refreshed: re-pointing a stored link because a listing changed is how
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
		if s.user = r.URLUser && s.url.Valid; s.user {
			s.url.String = r.UrlString
		}
		if r.Kind == s.kind && (r.Named || r.AltText == e.Label) && r.UrlString == s.url.String && r.URLUser == s.user &&
			r.ServesPage == s.page && r.LinkTargetId == s.link.String {
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
// mode, since a link's preview is its target's. A typed name stays, and so
// does a landed address while the row is still a url.
func (n *Namespace) convert(r ExtTile, label string, s entrySnapshot) error {
	return n.write("Refresh", tileRow(r.ID), func(ctx context.Context, tx *sql.Tx) error {
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
		_, err := tx.ExecContext(ctx, `UPDATE tiles SET kind = ?,
			alt_text = CASE WHEN alt_user = 1 THEN alt_text ELSE ? END, url_string = ?, url_user = ?, serves_page = ?,
			link_target_id = ?, updated_at = ? WHERE id = ? AND ns = ? AND tombstoned = 0`,
			s.kind, label, s.url, s.user, s.page, s.link, n.s.now().Unix(), r.ID, n.ns)
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

// LookupGrid is GridOf without the mint, for the join, which must not
// write.
func (n *Namespace) LookupGrid(key string) (int64, bool, error) {
	return n.lookupID(`SELECT id FROM grids WHERE ns = ? AND context_key = ?`, n.ns, key)
}

// LiveTileID is TileKey's inverse: an entry with a row is named by that row's
// id, never by its key.
func (n *Namespace) LiveTileID(gridID int64, key string) (int64, bool, error) {
	if gridID == 0 {
		return 0, false, nil
	}
	return n.lookupID(`SELECT id FROM tiles WHERE ns = ? AND grid_id = ? AND key = ? AND tombstoned = 0`, n.ns, gridID, key)
}

// lookupID answers the id query finds, ok false when it finds none.
func (n *Namespace) lookupID(query string, args ...any) (id int64, ok bool, err error) {
	err = n.s.db.QueryRow(query, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// tiles lists the live rows of a grid, by id, the stored columns through the
// one descriptor and the key beside them.
func (n *Namespace) tiles(gridID int64) ([]ExtTile, error) {
	rows, err := n.s.db.Query(`SELECT `+tileColumns+`, key, url_user FROM tiles
		WHERE ns = ? AND grid_id = ? AND tombstoned = 0 ORDER BY id`, n.ns, gridID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(rows *sql.Rows) (ExtTile, error) {
		var key string
		var user bool
		t, err := scanTile(rows, &key, &user)
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
		return ExtTile{ID: id, Key: key, ChildGridID: child, URLUser: user, Tile: t}, nil
	})
}

// write runs one write to this namespace's rows through the store's funnel
// (withMutation). A plugin row publishes no store event, so row names what it
// touched for the record: an id as rpc.EventKey spells it, or, before the
// mint, its grid and key.
func (n *Namespace) write(verb, row string, fn func(ctx context.Context, tx *sql.Tx) error) error {
	ctx := context.Background()
	return n.s.withMutation(ctx, verb, func(tx *sql.Tx, _ *[]*gridwellv1.Event) error {
		return fn(ctx, tx)
	}, n.ns+":"+row)
}

func tileRow(id int64) string { return "t/" + strconv.FormatInt(id, 10) }

// rowWrite is write for one existing row of table that carries no claim
// (store.withRowWrite): changed is false when it landed where the row already
// was, and the caller, which announces plugin rows itself, then says nothing.
func (n *Namespace) rowWrite(verb, table string, id int64, row string, fn func(ctx context.Context, tx *sql.Tx) error) (changed bool, err error) {
	ctx := context.Background()
	return n.s.withRowWrite(ctx, verb, table, id, func(tx *sql.Tx, _ *[]*gridwellv1.Event) error {
		return fn(ctx, tx)
	}, n.ns+":"+row)
}

// exec runs a single-row UPDATE on a live row of this namespace, mapping zero
// rows to ErrNotFound: a retired row refuses mutation.
func (n *Namespace) exec(verb, set string, tileID int64, args ...any) (changed bool, err error) {
	args = append(args, n.s.now().Unix(), tileID, n.ns)
	return n.rowWrite(verb, "tiles", tileID, tileRow(tileID), func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE tiles SET `+set+`, updated_at = ? WHERE id = ? AND ns = ? AND tombstoned = 0`, args...)
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
// gridID 0 keeps the row's grid and key; another is the plugin's move
// (pluginhost Adapter.moveAcross), and the row leaves the old selection.
func (n *Namespace) Move(tileID, gridID int64, key string, x, y, w, h int64, claim *gridwellv1.Placement) (changed bool, err error) {
	return n.rowWrite("MoveTile", "tiles", tileID, tileRow(tileID), func(ctx context.Context, tx *sql.Tx) error {
		cur := &gridwellv1.Tile{Id: strconv.FormatInt(tileID, 10)}
		from, was := int64(0), ""
		if err := liveRow(ctx, tx, n.ns, tileID, "x, y, w, h, grid_id, key", &cur.X, &cur.Y, &cur.W, &cur.H, &from, &was); err != nil {
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
// the plugin, as home's RenameTile keeps one; a text tile's name is its
// source's and is refused, since its presentation reads it.
func (n *Namespace) Rename(tileID, version int64, name string) (changed bool, err error) {
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

// SetAddress is where a navigation in an entry's url landed, kept on its row
// and never told to the plugin, as home's writeURLContent keeps one: http or
// https only. A served page has no address of its own and is refused.
func (n *Namespace) SetAddress(tileID, version int64, address string) (changed bool, err error) {
	address = strings.TrimSpace(address)
	if !rpc.HTTPAddress(address) {
		return false, fmt.Errorf("%w: only http/https URLs allowed", ErrInvalidArgument)
	}
	return n.userContent("WriteContent/url", tileID, version, func(t *gridwellv1.Tile) (string, any, error) {
		if !rpc.WebContent(t) || t.ServesPage || t.LinkTargetId != "" {
			return "", nil, fmt.Errorf("%w: a %s tile here has no address of its own", ErrInvalidArgument, t.Kind)
		}
		if t.UrlString == address {
			return "", nil, nil
		}
		return `url_string = ?, url_user = 1`, address, nil
	})
}

// userContent is the one write of a fact the user owns on an entry's row: it
// claims the version, and decide answers the latching SET and its value from
// the row as claimed, "" for a write that lands where the row already is.
// A write bumps the version.
func (n *Namespace) userContent(verb string, tileID, version int64, decide func(*gridwellv1.Tile) (string, any, error)) (changed bool, err error) {
	err = n.write(verb, tileRow(tileID), func(ctx context.Context, tx *sql.Tx) error {
		var t gridwellv1.Tile
		if err := liveRow(ctx, tx, n.ns, tileID, tileColumns, scanDests(tilesColumns, &t)...); err != nil {
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
func (n *Namespace) SetFraming(tileID, rootGridID int64, f rpc.Framing) (changed bool, err error) {
	table, id, row := "tiles", tileID, tileRow(tileID)
	if tileID == 0 {
		table, id, row = "grids", rootGridID, "f/"+strconv.FormatInt(rootGridID, 10)
	}
	return n.rowWrite("SetFraming", table, id, row, func(ctx context.Context, tx *sql.Tx) error {
		k, err := updateFraming(ctx, tx, n.ns, tileID, rootGridID, f, n.s.now().Unix())
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
func (n *Namespace) SetTextScroll(tileID, tx, ty, tw, th int64, mode string) (changed bool, err error) {
	var m any
	if mode != "" {
		m = mode
	}
	return n.exec("SetTextScroll", textScrollSet+textModeSet, tileID, tx, ty, tw, th, m)
}

// SetContentZoom persists the per-tile content scale.
func (n *Namespace) SetContentZoom(tileID int64, zoom rpc.ContentZoom) (changed bool, err error) {
	return n.exec("SetContentZoom", contentZoomSet, tileID, zoom.Float())
}

// Retire tombstones one tile row: the delete-gesture path. The row stays so a
// stale link stays interpretable, its screenshot is released, and it
// leaves its grid's selection.
func (n *Namespace) Retire(tileID int64) error {
	return n.write("Retire", tileRow(tileID), func(ctx context.Context, tx *sql.Tx) error {
		var preview sql.NullInt64
		var gridID int64
		if err := liveRow(ctx, tx, n.ns, tileID, "preview_blob_id, grid_id", &preview, &gridID); err != nil {
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
func (n *Namespace) RootFraming(gridID int64) (rpc.View, error) {
	var ncx, ncy, nzoom sql.NullFloat64
	err := n.s.db.QueryRow(`SELECT root_cx, root_cy, root_zoom FROM grids WHERE id = ? AND ns = ?`, gridID, n.ns).
		Scan(&ncx, &ncy, &nzoom)
	if err != nil || !ncx.Valid || !ncy.Valid || !nzoom.Valid {
		return rpc.View{}, notFound(err)
	}
	return rpc.ViewOf(ncx.Float64, ncy.Float64, nzoom.Float64), nil
}
