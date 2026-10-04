package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// A genuine v15 file has view_cx/cy/zoom NOT NULL DEFAULT 0, which a
// chain-built one never has (every rebuild materializes the current shape).
// v16 must drop the constraint and turn its zero-zoom rows into NULLs, so a
// well minted afterwards is never visited with nothing written.
func TestMigrateV16OverAGenuineV15File(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "genuinev15.db")
	db, root := buildDBAtV1(t, path)
	applyMigrationsUpTo(t, db, 15)
	for _, col := range []string{"view_cx", "view_cy", "view_zoom"} {
		for _, ddl := range []string{
			`ALTER TABLE tiles DROP COLUMN ` + col,
			`ALTER TABLE tiles ADD COLUMN ` + col + ` REAL NOT NULL DEFAULT 0`,
		} {
			if _, err := db.ExecContext(ctx, ddl); err != nil {
				t.Fatalf("restore the v15 shape (%s): %v", ddl, err)
			}
		}
	}
	for _, w := range []struct {
		alt  string
		zoom float64
	}{{"v15 never visited", 0}, {"v15 framed", 0.375}} {
		res, err := db.ExecContext(ctx, `INSERT INTO grids (created_at, updated_at) VALUES (100, 100)`)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO tiles
			(grid_id, kind, x, y, w, h, view_cx, view_cy, view_zoom, child_grid_id, alt_text, created_at, updated_at)
			VALUES (`+root+`, 'well', 0, 0, 2, 2, 4.5, 3.5, ?, ?, ?, 100, 100)`,
			w.zoom, mustID(t, res), w.alt); err != nil {
			t.Fatalf("plant %s: %v", w.alt, err)
		}
	}

	if err := storeOver(db).migrateUp(ctx, migrations, 16); err != nil {
		t.Fatalf("migrate a genuine v15 file to v16: %v", err)
	}

	for _, col := range []string{"view_cx", "view_cy", "view_zoom"} {
		if c := tableColumnsFP(t, db, "tiles")[col]; c.notNull {
			t.Errorf("tiles.%s is still NOT NULL after v16", col)
		}
	}
	for alt, want := range map[string]bool{"v15 never visited": false, "v15 framed": true} {
		var zoom sql.NullFloat64
		if err := db.QueryRowContext(ctx, `SELECT view_zoom FROM tiles WHERE alt_text = ?`, alt).Scan(&zoom); err != nil {
			t.Fatalf("%s did not survive v16: %v", alt, err)
		}
		if zoom.Valid != want {
			t.Errorf("%s: view_zoom = %+v, want framed=%v", alt, zoom, want)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("a genuine v15 file must open after v16: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w, err := s.CreateWell(ctx, root, 9, 9, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	var zoom sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `SELECT view_zoom FROM tiles WHERE id = ?`, w.Id).Scan(&zoom); err != nil || zoom.Valid {
		t.Errorf("a well minted after v16 stores view_zoom %+v (err %v), want NULL", zoom, err)
	}
}
