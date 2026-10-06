package server

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// TestFileWellLifecycleE2E exercises the whole new path through the public RPC
// surface: create a file well (Attach to the fs plugin), descend (GetGrid
// lists the directory), rearrange a tile (MoveTile persists across a
// re-descent), and delete a tile (the file is removed and swept from the
// grid). It is the end-to-end proof that file wells work through the plugin
// boundary and that the primary rule — things stay where you left them — holds
// across it.
func TestFileWellLifecycleE2E(t *testing.T) {
	cl, root, dir := newTestServerWithPlugins(t)
	if err := os.WriteFile(filepath.Join(dir, "alpha.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// 1. Mount the fs plugin (rooted at dir) by cloning its node-grid tile —
	//    the UI's right-drag gesture. It is a plain well in the local store
	//    whose child grid lives in the fs plugin.
	well := mountByClone(t, cl, fsPluginUUID, root, 0, 0)
	child := well.ChildGridId
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
	byName := map[string]*gridwellv1.Tile{}
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
	if !strings.HasPrefix(sub.ChildGridId, fsPluginUUID+"/") {
		t.Errorf("subdir child_grid_id = %q, want %q prefix", sub.ChildGridId, fsPluginUUID)
	}

	// 2b. The file tile's descent body routes to the plugin — and since the
	// content-types program, a .txt file's body is the FILE
	// ITSELF, verbatim, not a metadata summary.
	body, media, _, err := cl.ReadContent(ctx, alpha.Id)
	if err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	if string(body) != "a" || media != "text/plain" {
		t.Errorf("file content = (%q, %q), want the file's own bytes as text/plain", body, media)
	}

	// 3. Move alpha.txt and confirm the new position survives a re-descent.
	moved, err := cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: alpha.Id,
		GridId: child,
		X:      5, Y: 6, W: alpha.W, H: alpha.H,
	})
	if err != nil {
		t.Fatalf("MoveTile: %v", err)
	}
	if moved.X != 5 || moved.Y != 6 {
		t.Errorf("moved tile at (%d,%d), want (5,6)", moved.X, moved.Y)
	}
	// The move is the durable touch that mints the row, so the tile is named
	// by its row id from here on. The address the client was holding still
	// resolves to the same tile — nothing the user has is invalidated — and
	// from here the id never changes again.
	if held, err := cl.GetTile(ctx, alpha.Id); err != nil || held.Id != moved.Id {
		t.Errorf("the pre-mint address stopped resolving: %+v (%v), want %s", held, err, moved.Id)
	}
	again, err := cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: moved.Id, GridId: child, X: 5, Y: 6, W: alpha.W, H: alpha.H})
	if err != nil {
		t.Fatalf("second move: %v", err)
	}
	if again.Id != moved.Id {
		t.Errorf("move changed id %s→%s (must never re-row)", moved.Id, again.Id)
	}
	alpha.Id = moved.Id
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
	if _, err := cl.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{
		TileId: alpha.Id,
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
