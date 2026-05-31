package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

func TestCreateWellHappyPath(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path:   rpc.Path{},
		GridID: root, X: 1, Y: 2, W: 3, H: 4,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if w.Kind != rpc.KindWell {
		t.Errorf("kind=%q", w.Kind)
	}
	if w.X != 1 || w.Y != 2 || w.W != 3 || w.H != 4 {
		t.Errorf("dims wrong: %+v", w)
	}
	if w.ChildGridID == 0 {
		t.Error("no child grid")
	}
	if w.Version != 0 {
		t.Errorf("new tile version = %d, want 0", w.Version)
	}
	// Read the parent grid back.
	g, err := s.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) != 1 {
		t.Errorf("expected 1 tile, got %d", len(g.Tiles))
	}
}

func TestCreateWellOverlapRefused(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	if _, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 5, H: 5,
	}); err != nil {
		t.Fatal(err)
	}
	// Overlap.
	_, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 4, Y: 4, W: 2, H: 2,
	})
	if !errors.Is(err, ErrOverlap) {
		t.Errorf("got %v, want ErrOverlap", err)
	}
	// Adjacent (touching) is OK.
	if _, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 5, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Errorf("adjacent placement refused: %v", err)
	}
}

func TestCreateWellInvalidArgs(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	_, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 0, H: 1,
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("zero w: got %v", err)
	}
	_, err = s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: -1,
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("neg h: got %v", err)
	}
}

func TestDescentPathThenCreate(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Descend into well; create a sub-well inside it.
	sub, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: []int64{w.ID}},
		GridID: w.ChildGridID, X: 0, Y: 0, W: 2, H: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sub.GridID != w.ChildGridID {
		t.Errorf("sub.GridID = %d, want %d", sub.GridID, w.ChildGridID)
	}

	// Path with a non-existent well should fail.
	_, err = s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: []int64{9999}},
		GridID: w.ChildGridID, X: 1, Y: 1, W: 1, H: 1,
	})
	if !errors.Is(err, ErrInvalidPath) {
		t.Errorf("got %v, want ErrInvalidPath", err)
	}
}

func TestCreateTextSize(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	huge := make([]byte, MaxBlobBytes+1)
	_, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root,
		X: 0, Y: 0, W: 1, H: 1, Data: huge,
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("oversized: got %v", err)
	}
}

func TestCreateURLTile(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile, err := s.CreateURL(ctx, &rpc.CreateURLRequest{
		Path: rpc.Path{}, GridID: root,
		X: 0, Y: 0, W: 1, H: 1,
		URL: "https://example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tile.Kind != rpc.KindURL {
		t.Errorf("kind = %q, want %q", tile.Kind, rpc.KindURL)
	}
	if tile.URLString != "https://example.com" {
		t.Errorf("URLString = %q, want https://example.com", tile.URLString)
	}
	if tile.BlobID != 0 {
		t.Errorf("URL tile got BlobID = %d, want 0", tile.BlobID)
	}
	// Whitespace around the URL should be trimmed.
	tile2, err := s.CreateURL(ctx, &rpc.CreateURLRequest{
		Path: rpc.Path{}, GridID: root,
		X: 1, Y: 0, W: 1, H: 1,
		URL: "  https://example.org/path\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tile2.URLString != "https://example.org/path" {
		t.Errorf("URLString = %q (whitespace not trimmed)", tile2.URLString)
	}
	// Disallowed scheme → ErrInvalidArgument.
	_, err = s.CreateURL(ctx, &rpc.CreateURLRequest{
		Path: rpc.Path{}, GridID: root,
		X: 2, Y: 0, W: 1, H: 1,
		URL: "javascript:alert(1)",
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("javascript: scheme: got %v, want ErrInvalidArgument", err)
	}
	// GetTilePreview on a brand-new URL tile returns nil.
	jpeg, err := s.GetTilePreview(ctx, tile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(jpeg) != 0 {
		t.Errorf("new URL tile preview = %d bytes, want 0", len(jpeg))
	}
}

func TestCreateTextBlobReuse(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	data := []byte("hello world")
	a, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root,
		X: 0, Y: 0, W: 1, H: 1, Data: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root,
		X: 5, Y: 0, W: 1, H: 1, Data: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.BlobID != b.BlobID {
		t.Errorf("blob ids = %d, %d (want same)", a.BlobID, b.BlobID)
	}
	// Refcount should be 2.
	if rc := refcount(t, s, "blobs", a.BlobID); rc != 2 {
		t.Errorf("blob refcount = %d, want 2", rc)
	}
}

func TestResizeNode(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.ResizeTile(ctx, &rpc.ResizeTileRequest{
		Path: rpc.Path{}, TileID: w.ID, Version: w.Version, W: 3, H: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.W != 3 || r.H != 4 {
		t.Errorf("after resize %+v", r)
	}
	if r.Version != w.Version+1 {
		t.Errorf("version after resize = %d, want %d", r.Version, w.Version+1)
	}
	// Resize to overlap another tile should fail.
	_, err = s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 4, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ResizeTile(ctx, &rpc.ResizeTileRequest{
		Path: rpc.Path{}, TileID: r.ID, Version: r.Version, W: 5, H: 4,
	})
	if !errors.Is(err, ErrOverlap) {
		t.Errorf("expected ErrOverlap, got %v", err)
	}
}

func TestResizeVersionConflict(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ResizeTile(ctx, &rpc.ResizeTileRequest{
		Path: rpc.Path{}, TileID: w.ID, Version: w.Version + 99, W: 2, H: 2,
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Errorf("got %v, want ErrVersionConflict", err)
	}
}

func TestSetWellView(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SetWellView(ctx, &rpc.SetWellViewRequest{
		Path: rpc.Path{}, TileID: w.ID, Version: w.Version,
		ViewX: 5, ViewY: 7, ViewZoom: 1.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ViewX != 5 || got.ViewY != 7 || got.ViewZoom != 1.5 {
		t.Errorf("view %+v", got)
	}
	if got.Version != w.Version+1 {
		t.Errorf("version after set = %d, want %d", got.Version, w.Version+1)
	}
}

// TestSetTextViewPersistsWindowAndMode pins the text-tile preview-frame
// contract: SetTextView persists text_x/y/w/h and text_mode, and GetGrid
// reads them back.
func TestSetTextViewPersistsWindowAndMode(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	f, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root,
		X: 0, Y: 0, W: 2, H: 2, Data: []byte("hello"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SetTextView(ctx, &rpc.SetTextViewRequest{
		Path: rpc.Path{}, TileID: f.ID, Version: f.Version,
		TextX: 10, TextY: 20, TextW: 640, TextH: 480, TextMode: rpc.TextModeRendered,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.TextX != 10 || got.TextY != 20 || got.TextW != 640 || got.TextH != 480 || got.TextMode != rpc.TextModeRendered {
		t.Errorf("after write: %+v", got)
	}
	// Readback through GetGrid must see the same values.
	gg, err := s.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	var found *rpc.Tile
	for i := range gg.Tiles {
		if gg.Tiles[i].ID == f.ID {
			found = &gg.Tiles[i]
			break
		}
	}
	if found == nil {
		t.Fatal("tile missing from GetGrid")
	}
	if found.TextW != 640 || found.TextH != 480 || found.TextMode != rpc.TextModeRendered {
		t.Errorf("GetGrid round-trip: %+v", found)
	}
}

func TestSetWellViewRejectsNonWell(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	f, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root,
		X: 0, Y: 0, W: 1, H: 1, Data: []byte("x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SetWellView(ctx, &rpc.SetWellViewRequest{
		Path: rpc.Path{}, TileID: f.ID, Version: f.Version, ViewX: 1, ViewY: 1, ViewZoom: 1,
	})
	if !errors.Is(err, ErrNotWellTile) {
		t.Errorf("got %v, want ErrNotWellTile", err)
	}
}

func TestSetTextViewRejectsNonText(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SetTextView(ctx, &rpc.SetTextViewRequest{
		Path: rpc.Path{}, TileID: w.ID, Version: w.Version,
	})
	if !errors.Is(err, ErrNotTextTile) {
		t.Errorf("got %v, want ErrNotTextTile", err)
	}
}

func TestDeleteTile(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	childGridID := w.ChildGridID
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{Path: rpc.Path{}, TileID: w.ID, Version: w.Version}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.loadTile(ctx, s.db, w.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("well still exists: %v", err)
	}
	if _, err := s.loadGrid(ctx, s.db, childGridID); !errors.Is(err, ErrNotFound) {
		t.Errorf("child grid still exists: %v", err)
	}
}

func TestDeleteTileCascadesNonEmptyWell(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: []int64{w.ID}},
		GridID: w.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// w may have been bumped by the creation of inner in its child grid?
	// No: creating inner bumps the inner-grid version, not the outer well's.
	// Reload w for current version.
	wCur, err := s.loadTile(ctx, s.db, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{Path: rpc.Path{}, TileID: w.ID, Version: wCur.Version}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.loadTile(ctx, s.db, inner.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("inner well still exists after cascading delete")
	}
}

func TestDeleteTileVersionConflict(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.DeleteTile(ctx, &rpc.DeleteTileRequest{Path: rpc.Path{}, TileID: w.ID, Version: w.Version + 1})
	if !errors.Is(err, ErrVersionConflict) {
		t.Errorf("got %v, want ErrVersionConflict", err)
	}
}

func TestCreateBlackHole(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	bh, err := s.CreateBlackHole(ctx, &rpc.CreateBlackHoleRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bh.Kind != rpc.KindBlackHole {
		t.Errorf("kind = %q", bh.Kind)
	}
}
