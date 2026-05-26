package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// createURLTileForTest creates a uri-list tile and returns it.
func createURLTileForTest(t *testing.T, s *Store, root, x int64, url string) *rpc.Tile {
	t.Helper()
	tile, err := s.CreateFile(context.Background(), &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root,
		X: x, Y: 0, W: 1, H: 1,
		MimeType: rpc.MimeURIList, Data: []byte(url),
	})
	if err != nil {
		t.Fatalf("create URL tile: %v", err)
	}
	return tile
}

func TestForkURL(t *testing.T) {
	s := newTestStore(t)
	s.SetURLDriver(NewFakeURLDriver())
	root := rootID(t, s)
	ctx := context.Background()
	src := createURLTileForTest(t, s, root, 0, "https://example.com/a")
	// Seed a preview so we can verify it carries over.
	if err := s.SetURLPreview(ctx, src.ID, []byte("jpegbytes")); err != nil {
		t.Fatalf("seed preview: %v", err)
	}

	fork, err := s.ForkURL(ctx, &rpc.ForkURLRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: src.ID,
		DestGridID: root, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 2, Y: 0,
	})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if !fork.IsURL() {
		t.Errorf("fork is not a URL tile: %+v", fork)
	}
	if fork.URLString != src.URLString {
		t.Errorf("fork URLString = %q, want %q", fork.URLString, src.URLString)
	}
	if fork.ID == src.ID {
		t.Error("fork has same row id as source")
	}
	if fork.ObjectID == src.ObjectID {
		t.Error("fork shares object_id with source (forks should be distinct identities)")
	}
	// Preview bytes should have copied.
	jpeg, err := s.GetTilePreview(ctx, fork.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(jpeg) != "jpegbytes" {
		t.Errorf("fork preview = %q, want \"jpegbytes\"", string(jpeg))
	}
}

func TestForkURLRefusesNonURLTile(t *testing.T) {
	s := newTestStore(t)
	s.SetURLDriver(NewFakeURLDriver())
	root := rootID(t, s)
	w, err := s.CreateWell(context.Background(), &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root,
		X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ForkURL(context.Background(), &rpc.ForkURLRequest{
		Path: rpc.Path{}, ViewRect: largeView(), TileID: w.ID,
		DestGridID: root, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 2, Y: 0,
	})
	if !errors.Is(err, ErrNotURLTile) {
		t.Errorf("got %v, want ErrNotURLTile", err)
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
}

func TestSetURLPreviewRefusesNonURLTile(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	w, err := s.CreateWell(context.Background(), &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root,
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
