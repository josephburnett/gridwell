package store

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// parseID parses a string tile/grid id (now used for ChildGridID) to int64.
func parseID(s string) int64 {
	id, _ := strconv.ParseInt(s, 10, 64)
	return id
}

// TestSwapTileBlob exercises the blob-swap kernel directly: a content change
// (new blob, old released), a no-op identical write (no churn), and a dedup
// hit (point at an existing blob row, bumping its refcount instead of
// inserting a duplicate).
func TestSwapTileBlob(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	tile, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("orig"),
	})
	if err != nil {
		t.Fatal(err)
	}
	origBlob := tile.BlobID

	swap := func(bytes []byte) (int64, bool) {
		t.Helper()
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		id, changed, err := s.swapTileBlob(ctx, tx, tile.ID, "blob_id", bytes, mediaMarkdown)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return id, changed
	}

	id1, changed1 := swap([]byte("changed"))
	if !changed1 {
		t.Error("expected changed=true for new content")
	}
	if id1 == origBlob {
		t.Errorf("expected a new blob id, got original %d", origBlob)
	}
	if rc, err := blobRefcount(ctx, s, origBlob); !errors.Is(err, errBlobGone) {
		t.Errorf("orig blob refcount = %d (err %v), want gone", rc, err)
	}
	if rc, _ := blobRefcount(ctx, s, id1); rc != 1 {
		t.Errorf("new blob refcount = %d, want 1", rc)
	}

	id2, changed2 := swap([]byte("changed"))
	if changed2 {
		t.Error("expected changed=false for identical content")
	}
	if id2 != id1 {
		t.Errorf("no-op id = %d, want %d", id2, id1)
	}
	if rc, _ := blobRefcount(ctx, s, id1); rc != 1 {
		t.Errorf("refcount after no-op = %d, want 1 (no churn)", rc)
	}

	other, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root, X: 2, Y: 0, W: 1, H: 1, Data: []byte("shared"),
	})
	if err != nil {
		t.Fatal(err)
	}
	id3, changed3 := swap([]byte("shared"))
	if !changed3 {
		t.Error("expected changed=true switching to shared content")
	}
	if id3 != other.BlobID {
		t.Errorf("dedup blob id = %d, want shared %d", id3, other.BlobID)
	}
	if rc, _ := blobRefcount(ctx, s, id3); rc != 2 {
		t.Errorf("shared blob refcount = %d, want 2", rc)
	}
	verifyRefcounts(t, s)
}

// TestCloneCopiesChildGrid: cloning a well deep-copies its child subtree into
// fresh, independent rows — a new child grid id, and re-rowed inner tiles that
// keep the source's object_id as provenance. Nothing is shared.
func TestCloneCopiesChildGrid(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: []int64{w.ID}},
		GridID: parseID(w.ChildGridID), X: 5, Y: 5, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: w.ID, Version: w.Version,
		DestGridID: root, DestPath: rpc.Path{}, X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if clone.ObjectID != w.ObjectID {
		t.Errorf("clone object_id = %s, original = %s (should carry as provenance)", clone.ObjectID, w.ObjectID)
	}
	if clone.ID == w.ID {
		t.Errorf("clone has same row id as original")
	}
	if clone.ChildGridID == w.ChildGridID {
		t.Errorf("clone child grid = %s == original (expected an independent copy)", clone.ChildGridID)
	}

	cloneChild, err := s.GetGrid(ctx, parseID(clone.ChildGridID))
	if err != nil {
		t.Fatal(err)
	}
	if len(cloneChild.Tiles) != 1 {
		t.Fatalf("clone child has %d tiles, want 1", len(cloneChild.Tiles))
	}
	ct := cloneChild.Tiles[0]
	if ct.ID == inner.ID {
		t.Errorf("inner tile should be re-rowed in the copy, still has id %d", inner.ID)
	}
	if ct.ObjectID != inner.ObjectID {
		t.Errorf("copied inner object_id = %s, want %s (provenance)", ct.ObjectID, inner.ObjectID)
	}
	verifyRefcounts(t, s)
}

// TestCloneIndependentEdit: after cloning a well, editing inside the clone
// writes in place (id stable) and the original subtree is byte-identical.
func TestCloneIndependentEdit(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: []int64{w.ID}},
		GridID: parseID(w.ChildGridID), X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: w.ID, Version: w.Version,
		DestGridID: root, DestPath: rpc.Path{}, X: 10, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	cloneChild, err := s.GetGrid(ctx, parseID(clone.ChildGridID))
	if err != nil {
		t.Fatal(err)
	}
	cInner := cloneChild.Tiles[0]
	resized, err := s.ResizeTile(ctx, &rpc.ResizeTileRequest{
		Path: rpc.Path{WellIDs: []int64{clone.ID}}, TileID: cInner.ID,
		Version: cInner.Version, W: 3, H: 3,
	})
	if err != nil {
		t.Fatalf("resize through clone: %v", err)
	}
	if resized.ID != cInner.ID {
		t.Error("edit re-rowed the tile; copy-on-clone edits must be in place")
	}
	if resized.W != 3 || resized.H != 3 {
		t.Errorf("resize did not apply: %+v", resized)
	}

	origChild, err := s.GetGrid(ctx, parseID(w.ChildGridID))
	if err != nil {
		t.Fatal(err)
	}
	if len(origChild.Tiles) != 1 {
		t.Fatalf("original child has %d tiles, want 1", len(origChild.Tiles))
	}
	if origChild.Tiles[0].ID != inner.ID {
		t.Errorf("original inner re-rowed: %d -> %d", inner.ID, origChild.Tiles[0].ID)
	}
	if origChild.Tiles[0].W != 1 || origChild.Tiles[0].H != 1 {
		t.Errorf("original child tile was mutated: %+v", origChild.Tiles[0])
	}
	verifyRefcounts(t, s)
}

// TestCloneOneLevelByteIdentity: editing a cloned text tile leaves the
// original byte-identical, and the clone's tile keeps its row id.
func TestCloneOneLevelByteIdentity(t *testing.T) {
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
		GridID: parseID(outer.ChildGridID),
		X:      0, Y: 0, W: 1, H: 1, Data: original,
	})
	if err != nil {
		t.Fatal(err)
	}

	snap := func(outerID int64) []byte {
		t.Helper()
		ot, err := s.GetTile(ctx, outerID)
		if err != nil {
			t.Fatal(err)
		}
		g, err := s.GetGrid(ctx, parseID(ot.ChildGridID))
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
		return data
	}

	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: outer.ID, Version: outer.Version,
		DestGridID: root, DestPath: rpc.Path{}, X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}

	cloneChild, err := s.GetGrid(ctx, parseID(clone.ChildGridID))
	if err != nil {
		t.Fatal(err)
	}
	cText := cloneChild.Tiles[0]
	updated, err := s.UpdateText(ctx, &rpc.UpdateTextRequest{
		Path: rpc.Path{WellIDs: []int64{clone.ID}}, TileID: cText.ID,
		Version: cText.Version, Data: []byte("# mutated"),
	})
	if err != nil {
		t.Fatalf("update through clone: %v", err)
	}
	if updated.ID != cText.ID {
		t.Error("update re-rowed the clone's text; edits must be in place")
	}
	if updated.ObjectID != text.ObjectID {
		t.Errorf("object identity drift: %s -> %s", text.ObjectID, updated.ObjectID)
	}

	if got := snap(outer.ID); string(got) != string(original) {
		t.Errorf("original path = %q, want %q (mutation leaked)", got, original)
	}
	if got := snap(clone.ID); string(got) != "# mutated" {
		t.Errorf("clone path = %q, want # mutated", got)
	}
	verifyRefcounts(t, s)
}

// TestCloneTwoLevelByteIdentity: a two-level clone is fully independent —
// editing the original's deep leaf doesn't touch the clone.
func TestCloneTwoLevelByteIdentity(t *testing.T) {
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
		GridID: parseID(a.ChildGridID), X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("# original")
	text, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path:   rpc.Path{WellIDs: []int64{a.ID, b.ID}},
		GridID: parseID(b.ChildGridID),
		X:      0, Y: 0, W: 1, H: 1, Data: original,
	})
	if err != nil {
		t.Fatal(err)
	}

	leafBytes := func(outerWellID int64) []byte {
		t.Helper()
		ot, err := s.GetTile(ctx, outerWellID)
		if err != nil {
			t.Fatal(err)
		}
		g, err := s.GetGrid(ctx, parseID(ot.ChildGridID))
		if err != nil {
			t.Fatal(err)
		}
		if len(g.Tiles) != 1 || g.Tiles[0].Kind != rpc.KindWell {
			t.Fatalf("expected one well inside outer %d; got %+v", outerWellID, g.Tiles)
		}
		h, err := s.GetGrid(ctx, parseID(g.Tiles[0].ChildGridID))
		if err != nil {
			t.Fatal(err)
		}
		if len(h.Tiles) != 1 {
			t.Fatalf("expected one tile in H; got %d", len(h.Tiles))
		}
		data, err := s.GetBlob(ctx, h.Tiles[0].BlobID)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	a2, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: a.ID, Version: a.Version,
		DestGridID: root, DestPath: rpc.Path{}, X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone A: %v", err)
	}
	if a2.ChildGridID == a.ChildGridID {
		t.Fatalf("clone should deep-copy the child grid; got shared %s", a2.ChildGridID)
	}

	mutated := []byte("# mutated")
	updated, err := s.UpdateText(ctx, &rpc.UpdateTextRequest{
		Path:    rpc.Path{WellIDs: []int64{a.ID, b.ID}},
		TileID:  text.ID,
		Version: text.Version, Data: mutated,
	})
	if err != nil {
		t.Fatalf("update through [A, B]: %v", err)
	}
	if updated.ID != text.ID {
		t.Error("update re-rowed the original's text; edits must be in place")
	}

	if got := leafBytes(a.ID); string(got) != string(mutated) {
		t.Errorf("A path = %q, want %q", got, mutated)
	}
	if got := leafBytes(a2.ID); string(got) != string(original) {
		t.Errorf("A2 leak: bytes = %q, want %q", got, original)
	}
	verifyRefcounts(t, s)
}

// TestCloneThreeIndependentCopies: clone twice, edit the original; both copies
// keep the original content (independent rows).
func TestCloneThreeIndependentCopies(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	a, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("# original")
	text, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path:   rpc.Path{WellIDs: []int64{a.ID}},
		GridID: parseID(a.ChildGridID), X: 0, Y: 0, W: 1, H: 1, Data: original,
	})
	if err != nil {
		t.Fatal(err)
	}

	leafBytes := func(outerWellID int64) []byte {
		t.Helper()
		ot, err := s.GetTile(ctx, outerWellID)
		if err != nil {
			t.Fatal(err)
		}
		g, err := s.GetGrid(ctx, parseID(ot.ChildGridID))
		if err != nil {
			t.Fatal(err)
		}
		if len(g.Tiles) != 1 {
			t.Fatalf("expected 1 tile in outer %d's child; got %d", outerWellID, len(g.Tiles))
		}
		data, err := s.GetBlob(ctx, g.Tiles[0].BlobID)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	a2, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: a.ID, Version: a.Version,
		DestGridID: root, DestPath: rpc.Path{}, X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone A -> A2: %v", err)
	}
	a3, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: a2.ID, Version: a2.Version,
		DestGridID: root, DestPath: rpc.Path{}, X: 20, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone A2 -> A3: %v", err)
	}

	mutated := []byte("# mutated")
	if _, err := s.UpdateText(ctx, &rpc.UpdateTextRequest{
		Path:    rpc.Path{WellIDs: []int64{a.ID}},
		TileID:  text.ID,
		Version: text.Version, Data: mutated,
	}); err != nil {
		t.Fatalf("update through A: %v", err)
	}

	if got := leafBytes(a.ID); string(got) != string(mutated) {
		t.Errorf("A: bytes = %q, want %q", got, mutated)
	}
	if got := leafBytes(a2.ID); string(got) != string(original) {
		t.Errorf("A2 leak: bytes = %q, want %q", got, original)
	}
	if got := leafBytes(a3.ID); string(got) != string(original) {
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

// TestRefcountGCBlobOnTileDelete pins blob refcount/GC end-to-end: a cloned
// text shares the blob (refcount 2), GC'd only when the last reference goes.
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
		DestGridID: root, DestPath: rpc.Path{}, X: 5, Y: 0,
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

	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: rpc.Path{}, TileID: clone.ID, Version: clone.Version,
	}); err != nil {
		t.Fatal(err)
	}
	if rc := refcount(t, s, "blobs", a.BlobID); rc != 1 {
		t.Errorf("blob refcount after first delete = %d, want 1", rc)
	}

	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: rpc.Path{}, TileID: a.ID, Version: a.Version,
	}); err != nil {
		t.Fatal(err)
	}
	var rc int64
	if err := s.db.QueryRow(`SELECT refcount FROM blobs WHERE id = ?`, a.BlobID).Scan(&rc); err == nil {
		t.Errorf("blob row still present after final delete (refcount=%d)", rc)
	}
}

// TestDeleteGridCascadesBlobs: deleting a well recursively tears down its
// owned child grid (and nested grids), releasing every blob inside.
func TestDeleteGridCascadesBlobs(t *testing.T) {
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
		GridID: parseID(outer.ChildGridID), X: 0, Y: 0, W: 1, H: 1,
		Data: []byte("inside"),
	})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: []int64{outer.ID}},
		GridID: parseID(outer.ChildGridID), X: 5, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	subChildGrid := parseID(sub.ChildGridID)

	if rc := refcount(t, s, "blobs", mdTile.BlobID); rc != 1 {
		t.Fatalf("md blob refcount = %d, want 1", rc)
	}

	outerCur, err := s.loadTile(ctx, s.db, outer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: rpc.Path{}, TileID: outer.ID, Version: outerCur.Version,
	}); err != nil {
		t.Fatal(err)
	}

	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM grids WHERE id = ?`, parseID(outer.ChildGridID)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("outer child grid still present after delete")
	}
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM grids WHERE id = ?`, subChildGrid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("sub-well child grid still present after delete")
	}
	if err := s.db.QueryRow(`SELECT refcount FROM blobs WHERE id = ?`, mdTile.BlobID).Scan(&n); err == nil {
		t.Errorf("md blob still present after delete; refcount=%d", n)
	}
	verifyRefcounts(t, s)
}

// verifyRefcounts asserts the blob refcount invariant globally: every blob's
// stored refcount equals the number of tile columns that reference it. Grids
// are not refcounted (owned 1:1 under copy-on-clone), so there's nothing to
// check for them.
func verifyRefcounts(t *testing.T, s *Store) {
	t.Helper()
	rows, err := s.db.Query(`SELECT id, refcount FROM blobs`)
	if err != nil {
		t.Fatal(err)
	}
	type pair struct{ id, refcount int64 }
	var blobs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.refcount); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		blobs = append(blobs, p)
	}
	rows.Close()

	for _, b := range blobs {
		var n int64
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
