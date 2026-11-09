package server_test

// A directory moved within Gridwell carries its overlay (CLAUDE.md,
// 2026-11-08): through the web API, the router, home and the files over a
// temp root.

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// overText makes a home text on grid at (x, y).
func (n *filesNode) overText(grid string, x, y int64, body string) *gridwellv1.Tile {
	n.t.Helper()
	tl, err := n.cl.CreateWithContent(n.ctx, &gridwellv1.CreateTileRequest{GridId: grid,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: x, Y: y, W: 1, H: 1}}, []byte(body))
	if err != nil {
		n.t.Fatal(err)
	}
	return tl
}

// listed is the tile id grid lists, nil when it lists none.
func listed(g *gridwellv1.GetGridResponse, id string) *gridwellv1.Tile {
	i := slices.IndexFunc(g.Tiles, func(t *gridwellv1.Tile) bool { return t.Id == id })
	if i < 0 {
		return nil
	}
	return g.Tiles[i]
}

// trashGrid is home's trash grid, from the roster.
func (n *filesNode) trashGrid() string {
	n.t.Helper()
	lp, err := n.cl.Handshake(n.ctx)
	if err != nil {
		n.t.Fatal(err)
	}
	for _, g := range rpc.HomeRow(lp).GetSourceGrids() {
		if g.Glyph == rpc.GlyphTrash {
			return g.GridId
		}
	}
	n.t.Fatal("home declares no trash")
	return ""
}

// inTrash reports whether tile id sits in the trash tree.
func (n *filesNode) inTrash(id string) bool {
	n.t.Helper()
	resp, err := n.raw.Find(n.ctx, connect.NewRequest(&gridwellv1.FindRequest{Query: "id:" + id}))
	if err != nil {
		n.t.Fatal(err)
	}
	if len(resp.Msg.Results) != 1 {
		n.t.Fatalf("locate %s = %v", id, resp.Msg.Results)
	}
	r := resp.Msg.Results[0]
	trash := n.trashGrid()
	return r.Tile.GridId == trash || len(r.Path) > 0 && r.Path[0].GridId == trash
}

// A directory moved into another carries every Gridwell tile under it, its
// subdirectories' included, with their ids, cells and selection, and the
// files' rows under it keep their framing; the old ids read dead, and an
// open view hears each tile's new grid.
func TestADirectoryMovedCarriesItsOverlay(t *testing.T) {
	root := t.TempDir()
	a, b, dst := filepath.Join(root, "a"), filepath.Join(root, "a", "b"), filepath.Join(root, "dst")
	writeFile(t, filepath.Join(b, "f.txt"), "f")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	n := newFilesNode(t, root)
	top := n.overText(fileID(a), 3, 3, "top")
	deep := n.overText(fileID(b), 2, 2, "deep")
	if err := n.cl.SetSelection(n.ctx, &gridwellv1.SetSelectionRequest{GridId: fileID(b), Add: []string{deep.Id}}); err != nil {
		t.Fatal(err)
	}
	if _, err := n.cl.SetFraming(n.ctx, &gridwellv1.SetFramingRequest{TileId: fileID(b), Cx: 4, Cy: 5, Zoom: 2}); err != nil {
		t.Fatal(err)
	}
	f := n.tile(fileID(b), filepath.Join(b, "f.txt"))
	if _, err := n.move(f, "", 6, 6); err != nil {
		t.Fatal(err)
	}
	events := n.show(fileID(a), fileID(b))
	if _, err := n.move(n.tile(n.roots[0], a), fileID(dst), 0, 0); err != nil {
		t.Fatal(err)
	}
	na, nb := filepath.Join(dst, "a"), filepath.Join(dst, "a", "b")
	if _, err := os.Stat(nb); err != nil {
		t.Fatalf("a/b did not move on disk: %v", err)
	}
	heard := map[string]bool{}
	await(t, events, "the deep text's new grid and the old grid's change", func(ev *gridwellv1.Event) bool {
		tl := ev.GetTileChanged().GetTile()
		heard["tile"] = heard["tile"] || tl.GetId() == deep.Id && tl.GetGridId() == fileID(nb)
		heard["grid"] = heard["grid"] || ev.GetGridChanged().GetGridId() == fileID(b)
		return heard["tile"] && heard["grid"]
	})
	if got := listed(n.grid(fileID(na)), top.Id); got == nil || got.X != 3 || got.Y != 3 {
		t.Errorf("dst/a lists the top text as %v, want it at 3,3", got)
	}
	g := n.grid(fileID(nb))
	if got := listed(g, deep.Id); got == nil || got.X != 2 || got.Y != 2 || got.GridId != fileID(nb) {
		t.Errorf("dst/a/b lists the deep text as %v, want it at 2,2", got)
	}
	if !slices.Equal(g.Grid.Selection, []string{deep.Id}) {
		t.Errorf("dst/a/b's selection = %v, want the deep text", g.Grid.Selection)
	}
	if got := listed(g, fileID(filepath.Join(nb, "f.txt"))); got == nil || got.X != 6 || got.Y != 6 {
		t.Errorf("dst/a/b lists f.txt as %v, want it where it was left, 6,6", got)
	}
	if door := listed(n.grid(fileID(na)), fileID(nb)); door == nil || door.ViewCx != 4 || door.ViewZoom != 2 {
		t.Errorf("b's door under dst/a = %v, want its framing kept", door)
	}
	if got, err := n.cl.GetTile(n.ctx, deep.Id); err != nil || got.GridId != fileID(nb) {
		t.Errorf("the deep text reads %v, %v; want it on dst/a/b", got, err)
	}
	if _, err := n.cl.GetGrid(n.ctx, fileID(b)); err == nil {
		t.Error("the old path's grid still answers")
	}
}

// A directory moved onto a path where a gone directory's Gridwell tiles
// still sit is refused, and nothing moves.
func TestADirectoryMovedOntoAGoneOnesTilesIsRefused(t *testing.T) {
	root := t.TempDir()
	a, src := filepath.Join(root, "a"), filepath.Join(root, "src", "a")
	for _, d := range []string{a, src} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	n := newFilesNode(t, root)
	kept := n.overText(fileID(a), 1, 1, "kept")
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	door := n.tile(fileID(filepath.Join(root, "src")), src)
	if _, err := n.move(door, n.roots[0], 5, 5); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a move onto the gone a = %v, want refused", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("src/a moved though refused: %v", err)
	}
	if got, err := n.cl.GetTile(n.ctx, kept.Id); err != nil || got.GridId != fileID(a) {
		t.Errorf("the gone a's text reads %v, %v", got, err)
	}
	if _, err := n.cl.MoveTile(n.ctx, &gridwellv1.MoveTileRequest{TileId: kept.Id, GridId: n.home, X: 7, Y: 7, W: 1, H: 1,
		Claim: rpc.PlacementOf(kept)}); err != nil {
		t.Fatal(err)
	}
	if _, err := n.move(door, n.roots[0], 5, 5); err != nil {
		t.Fatalf("a move onto a gone a with no Gridwell tile left = %v, want it lands", err)
	}
	if g := n.grid(fileID(a)); len(g.Tiles) != 0 {
		t.Errorf("the a moved in lists %v, want nothing the gone one left", g.Tiles)
	}
}
