package files

import (
	"context"
	"os"
	"slices"
	"testing"

	"google.golang.org/grpc/codes"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

func createEntry(f *fixture, dir, kind, name string, x, y int64) (*pb.TileResponse, error) {
	return f.s.CreateTile(context.Background(), &pb.CreateTileRequest{GridId: idOf(dir), File: true,
		Tile: &pb.Tile{Kind: kind, AltText: name, X: x, Y: y, W: 2, H: 3}})
}

// The file primitive makes an empty file, its kind its name's, its row minted
// at the placement asked; the directory primitive makes a directory, a door.
func TestTheDiskPrimitivesMakeAFileAndADirectory(t *testing.T) {
	f := newFixture(t)
	resp, err := createEntry(f, f.root, rpc.KindText, "cat.png", 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	p := f.path("cat.png")
	if fi, err := os.Stat(p); err != nil || fi.Size() != 0 {
		t.Fatalf("cat.png on disk: %v, %v; want an empty file", fi, err)
	}
	got := f.tile(p)
	if resp.Tile.Id != idOf(p) || got.Kind != rpc.KindURL || got.X != 3 || got.Y != 4 || got.W != 2 || got.H != 3 {
		t.Errorf("made %v, listed %v; want cat.png a url at 3,4 2x3", resp.Tile, got)
	}
	if _, err := createEntry(f, f.root, rpc.KindDoor, "sub", 0, 0); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(f.path("sub")); err != nil || !fi.IsDir() {
		t.Fatalf("sub on disk: %v, %v; want a directory", fi, err)
	}
	if d := f.tile(f.path("sub")); d.Kind != rpc.KindDoor || d.ChildGridId != idOf(f.path("sub")) {
		t.Errorf("sub listed %v, want a door onto its own grid", d)
	}
}

// A name already there, one naming no single entry, and a Gridwell tile are
// each refused with nothing written.
func TestTheDiskPrimitivesRefuse(t *testing.T) {
	f := newFixture(t)
	f.write("Plan.md", "theirs")
	for _, name := range []string{"Plan.md", "", "..", "a/b", "nul\x00"} {
		_, err := createEntry(f, f.root, rpc.KindText, name, 0, 0)
		wantCode(t, "a create of "+name, err, codes.InvalidArgument)
	}
	_, err := f.s.CreateTile(context.Background(), &pb.CreateTileRequest{GridId: idOf(f.root),
		Tile: &pb.Tile{Kind: rpc.KindText, AltText: "Gridwell", W: 1, H: 1}})
	wantCode(t, "a Gridwell tile asked of the files", err, codes.InvalidArgument)
	if b, _ := os.ReadFile(f.path("Plan.md")); string(b) != "theirs" {
		t.Errorf("Plan.md holds %q, want theirs", b)
	}
	if es, _ := os.ReadDir(f.root); len(es) != 1 {
		t.Errorf("the root holds %d names, want Plan.md alone", len(es))
	}
}

// A name made after it was checked is never overwritten: the create is
// refused and the file stands.
func TestACreateNeverReplacesAFileMadeMeanwhile(t *testing.T) {
	f := newFixture(t)
	was := resolved
	t.Cleanup(func() { resolved = was })
	resolved = func() { f.write("Plan.md", "theirs") }
	_, err := createEntry(f, f.root, rpc.KindText, "Plan.md", 0, 0)
	wantCode(t, "a create over a file made meanwhile", err, codes.InvalidArgument)
	if b, _ := os.ReadFile(f.path("Plan.md")); string(b) != "theirs" {
		t.Errorf("Plan.md holds %q, want theirs", b)
	}
}

// A file copied into another directory is a new file of its bytes there,
// under its name, its row at the cell asked; a name already there is
// refused and the file standing is untouched.
func TestACopyWithinTheFilesCopiesTheFileOnDisk(t *testing.T) {
	f := newFixture(t)
	src := f.write("a/notes.txt", "body")
	dst := f.mkdir("b")
	resp, err := f.s.CopyTile(context.Background(), &pb.CopyTileRequest{TileId: idOf(src), DestGridId: idOf(dst), X: 2, Y: 1})
	if err != nil {
		t.Fatal(err)
	}
	p := f.path("b/notes.txt")
	if b, _ := os.ReadFile(p); string(b) != "body" {
		t.Errorf("the copy holds %q, want body", b)
	}
	if got := f.tile(p); resp.Tile.Id != idOf(p) || got.X != 2 || got.Y != 1 {
		t.Errorf("copied %v, listed %v; want notes.txt at 2,1", resp.Tile, got)
	}
	f.write("c/notes.txt", "theirs")
	_, err = f.s.CopyTile(context.Background(), &pb.CopyTileRequest{TileId: idOf(src), DestGridId: idOf(f.path("c"))})
	wantCode(t, "a copy over a name already there", err, codes.InvalidArgument)
	if b, _ := os.ReadFile(f.path("c/notes.txt")); string(b) != "theirs" {
		t.Errorf("c/notes.txt holds %q, want theirs", b)
	}
	if n := names(f.grid(f.path("b"))); !slices.Equal(n, []string{"notes.txt"}) {
		t.Errorf("b lists %v, want notes.txt alone", n)
	}
}
