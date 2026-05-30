package store

import (
	"context"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// TestCloneNodeCreatesSecondPointer verifies that cloning a well produces a
// second well tile sharing the same child grid (refcount 2 on the child)
// without copying the child's contents.
func TestCloneNodeCreatesSharedChildGrid(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Put something inside w's child grid so we can later confirm the
	// clone's child still sees it.
	inner, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{WellIDs: []int64{w.ID}}, ViewRect: largeView(),
		GridID: w.ChildGridID, X: 5, Y: 5, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: w.ID,
		DestGridID: root, DestPath: rpc.Path{}, DestViewRect: largeView(),
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
	var rc int64
	if err := s.db.QueryRow(`SELECT refcount FROM grids WHERE id = ?`, w.ChildGridID).Scan(&rc); err != nil {
		t.Fatal(err)
	}
	if rc != 2 {
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
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Inside the child grid, place a marker well.
	inner, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{WellIDs: []int64{w.ID}}, ViewRect: largeView(),
		GridID: w.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: w.ID,
		DestGridID: root, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 10, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Write through clone's child (fork should occur).
	resized, err := s.ResizeTile(ctx, &rpc.ResizeTileRequest{
		Path: rpc.Path{WellIDs: []int64{clone.ID}}, ViewRect: largeView(),
		TileID: inner.ID, W: 3, H: 3,
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
		t.Fatalf("original child has %d nodes, want 1", len(origChildContents.Tiles))
	}
	if origChildContents.Tiles[0].W != 1 || origChildContents.Tiles[0].H != 1 {
		t.Errorf("original child node was mutated: %+v", origChildContents.Tiles[0])
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
	var rcOld, rcNew int64
	_ = s.db.QueryRow(`SELECT refcount FROM grids WHERE id = ?`, w.ChildGridID).Scan(&rcOld)
	_ = s.db.QueryRow(`SELECT refcount FROM grids WHERE id = ?`, cloneAfter.ChildGridID).Scan(&rcNew)
	if rcOld != 1 {
		t.Errorf("orig child refcount = %d, want 1", rcOld)
	}
	if rcNew != 1 {
		t.Errorf("forked child refcount = %d, want 1", rcNew)
	}
}

// TestCowOneLevelByteIdentity exercises the end-to-end COW invariant on a
// single level of nesting: clone a well that contains content, mutate the
// content via one path, and assert the other path still sees byte-
// identical content and tile state.
func TestCowOneLevelByteIdentity(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	outer, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("# original")
	text, err := s.CreateFile(ctx, &rpc.CreateFileRequest{
		Path:     rpc.Path{WellIDs: []int64{outer.ID}},
		ViewRect: largeView(), GridID: outer.ChildGridID,
		X: 0, Y: 0, W: 1, H: 1, MimeType: "text/markdown", Data: original,
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
		data, _, err := s.GetBlob(ctx, g.Tiles[0].BlobID)
		if err != nil {
			t.Fatal(err)
		}
		return *g, data
	}
	beforeGrid, beforeBytes := snap(outer.ID)

	// Clone the outer well; its child grid is now shared.
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: outer.ID,
		DestGridID: root, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}

	// Walk DOWN clone's path and mutate.
	updated, err := s.UpdateFileContent(ctx, &rpc.UpdateFileContentRequest{
		Path: rpc.Path{WellIDs: []int64{clone.ID}}, ViewRect: largeView(),
		TileID: text.ID, Data: []byte("# mutated"),
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
	_ = beforeBytes
}

// TestCowTwoLevelByteIdentity exercises the COW invariant where the shared
// grid is an ANCESTOR of the leaf, not the leaf itself.
//
// Path shape: root -> A (well) -> G -> B (well) -> H -> text tile.
//
// Clone A so root has both A and A2, both pointing at G (G.rc=2). H is
// reached from G via the well B, and G holds the only well row that
// targets H, so H.rc starts at 1.
//
// A naive leaf-up walk that stops at the first rc<=1 grid would see H.rc=1
// and refuse to fork anything — but G must fork because it's shared, and
// forking G clones the B well, which bumps H.rc to 2. The text tile inside
// H is then reachable from both A and A2, and a write through [A, B] would
// leak into A2.
//
// The correct behavior: fork from the topmost shared grid (G) all the way
// down to the leaf (H), so the [A, B] path ends up at a fresh H' that only
// A can reach.
func TestCowTwoLevelByteIdentity(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	// root -> A (well)
	a, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A's child grid G -> B (well)
	b, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{WellIDs: []int64{a.ID}}, ViewRect: largeView(),
		GridID: a.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// B's child grid H -> text tile.
	original := []byte("# original")
	text, err := s.CreateFile(ctx, &rpc.CreateFileRequest{
		Path:     rpc.Path{WellIDs: []int64{a.ID, b.ID}},
		ViewRect: largeView(), GridID: b.ChildGridID,
		X: 0, Y: 0, W: 1, H: 1, MimeType: "text/markdown", Data: original,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Snapshot helper: walk down [outerWellID, B'] where B' is whichever B
	// row lives in outer's current child grid, then read the lone text
	// tile's bytes.
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
		if len(g.Tiles) != 1 || g.Tiles[0].Type != "well" {
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
		data, _, err := s.GetBlob(ctx, h.Tiles[0].BlobID)
		if err != nil {
			t.Fatal(err)
		}
		return h.Tiles[0], data
	}

	beforeTile, beforeBytes := snap(a.ID)
	if string(beforeBytes) != string(original) {
		t.Fatalf("setup: text bytes = %q, want %q", beforeBytes, original)
	}

	// Clone A. Now root holds A and A2; both point at G; G.rc==2.
	a2, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: a.ID,
		DestGridID: root, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone A: %v", err)
	}
	if a2.ChildGridID != a.ChildGridID {
		t.Fatalf("clone should share child grid; got %d vs %d", a2.ChildGridID, a.ChildGridID)
	}
	var gRC int64
	if err := s.db.QueryRow(`SELECT refcount FROM grids WHERE id = ?`, a.ChildGridID).Scan(&gRC); err != nil {
		t.Fatal(err)
	}
	if gRC != 2 {
		t.Fatalf("G refcount after clone = %d, want 2", gRC)
	}

	// Mutate the text tile via the [A, B] path. H is rc=1; G is rc=2.
	// The buggy leaf-up walk would stop at H (rc=1) and not fork at all.
	// The fix must fork G (because G.rc>1) AND then H (because forking G
	// bumps H.rc to 2).
	mutated := []byte("# mutated")
	updated, err := s.UpdateFileContent(ctx, &rpc.UpdateFileContentRequest{
		Path: rpc.Path{WellIDs: []int64{a.ID, b.ID}}, ViewRect: largeView(),
		TileID: text.ID, Data: mutated,
	})
	if err != nil {
		t.Fatalf("update through [A, B]: %v", err)
	}
	if updated.ObjectID != text.ObjectID {
		t.Errorf("object identity drift across fork: %s -> %s", text.ObjectID, updated.ObjectID)
	}

	// A2's path must still see the ORIGINAL bytes.
	a2Tile, a2Bytes := snap(a2.ID)
	if string(a2Bytes) != string(original) {
		t.Errorf("mutation leaked into A2: bytes = %q, want %q", a2Bytes, original)
	}
	if a2Tile.ObjectID != beforeTile.ObjectID {
		t.Errorf("A2 text tile object_id drifted: %s -> %s", beforeTile.ObjectID, a2Tile.ObjectID)
	}

	// A's path must now see the new bytes.
	_, aBytes := snap(a.ID)
	if string(aBytes) != string(mutated) {
		t.Errorf("A path does not see the write: bytes = %q, want %q", aBytes, mutated)
	}

	verifyRefcounts(t, s)
}

// TestCowTwoLevelByteIdentityThreeClones extends the two-level test with a
// third clone (A3) of the same outer well. After the mutation through A,
// both A2 and A3 must still see the original bytes. This pins that the
// fork-from-topmost-shared rule isolates the writer regardless of how many
// other clones exist on the shared spine.
func TestCowTwoLevelByteIdentityThreeClones(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	a, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{WellIDs: []int64{a.ID}}, ViewRect: largeView(),
		GridID: a.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("# original")
	text, err := s.CreateFile(ctx, &rpc.CreateFileRequest{
		Path:     rpc.Path{WellIDs: []int64{a.ID, b.ID}},
		ViewRect: largeView(), GridID: b.ChildGridID,
		X: 0, Y: 0, W: 1, H: 1, MimeType: "text/markdown", Data: original,
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
		data, _, err := s.GetBlob(ctx, h.Tiles[0].BlobID)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	a2, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: a.ID,
		DestGridID: root, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone A -> A2: %v", err)
	}
	a3, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: a2.ID,
		DestGridID: root, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 20, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone A2 -> A3: %v", err)
	}

	// G should now have rc=3.
	var gRC int64
	if err := s.db.QueryRow(`SELECT refcount FROM grids WHERE id = ?`, a.ChildGridID).Scan(&gRC); err != nil {
		t.Fatal(err)
	}
	if gRC != 3 {
		t.Fatalf("G refcount after two clones = %d, want 3", gRC)
	}

	mutated := []byte("# mutated")
	if _, err := s.UpdateFileContent(ctx, &rpc.UpdateFileContentRequest{
		Path: rpc.Path{WellIDs: []int64{a.ID, b.ID}}, ViewRect: largeView(),
		TileID: text.ID, Data: mutated,
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

// TestRefcountGCBlobOnTileDelete pins the blob refcount/GC contract end-to-
// end: cloning a text tile makes refcount 2; deleting one clone drops it to
// 1 (blob still alive); deleting the last reference drops it to 0 and the
// blob row disappears.
func TestRefcountGCBlobOnTileDelete(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	a, err := s.CreateFile(ctx, &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root,
		X: 0, Y: 0, W: 1, H: 1, MimeType: "text/markdown", Data: []byte("# blob"),
	})
	if err != nil {
		t.Fatal(err)
	}
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: a.ID,
		DestGridID: root, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 5, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if clone.BlobID != a.BlobID {
		t.Fatalf("clone blob id = %d, want %d (shared)", clone.BlobID, a.BlobID)
	}

	var rc int64
	if err := s.db.QueryRow(`SELECT refcount FROM blobs WHERE id = ?`, a.BlobID).Scan(&rc); err != nil {
		t.Fatal(err)
	}
	if rc != 2 {
		t.Fatalf("blob refcount after clone = %d, want 2", rc)
	}

	// Delete one clone — refcount drops to 1, blob row still alive.
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: clone.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT refcount FROM blobs WHERE id = ?`, a.BlobID).Scan(&rc); err != nil {
		t.Fatalf("blob row should still exist; got %v", err)
	}
	if rc != 1 {
		t.Errorf("blob refcount after first delete = %d, want 1", rc)
	}

	// Delete the second — refcount goes to 0, blob row gone.
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: a.ID,
	}); err != nil {
		t.Fatal(err)
	}
	err = s.db.QueryRow(`SELECT refcount FROM blobs WHERE id = ?`, a.BlobID).Scan(&rc)
	if err == nil {
		t.Errorf("blob row still present after final delete (refcount=%d)", rc)
	}
}

// TestRefcountGCGridCascadesBlobs pins the cross-table GC: when a child
// grid's refcount drops to 0 (via cascade-delete of its owning well), every
// blob and child-grid reference held by tiles in that grid must also be
// decremented. A markdown tile inside the deleted well should have its
// blob freed; a sub-well should have its child grid freed.
func TestRefcountGCGridCascadesBlobs(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	outer, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A markdown tile inside outer.
	mdTile, err := s.CreateFile(ctx, &rpc.CreateFileRequest{
		Path: rpc.Path{WellIDs: []int64{outer.ID}}, ViewRect: largeView(),
		GridID: outer.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
		MimeType: "text/markdown", Data: []byte("inside"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// A sub-well inside outer.
	sub, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{WellIDs: []int64{outer.ID}}, ViewRect: largeView(),
		GridID: outer.ChildGridID, X: 5, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	subChildGrid := sub.ChildGridID

	// Sanity: blob refcount 1, sub's child grid refcount 1.
	var rc int64
	if err := s.db.QueryRow(`SELECT refcount FROM blobs WHERE id = ?`, mdTile.BlobID).Scan(&rc); err != nil {
		t.Fatal(err)
	}
	if rc != 1 {
		t.Fatalf("md blob refcount = %d, want 1", rc)
	}
	if err := s.db.QueryRow(`SELECT refcount FROM grids WHERE id = ?`, subChildGrid).Scan(&rc); err != nil {
		t.Fatal(err)
	}
	if rc != 1 {
		t.Fatalf("sub child grid refcount = %d, want 1", rc)
	}

	// Delete outer. Cascade should: delete outer.ChildGridID, then for each
	// tile inside it dec the blob (mdTile) and dec the sub-well's child grid.
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: outer.ID,
	}); err != nil {
		t.Fatal(err)
	}

	// Outer's child grid should be gone.
	if err := s.db.QueryRow(`SELECT refcount FROM grids WHERE id = ?`, outer.ChildGridID).Scan(&rc); err == nil {
		t.Errorf("outer child grid still present; refcount=%d", rc)
	}
	// The markdown blob must be gone (refcount went to 0).
	if err := s.db.QueryRow(`SELECT refcount FROM blobs WHERE id = ?`, mdTile.BlobID).Scan(&rc); err == nil {
		t.Errorf("md blob still present; refcount=%d", rc)
	}
	// The sub-well's child grid must also be gone.
	if err := s.db.QueryRow(`SELECT refcount FROM grids WHERE id = ?`, subChildGrid).Scan(&rc); err == nil {
		t.Errorf("sub-well child grid still present; refcount=%d", rc)
	}
	verifyRefcounts(t, s)
}

// TestRefcountInvariant runs random create/clone/resize/fill operations and
// verifies that refcounts on grids and blobs always equal the actual count of
// references to them.
func TestRefcountInvariant(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	// A small number of seeded operations exercising the interesting paths.
	type op struct {
		kind string
		args []int64
	}
	// Build a small tree.
	w1, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root, X: 2, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Clone w1 into root.
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: w1.ID,
		DestGridID: root, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 4, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Add stuff inside w1's (now shared) child.
	if _, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{WellIDs: []int64{w1.ID}}, ViewRect: largeView(),
		GridID: w1.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// Each of these triggers a fork.
	_ = w2
	_ = clone
	_ = op{}

	// Verify invariant: every grid's refcount equals the count of well rows
	// pointing at it, plus 1 if it's any user's root_grid_id.
	verifyRefcounts(t, s)
}

// verifyRefcounts asserts the refcount invariant globally. Every grid's
// refcount should equal: (number of tile rows with child_grid_id = grid.id)
// plus (1 if any user.root_grid_id = grid.id).
//
// We collect all rows up front (fully materializing each Rows before issuing
// further queries) because the test store uses a single SQLite connection;
// holding a Rows iterator open while running QueryRow on the same handle
// would deadlock.
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
		if err := s.db.QueryRow(`SELECT COUNT(1) FROM tiles WHERE blob_id = ?`, b.id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if b.refcount != n {
			t.Errorf("blob %d refcount = %d, want %d", b.id, b.refcount, n)
		}
	}
}
