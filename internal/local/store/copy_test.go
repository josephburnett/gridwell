package store

import (
	"context"
	"errors"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The blob-swap kernel directly: a content change, a no-op identical write,
// and a dedup hit that bumps an existing blob's refcount.
func TestSwapTileBlob(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	tile, err := s.CreateText(ctx, root, 0, 0, 1, 1, []byte("orig"))
	if err != nil {
		t.Fatal(err)
	}
	origBlob := tile.BlobId

	tileIDInt, _ := parseID(tile.Id)
	swap := func(bytes []byte) (int64, bool) {
		t.Helper()
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		id, changed, err := s.swapTileBlob(ctx, tx, tileIDInt, "blob_id", bytes, mediaMarkdown)
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

	other, err := s.CreateText(ctx, root, 2, 0, 1, 1, []byte("shared"))
	if err != nil {
		t.Fatal(err)
	}
	id3, changed3 := swap([]byte("shared"))
	if !changed3 {
		t.Error("expected changed=true switching to shared content")
	}
	if id3 != other.BlobId {
		t.Errorf("dedup blob id = %d, want shared %d", id3, other.BlobId)
	}
	if rc, _ := blobRefcount(ctx, s, id3); rc != 2 {
		t.Errorf("shared blob refcount = %d, want 2", rc)
	}
	verifyRefcounts(t, s)
}

// TestCopyCopiesChildGrid: copying a well deep-copies its child subtree into
// fresh, independent rows — a new child grid id and re-rowed inner tiles.
// Nothing is shared.
func TestCopyCopiesChildGrid(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	w, err := s.CreateWell(ctx, root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateWell(ctx, w.ChildGridId, 5, 5, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}

	cp, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId:     w.Id,
		DestGridId: root, X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if cp.Id == w.Id {
		t.Errorf("copy has same row id as original")
	}
	if cp.ChildGridId == w.ChildGridId {
		t.Errorf("copy child grid = %s == original (expected an independent copy)", cp.ChildGridId)
	}

	copyChild, err := s.GetGrid(ctx, cp.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	if len(copyChild.Tiles) != 1 {
		t.Fatalf("copy child has %d tiles, want 1", len(copyChild.Tiles))
	}
	ct := copyChild.Tiles[0]
	if ct.Id == inner.Id {
		t.Errorf("inner tile should be re-rowed in the copy, still has id %s", inner.Id)
	}
	verifyRefcounts(t, s)
}

// TestCopyIndependentEdit: after copying a well, editing inside the copy
// writes in place (id stable) and the original subtree is byte-identical.
func TestCopyIndependentEdit(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	w, err := s.CreateWell(ctx, root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateWell(ctx, w.ChildGridId, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	cp, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId:     w.Id,
		DestGridId: root, X: 10, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	copyChild, err := s.GetGrid(ctx, cp.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	cInner := copyChild.Tiles[0]
	resized, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
		TileId: cInner.Id,
		GridId: cInner.GridId, X: 0, Y: 0, W: 3, H: 3,
	})
	if err != nil {
		t.Fatalf("resize through copy: %v", err)
	}
	if resized.Id != cInner.Id {
		t.Error("edit re-rowed the tile; an edit after a copy must be in place")
	}
	if resized.W != 3 || resized.H != 3 {
		t.Errorf("resize did not apply: %+v", resized)
	}

	origChild, err := s.GetGrid(ctx, w.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	if len(origChild.Tiles) != 1 {
		t.Fatalf("original child has %d tiles, want 1", len(origChild.Tiles))
	}
	if origChild.Tiles[0].Id != inner.Id {
		t.Errorf("original inner re-rowed: %s -> %s", inner.Id, origChild.Tiles[0].Id)
	}
	if origChild.Tiles[0].W != 1 || origChild.Tiles[0].H != 1 {
		t.Errorf("original child tile was mutated: %+v", origChild.Tiles[0])
	}
	verifyRefcounts(t, s)
}

// TestCopyOneLevelByteIdentity: editing a copied text tile leaves the
// original byte-identical, and the copy's tile keeps its row id.
func TestCopyOneLevelByteIdentity(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	outer, err := s.CreateWell(ctx, root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("# original")
	if _, err := s.CreateText(ctx, outer.ChildGridId, 0, 0, 1, 1, original); err != nil {
		t.Fatal(err)
	}

	snap := func(outerID string) []byte {
		t.Helper()
		ot, err := s.GetTile(ctx, outerID)
		if err != nil {
			t.Fatal(err)
		}
		g, err := s.GetGrid(ctx, ot.ChildGridId)
		if err != nil {
			t.Fatal(err)
		}
		if len(g.Tiles) != 1 {
			t.Fatalf("outer child should have 1 tile; got %d", len(g.Tiles))
		}
		data, err := s.GetBlob(ctx, g.Tiles[0].BlobId)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	cp, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId:     outer.Id,
		DestGridId: root, X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("copy: %v", err)
	}

	copyChild, err := s.GetGrid(ctx, cp.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	cText := copyChild.Tiles[0]
	updated, err := s.WriteContent(ctx, cText.Id, cText.Version, []byte("# mutated"))
	if err != nil {
		t.Fatalf("update through copy: %v", err)
	}
	if updated.Id != cText.Id {
		t.Error("update re-rowed the copy's text; edits must be in place")
	}

	if got := snap(outer.Id); string(got) != string(original) {
		t.Errorf("original path = %q, want %q (mutation leaked)", got, original)
	}
	if got := snap(cp.Id); string(got) != "# mutated" {
		t.Errorf("copy path = %q, want # mutated", got)
	}
	verifyRefcounts(t, s)
}

// TestCopyTwoLevelByteIdentity: a two-level copy is fully independent —
// editing the original's deep leaf doesn't touch the copy.
func TestCopyTwoLevelByteIdentity(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	a, err := s.CreateWell(ctx, root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateWell(ctx, a.ChildGridId, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("# original")
	text, err := s.CreateText(ctx, b.ChildGridId, 0, 0, 1, 1, original)
	if err != nil {
		t.Fatal(err)
	}

	leafBytes := func(outerWellID string) []byte {
		t.Helper()
		ot, err := s.GetTile(ctx, outerWellID)
		if err != nil {
			t.Fatal(err)
		}
		g, err := s.GetGrid(ctx, ot.ChildGridId)
		if err != nil {
			t.Fatal(err)
		}
		if len(g.Tiles) != 1 || g.Tiles[0].Kind != rpc.KindWell {
			t.Fatalf("expected one well inside outer %s; got %+v", outerWellID, g.Tiles)
		}
		h, err := s.GetGrid(ctx, g.Tiles[0].ChildGridId)
		if err != nil {
			t.Fatal(err)
		}
		if len(h.Tiles) != 1 {
			t.Fatalf("expected one tile in H; got %d", len(h.Tiles))
		}
		data, err := s.GetBlob(ctx, h.Tiles[0].BlobId)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	a2, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId:     a.Id,
		DestGridId: root, X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("copy A: %v", err)
	}
	if a2.ChildGridId == a.ChildGridId {
		t.Fatalf("copy should deep-copy the child grid; got shared %s", a2.ChildGridId)
	}

	mutated := []byte("# mutated")
	updated, err := s.WriteContent(ctx, text.Id, text.Version, mutated)
	if err != nil {
		t.Fatalf("update through [A, B]: %v", err)
	}
	if updated.Id != text.Id {
		t.Error("update re-rowed the original's text; edits must be in place")
	}

	if got := leafBytes(a.Id); string(got) != string(mutated) {
		t.Errorf("A path = %q, want %q", got, mutated)
	}
	if got := leafBytes(a2.Id); string(got) != string(original) {
		t.Errorf("A2 leak: bytes = %q, want %q", got, original)
	}
	verifyRefcounts(t, s)
}

// TestCopyThreeIndependentCopies: copy twice, edit the original; both copies
// keep the original content (independent rows).
func TestCopyThreeIndependentCopies(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	a, err := s.CreateWell(ctx, root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("# original")
	text, err := s.CreateText(ctx, a.ChildGridId, 0, 0, 1, 1, original)
	if err != nil {
		t.Fatal(err)
	}

	leafBytes := func(outerWellID string) []byte {
		t.Helper()
		ot, err := s.GetTile(ctx, outerWellID)
		if err != nil {
			t.Fatal(err)
		}
		g, err := s.GetGrid(ctx, ot.ChildGridId)
		if err != nil {
			t.Fatal(err)
		}
		if len(g.Tiles) != 1 {
			t.Fatalf("expected 1 tile in outer %s's child; got %d", outerWellID, len(g.Tiles))
		}
		data, err := s.GetBlob(ctx, g.Tiles[0].BlobId)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	a2, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId:     a.Id,
		DestGridId: root, X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("copy A -> A2: %v", err)
	}
	a3, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId:     a2.Id,
		DestGridId: root, X: 20, Y: 0,
	})
	if err != nil {
		t.Fatalf("copy A2 -> A3: %v", err)
	}

	mutated := []byte("# mutated")
	if _, err := s.WriteContent(ctx, text.Id, text.Version, mutated); err != nil {
		t.Fatalf("update through A: %v", err)
	}

	if got := leafBytes(a.Id); string(got) != string(mutated) {
		t.Errorf("A: bytes = %q, want %q", got, mutated)
	}
	if got := leafBytes(a2.Id); string(got) != string(original) {
		t.Errorf("A2 leak: bytes = %q, want %q", got, original)
	}
	if got := leafBytes(a3.Id); string(got) != string(original) {
		t.Errorf("A3 leak: bytes = %q, want %q", got, original)
	}
	verifyRefcounts(t, s)
}

// TestRefcountGCBlobOnTileDelete pins blob refcount/GC end-to-end: a copied
// text shares the blob (refcount 2), GC'd only when the last reference goes.
func TestRefcountGCBlobOnTileDelete(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	a, err := s.CreateText(ctx, root, 0, 0, 1, 1, []byte("# blob"))
	if err != nil {
		t.Fatal(err)
	}
	cp, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId:     a.Id,
		DestGridId: root, X: 5, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cp.BlobId != a.BlobId {
		t.Fatalf("copy blob id = %d, want %d (shared)", cp.BlobId, a.BlobId)
	}
	if rc := refcount(t, s, "blobs", a.BlobId); rc != 2 {
		t.Fatalf("blob refcount after copy = %d, want 2", rc)
	}

	hardDelete(t, s, cp.Id)
	if rc := refcount(t, s, "blobs", a.BlobId); rc != 1 {
		t.Errorf("blob refcount after first destroy = %d, want 1", rc)
	}

	hardDelete(t, s, a.Id)
	var rc int64
	if err := s.db.QueryRow(`SELECT refcount FROM blobs WHERE id = ?`, a.BlobId).Scan(&rc); err == nil {
		t.Errorf("blob row still present after final delete (refcount=%d)", rc)
	}
}

// TestDeleteGridCascadesBlobs: deleting a well recursively tears down its
// owned child grid (and nested grids), releasing every blob inside.
func TestDeleteGridCascadesBlobs(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	outer, err := s.CreateWell(ctx, root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	mdTile, err := s.CreateText(ctx, outer.ChildGridId, 0, 0, 1, 1, []byte("inside"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.CreateWell(ctx, outer.ChildGridId, 5, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	subChildGridID, _ := parseID(sub.ChildGridId)

	if rc := refcount(t, s, "blobs", mdTile.BlobId); rc != 1 {
		t.Fatalf("md blob refcount = %d, want 1", rc)
	}

	hardDelete(t, s, outer.Id)

	outerChildGridID, _ := parseID(outer.ChildGridId)
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM grids WHERE id = ?`, outerChildGridID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("outer child grid still present after delete")
	}
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM grids WHERE id = ?`, subChildGridID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("sub-well child grid still present after delete")
	}
	if err := s.db.QueryRow(`SELECT refcount FROM blobs WHERE id = ?`, mdTile.BlobId).Scan(&n); err == nil {
		t.Errorf("md blob still present after delete; refcount=%d", n)
	}
	verifyRefcounts(t, s)
}

// verifyRefcounts asserts every blob's stored refcount equals the number of
// tile columns referencing it. Grids are owned 1:1 and not refcounted.
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
