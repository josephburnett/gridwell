package store

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A copy names its source's tmux, a copy of a copy the same one, and a
// copy inside a copied door too: every copy is one more viewer of one shell.
func TestCopySharesTheShellTmux(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	src, err := s.CreateShell(ctx, root, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if src.ShellTmux != "" {
		t.Fatalf("a fresh shell names %q, want its own id (empty)", src.ShellTmux)
	}
	b, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{TileId: src.Id, DestGridId: root, X: 2})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{TileId: b.Id, DestGridId: root, X: 4})
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]*gridwellv1.Tile{"copy": b, "copy of a copy": c} {
		if rpc.ShellTmux(got) != src.Id {
			t.Errorf("%s names tmux %q, want the source's %q", name, rpc.ShellTmux(got), src.Id)
		}
	}

	w, err := s.CreateDoor(ctx, root, 6, 0, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateShell(ctx, w.ChildGridId, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	wc, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{TileId: w.Id, DestGridId: root, X: 8})
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.GetGrid(ctx, wc.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) != 1 || rpc.ShellTmux(g.Tiles[0]) != inner.Id {
		t.Fatalf("the shell inside a copied door = %v, want one naming %q", g.Tiles, inner.Id)
	}
}

// The count of rows naming a tmux covers the source and every copy, a
// trashed one included, and only a destroy takes a row out of it.
func TestShellTmuxNamersCountsEveryCopy(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	src, err := s.CreateShell(ctx, root, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{TileId: src.Id, DestGridId: root, X: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := func(stage string, rows, previewed int64) {
		t.Helper()
		got, err := s.ShellTmuxNamers(ctx, src.Id)
		if err != nil {
			t.Fatal(err)
		}
		if got != (TmuxNamers{Rows: rows, Previewed: previewed}) {
			t.Errorf("%s: namers = %+v, want {Rows:%d Previewed:%d}", stage, got, rows, previewed)
		}
	}
	want("source and copy", 2, 0)
	if _, err := s.SetShellPreview(ctx, b.Id, []byte("preview")); err != nil {
		t.Fatal(err)
	}
	want("the copy froze a preview", 2, 1)
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: src.Id}); err != nil {
		t.Fatal(err)
	}
	want("the source in the trash still names it", 2, 1)
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: src.Id}); err != nil {
		t.Fatal(err)
	}
	want("the source destroyed, the copy remains", 1, 1)
	hardDelete(t, s, b.Id)
	want("the last row destroyed", 0, 0)
}
