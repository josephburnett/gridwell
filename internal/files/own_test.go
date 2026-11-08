package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The node's own directory is outside every root, a root that holds it
// included: not listed, dead by its path or a link's, and nothing in it is
// written or trashed.
func TestTheNodesOwnDirectoryIsOutsideEveryRoot(t *testing.T) {
	f := newFixture(t)
	own := f.mkdir(".gridwell")
	cfg := f.write(".gridwell/server.yaml", "id: n1\n")
	f.write("notes.md", "# notes")
	if err := os.Symlink(cfg, f.path("cfg.yaml")); err != nil {
		t.Skip("symlinks unsupported")
	}
	s, err := New(f.st.Files(), []Root{{Path: f.root}}, func(*os.File, string) error {
		return errors.New("the trash was asked")
	}, own)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx := context.Background()
	g, err := s.GetGrid(ctx, &pb.GetGridRequest{GridId: idOf(f.root)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range g.Tiles {
		if tl.Id == idOf(own) {
			t.Errorf("the root lists the node's directory: %+v", tl)
		}
		if tl.Id == idOf(f.path("cfg.yaml")) && tl.LinkTargetId != idOf(cfg) {
			t.Errorf("a link into the node's directory = %+v, want a link to its path", tl)
		}
	}
	if _, err := s.GetGrid(ctx, &pb.GetGridRequest{GridId: idOf(own)}); !gwerr.IsDeadLink(err) {
		t.Errorf("GetGrid(the node's directory) = %v, want dead", err)
	}
	if _, err := s.GetTile(ctx, &pb.GetTileRequest{TileId: idOf(cfg)}); !gwerr.IsDeadLink(err) {
		t.Errorf("GetTile(server.yaml) = %v, want dead", err)
	}
	if _, err := s.GetTile(ctx, &pb.GetTileRequest{TileId: rpc.KeyTileID(cfg)}); !gwerr.IsDeadLink(err) {
		t.Errorf("GetTile(server.yaml) = %v, want dead", err)
	}
	if _, err := writeAll(s, idOf(f.path("cfg.yaml")), "", []byte("id: evil\n")); err == nil {
		t.Error("a write through a link into the node's directory landed")
	}
	if _, err := s.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: idOf(cfg)}); err == nil {
		t.Error("a delete in the node's directory was taken")
	}
	if b, _ := os.ReadFile(cfg); string(b) != "id: n1\n" {
		t.Errorf("server.yaml = %q, want it untouched", b)
	}
	if _, err := os.Stat(filepath.Join(own, "server.yaml")); err != nil {
		t.Errorf("server.yaml is gone: %v", err)
	}
}
