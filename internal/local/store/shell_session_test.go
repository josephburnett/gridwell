package store

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A clone names its source's session, a clone of a clone the same one, and a
// copy inside a cloned well too: every copy is one more viewer of one shell.
func TestCloneSharesTheShellSession(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	src, err := s.CreateShell(ctx, root, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if src.ShellSession != "" {
		t.Fatalf("a fresh shell names %q, want its own id (empty)", src.ShellSession)
	}
	b, err := s.CloneTile(ctx, &gridwellv1.CloneTileRequest{TileId: src.Id, DestGridId: root, X: 2})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CloneTile(ctx, &gridwellv1.CloneTileRequest{TileId: b.Id, DestGridId: root, X: 4})
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]*gridwellv1.Tile{"clone": b, "clone of a clone": c} {
		if rpc.ShellSession(got) != src.Id {
			t.Errorf("%s names session %q, want the source's %q", name, rpc.ShellSession(got), src.Id)
		}
	}

	w, err := s.CreateWell(ctx, root, 6, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateShell(ctx, w.ChildGridId, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	wc, err := s.CloneTile(ctx, &gridwellv1.CloneTileRequest{TileId: w.Id, DestGridId: root, X: 8})
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.GetGrid(ctx, wc.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) != 1 || rpc.ShellSession(g.Tiles[0]) != inner.Id {
		t.Fatalf("the shell inside a cloned well = %v, want one naming %q", g.Tiles, inner.Id)
	}
}

// The count of rows naming a session covers the source and every copy, a
// trashed one included, and only a destroy takes a row out of it.
func TestShellSessionNamersCountsEveryCopy(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	src, err := s.CreateShell(ctx, root, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CloneTile(ctx, &gridwellv1.CloneTileRequest{TileId: src.Id, DestGridId: root, X: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := func(stage string, rows, faced int64) {
		t.Helper()
		got, err := s.ShellSessionNamers(ctx, src.Id)
		if err != nil {
			t.Fatal(err)
		}
		if got != (SessionNamers{Rows: rows, Faced: faced}) {
			t.Errorf("%s: namers = %+v, want {Rows:%d Faced:%d}", stage, got, rows, faced)
		}
	}
	want("source and clone", 2, 0)
	if _, err := s.SetShellPreview(ctx, b.Id, []byte("face")); err != nil {
		t.Fatal(err)
	}
	want("the clone froze a face", 2, 1)
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: src.Id}); err != nil {
		t.Fatal(err)
	}
	want("the source in the trash still names it", 2, 1)
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: src.Id}); err != nil {
		t.Fatal(err)
	}
	want("the source destroyed, the clone remains", 1, 1)
	hardDelete(t, s, b.Id)
	want("the last row destroyed", 0, 0)
}
