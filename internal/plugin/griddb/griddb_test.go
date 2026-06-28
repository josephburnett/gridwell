package griddb

import (
	"database/sql"
	"strconv"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	_ "modernc.org/sqlite"
)

// newTestDB builds an in-memory DB with the same tiles table the fs/proc
// plugins use (labelCol picks the "name" vs "key" column). Only the columns
// griddb reads/writes are present; that is the contract griddb depends on.
func newTestDB(t *testing.T, labelCol string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE tiles (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		grid_id       INTEGER NOT NULL,
		` + labelCol + `  TEXT NOT NULL,
		kind          TEXT NOT NULL,
		x INTEGER NOT NULL DEFAULT 0, y INTEGER NOT NULL DEFAULT 0,
		w INTEGER NOT NULL DEFAULT 1, h INTEGER NOT NULL DEFAULT 1,
		child_grid_id INTEGER,
		view_x INTEGER NOT NULL DEFAULT 0, view_y INTEGER NOT NULL DEFAULT 0,
		view_zoom REAL NOT NULL DEFAULT 1.0)`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// insertTile adds a row and returns its id as a string (the form griddb's
// request types carry).
func insertTile(t *testing.T, db *sql.DB, labelCol string, gridID int64, label, kind string, x, y int64) string {
	t.Helper()
	res, err := db.Exec(`INSERT INTO tiles (grid_id, `+labelCol+`, kind, x, y) VALUES (?, ?, ?, ?, ?)`,
		gridID, label, kind, x, y)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return strconv.FormatInt(id, 10)
}

func TestApplyMoveRepositionsInGrid(t *testing.T) {
	db := newTestDB(t, "name")
	id := insertTile(t, db, "name", 1, "a.txt", "text", 0, 0)

	resp, err := ApplyMove(db, "name", &gridwellv1.MoveTileRequest{TileId: id, X: 4, Y: 5})
	if err != nil {
		t.Fatalf("ApplyMove: %v", err)
	}
	if resp.Tile.X != 4 || resp.Tile.Y != 5 {
		t.Errorf("moved tile = (%d,%d), want (4,5)", resp.Tile.X, resp.Tile.Y)
	}
	// The label round-trips into AltText via the configured column.
	if resp.Tile.AltText != "a.txt" {
		t.Errorf("AltText = %q, want a.txt", resp.Tile.AltText)
	}
}

// TestApplyMoveRejectsCrossGrid: a move whose dest grid differs from the tile's
// grid is refused (it would need an on-disk relocation). Same-grid passes.
func TestApplyMoveRejectsCrossGrid(t *testing.T) {
	db := newTestDB(t, "name")
	id := insertTile(t, db, "name", 1, "a.txt", "text", 0, 0)

	if _, err := ApplyMove(db, "name", &gridwellv1.MoveTileRequest{TileId: id, DestGridId: "2", X: 1, Y: 1}); err == nil {
		t.Error("cross-grid move should be rejected")
	}
	// Same-grid (matching dest) is allowed.
	if _, err := ApplyMove(db, "name", &gridwellv1.MoveTileRequest{TileId: id, DestGridId: "1", X: 1, Y: 1}); err != nil {
		t.Errorf("same-grid move should pass, got %v", err)
	}
}

func TestApplyMoveNotFound(t *testing.T) {
	db := newTestDB(t, "name")
	if _, err := ApplyMove(db, "name", &gridwellv1.MoveTileRequest{TileId: "999", X: 1, Y: 1}); err != ErrNotFound {
		t.Errorf("move of missing tile = %v, want ErrNotFound", err)
	}
}

func TestApplyResizeSetsFootprint(t *testing.T) {
	db := newTestDB(t, "key")
	id := insertTile(t, db, "key", 1, "1234", "well", 0, 0)

	resp, err := ApplyResize(db, "key", &gridwellv1.ResizeTileRequest{TileId: id, X: 2, Y: 3, W: 5, H: 6})
	if err != nil {
		t.Fatalf("ApplyResize: %v", err)
	}
	if resp.Tile.X != 2 || resp.Tile.Y != 3 || resp.Tile.W != 5 || resp.Tile.H != 6 {
		t.Errorf("resized = %+v, want x2 y3 w5 h6", resp.Tile)
	}
}

// TestApplySetWellViewPersistsFraming: the framing (view_x/y/zoom) is written
// and read back — this is what makes descent/ascent idempotent for fs/proc
// directory and process wells.
func TestApplySetWellViewPersistsFraming(t *testing.T) {
	db := newTestDB(t, "name")
	id := insertTile(t, db, "name", 1, "dir", "well", 0, 0)

	resp, err := ApplySetWellView(db, "name", &gridwellv1.SetTileRequest{
		TileId: id,
		Tile:   &gridwellv1.Tile{ViewX: 7, ViewY: 8, ViewZoom: 2.5},
	})
	if err != nil {
		t.Fatalf("ApplySetWellView: %v", err)
	}
	if resp.Tile.ViewX != 7 || resp.Tile.ViewY != 8 || resp.Tile.ViewZoom != 2.5 {
		t.Errorf("framing = (%d,%d,%v), want (7,8,2.5)", resp.Tile.ViewX, resp.Tile.ViewY, resp.Tile.ViewZoom)
	}
}

// TestLoadTilesOrdersByID and maps the label column into AltText for every row.
func TestLoadTilesOrdersByID(t *testing.T) {
	db := newTestDB(t, "name")
	insertTile(t, db, "name", 1, "b.txt", "text", 1, 0)
	insertTile(t, db, "name", 1, "a-dir", "well", 0, 0)
	insertTile(t, db, "name", 2, "other", "text", 0, 0) // different grid; excluded

	tiles, err := LoadTiles(db, "name", 1)
	if err != nil {
		t.Fatalf("LoadTiles: %v", err)
	}
	if len(tiles) != 2 {
		t.Fatalf("got %d tiles for grid 1, want 2", len(tiles))
	}
	if tiles[0].AltText != "b.txt" || tiles[1].AltText != "a-dir" {
		t.Errorf("labels/order wrong: %q, %q", tiles[0].AltText, tiles[1].AltText)
	}
}

// TestInvalidLabelColumnRejected: the column whitelist refuses anything outside
// {name,key} so the column interpolation can never be an injection vector.
func TestInvalidLabelColumnRejected(t *testing.T) {
	db := newTestDB(t, "name")
	if _, err := LoadTile(db, "name; DROP TABLE tiles;--", 1); err == nil {
		t.Error("a non-whitelisted label column must be rejected")
	}
	if _, err := LoadTiles(db, "evil", 1); err == nil {
		t.Error("LoadTiles must reject a non-whitelisted label column")
	}
}

// TestParseTileIDInvalid: a non-numeric tile id is a clear error, not a panic.
func TestApplyMoveInvalidTileID(t *testing.T) {
	db := newTestDB(t, "name")
	if _, err := ApplyMove(db, "name", &gridwellv1.MoveTileRequest{TileId: "not-a-number"}); err == nil {
		t.Error("invalid tile id should error")
	}
}
