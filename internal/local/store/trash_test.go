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
// subgrid with id and links surviving, a closed visit's included, delete
// inside the trash tree destroys for real, and month minting is idempotent.

// trashMonthGrid resolves the month door's child grid in the trash root,
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
		if tl.Kind == rpc.KindDoor && tl.AltText == month {
			return tl.ChildGridId
		}
	}
	t.Fatalf("no %q month door in trash root: %+v", month, g.Tiles)
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
	w, err := s.CreateDoor(ctx, root, 0, 0, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateDoor(ctx, w.ChildGridId, 0, 0, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	// First delete: to the trash, subtree intact.
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: w.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTile(ctx, inner.Id); err != nil {
		t.Fatalf("inner door must survive the trash move: %v", err)
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
	deep, err := s.CreateDoor(ctx, root, 5, 5, 1, 1, "", false)
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
		t.Errorf("delete inside a trashed door's grid must be real: %v", err)
	}
}

// A closed visit goes to the trash as any deleted tile does: the same row and
// id, its version and content untouched, filed in the month grid by the trash's
// own placer, and told as the move it is, so an open view of the month grid
// shows it arrive. Every kind keeps what it holds: a text its body, a url its
// address and preview, a shell its tmux named, a door its grid and what is on
// it, a workspace tile its layout. Two visits share the scratch grid's cell and
// land on cells of their own.
func TestAClosedVisitGoesToTheTrash(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	s.SetNodeID("k3x9m2q") // without it the store holds every visit
	root := rootID(t, s)
	primeTrash(t, s)
	month := trashMonthGrid(t, s, "2026-01")
	scratch, err := s.ScratchGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	visit := func(src *gridwellv1.Tile) *gridwellv1.Tile {
		v, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{TileId: src.Id, DestGridId: scratch})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	text, err := s.CreateText(ctx, root, 0, 0, 1, 1, []byte("# kept body"))
	if err != nil {
		t.Fatal(err)
	}
	door, err := s.CreateDoor(ctx, root, 1, 0, 1, 1, "d", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateText(ctx, door.ChildGridId, 0, 0, 1, 1, []byte("inside")); err != nil {
		t.Fatal(err)
	}
	layout := []byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`)
	ws, err := s.CreateWorkspace(ctx, root, 2, 0, 1, 1, "ws", true, layout)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateScratchURL(ctx, "https://example.com/kept")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetURLState(ctx, u.Id, []byte{0xff, 0xd8, 1}, "", ""); err != nil {
		t.Fatal(err)
	}
	sh, err := s.CreateScratchShell(ctx)
	if err != nil {
		t.Fatal(err)
	}
	visits := []*gridwellv1.Tile{visit(text), visit(door), visit(ws), u, sh}

	cells := map[[2]int64]bool{}
	for _, v := range visits {
		seen, err := s.GetTile(ctx, v.Id)
		if err != nil {
			t.Fatal(err)
		}
		ch, cancel := s.SubscribeEvents()
		if err := s.DeleteTile(ctx, rpc.DeleteOf(seen)); err != nil {
			cancel()
			t.Fatalf("close the %s visit: %v", seen.Kind, err)
		}
		evs := drainEvents(t, ch)
		cancel()
		assertCounts(t, "close the "+seen.Kind+" visit", countKinds(evs), map[string]int{
			"tile_removed": 1, "tile_changed": 1,
		})
		for _, ev := range evs {
			if r := ev.GetTileRemoved(); r != nil && r.GridId != scratch {
				t.Errorf("%s: TileRemoved grid = %s, want the scratch grid %s", seen.Kind, r.GridId, scratch)
			}
			if c := ev.GetTileChanged(); c != nil && c.GetTile().GridId != month {
				t.Errorf("%s: TileChanged grid = %s, want the month grid %s", seen.Kind, c.GetTile().GridId, month)
			}
		}
		got, err := s.GetTile(ctx, v.Id)
		if err != nil {
			t.Fatalf("the closed %s visit is gone: %v", seen.Kind, err)
		}
		if got.GridId != month || got.Version != seen.Version || got.BlobId != seen.BlobId ||
			got.PreviewBlobId != seen.PreviewBlobId || got.UrlString != seen.UrlString ||
			got.ChildGridId != seen.ChildGridId || rpc.ShellTmux(got) != rpc.ShellTmux(seen) {
			t.Errorf("the closed %s visit changed on its way to the trash:\n got %+v\nwant %+v", seen.Kind, got, seen)
		}
		k := [2]int64{got.X, got.Y}
		if cells[k] {
			t.Errorf("two closed visits share the month grid's cell %v", k)
		}
		cells[k] = true
	}

	if body, _, _, err := s.ReadContent(ctx, visits[0].Id); err != nil || string(body) != "# kept body" {
		t.Errorf("the trashed text's body = %q, %v", body, err)
	}
	inside, err := s.GetGrid(ctx, visits[1].ChildGridId)
	if err != nil || len(inside.Tiles) != 1 {
		t.Errorf("the trashed door's grid = %+v, %v; want its one tile", inside, err)
	}
	if body, _, _, err := s.ReadContent(ctx, visits[2].Id); err != nil || string(body) != string(layout) {
		t.Errorf("the trashed workspace tile's layout = %q, %v", body, err)
	}
	if n, err := s.ShellTmuxNamers(ctx, sh.Id); err != nil || n.Rows != 1 {
		t.Errorf("the trashed shell's tmux namers = %+v, %v; want its row still naming it", n, err)
	}
	if g, _ := s.GetGrid(ctx, scratch); len(g.Tiles) != 0 {
		t.Errorf("the scratch grid still holds %d closed visits", len(g.Tiles))
	}

	// In the trash a visit is an ordinary tile: a delete there destroys it.
	for _, v := range visits {
		got, _ := s.GetTile(ctx, v.Id)
		if err := s.DeleteTile(ctx, rpc.DeleteOf(got)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetTile(ctx, v.Id); !errors.Is(err, ErrNotFound) {
			t.Errorf("a trashed visit deleted from the trash must be destroyed: %v", err)
		}
	}
}

// A visit's close claims the row as the visit's writes left it, so a stale
// claim moves nothing.
func TestAClosedVisitClaimsTheRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	s.SetNodeID("k3x9m2q")
	u, err := s.CreateScratchURL(ctx, "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	stale := rpc.DeleteOf(u)
	stale.ClaimVersion++
	if err := s.DeleteTile(ctx, stale); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("a stale close = %v, want a version conflict", err)
	}
	if got, err := s.GetTile(ctx, u.Id); err != nil || got.GridId != u.GridId {
		t.Errorf("a refused close moved the visit: %+v, %v", got, err)
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
		t.Fatalf("trash root = %d doors, want the one 2026-01 month", len(g.Tiles))
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

	// A new month files under a NEW door.
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
		t.Fatalf("trash root = %d doors after a second month, want 2", len(g.Tiles))
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
	// MoveTile's cross-grid shape exactly: remove-from-source +
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
	back, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
		TileId: txt.Id, GridId: root, X: 4, Y: 4, W: 1, H: 1,
	})
	if err != nil {
		t.Fatalf("restore (MoveTile out of trash): %v", err)
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

// A delete claims what the writer saw, the content and the place: a tile
// edited or moved since is refused and stands; one unchanged goes.
func TestDeleteClaimsWhatTheWriterSaw(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	tile, err := s.CreateText(ctx, root, 0, 0, 1, 1, []byte("# hi"))
	if err != nil {
		t.Fatal(err)
	}
	seen := &gridwellv1.Placement{GridId: tile.GridId, X: 0, Y: 0, W: 1, H: 1}
	edited, err := s.WriteContent(ctx, tile.Id, tile.Version, []byte("# edited"))
	if err != nil {
		t.Fatal(err)
	}
	del := func(version int64, p *gridwellv1.Placement) error {
		return s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: tile.Id, ClaimVersion: version, Claim: p})
	}
	if err := del(tile.Version, seen); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("delete over an edit it never saw: got %v, want ErrVersionConflict", err)
	}
	if _, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{TileId: tile.Id, GridId: root, X: 4, Y: 0, W: 1, H: 1, Claim: seen}); err != nil {
		t.Fatal(err)
	}
	if err := del(edited.Version, seen); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("delete over a move it never saw: got %v, want ErrVersionConflict", err)
	}
	if got, err := s.GetTile(ctx, tile.Id); err != nil || got.GridId != tile.GridId {
		t.Fatalf("the refused tile did not stand: %v, %v", got, err)
	}
	if err := del(edited.Version, &gridwellv1.Placement{GridId: tile.GridId, X: 4, Y: 0, W: 1, H: 1}); err != nil {
		t.Fatalf("delete of what the writer saw: %v", err)
	}
}
