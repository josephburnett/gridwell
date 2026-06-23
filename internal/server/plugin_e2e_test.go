package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// TestFileWellLifecycleE2E exercises the whole new path through the public RPC
// surface: create a file well (Attach to the fs plugin), descend (GetGrid
// lists the directory), rearrange a tile (MoveTile persists across a
// re-descent), and delete a tile (the file is removed and swept from the
// grid). It is the end-to-end proof that file wells work through the plugin
// boundary and that the primary rule — things stay where you left them — holds
// across it.
func TestFileWellLifecycleE2E(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	cl, root := newTestServerWithPlugins(t)
	ctx := context.Background()

	// 1. Create the file well. It is a plain well in the local store whose
	//    child grid lives in the fs plugin.
	well, err := cl.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: dir,
	})
	if err != nil {
		t.Fatalf("CreateFileWell: %v", err)
	}
	child := well.ChildGridID
	if !strings.HasPrefix(child, fsPluginUUID+"/") {
		t.Fatalf("child_grid_id = %q, want %q prefix", child, fsPluginUUID)
	}

	// 2. Descend: GetGrid on the child routes to the fs plugin and lists the
	//    directory. The subdir well's child is qualified to the fs plugin too,
	//    so descent stays inside the plugin.
	g, err := cl.GetGrid(ctx, child)
	if err != nil {
		t.Fatalf("GetGrid child: %v", err)
	}
	byName := map[string]rpc.Tile{}
	for _, tile := range g.Tiles {
		byName[tile.AltText] = tile
	}
	alpha, ok := byName["alpha.txt"]
	if !ok {
		t.Fatalf("alpha.txt tile missing; got %v", byName)
	}
	sub, ok := byName["subdir"]
	if !ok {
		t.Fatal("subdir tile missing")
	}
	if sub.Kind != rpc.KindWell {
		t.Errorf("subdir kind = %q, want well", sub.Kind)
	}
	if !strings.HasPrefix(sub.ChildGridID, fsPluginUUID+"/") {
		t.Errorf("subdir child_grid_id = %q, want %q prefix", sub.ChildGridID, fsPluginUUID)
	}

	// 2b. The file tile's descent body (its metadata) routes to the plugin.
	body, err := cl.GetTileContent(ctx, alpha.ID)
	if err != nil {
		t.Fatalf("GetTileContent: %v", err)
	}
	if !strings.Contains(string(body), "alpha.txt") {
		t.Errorf("file content %q does not mention the file name", body)
	}

	// 3. Move alpha.txt and confirm the new position survives a re-descent.
	moved, err := cl.MoveTile(ctx, &rpc.MoveTileRequest{
		Path:       rpc.Path{WellIDs: []string{well.ID}},
		TileID:     alpha.ID,
		DestGridID: child,
		X:          5, Y: 6,
	})
	if err != nil {
		t.Fatalf("MoveTile: %v", err)
	}
	if moved.X != 5 || moved.Y != 6 {
		t.Errorf("moved tile at (%d,%d), want (5,6)", moved.X, moved.Y)
	}
	if moved.ID != alpha.ID {
		t.Errorf("move changed id %s→%s (must never re-row)", alpha.ID, moved.ID)
	}
	g2, err := cl.GetGrid(ctx, child)
	if err != nil {
		t.Fatalf("GetGrid after move: %v", err)
	}
	for _, tile := range g2.Tiles {
		if tile.AltText == "alpha.txt" && (tile.X != 5 || tile.Y != 6) {
			t.Errorf("alpha.txt position not persisted: (%d,%d), want (5,6)", tile.X, tile.Y)
		}
	}

	// 4. Delete alpha.txt: the file is removed from disk and swept from the grid.
	if err := cl.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path:   rpc.Path{WellIDs: []string{well.ID}},
		TileID: alpha.ID,
	}); err != nil {
		t.Fatalf("DeleteTile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "alpha.txt")); !os.IsNotExist(err) {
		t.Errorf("alpha.txt still on disk after delete (err=%v)", err)
	}
	g3, err := cl.GetGrid(ctx, child)
	if err != nil {
		t.Fatalf("GetGrid after delete: %v", err)
	}
	for _, tile := range g3.Tiles {
		if tile.AltText == "alpha.txt" {
			t.Error("alpha.txt still present in grid after delete")
		}
	}
}
