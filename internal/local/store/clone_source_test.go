package store

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store/storetest"
)

// Clone is an eager deep copy: new ids all the way down, blobs shared by
// content address and refcount, no structural sharing. The existing byte
// identity tests prove the halves diverge once one of them is edited. This one
// is about the copy itself: taking it is not a change to the thing copied, so
// every row of the source subtree — its framings, its captures, its text
// windows, its arrangement — is byte-identical afterwards, and the copy shares
// no id with it.

// subtree walks a well's contents: the grids under it and the tiles in them,
// the doorway included.
func subtree(t *testing.T, s *Store, door *gridwellv1.Tile) (tiles, grids map[string]bool) {
	t.Helper()
	ctx := context.Background()
	tiles, grids = map[string]bool{door.Id: true}, map[string]bool{}
	queue := []string{door.ChildGridId}
	for len(queue) > 0 {
		gid := queue[0]
		queue = queue[1:]
		if gid == "" || grids[gid] {
			continue
		}
		grids[gid] = true
		g, err := s.GetGrid(ctx, gid)
		if err != nil {
			t.Fatalf("walk %s: %v", gid, err)
		}
		for _, tile := range g.Tiles {
			tiles[tile.Id] = true
			if tile.ChildGridId != "" {
				queue = append(queue, tile.ChildGridId)
			}
		}
	}
	return tiles, grids
}

// rowsOf narrows a dump to the named rows of one table.
func rowsOf(d storetest.Dump, table string, ids map[string]bool) map[string]map[string]string {
	out := map[string]map[string]string{}
	for id, row := range d[table] {
		if ids[id] {
			out[id] = row
		}
	}
	return out
}

func TestCloningLeavesTheSourceSubtreeByteIdentical(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)

	// A room with something of every kind in it, each carrying a fact the user
	// made: a scroll, a zoom, a freeze, a viewport, a nested room.
	outer, err := s.CreateWell(ctx, root, 0, 0, 2, 2, "the room")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		TileId: outer.Id, Cx: 1.5, Cy: -2.25, Zoom: 0.8,
	}); err != nil {
		t.Fatal(err)
	}
	text, err := s.CreateText(ctx, outer.ChildGridId, 0, 0, 2, 2, []byte("# a document\n\nwords"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTextView(ctx, text.Id, 0, 120, 640, 480, "rendered"); err != nil {
		t.Fatal(err)
	}
	url, err := s.CreateURL(ctx, outer.ChildGridId, 3, 0, 2, 2, "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetURLState(ctx, url.Id, []byte("jpegbytes"), "https://example.com/deep", "Example", `["https://example.com"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFrozen(ctx, url.Id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetContentZoom(ctx, url.Id, 1.25); err != nil {
		t.Fatal(err)
	}
	shell, err := s.CreateShell(ctx, outer.ChildGridId, 6, 0, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetShellPreview(ctx, shell.Id, []byte("shelljpeg")); err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateWell(ctx, outer.ChildGridId, 0, 3, 2, 2, "a smaller room")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateText(ctx, inner.ChildGridId, 1, 1, 1, 1, []byte("deeper still")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateLeafLink(ctx, outer.ChildGridId, 3, 3, 1, 1, rpc.KindText, remoteTarget, "elsewhere"); err != nil {
		t.Fatal(err)
	}

	outer, err = s.GetTile(ctx, outer.Id)
	if err != nil {
		t.Fatal(err)
	}
	srcTiles, srcGrids := subtree(t, s, outer)
	before := storetest.DumpOf(t, s.SQL())

	clone, err := s.CloneTile(ctx, &gridwellv1.CloneTileRequest{
		TileId: outer.Id, DestGridId: root, X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	after := storetest.DumpOf(t, s.SQL())

	for _, table := range []string{"tiles", "grids"} {
		ids := srcTiles
		if table == "grids" {
			ids = srcGrids
		}
		was, now := rowsOf(before, table, ids), rowsOf(after, table, ids)
		if len(was) != len(ids) {
			t.Fatalf("the source subtree has %d %s rows, expected %d", len(was), table, len(ids))
		}
		for id, row := range was {
			for col, v := range row {
				if now[id][col] != v {
					t.Errorf("taking the copy changed the source: %s[%s].%s %q -> %q",
						table, id, col, v, now[id][col])
				}
			}
		}
	}

	// And it is a copy, not a second name for the same rows: nothing in it is
	// a row of the source subtree, at any depth.
	cpyTiles, cpyGrids := subtree(t, s, clone)
	for id := range cpyTiles {
		if srcTiles[id] {
			t.Errorf("the copy holds the source's tile %s — a clone is a deep copy", id)
		}
	}
	for id := range cpyGrids {
		if srcGrids[id] {
			t.Errorf("the copy holds the source's grid %s — a clone is a deep copy", id)
		}
	}
	if len(cpyTiles) != len(srcTiles) || len(cpyGrids) != len(srcGrids) {
		t.Errorf("the copy is %d tiles in %d grids, the source %d in %d",
			len(cpyTiles), len(cpyGrids), len(srcTiles), len(srcGrids))
	}

	// A shared blob is shared by content address and counted, so its bytes and
	// media type are the same row and only its refcount moved.
	for id, row := range before["blobs"] {
		for col, v := range row {
			if col == "refcount" {
				continue
			}
			if after["blobs"][id][col] != v {
				t.Errorf("taking the copy rewrote blob %s.%s", id, col)
			}
		}
	}
	verifyRefcounts(t, s)
}
