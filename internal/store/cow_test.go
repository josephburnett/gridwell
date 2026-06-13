package store

import (
	"context"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// TestCloneNodeCreatesSharedChildGrid verifies that cloning a well produces
// a second well tile sharing the same child grid (refcount 2 on the child)
// without copying the child's contents.
func TestCloneNodeCreatesSharedChildGrid(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Put something inside w's child grid so we can later confirm the
	// clone's child still sees it.
	inner, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: []int64{w.ID}},
		GridID: w.ChildGridID, X: 5, Y: 5, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: w.ID, Version: w.Version,
		DestGridID: root, DestPath: rpc.Path{},
		X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if clone.ObjectID != w.ObjectID {
		t.Errorf("clone object_id = %s, original = %s", clone.ObjectID, w.ObjectID)
	}
	if clone.ID == w.ID {
		t.Errorf("clone has same row id as original")
	}
	if clone.ChildGridID != w.ChildGridID {
		t.Errorf("clone child grid = %d, original = %d (expected shared)", clone.ChildGridID, w.ChildGridID)
	}

	// Refcount on the child grid should be 2.
	if rc := refcount(t, s, "grids", w.ChildGridID); rc != 2 {
		t.Errorf("child refcount = %d, want 2", rc)
	}
	_ = inner
}

// TestCowForkOnWriteIntoSharedChild: after clone, writing through one of the
// pointers must fork; writing through the other must still see the
// pre-fork state.
func TestCowForkOnWriteIntoSharedChild(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Inside the child grid, place a marker well.
	inner, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: []int64{w.ID}},
		GridID: w.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: w.ID, Version: w.Version,
		DestGridID: root, DestPath: rpc.Path{},
		X: 10, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Write through clone's child (fork should occur).
	resized, err := s.ResizeTile(ctx, &rpc.ResizeTileRequest{
		Path: rpc.Path{WellIDs: []int64{clone.ID}}, TileID: inner.ID,
		Version: inner.Version,
		W:       3, H: 3,
	})
	if err != nil {
		t.Fatalf("resize through clone: %v", err)
	}
	// resized should have a NEW row id (different from inner.ID) but the
	// same object_id, because preWrite forked the child grid.
	if resized.ID == inner.ID {
		t.Error("expected fork; got same row id")
	}
	if resized.ObjectID != inner.ObjectID {
		t.Errorf("object_id changed across fork: %s -> %s", inner.ObjectID, resized.ObjectID)
	}
	if resized.W != 3 || resized.H != 3 {
		t.Errorf("resize did not apply: %+v", resized)
	}

	// The original well's child grid should still contain the unchanged inner.
	origChildContents, err := s.GetGrid(ctx, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	if len(origChildContents.Tiles) != 1 {
		t.Fatalf("original child has %d tiles, want 1", len(origChildContents.Tiles))
	}
	if origChildContents.Tiles[0].W != 1 || origChildContents.Tiles[0].H != 1 {
		t.Errorf("original child tile was mutated: %+v", origChildContents.Tiles[0])
	}

	// The clone's child grid should now be a different id from w's.
	cloneAfter, err := s.loadTile(ctx, s.db, clone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cloneAfter.ChildGridID == w.ChildGridID {
		t.Error("clone's child grid was not forked")
	}

	// Refcounts: original child should be back to 1; new forked child is 1.
	if rc := refcount(t, s, "grids", w.ChildGridID); rc != 1 {
		t.Errorf("orig child refcount = %d, want 1", rc)
	}
	if rc := refcount(t, s, "grids", cloneAfter.ChildGridID); rc != 1 {
		t.Errorf("forked child refcount = %d, want 1", rc)
	}
}

// TestCowOneLevelByteIdentity exercises the end-to-end COW invariant on a
// single level of nesting.
func TestCowOneLevelByteIdentity(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	outer, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("# original")
	text, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path:   rpc.Path{WellIDs: []int64{outer.ID}},
		GridID: outer.ChildGridID,
		X:      0, Y: 0, W: 1, H: 1, Data: original,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Snapshot the "other" path.
	snap := func(outerID int64) (rpc.GetGridResponse, []byte) {
		t.Helper()
		ot, err := s.GetTile(ctx, outerID)
		if err != nil {
			t.Fatal(err)
		}
		g, err := s.GetGrid(ctx, ot.ChildGridID)
		if err != nil {
			t.Fatal(err)
		}
		if len(g.Tiles) != 1 {
			t.Fatalf("outer child should have 1 tile; got %d", len(g.Tiles))
		}
		data, err := s.GetBlob(ctx, g.Tiles[0].BlobID)
		if err != nil {
			t.Fatal(err)
		}
		return *g, data
	}
	beforeGrid, _ := snap(outer.ID)

	// Clone the outer well; its child grid is now shared.
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: outer.ID, Version: outer.Version,
		DestGridID: root, DestPath: rpc.Path{},
		X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}

	// Walk DOWN clone's path and mutate.
	updated, err := s.UpdateText(ctx, &rpc.UpdateTextRequest{
		Path: rpc.Path{WellIDs: []int64{clone.ID}}, TileID: text.ID,
		Version: text.Version, Data: []byte("# mutated"),
	})
	if err != nil {
		t.Fatalf("update through clone: %v", err)
	}
	if updated.ObjectID != text.ObjectID {
		t.Errorf("object identity drift: %s -> %s", text.ObjectID, updated.ObjectID)
	}

	// Original path must be byte-identical to the pre-mutation snapshot.
	afterGrid, afterBytes := snap(outer.ID)
	if afterGrid.Grid.ID != beforeGrid.Grid.ID {
		t.Errorf("outer child grid id changed under original path: %d -> %d",
			beforeGrid.Grid.ID, afterGrid.Grid.ID)
	}
	if !tilesEqual(beforeGrid.Tiles, afterGrid.Tiles) {
		t.Errorf("outer child tiles diverged:\n  before=%+v\n  after=%+v",
			beforeGrid.Tiles, afterGrid.Tiles)
	}
	if string(afterBytes) != string(original) {
		t.Errorf("text on original path = %q, want %q (mutation leaked)",
			afterBytes, original)
	}
}

// TestCowTwoLevelByteIdentity pins fork-from-topmost-shared, NOT leaf-up.
func TestCowTwoLevelByteIdentity(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	// root -> A (well)
	a, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A's child grid G -> B (well)
	b, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: []int64{a.ID}},
		GridID: a.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// B's child grid H -> text tile.
	original := []byte("# original")
	text, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path:   rpc.Path{WellIDs: []int64{a.ID, b.ID}},
		GridID: b.ChildGridID,
		X:      0, Y: 0, W: 1, H: 1, Data: original,
	})
	if err != nil {
		t.Fatal(err)
	}

	snap := func(outerWellID int64) (rpc.Tile, []byte) {
		t.Helper()
		ot, err := s.GetTile(ctx, outerWellID)
		if err != nil {
			t.Fatal(err)
		}
		g, err := s.GetGrid(ctx, ot.ChildGridID)
		if err != nil {
			t.Fatal(err)
		}
		if len(g.Tiles) != 1 || g.Tiles[0].Kind != rpc.KindWell {
			t.Fatalf("expected exactly one well inside outer %d; got %+v", outerWellID, g.Tiles)
		}
		bTile := g.Tiles[0]
		h, err := s.GetGrid(ctx, bTile.ChildGridID)
		if err != nil {
			t.Fatal(err)
		}
		if len(h.Tiles) != 1 {
			t.Fatalf("expected exactly one tile in H; got %d", len(h.Tiles))
		}
		data, err := s.GetBlob(ctx, h.Tiles[0].BlobID)
		if err != nil {
			t.Fatal(err)
		}
		return h.Tiles[0], data
	}

	beforeTile, beforeBytes := snap(a.ID)
	if string(beforeBytes) != string(original) {
		t.Fatalf("setup: text bytes = %q, want %q", beforeBytes, original)
	}

	// Clone A.
	a2, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: a.ID, Version: a.Version,
		DestGridID: root, DestPath: rpc.Path{},
		X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone A: %v", err)
	}
	if a2.ChildGridID != a.ChildGridID {
		t.Fatalf("clone should share child grid; got %d vs %d", a2.ChildGridID, a.ChildGridID)
	}
	if rc := refcount(t, s, "grids", a.ChildGridID); rc != 2 {
		t.Fatalf("G refcount after clone = %d, want 2", rc)
	}

	// Mutate via [A, B].
	mutated := []byte("# mutated")
	updated, err := s.UpdateText(ctx, &rpc.UpdateTextRequest{
		Path:    rpc.Path{WellIDs: []int64{a.ID, b.ID}},
		TileID:  text.ID,
		Version: text.Version, Data: mutated,
	})
	if err != nil {
		t.Fatalf("update through [A, B]: %v", err)
	}
	if updated.ObjectID != text.ObjectID {
		t.Errorf("object identity drift across fork: %s -> %s", text.ObjectID, updated.ObjectID)
	}

	a2Tile, a2Bytes := snap(a2.ID)
	if string(a2Bytes) != string(original) {
		t.Errorf("mutation leaked into A2: bytes = %q, want %q", a2Bytes, original)
	}
	if a2Tile.ObjectID != beforeTile.ObjectID {
		t.Errorf("A2 text tile object_id drifted: %s -> %s", beforeTile.ObjectID, a2Tile.ObjectID)
	}

	_, aBytes := snap(a.ID)
	if string(aBytes) != string(mutated) {
		t.Errorf("A path does not see the write: bytes = %q, want %q", aBytes, mutated)
	}

	verifyRefcounts(t, s)
}

// TestCowTwoLevelByteIdentityThreeClones extends the two-level test.
func TestCowTwoLevelByteIdentityThreeClones(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	a, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: []int64{a.ID}},
		GridID: a.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("# original")
	text, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path:   rpc.Path{WellIDs: []int64{a.ID, b.ID}},
		GridID: b.ChildGridID,
		X:      0, Y: 0, W: 1, H: 1, Data: original,
	})
	if err != nil {
		t.Fatal(err)
	}

	snap := func(outerWellID int64) []byte {
		t.Helper()
		ot, err := s.GetTile(ctx, outerWellID)
		if err != nil {
			t.Fatal(err)
		}
		g, err := s.GetGrid(ctx, ot.ChildGridID)
		if err != nil {
			t.Fatal(err)
		}
		if len(g.Tiles) != 1 {
			t.Fatalf("expected 1 tile in outer %d's child; got %d", outerWellID, len(g.Tiles))
		}
		h, err := s.GetGrid(ctx, g.Tiles[0].ChildGridID)
		if err != nil {
			t.Fatal(err)
		}
		if len(h.Tiles) != 1 {
			t.Fatalf("expected 1 tile in H under outer %d; got %d", outerWellID, len(h.Tiles))
		}
		data, err := s.GetBlob(ctx, h.Tiles[0].BlobID)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	a2, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: a.ID, Version: a.Version,
		DestGridID: root, DestPath: rpc.Path{},
		X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone A -> A2: %v", err)
	}
	a3, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: a2.ID, Version: a2.Version,
		DestGridID: root, DestPath: rpc.Path{},
		X: 20, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone A2 -> A3: %v", err)
	}

	if rc := refcount(t, s, "grids", a.ChildGridID); rc != 3 {
		t.Fatalf("G refcount after two clones = %d, want 3", rc)
	}

	mutated := []byte("# mutated")
	if _, err := s.UpdateText(ctx, &rpc.UpdateTextRequest{
		Path:    rpc.Path{WellIDs: []int64{a.ID, b.ID}},
		TileID:  text.ID,
		Version: text.Version, Data: mutated,
	}); err != nil {
		t.Fatalf("update through [A, B]: %v", err)
	}

	if got := snap(a.ID); string(got) != string(mutated) {
		t.Errorf("A: bytes = %q, want %q", got, mutated)
	}
	if got := snap(a2.ID); string(got) != string(original) {
		t.Errorf("A2 leak: bytes = %q, want %q", got, original)
	}
	if got := snap(a3.ID); string(got) != string(original) {
		t.Errorf("A3 leak: bytes = %q, want %q", got, original)
	}

	verifyRefcounts(t, s)
}

// tilesEqual reports whether two tile slices are equal by all observable
// field values.
func tilesEqual(a, b []rpc.Tile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRefcountGCBlobOnTileDelete pins blob refcount/GC end-to-end.
func TestRefcountGCBlobOnTileDelete(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	a, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root,
		X: 0, Y: 0, W: 1, H: 1, Data: []byte("# blob"),
	})
	if err != nil {
		t.Fatal(err)
	}
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: a.ID, Version: a.Version,
		DestGridID: root, DestPath: rpc.Path{},
		X: 5, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if clone.BlobID != a.BlobID {
		t.Fatalf("clone blob id = %d, want %d (shared)", clone.BlobID, a.BlobID)
	}

	if rc := refcount(t, s, "blobs", a.BlobID); rc != 2 {
		t.Fatalf("blob refcount after clone = %d, want 2", rc)
	}

	// Delete one clone.
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: rpc.Path{}, TileID: clone.ID, Version: clone.Version,
	}); err != nil {
		t.Fatal(err)
	}
	if rc := refcount(t, s, "blobs", a.BlobID); rc != 1 {
		t.Errorf("blob refcount after first delete = %d, want 1", rc)
	}

	// Delete the other.
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: rpc.Path{}, TileID: a.ID, Version: a.Version,
	}); err != nil {
		t.Fatal(err)
	}
	var rc int64
	err = s.db.QueryRow(`SELECT refcount FROM blobs WHERE id = ?`, a.BlobID).Scan(&rc)
	if err == nil {
		t.Errorf("blob row still present after final delete (refcount=%d)", rc)
	}
}

// TestRefcountGCGridCascadesBlobs pins cross-table GC.
func TestRefcountGCGridCascadesBlobs(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	outer, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	mdTile, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path:   rpc.Path{WellIDs: []int64{outer.ID}},
		GridID: outer.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
		Data: []byte("inside"),
	})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: []int64{outer.ID}},
		GridID: outer.ChildGridID, X: 5, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	subChildGrid := sub.ChildGridID

	if rc := refcount(t, s, "blobs", mdTile.BlobID); rc != 1 {
		t.Fatalf("md blob refcount = %d, want 1", rc)
	}
	if rc := refcount(t, s, "grids", subChildGrid); rc != 1 {
		t.Fatalf("sub child grid refcount = %d, want 1", rc)
	}

	// Reload outer for current version.
	outerCur, err := s.loadTile(ctx, s.db, outer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: rpc.Path{}, TileID: outer.ID, Version: outerCur.Version,
	}); err != nil {
		t.Fatal(err)
	}

	var rc int64
	if err := s.db.QueryRow(`SELECT refcount FROM grids WHERE id = ?`, outer.ChildGridID).Scan(&rc); err == nil {
		t.Errorf("outer child grid still present; refcount=%d", rc)
	}
	if err := s.db.QueryRow(`SELECT refcount FROM blobs WHERE id = ?`, mdTile.BlobID).Scan(&rc); err == nil {
		t.Errorf("md blob still present; refcount=%d", rc)
	}
	if err := s.db.QueryRow(`SELECT refcount FROM grids WHERE id = ?`, subChildGrid).Scan(&rc); err == nil {
		t.Errorf("sub-well child grid still present; refcount=%d", rc)
	}
	verifyRefcounts(t, s)
}

// verifyRefcounts asserts the refcount invariant globally.
func verifyRefcounts(t *testing.T, s *Store) {
	t.Helper()
	type pair struct{ id, refcount int64 }
	var grids, blobs []pair

	rows, err := s.db.Query(`SELECT id, refcount FROM grids`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.refcount); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		grids = append(grids, p)
	}
	rows.Close()

	brows, err := s.db.Query(`SELECT id, refcount FROM blobs`)
	if err != nil {
		t.Fatal(err)
	}
	for brows.Next() {
		var p pair
		if err := brows.Scan(&p.id, &p.refcount); err != nil {
			brows.Close()
			t.Fatal(err)
		}
		blobs = append(blobs, p)
	}
	brows.Close()

	rootID, err := rootGridID(context.Background(), s.db)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range grids {
		var pointers int64
		if err := s.db.QueryRow(`SELECT COUNT(1) FROM tiles WHERE child_grid_id = ?`, g.id).Scan(&pointers); err != nil {
			t.Fatal(err)
		}
		asRoot := int64(0)
		if g.id == rootID {
			asRoot = 1
		}
		want := pointers + asRoot
		if g.refcount != want {
			t.Errorf("grid %d refcount = %d, want %d", g.id, g.refcount, want)
		}
	}
	for _, b := range blobs {
		var n int64
		// A blob is referenced from two columns: text tiles via blob_id and
		// url/shell previews via preview_blob_id. Count both — otherwise a
		// leaked or double-released preview blob slips past this invariant.
		if err := s.db.QueryRow(
			`SELECT (SELECT COUNT(1) FROM tiles WHERE blob_id = ?)
			      + (SELECT COUNT(1) FROM tiles WHERE preview_blob_id = ?)`,
			b.id, b.id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if b.refcount != n {
			t.Errorf("blob %d refcount = %d, want %d", b.id, b.refcount, n)
		}
	}
}
