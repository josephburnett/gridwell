package store

import (
	"context"
	"slices"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A home row placed on a directory's grid names that grid by the directory's
// id wherever it is spelled, its row, its events and its set, and the one
// grids row keyed by the path holds it beside the files' rows: minted on the
// first write, found by the files, and left when the row moves home.
func TestAHomeRowOverADirectory(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	dir := rpc.KeyTileID("/r/sub")
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	note, err := s.CreateText(ctx, dir, 1, 1, 1, 1, []byte("Plan"))
	if err != nil {
		t.Fatal(err)
	}
	if note.GridId != dir {
		t.Fatalf("the row's grid = %q, want the directory's %q", note.GridId, dir)
	}
	var told []string
	for _, ev := range drainEvents(t, ch) {
		if g := ev.GetGridChanged().GetGridId(); g != "" {
			told = append(told, "grid "+g)
		}
		if tl := ev.GetTileChanged().GetTile(); tl != nil {
			told = append(told, "tile "+tl.GridId)
		}
	}
	if !slices.Contains(told, "grid "+dir) || !slices.Contains(told, "tile "+dir) {
		t.Errorf("the create told %v, want GridChanged and TileChanged for %s", told, dir)
	}
	gid, ok, err := s.Files().LookupGrid("/r/sub")
	if err != nil || !ok {
		t.Fatalf("the directory's grid row: %v, %v; want the one the write minted", ok, err)
	}
	if over, err := s.Files().Over(gid); err != nil || len(over) != 1 || over[0].Id != note.Id {
		t.Errorf("the files read %v, %v over the directory, want the text", over, err)
	}
	if changed, err := s.Files().SetSelection(gid, SelectOp{Add: []int64{rowOf(t, note.Id)}}); err != nil || len(changed) != 1 {
		t.Fatalf("select the text on the directory: %v, %v", changed, err)
	}
	if set, err := s.Files().Selection(gid); err != nil || !slices.Equal(set, []string{note.Id}) {
		t.Errorf("the directory's set = %v, %v; want the text by its row", set, err)
	}
	root, err := s.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, ch)
	moved, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{TileId: note.Id, GridId: root, X: 2, Y: 2, W: 1, H: 1, Claim: rpc.PlacementOf(note)})
	if err != nil {
		t.Fatal(err)
	}
	if moved.GridId != root {
		t.Errorf("moved home onto %q, want %q", moved.GridId, root)
	}
	left := false
	for _, ev := range drainEvents(t, ch) {
		if r := ev.GetTileRemoved(); r != nil && r.GridId == dir {
			left = true
		}
		if sel := ev.GetSelectionChanged(); sel != nil && (sel.GridId != dir || len(sel.TileIds) != 0) {
			t.Errorf("the set the move left = %v, want the directory's emptied", sel)
		}
	}
	if !left {
		t.Error("the move told no TileRemoved for the directory")
	}
	if _, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{TileId: note.Id, GridId: dir, X: 1, Y: 1, W: 1, H: 1, Claim: rpc.PlacementOf(note)}); err == nil {
		t.Error("a move claiming the directory the row has left landed")
	}
}

func rowOf(t *testing.T, id string) int64 {
	t.Helper()
	n, err := parseID(id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
