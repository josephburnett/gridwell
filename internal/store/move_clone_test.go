package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

func TestMoveNodeWithinGrid(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: w.ID, Version: w.Version,
		GridID: root, X: 5, Y: 5, W: w.W, H: w.H,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if got.X != 5 || got.Y != 5 {
		t.Errorf("after move %+v", got)
	}
}

func TestMoveNodeOverlapRefused(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	a, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 2, H: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 5, Y: 5, W: 2, H: 2,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: a.ID, Version: a.Version,
		GridID: root, X: 4, Y: 4, W: a.W, H: a.H,
	})
	if !errors.Is(err, ErrOverlap) {
		t.Errorf("got %v, want ErrOverlap", err)
	}
}

func TestMoveNodeAcrossGrids(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	a, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 5, Y: 5, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: target.ID, Version: target.Version,
		GridID: a.ChildGridID, X: 0, Y: 0, W: target.W, H: target.H,
	})
	if err != nil {
		t.Fatalf("move across: %v", err)
	}
	if moved.GridID != a.ChildGridID {
		t.Errorf("moved.GridID = %s, want %s", moved.GridID, a.ChildGridID)
	}
	g, _ := s.GetGrid(ctx, root)
	for _, n := range g.Tiles {
		if n.ID == target.ID && n.GridID == root {
			t.Errorf("target still in root grid: %+v", n)
		}
	}
}

func TestUpdateTextHappy(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	mdFile, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root,
		X:      0, Y: 0, W: 1, H: 1, Data: []byte("# hello"),
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.WriteContent(ctx, mdFile.ID, mdFile.Version, []byte("# updated"))
	if err != nil {
		t.Fatalf("update md: %v", err)
	}
	if updated.BlobID == mdFile.BlobID {
		t.Error("blob id did not change after content edit")
	}
	if updated.Version != mdFile.Version+1 {
		t.Errorf("version after update = %d, want %d", updated.Version, mdFile.Version+1)
	}
}

// TestUpdateTextIdenticalContentNoOp: re-saving byte-identical content must not
// bump the version (the edit-history spine) or change the blob — a debounced
// auto-save that fires on a tile the user didn't actually edit is a true no-op,
// per "things stay as you left them". The original version still validates after.
func TestUpdateTextIdenticalContentNoOp(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	mdFile, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("# hello"),
	})
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.WriteContent(ctx, mdFile.ID, mdFile.Version, []byte("# hello"))
	if err != nil {
		t.Fatalf("no-op update: %v", err)
	}
	if same.Version != mdFile.Version {
		t.Errorf("version after identical save = %d, want %d (no bump)", same.Version, mdFile.Version)
	}
	if same.BlobID != mdFile.BlobID {
		t.Errorf("blob id changed on identical save: %d → %d", mdFile.BlobID, same.BlobID)
	}
	// The original version still validates (it was never bumped), and a real
	// edit from it bumps exactly once.
	changed, err := s.WriteContent(ctx, mdFile.ID, mdFile.Version, []byte("# changed"))
	if err != nil {
		t.Fatalf("real edit after no-op: %v", err)
	}
	if changed.Version != mdFile.Version+1 {
		t.Errorf("version after real edit = %d, want %d", changed.Version, mdFile.Version+1)
	}
}

func TestUpdateTextRejectsNonText(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteContent(ctx, w.ID, w.Version, []byte("x"))
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("expected ErrInvalidArgument (kind has no writable content), got %v", err)
	}
}

func TestUpdateTextVersionConflict(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	f, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root,
		X:      0, Y: 0, W: 1, H: 1, Data: []byte("# v1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteContent(ctx, f.ID, f.Version+1, []byte("# v2"))
	if !errors.Is(err, ErrVersionConflict) {
		t.Errorf("got %v, want ErrVersionConflict", err)
	}
}

// TestWriteContentAddressesNestedTileByID: the write is id-addressed — the
// tile's row owns its location, so a save works no matter where the writer
// "is". This is what lets the dirty-content sweep post an edit after the
// editing pane moved elsewhere.
func TestWriteContentAddressesNestedTileByID(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	nested, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: w.ChildGridID,
		X:      0, Y: 0, W: 1, H: 1, Data: []byte("# nested"),
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.WriteContent(ctx, nested.ID, nested.Version, []byte("# nested v2"))
	if err != nil {
		t.Fatalf("empty-path update of a nested tile: %v", err)
	}
	if updated.Version != nested.Version+1 {
		t.Errorf("version = %d, want %d", updated.Version, nested.Version+1)
	}
}
