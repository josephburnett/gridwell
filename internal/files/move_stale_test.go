package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// staleRow mints a row for name under the root by placing it, then removes
// it on disk with no listing since, so its row stays live and unswept.
func (f *fixture) staleRow(name string, isDir bool) {
	f.t.Helper()
	p := f.path(name)
	if isDir {
		f.mkdir(name)
		f.write(name+"/inner.txt", "inner")
		in := tileIn(f.t, f.grid(p), filepath.Join(p, "inner.txt"))
		if _, err := f.s.MoveTile(context.Background(), &pb.MoveTileRequest{TileId: in.Id, X: 4, Y: 4, W: 1, H: 1, Claim: rpc.PlacementOf(in)}); err != nil {
			f.t.Fatal(err)
		}
	} else {
		f.write(name, "old")
	}
	tl := tileIn(f.t, f.grid(filepath.Dir(p)), p)
	if _, err := f.s.MoveTile(context.Background(), &pb.MoveTileRequest{TileId: tl.Id, X: 5, Y: 5, W: 1, H: 1, Claim: rpc.PlacementOf(tl)}); err != nil {
		f.t.Fatal(err)
	}
	if err := os.RemoveAll(p); err != nil {
		f.t.Fatal(err)
	}
}

// moveInto moves path into dir, claiming the placement it reads.
func (f *fixture) moveInto(path, dir string) (*pb.TileResponse, error) {
	tl := tileIn(f.t, f.grid(filepath.Dir(path)), path)
	return f.s.MoveTile(context.Background(), &pb.MoveTileRequest{TileId: tl.Id, GridId: idOf(dir), X: 2, Y: 0, W: 1, H: 1, Claim: rpc.PlacementOf(tl)})
}

// liveID is the live row at path on dir's grid, 0 when there is none.
func (f *fixture) liveID(dir, path string) int64 {
	gid, _, _ := f.s.mem.LookupGrid(dir)
	id, _, _ := f.s.mem.LiveTileID(gid, path)
	return id
}

// A file moved onto a name whose row a listing never swept lands: the
// rename proved that name gone, so its row retires and the moved row takes
// the name, under its own id.
func TestAMoveOntoAStaleRowRetiresIt(t *testing.T) {
	f := newFixture(t)
	to := f.mkdir("to")
	f.staleRow("to/a.txt", false)
	landed := filepath.Join(to, "a.txt")
	stale := f.liveID(to, landed)
	p := f.write("a.txt", "new")
	f.grid(f.root)
	ev := f.events()
	resp, err := f.moveInto(p, to)
	if err != nil {
		t.Fatalf("move onto a stale row: %v", err)
	}
	if got := f.liveID(to, landed); got == 0 || got == stale || resp.Tile.X != 2 {
		t.Fatalf("landed row %d, stale %d, tile %+v", got, stale, resp.Tile)
	}
	if f.liveID(f.root, p) != 0 {
		t.Error("the old directory still names the moved row")
	}
	await(t, ev, "the old directory's change", gridChanged(f.root))
	await(t, ev, "the new directory's change", gridChanged(to))
}

// A directory moved onto a stale directory row carries its Gridwell tiles in
// the same write, and the stale row and the rows under it retire.
func TestADirectoryMovedOntoAStaleRowCarriesItsOverlay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	to := f.mkdir("to")
	f.staleRow("to/d", true)
	landed := filepath.Join(to, "d")
	staleInner := f.liveID(landed, filepath.Join(landed, "inner.txt"))
	d := f.mkdir("d")
	f.write("d/f.txt", "f")
	over, err := f.st.CreateText(ctx, idOf(d), 0, 3, 1, 1, []byte("over"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.moveInto(d, to); err != nil {
		t.Fatalf("move onto a stale row: %v", err)
	}
	now, err := f.st.GetTile(ctx, over.Id)
	if err != nil {
		t.Fatal(err)
	}
	if now.GridId != idOf(landed) {
		t.Fatalf("the Gridwell tile sits on %s, want %s", now.GridId, idOf(landed))
	}
	if g := f.grid(landed); len(g.Tiles) != 2 {
		t.Errorf("the moved directory lists %v", names(g))
	}
	if _, _, tomb, err := f.s.mem.TileKey(staleInner); err != nil || !tomb {
		t.Errorf("the stale row under the landing name: tombstoned %v (%v)", tomb, err)
	}
}
