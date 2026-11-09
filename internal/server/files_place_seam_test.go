package server_test

// A directory is a place for Gridwell tiles (CLAUDE.md, 2026-11-08): home's
// rows over a directory's grid, through the web API, the router, home and
// the files over a temp root.

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A text made on a directory is home's row, its grid the directory's id;
// the directory's grid lists it beside the files, routed to home by its id,
// and an open view hears its grid change; a door made there opens a home
// grid, and a copy of a home tile onto the directory lands there as home's.
func TestAGridwellTileSitsOverADirectory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	n := newFilesNode(t, root)
	dir := n.roots[0]
	es, err := n.cl.Subscribe(n.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()
	if err := n.cl.SetView(n.ctx, &gridwellv1.SetViewRequest{ShownGridIds: []string{dir}}); err != nil {
		t.Fatal(err)
	}
	note, err := n.cl.CreateWithContent(n.ctx, &gridwellv1.CreateTileRequest{GridId: dir,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 3, Y: 0, W: 1, H: 1}}, []byte("Plan"))
	if err != nil {
		t.Fatal(err)
	}
	if rpc.IsFilesID(note.Id) || note.GridId != dir {
		t.Fatalf("a text made on a directory = %v, want home's row on %s", note, dir)
	}
	if es, _ := os.ReadDir(root); len(es) != 1 {
		t.Errorf("the directory holds %d names, want a.txt alone: nothing is written to disk", len(es))
	}
	heard := map[string]bool{}
	for deadline := time.After(10 * time.Second); !heard["grid"] || !heard["tile"]; {
		ev, ok, err := es.Recv()
		if err != nil || !ok {
			t.Fatalf("the stream ended: %v", err)
		}
		heard["grid"] = heard["grid"] || ev.GetGridChanged().GetGridId() == dir
		heard["tile"] = heard["tile"] || ev.GetTileChanged().GetTile().GetId() == note.Id && ev.GetTileChanged().GetTile().GetGridId() == dir
		select {
		case <-deadline:
			t.Fatalf("an open view of the directory heard %v", heard)
		default:
		}
	}
	g := n.grid(dir)
	if len(g.Tiles) != 2 || !slices.ContainsFunc(g.Tiles, func(tl *gridwellv1.Tile) bool { return tl.Id == note.Id }) {
		t.Fatalf("the directory lists %v, want a.txt and the text", g.Tiles)
	}
	if b, _, _, err := n.cl.ReadContent(n.ctx, note.Id); err != nil || string(b) != "Plan" {
		t.Errorf("the text reads %q, %v", b, err)
	}
	door, err := n.cl.CreateTile(n.ctx, &gridwellv1.CreateTileRequest{GridId: dir,
		Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 4, Y: 0, W: 1, H: 1, AltText: "Notes"}})
	if err != nil {
		t.Fatal(err)
	}
	if door.Link || rpc.IsFilesID(door.ChildGridId) || n.grid(door.ChildGridId).Grid.Id != door.ChildGridId {
		t.Errorf("a door made on a directory = %v, want one opening a home grid", door)
	}
	copied, err := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: note.Id, DestGridId: dir, X: 5, Y: 0})
	if err != nil {
		t.Fatal(err)
	}
	if copied.Link || copied.GridId != dir || rpc.IsFilesID(copied.Id) {
		t.Errorf("a copy onto the directory = %v, want home's text there", copied)
	}
	if _, err := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: note.Id, DestGridId: dir, X: 0, Y: 0}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a copy over a.txt's derived cell = %v, want the overlap refusal", err)
	}
}

// The file and directory primitives make them on disk; with file false the
// same create is a Gridwell tile.
func TestTheFilePrimitivesThroughTheRouter(t *testing.T) {
	root := t.TempDir()
	n := newFilesNode(t, root)
	made, err := n.cl.CreateTile(n.ctx, &gridwellv1.CreateTileRequest{GridId: n.roots[0], File: true,
		Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 1, Y: 0, W: 1, H: 1, AltText: "sub"}})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(root, "sub")); err != nil || !fi.IsDir() || made.Id != fileID(filepath.Join(root, "sub")) {
		t.Fatalf("the directory primitive made %v (%v, %v), want sub on disk", made, fi, err)
	}
	if _, err := n.cl.CreateTile(n.ctx, &gridwellv1.CreateTileRequest{GridId: n.roots[0], File: true,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 2, Y: 0, W: 1, H: 1, AltText: "sub"}}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("a file over a name already there = %v, want refused", err)
	}
}

// A Gridwell tile on a directory is in its selection and found by a find
// scoped to the directory, or to one above it with recurse; a file copied
// within the files copies on disk, and onto home links.
func TestAGridwellTileOnADirectoryIsSelectedAndFound(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	src := filepath.Join(root, "a.txt")
	writeFile(t, src, "a")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	n := newFilesNode(t, root)
	subGrid := rpc.QualifyID(localNodeID, rpc.KeyTileID(sub))
	note, err := n.cl.CreateWithContent(n.ctx, &gridwellv1.CreateTileRequest{GridId: subGrid,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 3, Y: 3, W: 1, H: 1}}, []byte("Plan"))
	if err != nil {
		t.Fatal(err)
	}
	file := fileID(src)
	if err := n.cl.SetSelection(n.ctx, &gridwellv1.SetSelectionRequest{GridId: n.roots[0], Add: []string{file}}); err != nil {
		t.Fatal(err)
	}
	if err := n.cl.SetSelection(n.ctx, &gridwellv1.SetSelectionRequest{GridId: subGrid, Add: []string{note.Id}}); err != nil {
		t.Fatal(err)
	}
	if got := n.grid(subGrid).Grid.Selection; !slices.Equal(got, []string{note.Id}) {
		t.Errorf("the directory's selection = %v, want the text", got)
	}
	for _, req := range []*gridwellv1.FindRequest{
		{GridId: subGrid, Kind: rpc.KindText},
		{GridId: subGrid, Query: "Plan"},
		{GridId: n.roots[0], Kind: rpc.KindText, Recurse: true},
		{GridId: subGrid, Selected: true},
	} {
		resp, err := n.raw.Find(n.ctx, connect.NewRequest(req))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(resp.Msg.Results, func(r *gridwellv1.FindResult) bool { return r.Tile.GetId() == note.Id }) {
			t.Errorf("find %v missed the text: %v", req, resp.Msg.Results)
		}
	}
	copied, err := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: file, DestGridId: subGrid, X: 0, Y: 0})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(sub, "a.txt")); string(b) != "a" || copied.Id != fileID(filepath.Join(sub, "a.txt")) {
		t.Errorf("a file copied into sub = %v holding %q, want a.txt there on disk", copied, b)
	}
	link, err := n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: file, DestGridId: subGrid, X: 6, Y: 6})
	if err == nil {
		t.Errorf("a second copy over the name = %v, want refused", link)
	}
	link, err = n.cl.CopyTile(n.ctx, &gridwellv1.CopyTileRequest{TileId: file, DestGridId: n.home, X: 6, Y: 6})
	if err != nil || !link.Link || link.LinkTargetId != file {
		t.Errorf("a file copied onto home = %v, %v; want a link to it", link, err)
	}
}

// A file listed after a Gridwell tile was placed on its directory flows
// around it, as a Gridwell tile is refused over a file's derived cell: no
// two tiles of a directory's grid share a cell, and the Gridwell tile stays.
func TestAFileListedLaterFlowsAroundAGridwellTile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "b.txt"), "b")
	n := newFilesNode(t, root)
	dir := n.roots[0]
	b := fileID(filepath.Join(root, "b.txt"))
	var cell *gridwellv1.Tile
	for _, tl := range n.grid(dir).Tiles {
		if tl.Id == b {
			cell = tl
		}
	}
	if cell == nil {
		t.Fatal("b.txt is not listed")
	}
	if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatal(err)
	}
	n.grid(dir)
	note, err := n.cl.CreateWithContent(n.ctx, &gridwellv1.CreateTileRequest{GridId: dir,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: cell.X, Y: cell.Y, W: 1, H: 1}}, []byte("Plan"))
	if err != nil {
		t.Fatalf("a text at the cell b.txt left: %v", err)
	}
	writeFile(t, filepath.Join(root, "b.txt"), "b")
	g := n.grid(dir)
	if len(g.Tiles) != 3 {
		t.Fatalf("the directory lists %v, want a.txt, b.txt and the text", g.Tiles)
	}
	for i, p := range g.Tiles {
		for _, q := range g.Tiles[i+1:] {
			if p.X < q.X+q.W && q.X < p.X+p.W && p.Y < q.Y+q.H && q.Y < p.Y+p.H {
				t.Errorf("%s at (%d,%d) shares a cell with %s at (%d,%d)", p.Id, p.X, p.Y, q.Id, q.X, q.Y)
			}
		}
		if p.Id == note.Id && (p.X != cell.X || p.Y != cell.Y) {
			t.Errorf("the text moved to (%d,%d) when the file came back", p.X, p.Y)
		}
	}
}
