package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

func TestMoveNodeWithinGrid(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.MoveNode(ctx, u.ID, &rpc.MoveNodeRequest{
		Path: rpc.Path{}, ViewRect: largeView(), NodeID: w.ID,
		DestGridID: u.RootGridID, DestPath: rpc.Path{}, DestViewRect: largeView(),
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
	u := fixtureUser(t, s)
	ctx := context.Background()
	a, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 2, H: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 5, Y: 5, W: 2, H: 2,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.MoveNode(ctx, u.ID, &rpc.MoveNodeRequest{
		Path: rpc.Path{}, ViewRect: largeView(), NodeID: a.ID,
		DestGridID: u.RootGridID, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 4, Y: 4,
	})
	if !errors.Is(err, ErrOverlap) {
		t.Errorf("got %v, want ErrOverlap", err)
	}
}

func TestMoveNodeAcrossGrids(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	// Build: root has well A (containing nothing) and a target well T at (5,5).
	a, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 5, Y: 5, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Move target into A's child grid.
	moved, err := s.MoveNode(ctx, u.ID, &rpc.MoveNodeRequest{
		Path: rpc.Path{}, ViewRect: largeView(), NodeID: target.ID,
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
	root, _ := s.GetGrid(ctx, u.ID, u.RootGridID)
	for _, n := range root.Nodes {
		if n.ID == target.ID && n.GridID == u.RootGridID {
			t.Errorf("target still in root grid: %+v", n)
		}
	}
}

func TestUpdateFileContentMarkdownOnly(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	mdFile, err := s.CreateFile(ctx, u.ID, &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID,
		X: 0, Y: 0, W: 1, H: 1, MimeType: "text/markdown", Data: []byte("# hello"),
	})
	if err != nil {
		t.Fatal(err)
	}
	imgFile, err := s.CreateFile(ctx, u.ID, &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID,
		X: 5, Y: 0, W: 1, H: 1, MimeType: "image/png", Data: []byte("PNG"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Update markdown is allowed.
	updated, err := s.UpdateFileContent(ctx, u.ID, &rpc.UpdateFileContentRequest{
		Path: rpc.Path{}, ViewRect: largeView(), NodeID: mdFile.ID, Data: []byte("# updated"),
	})
	if err != nil {
		t.Fatalf("update md: %v", err)
	}
	if updated.BlobID == mdFile.BlobID {
		t.Error("blob id did not change after content edit")
	}
	// Update image is refused.
	if _, err := s.UpdateFileContent(ctx, u.ID, &rpc.UpdateFileContentRequest{
		Path: rpc.Path{}, ViewRect: largeView(), NodeID: imgFile.ID, Data: []byte("X"),
	}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("expected ErrInvalidArgument for image edit, got %v", err)
	}
}

// TestCloneRequiresWriteOnSource verifies that read-only access on the source
// is not enough to clone (spec §7.3 / §3.9).
func TestCloneRequiresWriteOnSource(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Strip write bit on the well node, leaving owner-read only.
	if _, err := s.db.Exec(`UPDATE nodes SET mode = ? WHERE id = ?`, 0o400, w.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.CloneNode(ctx, u.ID, &rpc.CloneNodeRequest{
		Path: rpc.Path{}, ViewRect: largeView(), NodeID: w.ID,
		DestGridID: u.RootGridID, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 5, Y: 5,
	})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("expected ErrPermissionDenied, got %v", err)
	}
}
