package store

import (
	"context"
	"errors"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

func TestMoveNodeWithinGrid(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: w.Id,
		GridId: root, X: 5, Y: 5, W: w.W, H: w.H,
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
	a, err := s.CreateWell(ctx, root, 0, 0, 2, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWell(ctx, root, 5, 5, 2, 2, ""); err != nil {
		t.Fatal(err)
	}
	_, err = s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: a.Id,
		GridId: root, X: 4, Y: 4, W: a.W, H: a.H,
	})
	if !errors.Is(err, ErrOverlap) {
		t.Errorf("got %v, want ErrOverlap", err)
	}
}

func TestMoveNodeAcrossGrids(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	a, err := s.CreateWell(ctx, root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.CreateWell(ctx, root, 5, 5, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	moved, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: target.Id,
		GridId: a.ChildGridId, X: 0, Y: 0, W: target.W, H: target.H,
	})
	if err != nil {
		t.Fatalf("move across: %v", err)
	}
	if moved.GridId != a.ChildGridId {
		t.Errorf("moved.GridId = %s, want %s", moved.GridId, a.ChildGridId)
	}
	g, _ := s.GetGrid(ctx, root)
	for _, n := range g.Tiles {
		if n.Id == target.Id && n.GridId == root {
			t.Errorf("target still in root grid: %+v", n)
		}
	}
}

func TestUpdateTextHappy(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	mdFile, err := s.CreateText(ctx, root, 0, 0, 1, 1, []byte("# hello"))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.WriteContent(ctx, mdFile.Id, mdFile.Version, []byte("# updated"))
	if err != nil {
		t.Fatalf("update md: %v", err)
	}
	if updated.BlobId == mdFile.BlobId {
		t.Error("blob id did not change after content edit")
	}
	if updated.Version != mdFile.Version+1 {
		t.Errorf("version after update = %d, want %d", updated.Version, mdFile.Version+1)
	}
}

// Re-saving byte-identical content must not bump the version or change the
// blob: a debounced auto-save on a tile the user did not edit is a true no-op,
// and the original version still validates after.
func TestUpdateTextIdenticalContentNoOp(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	mdFile, err := s.CreateText(ctx, root, 0, 0, 1, 1, []byte("# hello"))
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.WriteContent(ctx, mdFile.Id, mdFile.Version, []byte("# hello"))
	if err != nil {
		t.Fatalf("no-op update: %v", err)
	}
	if same.Version != mdFile.Version {
		t.Errorf("version after identical save = %d, want %d (no bump)", same.Version, mdFile.Version)
	}
	if same.BlobId != mdFile.BlobId {
		t.Errorf("blob id changed on identical save: %d → %d", mdFile.BlobId, same.BlobId)
	}
	// The original version still validates (it was never bumped), and a real
	// edit from it bumps exactly once.
	changed, err := s.WriteContent(ctx, mdFile.Id, mdFile.Version, []byte("# changed"))
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
	w, err := s.CreateWell(ctx, root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteContent(ctx, w.Id, w.Version, []byte("x"))
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("expected ErrInvalidArgument (kind has no writable content), got %v", err)
	}
}

func TestUpdateTextVersionConflict(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	f, err := s.CreateText(ctx, root, 0, 0, 1, 1, []byte("# v1"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteContent(ctx, f.Id, f.Version+1, []byte("# v2"))
	if !errors.Is(err, ErrVersionConflict) {
		t.Errorf("got %v, want ErrVersionConflict", err)
	}
}

// The write is id-addressed, so a save works wherever the writer is. That is
// what lets the dirty-content sweep post an edit after the editing pane moved.
func TestWriteContentAddressesNestedTileByID(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	nested, err := s.CreateText(ctx, w.ChildGridId, 0, 0, 1, 1, []byte("# nested"))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.WriteContent(ctx, nested.Id, nested.Version, []byte("# nested v2"))
	if err != nil {
		t.Fatalf("empty-path update of a nested tile: %v", err)
	}
	if updated.Version != nested.Version+1 {
		t.Errorf("version = %d, want %d", updated.Version, nested.Version+1)
	}
}
