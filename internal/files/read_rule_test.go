package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/local/store/storetest"
)

// readEveryVerb runs every read the files serve over dir: the listing, each
// tile, its preview, its bytes or its page, a door's grid, Info and Find.
func readEveryVerb(t *testing.T, s *Source, dir string) {
	t.Helper()
	ctx := context.Background()
	g, err := s.GetGrid(ctx, &pb.GetGridRequest{GridId: idOf(dir)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range g.Tiles {
		if _, err := s.GetTile(ctx, &pb.GetTileRequest{TileId: tl.Id}); err != nil {
			t.Fatalf("GetTile %s: %v", tl.AltText, err)
		}
		if _, err := s.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: tl.Id}); err != nil {
			t.Fatalf("GetTilePreview %s: %v", tl.AltText, err)
		}
		switch {
		case tl.Kind == rpc.KindDoor:
			if _, err := s.GetGrid(ctx, &pb.GetGridRequest{GridId: tl.ChildGridId}); err != nil {
				t.Fatalf("GetGrid %s: %v", tl.AltText, err)
			}
		case tl.ServesPage:
			if err := s.ServeContent(ctx, &pb.ServeContentRequest{TileId: tl.Id}, func(*pb.ServeContentChunk) error { return nil }); err != nil {
				t.Fatalf("ServeContent %s: %v", tl.AltText, err)
			}
		default:
			if err := s.ReadContent(ctx, &pb.ReadContentRequest{TileId: tl.Id}, func(*pb.ContentChunk) error { return nil }); err != nil {
				t.Fatalf("ReadContent %s: %v", tl.AltText, err)
			}
		}
		if _, err := s.Find(ctx, &pb.FindRequest{Query: "id:" + tl.Id}); err != nil {
			t.Fatalf("Find %s: %v", tl.AltText, err)
		}
	}
	if _, err := s.Info(ctx, &pb.InfoRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Find(ctx, &pb.FindRequest{GridId: idOf(dir), Recurse: true, Kind: rpc.KindText}); err != nil {
		t.Fatal(err)
	}
}

// Reading never mutates: every read of a tree nobody touched leaves the
// store as it was, and once a touch minted rows, every read leaves them as
// the touch left them.
func TestReadingTheFilesWritesNothing(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	for name, body := range map[string]string{"notes.md": "# notes", "main.go": "package main", "pic.png": "\x89PNG", "sub/deep.md": "deep"} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dbPath := filepath.Join(base, "gridwell.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s, err := New(st.Files(), []Root{{Path: root}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	db := storetest.Reader(t, dbPath)

	before := storetest.Snapshot(t, db)
	readEveryVerb(t, s, root)
	if after := storetest.Snapshot(t, db); after != before {
		t.Errorf("reading an untouched tree wrote:\n%s", storetest.Diff(before, after))
	}

	ctx := context.Background()
	notes := filepath.Join(root, "notes.md")
	nt, err := s.GetTile(ctx, &pb.GetTileRequest{TileId: idOf(notes)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveTile(ctx, &pb.MoveTileRequest{TileId: nt.Tile.Id, X: 7, Y: 3, W: 2, H: 2, Claim: rpc.PlacementOf(nt.Tile)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFraming(ctx, &pb.SetFramingRequest{TileId: idOf(filepath.Join(root, "sub")), Cx: 2, Cy: 3, Zoom: 0.75}); err != nil {
		t.Fatal(err)
	}
	touched := storetest.Snapshot(t, db)
	if touched == before {
		t.Fatal("the touch minted nothing, so the case below proves nothing")
	}
	readEveryVerb(t, s, root)
	if after := storetest.Snapshot(t, db); after != touched {
		t.Errorf("reading a touched tree rewrote it:\n%s", storetest.Diff(touched, after))
	}
}

// A door's grid is named by its path before and after a touch mints its
// rows, and GetGrid answers it under the id asked.
func TestATouchedDoorsGridKeepsItsName(t *testing.T) {
	f := newFixture(t)
	sub := f.mkdir("sub")
	before := tileIn(t, f.grid(f.root), sub)
	if _, err := f.s.MoveTile(context.Background(), &pb.MoveTileRequest{TileId: before.Id, X: 5, Y: 2, W: 1, H: 1, Claim: rpc.PlacementOf(before)}); err != nil {
		t.Fatal(err)
	}
	after := tileIn(t, f.grid(f.root), sub)
	if after.ChildGridId != before.ChildGridId || after.Id != before.Id {
		t.Fatalf("the touch renamed the door: %s/%s, was %s/%s", after.Id, after.ChildGridId, before.Id, before.ChildGridId)
	}
	if g := f.grid(sub); g.Grid.Id != after.ChildGridId {
		t.Errorf("GetGrid(%s) answered %s", after.ChildGridId, g.Grid.Id)
	}
}
