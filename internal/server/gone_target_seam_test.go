package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
)

// goneReads asserts every read a link on screen takes answers the dead
// verdict: through the link's own id, and through its target's, which is the
// id the client reads a leaf link's body and face by (rpc.ContentID).
func goneReads(ctx context.Context, t *testing.T, cl *rpc.Client, link *gridwellv1.Tile, why string) {
	t.Helper()
	if _, _, _, err := cl.ReadContent(ctx, link.Id); !gwerr.IsDeadRef(err) {
		t.Errorf("%s: ReadContent through the link = %v, want the dead verdict", why, err)
	}
	if _, err := cl.GetTilePreview(ctx, link.Id); !gwerr.IsDeadRef(err) {
		t.Errorf("%s: GetTilePreview through the link = %v, want the dead verdict", why, err)
	}
	if _, _, _, err := cl.ReadContent(ctx, link.LinkTargetId); !gwerr.IsDeadRef(err) {
		t.Errorf("%s: ReadContent of the target = %v, want the dead verdict", why, err)
	}
	if _, err := cl.GetTilePreview(ctx, link.LinkTargetId); !gwerr.IsDeadRef(err) {
		t.Errorf("%s: GetTilePreview of the target = %v, want the dead verdict", why, err)
	}
	if _, err := cl.GetTile(ctx, link.LinkTargetId); !gwerr.IsDeadRef(err) {
		t.Errorf("%s: GetTile of the target = %v, want the dead verdict", why, err)
	}
}

func bodyOf(ctx context.Context, t *testing.T, cl *rpc.Client, id string) string {
	t.Helper()
	data, _, _, err := cl.ReadContent(ctx, id)
	if err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return string(data)
}

// A link whose file moved away is dead, however its reads reach the fs
// plugin, and reads again the moment the file is back where the link names:
// the link is never rewritten. The directory is what moves, the case a
// project reorganised on disk leaves behind.
func TestALinkToAMovedFileIsDeadUntilItIsBack(t *testing.T) {
	cl, _, _, fsRoot := lazyStack(t)
	ctx := context.Background()
	root := fsGrid(t, cl, fsRoot, 1)
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	var sub string
	for _, tile := range g.Tiles {
		if tile.AltText == "sub" {
			sub = tile.ChildGridId
		}
	}
	inner, err := cl.GetGrid(ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	var deep *gridwellv1.Tile
	for _, tile := range inner.Tiles {
		if tile.AltText == "deep.txt" {
			deep = tile
		}
	}
	if deep == nil {
		t.Fatalf("no deep.txt in %v", inner.Tiles)
	}
	link, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rpc.HomeGrid(lp),
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 4, W: 1, H: 1, LinkTargetId: deep.Id, AltText: deep.AltText}})
	if err != nil {
		t.Fatal(err)
	}
	if b := bodyOf(ctx, t, cl, link.Id); b != "deep" {
		t.Fatalf("read through the link = %q, want the file", b)
	}

	if err := os.Rename(filepath.Join(fsRoot, "sub"), filepath.Join(fsRoot, "moved")); err != nil {
		t.Fatal(err)
	}
	goneReads(ctx, t, cl, link, "the file moved away")

	if err := os.Rename(filepath.Join(fsRoot, "moved"), filepath.Join(fsRoot, "sub")); err != nil {
		t.Fatal(err)
	}
	if b := bodyOf(ctx, t, cl, link.Id); b != "deep" {
		t.Fatalf("the file is back and the link reads %q", b)
	}
	if b := bodyOf(ctx, t, cl, link.LinkTargetId); b != "deep" {
		t.Fatalf("the file is back and its target reads %q", b)
	}
}

// Home answers a destroyed target the same way: its id is never reassigned,
// so the link's path ends in nothing for good.
func TestALinkToADestroyedHomeTileIsDead(t *testing.T) {
	cl, _, _, _ := lazyStack(t)
	ctx := context.Background()
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home := rpc.HomeGrid(lp)
	target, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: home,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	link, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: home,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 2, Y: 0, W: 1, H: 1, LinkTargetId: target.Id}})
	if err != nil {
		t.Fatal(err)
	}
	// The first delete moves the target to the trash, where links keep
	// resolving; the second destroys it.
	for range 2 {
		if _, err := cl.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: target.Id}); err != nil {
			t.Fatal(err)
		}
		if _, err := cl.GetTile(ctx, link.Id); err != nil {
			t.Fatalf("the link itself = %v, want it still there to delete", err)
		}
	}
	goneReads(ctx, t, cl, link, "the target was destroyed")
	if _, err := cl.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: link.Id}); err != nil {
		t.Fatalf("deleting the dead link: %v", err)
	}
}
