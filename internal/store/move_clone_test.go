package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

func TestMoveNodeWithinGrid(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.MoveTile(ctx, &rpc.MoveTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: w.ID,
		DestGridID: root, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 5, Y: 5,
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
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root, X: 0, Y: 0, W: 2, H: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root, X: 5, Y: 5, W: 2, H: 2,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.MoveTile(ctx, &rpc.MoveTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: a.ID,
		DestGridID: root, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 4, Y: 4,
	})
	if !errors.Is(err, ErrOverlap) {
		t.Errorf("got %v, want ErrOverlap", err)
	}
}

func TestMoveNodeAcrossGrids(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	// Build: root has well A (containing nothing) and a target well T at (5,5).
	a, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root, X: 5, Y: 5, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Move target into A's child grid.
	moved, err := s.MoveTile(ctx, &rpc.MoveTileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: target.ID,
		DestGridID: a.ChildGridID, DestPath: rpc.Path{WellIDs: []int64{a.ID}}, DestViewRect: largeView(),
		X: 0, Y: 0,
	})
	if err != nil {
		t.Fatalf("move across: %v", err)
	}
	if moved.GridID != a.ChildGridID {
		t.Errorf("moved.GridID = %d, want %d", moved.GridID, a.ChildGridID)
	}
	// Original location should now be empty.
	g, _ := s.GetGrid(ctx, root)
	for _, n := range g.Tiles {
		if n.ID == target.ID && n.GridID == root {
			t.Errorf("target still in root grid: %+v", n)
		}
	}
}

func TestUpdateFileContentMarkdownOnly(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	mdFile, err := s.CreateFile(ctx, &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root,
		X: 0, Y: 0, W: 1, H: 1, MimeType: "text/markdown", Data: []byte("# hello"),
	})
	if err != nil {
		t.Fatal(err)
	}
	imgFile, err := s.CreateFile(ctx, &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root,
		X: 5, Y: 0, W: 1, H: 1, MimeType: "image/png", Data: []byte("PNG"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Update markdown is allowed.
	updated, err := s.UpdateFileContent(ctx, &rpc.UpdateFileContentRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: mdFile.ID, Data: []byte("# updated"),
	})
	if err != nil {
		t.Fatalf("update md: %v", err)
	}
	if updated.BlobID == mdFile.BlobID {
		t.Error("blob id did not change after content edit")
	}
	// Update image is refused.
	if _, err := s.UpdateFileContent(ctx, &rpc.UpdateFileContentRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: imgFile.ID, Data: []byte("X"),
	}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("expected ErrInvalidArgument for image edit, got %v", err)
	}
}

