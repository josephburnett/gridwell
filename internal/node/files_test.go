package node

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
)

// A node whose server.yaml declares files: serves one files row between home
// and the connections, a grid per root in config order, and the root's grid
// answers through the web API.
func TestFilesDeclaredInConfigAreServed(t *testing.T) {
	dir, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	cfg, err := BuildConfig(dir, filepath.Join(dir, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Web.Bind = "127.0.0.1:0"
	cfg.Files = []config.FilesRoot{{Root: a, Label: "Docs"}, {Root: b}}
	n, err := Start(Options{Dir: dir, Cfg: cfg})
	if err != nil {
		t.Fatal(err)
	}
	n.ServeBackground()
	t.Cleanup(func() { _ = n.Close() })
	cl := webClient(t, "http://"+n.Ln.Addr().String(), cfg.WebPassword)
	lp, err := cl.Handshake(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(lp.Sources) < 2 || lp.Sources[1].Uuid != rpc.FilesOwner(cfg.ID) {
		t.Fatalf("roster = %v, want the files second", lp.Sources)
	}
	grids := lp.Sources[1].SourceGrids
	if len(grids) != 2 || grids[0].Label != "Docs" || grids[1].Label != filepath.Base(b) ||
		grids[0].GridId != rpc.QualifyID(cfg.ID, rpc.KeyTileID(a)) {
		t.Fatalf("roots = %v", grids)
	}
	if _, err := cl.GetGrid(context.Background(), grids[0].GridId); err != nil {
		t.Fatalf("the first root's grid: %v", err)
	}
}

// A node that declares no files has no files row.
func TestNoFilesNoRow(t *testing.T) {
	dir := t.TempDir()
	cfg, err := BuildConfig(dir, filepath.Join(dir, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Web.Bind = "127.0.0.1:0"
	n, err := Start(Options{Dir: dir, Cfg: cfg})
	if err != nil {
		t.Fatal(err)
	}
	n.ServeBackground()
	t.Cleanup(func() { _ = n.Close() })
	lp, err := webClient(t, "http://"+n.Ln.Addr().String(), cfg.WebPassword).Handshake(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range lp.Sources {
		if p.Uuid == rpc.FilesOwner(cfg.ID) {
			t.Fatalf("a node with no files: has a files row %v", p)
		}
	}
}
