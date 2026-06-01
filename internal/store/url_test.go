package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// createURLTileForTest creates a URL tile and returns it.
func createURLTileForTest(t *testing.T, s *Store, root, x int64, url string) *rpc.Tile {
	t.Helper()
	tile, err := s.CreateURL(context.Background(), &rpc.CreateURLRequest{
		Path: rpc.Path{}, GridID: root,
		X: x, Y: 0, W: 1, H: 1, URL: url,
	})
	if err != nil {
		t.Fatalf("create URL tile: %v", err)
	}
	return tile
}

// TestCloneURLTile verifies that CloneTile of a URL tile carries the URL
// and preview JPEG, and that the clone shares the source's object_id.
func TestCloneURLTile(t *testing.T) {
	s := newTestStore(t)
	s.SetURLDriver(NewFakeURLDriver())
	root := rootID(t, s)
	ctx := context.Background()
	src := createURLTileForTest(t, s, root, 0, "https://example.com/a")
	// Seed a preview so we can verify it carries over.
	if err := s.SetURLPreview(ctx, src.ID, []byte("jpegbytes")); err != nil {
		t.Fatalf("seed preview: %v", err)
	}
	// SetURLPreview bumped version; reload.
	src, err := s.loadTile(ctx, s.db, src.ID)
	if err != nil {
		t.Fatal(err)
	}

	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: src.ID, Version: src.Version,
		DestGridID: root, DestPath: rpc.Path{},
		X: 2, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if clone.Kind != rpc.KindURL {
		t.Errorf("clone kind = %q, want %q", clone.Kind, rpc.KindURL)
	}
	if clone.URLString != src.URLString {
		t.Errorf("clone URLString = %q, want %q", clone.URLString, src.URLString)
	}
	if clone.ID == src.ID {
		t.Error("clone has same row id as source")
	}
	if clone.ObjectID != src.ObjectID {
		t.Errorf("clone object_id = %q, want %q (shared identity)", clone.ObjectID, src.ObjectID)
	}
	if clone.Version != src.Version {
		t.Errorf("clone version = %d, want %d (shared until divergence)", clone.Version, src.Version)
	}
	// Preview bytes should have copied.
	jpeg, err := s.GetTilePreview(ctx, clone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(jpeg) != "jpegbytes" {
		t.Errorf("clone preview = %q, want \"jpegbytes\"", string(jpeg))
	}
}

func TestSetURLString(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile := createURLTileForTest(t, s, root, 0, "https://example.com/a")

	if err := s.SetURLString(ctx, tile.ID, "https://example.com/b"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.loadTile(ctx, s.db, tile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.URLString != "https://example.com/b" {
		t.Errorf("URLString = %q, want https://example.com/b", got.URLString)
	}
	if got.Version != tile.Version+1 {
		t.Errorf("version after SetURLString = %d, want %d", got.Version, tile.Version+1)
	}
}

func TestSetURLPreview(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile := createURLTileForTest(t, s, root, 0, "https://example.com")

	if err := s.SetURLPreview(ctx, tile.ID, []byte("xxx")); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.GetTilePreview(ctx, tile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "xxx" {
		t.Errorf("got %q, want xxx", string(got))
	}
	tileAfter, err := s.loadTile(ctx, s.db, tile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tileAfter.Version != tile.Version+1 {
		t.Errorf("version after SetURLPreview = %d, want %d", tileAfter.Version, tile.Version+1)
	}
}

func TestSetTileAlt(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile := createURLTileForTest(t, s, root, 0, "https://example.com")

	if err := s.SetTileAlt(ctx, tile.ID, "Example Title"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.loadTile(ctx, s.db, tile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AltText != "Example Title" {
		t.Errorf("AltText = %q, want %q", got.AltText, "Example Title")
	}
	if got.Version != tile.Version+1 {
		t.Errorf("version after SetTileAlt = %d, want %d", got.Version, tile.Version+1)
	}
	// Setting back to empty clears the column.
	if err := s.SetTileAlt(ctx, tile.ID, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err = s.loadTile(ctx, s.db, tile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AltText != "" {
		t.Errorf("AltText after clear = %q, want empty", got.AltText)
	}
}

func TestFakeURLDriverAvailable(t *testing.T) {
	d := NewFakeURLDriver()
	if !d.Available() {
		t.Error("fresh FakeURLDriver should be available")
	}
	d.SetAvailable(false)
	if d.Available() {
		t.Error("after SetAvailable(false), Available() should report false")
	}
	d.SetAvailable(true)
	if !d.Available() {
		t.Error("after SetAvailable(true), Available() should report true")
	}
}

func TestSetURLPreviewRefusesNonURLTile(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	w, err := s.CreateWell(context.Background(), &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root,
		X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.SetURLPreview(context.Background(), w.ID, []byte("x"))
	if !errors.Is(err, ErrNotURLTile) {
		t.Errorf("got %v, want ErrNotURLTile", err)
	}
}
