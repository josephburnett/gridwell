package server_test

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A url copied onto a far node keeps the name the copier saw, the page's or a
// typed one, and whether it was typed: a page-title capture on the copy
// replaces a name that followed the page and never a typed one (CLAUDE.md,
// 2026-10-20).
func TestAURLCopiedAcrossNodesKeepsItsName(t *testing.T) {
	cl, near, far := newTwoHomes(t, nil)
	ctx := context.Background()
	create := func(x int64, name string) *gridwellv1.Tile {
		t.Helper()
		u, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: near,
			Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: x, Y: 0, W: 1, H: 1, UrlString: "https://example.com/" + name}})
		if err != nil {
			t.Fatal(err)
		}
		u, err = cl.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: u.Id, Tile: &gridwellv1.Tile{Kind: rpc.KindURL, AltText: name}})
		if err != nil {
			t.Fatalf("capture %q: %v", name, err)
		}
		return u
	}
	src, plain := create(0, "Source A"), create(1, "Source B")
	renamed, err := cl.RenameTile(ctx, src.Id, src.Version, "mine")
	if err != nil {
		t.Fatalf("rename the url: %v", err)
	}

	for i, c := range []struct {
		from       *gridwellv1.Tile
		name, want string
	}{
		{renamed, "mine", "mine"},
		{plain, "Source B", "Page title"},
	} {
		copied, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{TileId: c.from.Id, DestGridId: far, X: int64(4 + i), Y: 4})
		if err != nil {
			t.Fatalf("copy %q across: %v", c.name, err)
		}
		if copied.AltText != c.name {
			t.Fatalf("the copy is named %q, want %q, the name it was copied with", copied.AltText, c.name)
		}
		if _, err := cl.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: copied.Id,
			Tile: &gridwellv1.Tile{Kind: rpc.KindURL, AltText: "Page title"}}); err != nil {
			t.Fatalf("capture the copy's page title: %v", err)
		}
		got, err := cl.GetTile(ctx, copied.Id)
		if err != nil {
			t.Fatal(err)
		}
		if got.AltText != c.want {
			t.Errorf("a copy of %q after a page-title capture is named %q, want %q", c.name, got.AltText, c.want)
		}
	}
}

// Every kind a copy carries into another namespace keeps the name its source
// had, and whether it was typed: a url and a door as copies, a shell and a
// link as links.
func TestEveryKindCopiedAcrossNamespacesKeepsItsName(t *testing.T) {
	cl, rootB, rootA := newTwoHomes(t, nil)
	ctx := context.Background()
	create := func(tl *gridwellv1.Tile) *gridwellv1.Tile {
		t.Helper()
		created, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootA, Tile: tl})
		if err != nil {
			t.Fatalf("create %s: %v", tl.Kind, err)
		}
		return created
	}
	named := func(tl *gridwellv1.Tile, name string) *gridwellv1.Tile {
		t.Helper()
		renamed, err := cl.RenameTile(ctx, tl.Id, tl.Version, name)
		if err != nil {
			t.Fatalf("rename %s: %v", tl.Kind, err)
		}
		return renamed
	}
	target := create(&gridwellv1.Tile{Kind: rpc.KindText, X: 9, Y: 9, W: 1, H: 1})
	for i, src := range []*gridwellv1.Tile{
		named(create(&gridwellv1.Tile{Kind: rpc.KindURL, X: 0, Y: 0, W: 1, H: 1, UrlString: "https://example.com/"}), "url name"),
		named(create(&gridwellv1.Tile{Kind: rpc.KindShell, X: 1, Y: 0, W: 1, H: 1}), "shell name"),
		create(&gridwellv1.Tile{Kind: rpc.KindDoor, X: 2, Y: 0, W: 1, H: 1, AltText: "door name"}),
		create(&gridwellv1.Tile{Kind: rpc.KindText, X: 3, Y: 0, W: 1, H: 1, LinkTargetId: target.Id, AltText: "link name"}),
	} {
		copied, err := cl.CopyTile(ctx, &gridwellv1.CopyTileRequest{TileId: src.Id, DestGridId: rootB, X: int64(i), Y: 5})
		if err != nil {
			t.Fatalf("copy %s %q: %v", src.Kind, src.AltText, err)
		}
		if copied.AltText != src.AltText || copied.Named != src.Named {
			t.Errorf("a %s named %q (typed %v) copied across namespaces is named %q (typed %v)",
				src.Kind, src.AltText, src.Named, copied.AltText, copied.Named)
		}
	}
}
