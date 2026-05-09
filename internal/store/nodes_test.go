package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/ascent/internal/rpc"
)

// fixtureUser creates a user "alice" and returns the user.
func fixtureUser(t *testing.T, s *Store) *User {
	t.Helper()
	u, err := s.CreateUser(context.Background(), "alice", "p")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

// largeView returns a viewrect that contains anything reasonable.
func largeView() rpc.ViewRect { return rpc.ViewRect{X: -1000, Y: -1000, W: 2000, H: 2000} }

func TestCreateWellHappyPath(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(),
		GridID: u.RootGridID, X: 1, Y: 2, W: 3, H: 4,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if w.Type != "well" {
		t.Errorf("type=%q", w.Type)
	}
	if w.X != 1 || w.Y != 2 || w.W != 3 || w.H != 4 {
		t.Errorf("dims wrong: %+v", w)
	}
	if w.ChildGridID == 0 {
		t.Error("no child grid")
	}
	// Read the parent grid back.
	g, err := s.GetGrid(ctx, u.ID, u.RootGridID)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(g.Nodes))
	}
}

func TestCreateWellOverlapRefused(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	if _, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 5, H: 5,
	}); err != nil {
		t.Fatal(err)
	}
	// Overlap.
	_, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 4, Y: 4, W: 2, H: 2,
	})
	if !errors.Is(err, ErrOverlap) {
		t.Errorf("got %v, want ErrOverlap", err)
	}
	// Adjacent (touching) is OK.
	if _, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 5, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Errorf("adjacent placement refused: %v", err)
	}
}

func TestCreateWellLocalityEnforced(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	// Footprint outside the view rect.
	_, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: rpc.ViewRect{X: 0, Y: 0, W: 5, H: 5},
		GridID: u.RootGridID, X: 10, Y: 10, W: 1, H: 1,
	})
	if !errors.Is(err, ErrLocality) {
		t.Errorf("got %v, want ErrLocality", err)
	}
}

func TestCreateWellInvalidArgs(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	_, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 0, H: 1,
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("zero w: got %v", err)
	}
	_, err = s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: -1,
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("neg h: got %v", err)
	}
}

func TestDescentPathThenCreate(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Descend into well; create a sub-well inside it.
	sub, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{WellIDs: []int64{w.ID}}, ViewRect: largeView(),
		GridID: w.ChildGridID, X: 0, Y: 0, W: 2, H: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sub.GridID != w.ChildGridID {
		t.Errorf("sub.GridID = %d, want %d", sub.GridID, w.ChildGridID)
	}

	// Path with a non-existent well should fail.
	_, err = s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{WellIDs: []int64{9999}}, ViewRect: largeView(),
		GridID: w.ChildGridID, X: 1, Y: 1, W: 1, H: 1,
	})
	if !errors.Is(err, ErrInvalidPath) {
		t.Errorf("got %v, want ErrInvalidPath", err)
	}
}

func TestCreateFileMimeAndSize(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	_, err := s.CreateFile(ctx, u.ID, &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID,
		X: 0, Y: 0, W: 1, H: 1, MimeType: "application/zip", Data: []byte("x"),
	})
	if !errors.Is(err, ErrUnsupportedMime) {
		t.Errorf("bad mime: got %v", err)
	}
	huge := make([]byte, MaxBlobBytes+1)
	_, err = s.CreateFile(ctx, u.ID, &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID,
		X: 0, Y: 0, W: 1, H: 1, MimeType: "text/markdown", Data: huge,
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("oversized: got %v", err)
	}
}

func TestCreateFileBlobReuse(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	data := []byte("hello world")
	a, err := s.CreateFile(ctx, u.ID, &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID,
		X: 0, Y: 0, W: 1, H: 1, MimeType: "text/markdown", Data: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateFile(ctx, u.ID, &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID,
		X: 5, Y: 0, W: 1, H: 1, MimeType: "text/markdown", Data: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.BlobID != b.BlobID {
		t.Errorf("blob ids = %d, %d (want same)", a.BlobID, b.BlobID)
	}
	// Refcount should be 2.
	var rc int64
	if err := s.db.QueryRow(`SELECT refcount FROM blobs WHERE id = ?`, a.BlobID).Scan(&rc); err != nil {
		t.Fatal(err)
	}
	if rc != 2 {
		t.Errorf("blob refcount = %d, want 2", rc)
	}
}

func TestResizeNode(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.ResizeNode(ctx, u.ID, &rpc.ResizeNodeRequest{
		Path: rpc.Path{}, ViewRect: largeView(), NodeID: w.ID, W: 3, H: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.W != 3 || r.H != 4 {
		t.Errorf("after resize %+v", r)
	}
	// Resize to overlap another node should fail.
	_, err = s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 4, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ResizeNode(ctx, u.ID, &rpc.ResizeNodeRequest{
		Path: rpc.Path{}, ViewRect: largeView(), NodeID: r.ID, W: 5, H: 4,
	})
	if !errors.Is(err, ErrOverlap) {
		t.Errorf("expected ErrOverlap, got %v", err)
	}
}

func TestSetNodeViewport(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SetNodeViewport(ctx, u.ID, &rpc.SetNodeViewportRequest{
		Path: rpc.Path{}, ViewRect: largeView(), NodeID: w.ID, ViewX: 5, ViewY: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ViewX != 5 || got.ViewY != 7 {
		t.Errorf("viewport %+v", got)
	}
}

func TestCapRedig(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CapWell(ctx, u.ID, &rpc.CapWellRequest{Path: rpc.Path{}, ViewRect: largeView(), NodeID: w.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Capped {
		t.Error("expected capped=true")
	}
	// Already capped.
	if _, err := s.CapWell(ctx, u.ID, &rpc.CapWellRequest{Path: rpc.Path{}, ViewRect: largeView(), NodeID: w.ID}); !errors.Is(err, ErrCapped) {
		t.Errorf("expected ErrCapped, got %v", err)
	}
	r, err := s.RedigWell(ctx, u.ID, &rpc.RedigWellRequest{Path: rpc.Path{}, ViewRect: largeView(), NodeID: w.ID})
	if err != nil {
		t.Fatal(err)
	}
	if r.Capped {
		t.Error("expected capped=false")
	}
	if _, err := s.RedigWell(ctx, u.ID, &rpc.RedigWellRequest{Path: rpc.Path{}, ViewRect: largeView(), NodeID: w.ID}); !errors.Is(err, ErrNotCapped) {
		t.Errorf("expected ErrNotCapped, got %v", err)
	}
}

func TestFillEmptyWell(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	childGridID := w.ChildGridID
	if err := s.FillWell(ctx, u.ID, &rpc.FillWellRequest{Path: rpc.Path{}, ViewRect: largeView(), NodeID: w.ID}); err != nil {
		t.Fatalf("fill: %v", err)
	}
	// Well row is gone.
	if _, err := s.loadNode(ctx, s.db, w.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("well still exists: %v", err)
	}
	// Child grid is gone.
	if _, err := s.loadGrid(ctx, s.db, childGridID); !errors.Is(err, ErrNotFound) {
		t.Errorf("child grid still exists: %v", err)
	}
}

func TestFillNonEmptyWellRefused(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{WellIDs: []int64{w.ID}}, ViewRect: largeView(),
		GridID: w.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.FillWell(ctx, u.ID, &rpc.FillWellRequest{Path: rpc.Path{}, ViewRect: largeView(), NodeID: w.ID}); !errors.Is(err, ErrNotEmpty) {
		t.Errorf("expected ErrNotEmpty, got %v", err)
	}
}

func TestAscendAtRoot(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	oldRoot := u.RootGridID
	resp, err := s.AscendAtRoot(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.NewRootGridID == 0 || resp.NewRootGridID == oldRoot {
		t.Errorf("new root not set: %+v", resp)
	}
	got, err := s.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RootGridID != resp.NewRootGridID {
		t.Errorf("user root = %d, expected %d", got.RootGridID, resp.NewRootGridID)
	}
	// Verify the well exists in the new root pointing at the old root.
	well, err := s.loadNode(ctx, s.db, resp.WellID)
	if err != nil {
		t.Fatal(err)
	}
	if well.ChildGridID != oldRoot {
		t.Errorf("well child=%d, want %d", well.ChildGridID, oldRoot)
	}
	// Old root's refcount: was 1 (root +1). Now: 0 (no longer root) + 1 (well points). Net 1.
	var rc int64
	if err := s.db.QueryRow(`SELECT refcount FROM grids WHERE id = ?`, oldRoot).Scan(&rc); err != nil {
		t.Fatal(err)
	}
	if rc != 1 {
		t.Errorf("old root refcount = %d, want 1", rc)
	}
}
