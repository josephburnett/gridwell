package files

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A directory swapped for a symlink out of the tree after a path was checked
// and resolved, and before its bytes are opened, is refused: no verb reads,
// writes, previews, serves, trashes or moves anything outside every root.
func TestADirectorySwappedForALinkOutIsNeverFollowed(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(f.base, "outside")
	for _, name := range []string{"n.txt", "p.png", "gone.txt", "moved.txt"} {
		if err := os.MkdirAll(outside, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, name), []byte("SECRET"), 0o644); err != nil {
			t.Fatal(err)
		}
		f.write("d/"+name, "inside")
	}
	f.mkdir("to")
	swapped := false
	swap := func() {
		if swapped {
			return
		}
		swapped = true
		if err := os.Rename(f.path("d"), f.path("d.real")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, f.path("d")); err != nil {
			t.Skip("symlinks unsupported")
		}
	}
	unswap := func() {
		_ = os.Remove(f.path("d"))
		_ = os.Rename(f.path("d.real"), f.path("d"))
		swapped = false
	}
	was := resolved
	t.Cleanup(func() { resolved = was })
	resolved = swap
	ctx := context.Background()
	id := idOf(f.path("d/n.txt"))
	stamp := f.tile(f.path("d/n.txt")).ContentStamp
	unswap()
	resolved = swap

	var got []byte
	_ = f.s.ReadContent(ctx, &pb.ReadContentRequest{TileId: id}, func(c *pb.ContentChunk) error {
		got = append(got, c.Data...)
		return nil
	})
	if strings.Contains(string(got), "SECRET") {
		t.Errorf("ReadContent read %q from outside every root", got)
	}
	unswap()
	_, _ = writeAll(f.s, id, stamp, []byte("written"))
	if b, _ := os.ReadFile(filepath.Join(outside, "n.txt")); string(b) != "SECRET" {
		t.Errorf("WriteContent wrote %q outside every root", b)
	}
	unswap()
	got = nil
	_ = f.s.ServeContent(ctx, &pb.ServeContentRequest{TileId: idOf(f.path("d/p.png"))}, func(c *pb.ServeContentChunk) error {
		got = append(got, c.Data...)
		return nil
	})
	if strings.Contains(string(got), "SECRET") {
		t.Errorf("ServeContent served %q from outside every root", got)
	}
	unswap()
	_, _ = f.s.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: idOf(f.path("d/p.png"))})
	unswap()
	_, _ = f.s.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: idOf(f.path("d/gone.txt"))})
	if _, err := os.Lstat(filepath.Join(outside, "gone.txt")); err != nil {
		t.Errorf("DeleteTile trashed a file outside every root: %v", err)
	}
	unswap()
	moved := f.tile(f.path("d/moved.txt"))
	_, _ = f.s.MoveTile(ctx, &pb.MoveTileRequest{TileId: moved.Id, GridId: idOf(f.path("to")), W: 1, H: 1, Claim: rpc.PlacementOf(moved)})
	if _, err := os.Lstat(filepath.Join(outside, "moved.txt")); err != nil {
		t.Errorf("MoveTile moved a file from outside every root: %v", err)
	}
	unswap()
}
