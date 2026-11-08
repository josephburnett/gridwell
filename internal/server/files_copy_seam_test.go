package server_test

import (
	"os"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A left-drag of a directory into home is a link door holding the address
// dragged, opening the very grid the directory does, and named as it is.
func TestFilesDirectoryLeftDraggedIntoHomeLinks(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	writeFile(t, filepath.Join(sub, "c.txt"), "c")
	n := newFilesNode(t, root)
	dir := n.tile(n.roots[0], sub)
	link, err := n.cl.CreateTile(n.ctx, &gridwellv1.CreateTileRequest{GridId: n.home, Tile: &gridwellv1.Tile{
		Kind: rpc.KindDoor, X: 1, Y: 1, W: dir.W, H: dir.H, ChildGridId: dir.ChildGridId, AltText: dir.AltText}})
	if err != nil {
		t.Fatal(err)
	}
	if !link.Link || link.ChildGridId != dir.ChildGridId || link.AltText != "sub" || rpc.IsFilesID(link.Id) {
		t.Fatalf("link = %v, want a home link door onto %s named sub", link, dir.ChildGridId)
	}
	if via, direct := n.grid(link.ChildGridId), n.grid(dir.ChildGridId); via.Grid.Id != direct.Grid.Id || len(via.Tiles) != 1 {
		t.Errorf("the link opens %q with %d tiles, the directory %q", via.Grid.Id, len(via.Tiles), direct.Grid.Id)
	}
}

// A page file has no address to copy and its page is the node's, so a
// right-drag of one into home lands a url link to it, as the ghost says
// (rpc.CopiesAsLink); never a url tile with no address.
func TestFilesPageFileCopiedIntoHomeLinks(t *testing.T) {
	root := t.TempDir()
	pdf := filepath.Join(root, "doc.pdf")
	writeFile(t, pdf, "%PDF-1.4\n")
	n := newFilesNode(t, root)
	page := n.tile(n.roots[0], pdf)
	if !rpc.CopiesAsLink(page, true) {
		t.Error("rpc.CopiesAsLink says the page copies, so the ghost would show a copy the node never makes")
	}
	copied, err := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: page.Id, DestGridId: n.home, X: 3, Y: 3})
	if err != nil {
		t.Fatal(err)
	}
	if copied.LinkTargetId != page.Id || copied.Kind != rpc.KindURL || rpc.IsFilesID(copied.Id) {
		t.Errorf("copy of a page file = %v, want a home url link to %s", copied, page.Id)
	}
}

// A directory or a page file renamed keeps the typed name on its row, and
// the link a copy into home lands carries that name, as a copy of one of
// home's tiles does.
func TestFilesRenamedTileCopiesWithItsName(t *testing.T) {
	root := t.TempDir()
	sub, pdf := filepath.Join(root, "sub"), filepath.Join(root, "doc.pdf")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pdf, "%PDF-1.4\n")
	n := newFilesNode(t, root)
	for i, path := range []string{sub, pdf} {
		tl := n.tile(n.roots[0], path)
		renamed, err := n.cl.RenameTile(n.ctx, tl.Id, tl.Version, "mine")
		if err != nil {
			t.Fatalf("rename %s: %v", path, err)
		}
		if renamed.AltText != "mine" || renamed.Id != tl.Id {
			t.Fatalf("rename answered %v, want the typed name on %s", renamed, tl.Id)
		}
		if got := n.tile(n.roots[0], path); got.AltText != "mine" {
			t.Errorf("%s relisted as %q, want the typed name", path, got.AltText)
		}
		copied, err := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: tl.Id, DestGridId: n.home, X: int64(i), Y: 4})
		if err != nil {
			t.Fatal(err)
		}
		if copied.AltText != "mine" || !copied.Named {
			t.Errorf("the copy of %s is named %q (typed %v), want the typed name", path, copied.AltText, copied.Named)
		}
	}
}
