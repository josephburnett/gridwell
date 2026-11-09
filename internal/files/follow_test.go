package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
)

func readBody(t *testing.T, s *Source, id string) (string, error) {
	t.Helper()
	var got []byte
	err := s.ReadContent(context.Background(), &pb.ReadContentRequest{TileId: id}, func(c *pb.ContentChunk) error {
		got = append(got, c.Data...)
		return nil
	})
	return string(got), err
}

// A symlink is followed wherever it goes: to a directory outside every root a
// door onto that directory's grid, to a file there a link whose target reads,
// and a path through the linked directory resolves.
func TestASymlinkIsFollowedWhereverItGoes(t *testing.T) {
	f := newFixture(t)
	elsewhere := filepath.Join(f.base, "elsewhere")
	if err := os.MkdirAll(filepath.Join(elsewhere, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "far.md"), []byte("# far"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, dest := range map[string]string{"outdir": elsewhere, "out.md": filepath.Join(elsewhere, "far.md")} {
		if err := os.Symlink(dest, f.path(name)); err != nil {
			t.Skip("symlinks unsupported")
		}
	}
	resp := f.grid(f.root)
	door := tileIn(t, resp, f.path("outdir"))
	if door.Kind != rpc.KindDoor || door.ChildGridId != idOf(elsewhere) || !door.Link {
		t.Errorf("outdir = kind %q child %q link %v, want a linked door onto %s", door.Kind, door.ChildGridId, door.Link, elsewhere)
	}
	tileIn(t, f.grid(elsewhere), filepath.Join(elsewhere, "far.md"))
	link := tileIn(t, resp, f.path("out.md"))
	if link.LinkTargetId != idOf(filepath.Join(elsewhere, "far.md")) {
		t.Errorf("out.md targets %q", link.LinkTargetId)
	}
	if got, err := readBody(t, f.s, link.LinkTargetId); err != nil || got != "# far" {
		t.Errorf("the link's target reads %q, %v", got, err)
	}
	through := f.path("outdir/far.md")
	if got, err := readBody(t, f.s, idOf(through)); err != nil || got != "# far" {
		t.Errorf("a path through the linked directory reads %q, %v", got, err)
	}
	if tl := f.tile(through); tl.Kind != rpc.KindText {
		t.Errorf("a path through the linked directory = %+v", tl)
	}
	tileIn(t, f.grid(f.path("outdir")), through)
	stamp := f.tile(through).ContentStamp
	if _, err := writeAll(f.s, idOf(through), stamp, []byte("# mine")); err != nil {
		t.Fatalf("a write through the linked directory: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(elsewhere, "far.md")); string(b) != "# mine" {
		t.Errorf("the file the write reached holds %q", b)
	}
}

// Every absolute path is addressable: a file nowhere under a root is live by
// its id, and a path is dead only when it is gone or its id is not canonical.
func TestEveryPathIsAddressable(t *testing.T) {
	f := newFixture(t)
	far := filepath.Join(f.base, "far", "note.txt")
	if err := os.MkdirAll(filepath.Dir(far), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(far, []byte("far"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if tl := f.tile(far); tl.Id != idOf(far) || tl.Kind != rpc.KindText {
		t.Errorf("GetTile(%s) = %+v", far, tl)
	}
	if got, err := readBody(t, f.s, idOf(far)); err != nil || got != "far" {
		t.Errorf("ReadContent(%s) = %q, %v", far, got, err)
	}
	tileIn(t, f.grid(f.base), f.root)
	gone := filepath.Join(f.base, "gone.txt")
	if _, err := f.s.GetTile(ctx, &pb.GetTileRequest{TileId: idOf(gone)}); !gwerr.IsDeadLink(err) {
		t.Errorf("GetTile(a gone path) = %v, want dead", err)
	}
	if _, err := f.s.GetGrid(ctx, &pb.GetGridRequest{GridId: idOf(filepath.Join(f.base, "gone"))}); !gwerr.IsDeadLink(err) {
		t.Errorf("GetGrid(a gone directory) = %v, want dead", err)
	}
	padded := idOf(far) + "="
	if _, err := f.s.GetTile(ctx, &pb.GetTileRequest{TileId: padded}); !gwerr.IsDeadLink(err) {
		t.Errorf("GetTile(non-canonical base64) = %v, want dead", err)
	}
}

// The node's own directory is a directory like any other: listed in a root
// that holds it, read, written and trashed.
func TestTheNodesOwnDirectoryIsLikeAnyOther(t *testing.T) {
	f := newFixture(t)
	own := f.mkdir(".gridwell")
	cfg := f.write(".gridwell/server.yaml", "id: n1\n")
	tileIn(t, f.grid(f.root), own)
	tl := tileIn(t, f.grid(own), cfg)
	if got, err := readBody(t, f.s, idOf(cfg)); err != nil || got != "id: n1\n" {
		t.Errorf("ReadContent(server.yaml) = %q, %v", got, err)
	}
	if _, err := writeAll(f.s, idOf(cfg), tl.ContentStamp, []byte("id: n2\n")); err != nil {
		t.Errorf("a write in the node's directory: %v", err)
	}
	tl = f.tile(cfg)
	if _, err := f.s.DeleteTile(context.Background(), rpc.DeleteOf(tl)); err != nil {
		t.Errorf("a delete in the node's directory: %v", err)
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Errorf("server.yaml was not trashed: %v", err)
	}
}
