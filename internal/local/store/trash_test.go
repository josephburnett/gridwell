package store

import (
	"context"
	"errors"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The trashcan semantics: delete moves the tile to the current month's trash
// subgrid with id and links surviving, delete inside the trash tree destroys
// for real, scratch ephemerals bypass the trash, and month minting is
// idempotent.

// trashMonthGrid resolves the month well's child grid in the trash root,
// failing the test if absent.
func trashMonthGrid(t *testing.T, s *Store, month string) string {
	t.Helper()
	ctx := context.Background()
	trash, err := s.TrashGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.GetGrid(ctx, trash)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range g.Tiles {
		if tl.Kind == rpc.KindWell && tl.AltText == month {
			return tl.ChildGridId
		}
	}
	t.Fatalf("no %q month well in trash root: %+v", month, g.Tiles)
	return ""
}

// primeTrash mints the current month's trash subgrid (by
// trashing and destroying a sacrificial tile) so count-based tests
// measure their own operation, not the first-use minting.
func primeTrash(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	txt, err := s.CreateText(ctx, rootID(t, s), 7, 6, 1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	hardDelete(t, s, txt.Id)
}

// hardDelete collapses the two-stage gesture for tests that assert
// DESTRUCTION: delete once (to the trash), reload, delete again (real).
func hardDelete(t *testing.T, s *Store, tileID string) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := s.GetTile(ctx, tileID); err != nil {
			t.Fatalf("hardDelete load (round %d): %v", i+1, err)
		}
		if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: tileID}); err != nil {
			t.Fatalf("hardDelete (round %d): %v", i+1, err)
		}
	}
}

func TestDeleteMovesToMonthTrashGrid(t *testing.T) {
	s := newTestStore(t) // clock fixed at 2026-01-01
	root := rootID(t, s)
	ctx := context.Background()
	txt, err := s.CreateText(ctx, root, 3, 3, 2, 1, []byte("keep me"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: txt.Id}); err != nil {
		t.Fatal(err)
	}
	// The SAME tile — id continues, content intact. The version does NOT
	// move: a trash filing is a move, and a move is layout, not content.
	got, err := s.GetTile(ctx, txt.Id)
	if err != nil {
		t.Fatalf("trashed tile must still exist: %v", err)
	}
	month := trashMonthGrid(t, s, "2026-01")
	if got.GridId != month {
		t.Errorf("trashed tile grid = %s, want month grid %s", got.GridId, month)
	}
	if got.Version != txt.Version {
		t.Errorf("move moved the version %d -> %d; layout does not bump", txt.Version, got.Version)
	}
	if got.W != 2 || got.H != 1 {
		t.Errorf("footprint must ride along: got %dx%d", got.W, got.H)
	}
	body, _, _, err := s.ReadContent(ctx, txt.Id)
	if err != nil || string(body) != "keep me" {
		t.Errorf("content after trash = %q (%v), want intact", body, err)
	}
	// Gone from the source grid.
	rg, err := s.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range rg.Tiles {
		if tl.Id == txt.Id {
			t.Error("trashed tile still listed in the source grid")
		}
	}
}

func TestDeleteInsideTrashIsReal(t *testing.T) {
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
	// First delete: to the trash, subtree intact.
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: w.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTile(ctx, inner.Id); err != nil {
		t.Fatalf("inner well must survive the trash move: %v", err)
	}
	// Second delete (the tile now sits in the month grid): real, cascades.
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: w.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTile(ctx, w.Id); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete must destroy: %v", err)
	}
	if _, err := s.GetTile(ctx, inner.Id); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete must cascade the subtree: %v", err)
	}

	// The same is true anywhere DEEPER in the trash tree: delete a tile
	// while it already sits in a month grid's own subtree.
	deep, err := s.CreateWell(ctx, root, 5, 5, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: deep.Id}); err != nil {
		t.Fatal(err)
	}
	kid, err := s.CreateText(ctx, deep.ChildGridId, 0, 0, 1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: kid.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTile(ctx, kid.Id); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete inside a trashed well's grid must be real: %v", err)
	}
}

func TestDeleteScratchTileBypassesTrash(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u, err := s.CreateScratchURL(ctx, "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: u.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTile(ctx, u.Id); !errors.Is(err, ErrNotFound) {
		t.Errorf("scratch ephemerals must delete for real: %v", err)
	}
}

func TestTrashMonthMintingIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	for i := int64(0); i < 3; i++ {
		txt, err := s.CreateText(ctx, root, i, 0, 1, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: txt.Id}); err != nil {
			t.Fatal(err)
		}
	}
	trash, _ := s.TrashGridID(ctx)
	g, err := s.GetGrid(ctx, trash)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) != 1 {
		t.Fatalf("trash root = %d wells, want the one 2026-01 month", len(g.Tiles))
	}
	mg, err := s.GetGrid(ctx, g.Tiles[0].ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	if len(mg.Tiles) != 3 {
		t.Errorf("month grid = %d tiles, want 3, each at its own slot", len(mg.Tiles))
	}
	seen := map[[2]int64]bool{}
	for _, tl := range mg.Tiles {
		k := [2]int64{tl.X, tl.Y}
		if seen[k] {
			t.Errorf("two trashed tiles share cell %v", k)
		}
		seen[k] = true
	}

	// A new month files under a NEW well.
	s.SetClock(func() time.Time { return time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC) })
	txt, err := s.CreateText(ctx, root, 7, 7, 1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: txt.Id}); err != nil {
		t.Fatal(err)
	}
	g, _ = s.GetGrid(ctx, trash)
	if len(g.Tiles) != 2 {
		t.Fatalf("trash root = %d wells after a second month, want 2", len(g.Tiles))
	}
}

func TestDeleteToTrashEmitsMoveShape(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	txt, err := s.CreateText(ctx, root, 0, 0, 1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: txt.Id}); err != nil {
		t.Fatal(err)
	}
	evs := drainEvents(t, ch)
	got := countKinds(evs)
	// PlaceTile's cross-grid shape exactly: remove-from-source +
	// appear-at-destination — the shape every client already reconciles.
	assertCounts(t, "DeleteTile(to trash)", got, map[string]int{
		"tile_removed": 1,
		"tile_changed": 1,
	})
	for _, ev := range evs {
		if r := ev.GetTileRemoved(); r != nil && r.GridId != root {
			t.Errorf("TileRemoved grid = %s, want source %s", r.GridId, root)
		}
	}
}

func TestRestoreFromTrashIsAPlainMove(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	txt, err := s.CreateText(ctx, root, 0, 0, 1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: txt.Id}); err != nil {
		t.Fatal(err)
	}
	back, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: txt.Id, GridId: root, X: 4, Y: 4, W: 1, H: 1,
	})
	if err != nil {
		t.Fatalf("restore (PlaceTile out of trash): %v", err)
	}
	if back.GridId != root {
		t.Errorf("restored tile grid = %s, want root", back.GridId)
	}
	// And a delete AFTER restore trashes again (the route is by location,
	// not history).
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: back.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTile(ctx, back.Id); err != nil {
		t.Errorf("re-deleted tile must be back in the trash, not destroyed: %v", err)
	}
}
