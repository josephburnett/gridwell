// Package griddb holds the SQLite tile operations shared by the fs and proc
// plugins. Both plugins store tiles in an identical `tiles` table — (id,
// grid_id, kind, x, y, w, h, child_grid_id, view_x, view_y, view_zoom) — and
// differ only in the column that holds the tile's display label (fs: "name",
// proc: "key") and in how grids reconcile against their backing source.
//
// The placement and framing mutations below are byte-identical across both
// plugins, so they live here once. They are the write side of the primary
// rule: a tile a user drags stays where it was dropped, and a well's preview
// framing is restored on descent — both persisted in the plugin's own DB so
// they survive a restart.
package griddb

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// ErrNotFound is returned when a mutation or load targets a tile id that has
// no row.
var ErrNotFound = errors.New("griddb: tile not found")

// labelColumns whitelists the per-plugin label column names. labelCol is an
// internal constant (never user input), but the whitelist keeps the column
// interpolation below provably safe.
var labelColumns = map[string]bool{"name": true, "key": true}

func checkLabelCol(labelCol string) error {
	if !labelColumns[labelCol] {
		return fmt.Errorf("griddb: invalid label column %q", labelCol)
	}
	return nil
}

// ApplyMove repositions a tile to (x, y) and returns the updated tile.
// Cross-grid moves (e.g. dragging a file into a different directory) would
// require an on-disk side effect and are rejected here; only in-grid
// repositioning is supported.
func ApplyMove(db *sql.DB, labelCol string, req *gridwellv1.MoveTileRequest) (*gridwellv1.TileResponse, error) {
	tileID, err := parseTileID(req.TileId)
	if err != nil {
		return nil, err
	}
	if err := guardSameGrid(db, tileID, req.DestGridId); err != nil {
		return nil, err
	}
	if err := exec(db, `UPDATE tiles SET x = ?, y = ? WHERE id = ?`, req.X, req.Y, tileID); err != nil {
		return nil, err
	}
	return tileResp(db, labelCol, tileID)
}

// ApplyResize sets a tile's footprint (x, y, w, h) and returns the updated tile.
func ApplyResize(db *sql.DB, labelCol string, req *gridwellv1.ResizeTileRequest) (*gridwellv1.TileResponse, error) {
	tileID, err := parseTileID(req.TileId)
	if err != nil {
		return nil, err
	}
	if err := exec(db, `UPDATE tiles SET x = ?, y = ?, w = ?, h = ? WHERE id = ?`, req.X, req.Y, req.W, req.H, tileID); err != nil {
		return nil, err
	}
	return tileResp(db, labelCol, tileID)
}

// ApplySetWellView persists a well tile's preview framing (view_x, view_y,
// view_zoom). The framing *is* the well's preview and its descent target, so
// storing it here is what makes descent/ascent idempotent for source-backed
// wells.
// ApplySetWellView writes a well tile's framing from a SetTile request (the
// single writeback). Only the view_* fields of req.Tile are read — fs/proc
// support framing on their directory/process wells, nothing else.
func ApplySetWellView(db *sql.DB, labelCol string, req *gridwellv1.SetTileRequest) (*gridwellv1.TileResponse, error) {
	tileID, err := parseTileID(req.TileId)
	if err != nil {
		return nil, err
	}
	t := req.GetTile()
	if err := exec(db, `UPDATE tiles SET view_x = ?, view_y = ?, view_zoom = ? WHERE id = ?`, t.GetViewX(), t.GetViewY(), t.GetViewZoom(), tileID); err != nil {
		return nil, err
	}
	return tileResp(db, labelCol, tileID)
}

func parseTileID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("griddb: invalid tile_id %q", s)
	}
	return id, nil
}

// guardSameGrid rejects a move whose destination grid differs from the tile's
// current grid. An empty destGridID (in-grid reposition) always passes.
func guardSameGrid(db *sql.DB, tileID int64, destGridID string) error {
	if destGridID == "" {
		return nil
	}
	dest, err := strconv.ParseInt(destGridID, 10, 64)
	if err != nil {
		return fmt.Errorf("griddb: invalid dest_grid_id %q", destGridID)
	}
	var cur int64
	if err := db.QueryRow(`SELECT grid_id FROM tiles WHERE id = ?`, tileID).Scan(&cur); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if dest != cur {
		return fmt.Errorf("griddb: cross-grid move not supported (tile %d grid %d → %d)", tileID, cur, dest)
	}
	return nil
}

// tileResp loads a tile and wraps it in a TileResponse.
func tileResp(db *sql.DB, labelCol string, tileID int64) (*gridwellv1.TileResponse, error) {
	t, err := LoadTile(db, labelCol, tileID)
	if err != nil {
		return nil, err
	}
	return &gridwellv1.TileResponse{Tile: t}, nil
}

// exec runs a single-row UPDATE and maps a zero-rows result to ErrNotFound so
// callers can surface a not-found rather than silently succeeding.
func exec(db *sql.DB, q string, args ...any) error {
	res, err := db.Exec(q, args...)
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

// LoadTile reads one tile row and converts it to a proto Tile. labelCol is the
// plugin's label column ("name" or "key"); its value becomes AltText.
func LoadTile(db *sql.DB, labelCol string, tileID int64) (*gridwellv1.Tile, error) {
	if err := checkLabelCol(labelCol); err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT id, grid_id, `+labelCol+`, kind, x, y, w, h,
		COALESCE(child_grid_id,0), view_x, view_y, view_zoom
		FROM tiles WHERE id = ?`, tileID)
	t, err := scanTile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// LoadTiles reads every tile row for a grid (ordered by id for stable output)
// and converts them to proto Tiles.
func LoadTiles(db *sql.DB, labelCol string, gridID int64) ([]*gridwellv1.Tile, error) {
	if err := checkLabelCol(labelCol); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, grid_id, `+labelCol+`, kind, x, y, w, h,
		COALESCE(child_grid_id,0), view_x, view_y, view_zoom
		FROM tiles WHERE grid_id = ? ORDER BY id`, gridID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*gridwellv1.Tile
	for rows.Next() {
		t, err := scanTile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// scanRow is the subset of *sql.Row / *sql.Rows that scanTile needs.
type scanRow interface {
	Scan(dest ...any) error
}

func scanTile(row scanRow) (*gridwellv1.Tile, error) {
	var id, gridID, x, y, w, h, childGrid, vx, vy int64
	var vz float64
	var label, kind string
	if err := row.Scan(&id, &gridID, &label, &kind, &x, &y, &w, &h, &childGrid, &vx, &vy, &vz); err != nil {
		return nil, err
	}
	return &gridwellv1.Tile{
		Id:          strconv.FormatInt(id, 10),
		GridId:      strconv.FormatInt(gridID, 10),
		Kind:        kind,
		X:           x,
		Y:           y,
		W:           w,
		H:           h,
		AltText:     label,
		ChildGridId: strconv.FormatInt(childGrid, 10),
		ViewX:       vx,
		ViewY:       vy,
		ViewZoom:    vz,
	}, nil
}
