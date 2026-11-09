package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// sizeFixture is a node holding live rows of every place, the trash, the
// templates, a tombstoned file row, and planted garbage of each class. It
// answers the ids of the garbage rows, so a dump can leave them out.
func sizeFixture(t *testing.T) (s *Store, path string, garbage map[string]bool) {
	t.Helper()
	ctx := context.Background()
	s, path = newTestStoreFile(t)
	root := rootID(t, s)
	text, err := s.CreateText(ctx, root, 0, 0, 1, 1, []byte("kept body"))
	if err != nil {
		t.Fatal(err)
	}
	door, err := s.CreateDoor(ctx, root, 1, 0, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateText(ctx, door.ChildGridId, 0, 0, 1, 1, []byte("under the door")); err != nil {
		t.Fatal(err)
	}
	url, err := s.CreateURL(ctx, root, 2, 0, 1, 1, "https://example.com", "", false)
	if err != nil {
		t.Fatal(err)
	}
	trashed, err := s.CreateText(ctx, root, 3, 0, 1, 1, []byte("in the trash"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateScratchURL(ctx, "https://example.org"); err != nil {
		t.Fatal(err)
	}
	// A template, and one that is a door with a text under it: kept.
	templates, err := s.TemplatesGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateText(ctx, templates, 0, 0, 1, 1, []byte("a template")); err != nil {
		t.Fatal(err)
	}
	tdoor, err := s.CreateDoor(ctx, templates, 1, 0, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateText(ctx, tdoor.ChildGridId, 0, 0, 1, 1, []byte("under a template")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: trashed.Id}); err != nil {
		t.Fatal(err)
	}

	garbage = map[string]bool{}
	exec := func(q string, args ...any) int64 {
		t.Helper()
		res, err := s.db.Exec(q, args...)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	err = s.withMutation(ctx, "fixture", func(tx *sql.Tx, _ *[]*gridwellv1.Event) error {
		id, _ := parseID(url.Id)
		_, _, err := s.swapTileBlob(ctx, tx, id, "preview_blob_id", []byte("jpeg"), "image/jpeg")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// A directory's grid, a live file row and a tombstoned one: kept.
	dir := exec(`INSERT INTO grids (created_at, updated_at, key) VALUES (1, 1, '/notes')`)
	exec(`INSERT INTO tiles (grid_id, kind, x, y, key, created_at, updated_at) VALUES (?, 'text', 0, 0, '/notes/a.md', 1, 1)`, dir)
	exec(`INSERT INTO tiles (grid_id, kind, x, y, key, tombstoned, created_at, updated_at) VALUES (?, 'text', 1, 0, '/notes/b.md', 1, 1, 1)`, dir)

	// A blob no row names.
	garbage[fmt.Sprintf("blobs:%d", exec(`INSERT INTO blobs (hash, data, refcount) VALUES ('unnamed', 'xxxxx', 0)`))] = true
	// A grid no door owns, holding a text and a door to a grid of its own.
	orphan := exec(`INSERT INTO grids (created_at, updated_at) VALUES (1, 1)`)
	under := exec(`INSERT INTO grids (created_at, updated_at) VALUES (1, 1)`)
	ob := exec(`INSERT INTO blobs (hash, data, refcount) VALUES ('orphan', 'orphan body', 1)`)
	garbage[fmt.Sprintf("tiles:%d", exec(`INSERT INTO tiles (grid_id, kind, x, y, blob_id, created_at, updated_at) VALUES (?, 'text', 0, 0, ?, 1, 1)`, orphan, ob))] = true
	garbage[fmt.Sprintf("tiles:%d", exec(`INSERT INTO tiles (grid_id, kind, x, y, child_grid_id, created_at, updated_at) VALUES (?, 'door', 1, 0, ?, 1, 1)`, orphan, under))] = true
	garbage[fmt.Sprintf("tiles:%d", exec(`INSERT INTO tiles (grid_id, kind, x, y, created_at, updated_at) VALUES (?, 'shell', 0, 0, 1, 1)`, under))] = true
	garbage[fmt.Sprintf("grids:%d", orphan)], garbage[fmt.Sprintf("grids:%d", under)] = true, true
	garbage[fmt.Sprintf("blobs:%d", ob)] = true
	// A tile on a grid that is gone, sharing the kept text's blob.
	exec(`PRAGMA foreign_keys=OFF`)
	garbage[fmt.Sprintf("tiles:%d", exec(`INSERT INTO tiles (grid_id, kind, x, y, blob_id, created_at, updated_at) VALUES (99999, 'text', 0, 0, ?, 1, 1)`, text.BlobId))] = true
	exec(`UPDATE blobs SET refcount = refcount + 1 WHERE id = ?`, text.BlobId)
	exec(`PRAGMA foreign_keys=ON`)
	return s, path, garbage
}

// dumpKept renders every row of the node's tables but the garbage, so a
// clean that left the rest as it was dumps the same before and after. A
// blob's refcount is left out: it counts the rows naming the blob, garbage
// among them, and verifyRefcounts holds it.
func dumpKept(t *testing.T, s *Store, garbage map[string]bool) string {
	t.Helper()
	var b strings.Builder
	for _, table := range []string{"tiles", "grids", "blobs", "system", "connections", "sqlite_sequence"} {
		sel := "*"
		if table == "blobs" {
			sel = "id, hash, data, media_type"
		}
		rows, err := s.db.Query(`SELECT ` + sel + ` FROM ` + table + ` ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			if garbage[fmt.Sprintf("%s:%v", table, vals[0])] {
				continue
			}
			fmt.Fprintf(&b, "%s %v\n", table, vals)
		}
		rows.Close()
	}
	return b.String()
}

// The analysis counts exactly the planted garbage, and Clean removes exactly
// it: every other row of every table is byte-identical after.
func TestCleanRemovesWhatNothingReaches(t *testing.T) {
	ctx := context.Background()
	s, _, garbage := sizeFixture(t)
	a, err := s.Analysis(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{
		"unreached grids": a.UnreachedGrids,
		"unreached tiles": a.Kinds["text"][PlaceUnreached] + a.Kinds["door"][PlaceUnreached] + a.Kinds["shell"][PlaceUnreached],
		"gone-grid tiles": a.Kinds["text"][PlaceGoneGrid],
		"unnamed blobs":   a.Unnamed.Count,
		"unreached-only":  a.UnreachedOnly.Count,
		"drift":           a.RefcountDrift,
		"home texts":      a.Kinds["text"][PlaceHome],
		"trash texts":     a.Kinds["text"][PlaceTrash],
		"template texts":  a.Kinds["text"][PlaceTemplates],
		"scratch urls":    a.Kinds["url"][PlaceScratch],
		"files texts":     a.Kinds["text"][PlaceFiles],
		"tombstoned":      a.Kinds["text"][PlaceTombstoned],
		"previews":        a.Previews.Count,
		"trash only":      a.TrashOnly.Count,
	}
	want := map[string]int{
		"unreached grids": 2, "unreached tiles": 3, "gone-grid tiles": 1, "unnamed blobs": 1,
		"unreached-only": 1, "drift": 0, "home texts": 2, "trash texts": 1, "template texts": 2, "scratch urls": 1,
		"files texts": 1, "tombstoned": 1, "previews": 1, "trash only": 1,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %d, want %d", k, got[k], w)
		}
	}
	if a.Removable() != 8 {
		t.Errorf("Removable() = %d, want 8", a.Removable())
	}

	before := dumpKept(t, s, garbage)
	if err := s.Clean(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	if after := dumpKept(t, s, nil); after != before {
		t.Errorf("clean changed a kept row:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	a, err = s.Analysis(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a.Removable() != 0 || a.RefcountDrift != 0 {
		t.Errorf("after clean: garbage %d, drift %d, want 0", a.Removable(), a.RefcountDrift)
	}
	verifyRefcounts(t, s)
}

// A grid owned by a door on a reached grid and by one on an unreached grid
// would go with the unreached one's teardown, so Clean refuses whole.
func TestCleanRefusesToTouchAReachedGrid(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	door, err := s.CreateDoor(ctx, rootID(t, s), 0, 0, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := s.db.Exec(`INSERT INTO grids (created_at, updated_at) VALUES (1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	og, _ := orphan.LastInsertId()
	if _, err := s.db.Exec(`INSERT INTO tiles (grid_id, kind, x, y, child_grid_id, created_at, updated_at) VALUES (?, 'door', 0, 0, ?, 1, 1)`, og, door.ChildGridId); err != nil {
		t.Fatal(err)
	}
	before := dumpKept(t, s, nil)
	if err := s.Clean(ctx); err == nil {
		t.Fatal("clean tore down a reached grid's owner and was not refused")
	}
	if after := dumpKept(t, s, nil); after != before {
		t.Error("a refused clean changed the database")
	}
}

// A fresh node holds nothing nothing reaches, and every released format
// brought forward reads without error through the read-only opener.
func TestAnalysisOverFreshAndMigratedFiles(t *testing.T) {
	ctx := context.Background()
	_, path := newTestStoreFile(t)
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := ro.Analysis(ctx)
	ro.Close()
	if err != nil {
		t.Fatal(err)
	}
	if a.Removable() != 0 || a.RefcountDrift != 0 || a.MissingBlobs != 0 || a.MissingGrids != 0 {
		t.Errorf("fresh node: %+v", a)
	}
	if len(a.Tables) == 0 {
		t.Error("no table bytes: this sqlite build has no dbstat")
	}
	for _, f := range migrationFixtures {
		t.Run(fmt.Sprintf("v%d", f.version), func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "fixture.db")
			db, root := buildDBAtV1(t, p)
			applyMigrationsUpTo(t, db, f.version-1)
			if f.seed != nil {
				f.seed(t, db, root)
			}
			db.Close()
			if _, err := OpenReadOnly(p); err == nil {
				t.Error("the read-only opener took a file behind this binary's format")
			}
			s, err := Open(p)
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			ro, err := OpenReadOnly(p)
			if err != nil {
				t.Fatal(err)
			}
			defer ro.Close()
			if _, err := ro.Analysis(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
