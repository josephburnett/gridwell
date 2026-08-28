package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// A node-grid write that fails to persist must not leave memory ahead of
// the file: the client would see a placement (or viewport) that vanishes
// on the next restart — the opposite of "things stay as you left them".
// Before the copy-persist-swap shape, SetRootView and PlaceTile mutated
// memory first and only then wrote, so a failed write reported an error
// AND kept serving the unpersisted state for the rest of the session.
func TestNodeGridWriteFailureLeavesMemoryUntouched(t *testing.T) {
	// A state path whose parent is a FILE: every write fails with ENOTDIR
	// (works as root too, unlike a read-only dir).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blocker"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry()
	reg.Register("p1", "home", nil, nil)
	n := &nodeGrid{
		reg:        reg,
		info:       func(context.Context, string) (*pb.InfoResponse, error) { return nil, errors.New("no info") },
		invalidate: func(string) {},
		statePath:  filepath.Join(dir, "blocker", "node-view.json"),
	}
	n.loadView()
	ctx := context.Background()

	before, err := n.GetTile(ctx, &pb.GetTileRequest{TileId: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.PlaceTile(ctx, &pb.PlaceTileRequest{TileId: "p1", X: 7, Y: 9, W: 2, H: 2}); err == nil {
		t.Fatal("PlaceTile succeeded against an unwritable state path")
	}
	after, err := n.GetTile(ctx, &pb.GetTileRequest{TileId: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Tile.X != before.Tile.X || after.Tile.Y != before.Tile.Y || after.Tile.W != before.Tile.W || after.Tile.H != before.Tile.H {
		t.Fatalf("a failed PlaceTile moved the tile in memory: before (%d,%d %dx%d) after (%d,%d %dx%d)",
			before.Tile.X, before.Tile.Y, before.Tile.W, before.Tile.H, after.Tile.X, after.Tile.Y, after.Tile.W, after.Tile.H)
	}

	if _, err := n.SetRootView(ctx, &pb.SetRootViewRequest{RootGridId: nodeGridID, Cx: 3, Cy: 4, Zoom: 2}); err == nil {
		t.Fatal("SetRootView succeeded against an unwritable state path")
	}
	info, err := n.Info(ctx, &pb.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.RootViewCx != 0 || info.RootViewCy != 0 || info.RootViewZoom != 0 {
		t.Fatalf("a failed SetRootView changed the viewport in memory: (%v,%v,%v)", info.RootViewCx, info.RootViewCy, info.RootViewZoom)
	}
}
