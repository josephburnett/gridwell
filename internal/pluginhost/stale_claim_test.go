package pluginhost

import (
	"context"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A plugin's row claims what the writer saw exactly as home's does: a tile
// behaves the same wherever its content comes from. Its version stays 0.

func claimAdapter(t *testing.T) (*Adapter, map[string]*gridwellv1.Tile) {
	t.Helper()
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, closer, err := plugintest.Loopback(textAndURLPlugin{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	a := New(cp, memStore.Namespace("p1"), nil)
	info, err := a.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	listing, err := a.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: info.PluginGrids[0].GridId})
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]*gridwellv1.Tile{}
	for _, tile := range listing.Tiles {
		rows[tile.Kind] = tile
	}
	return a, rows
}

func wantConflict(t *testing.T, what string, err error) {
	t.Helper()
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("%s: got %v, want the version conflict", what, err)
	}
}

func TestAPluginRowsPlacementClaimsWhatTheWriterSaw(t *testing.T) {
	ctx := context.Background()
	a, rows := claimAdapter(t)
	row := rows[rpc.KindText]
	seen := &gridwellv1.Placement{GridId: row.GridId, X: row.X, Y: row.Y, W: row.W, H: row.H}
	moved, err := a.MoveTile(ctx, &gridwellv1.MoveTileRequest{TileId: row.Id, X: 9, Y: 9, W: 1, H: 1, Claim: seen})
	if err != nil {
		t.Fatalf("first move: %v", err)
	}
	_, err = a.MoveTile(ctx, &gridwellv1.MoveTileRequest{TileId: moved.Tile.Id, X: 11, Y: 9, W: 1, H: 1, Claim: seen})
	wantConflict(t, "stale move", err)
	got, err := a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: moved.Tile.Id})
	if err != nil {
		t.Fatal(err)
	}
	if got.Tile.X != 9 || got.Tile.Version != 0 {
		t.Fatalf("row at x=%d version %d, want the first move's 9 at version 0", got.Tile.X, got.Tile.Version)
	}
}

func TestAPluginRowsDeleteClaimsWhatTheWriterSaw(t *testing.T) {
	ctx := context.Background()
	a, rows := claimAdapter(t)
	row := rows[rpc.KindText]
	seen := &gridwellv1.Placement{GridId: row.GridId, X: row.X, Y: row.Y, W: row.W, H: row.H}
	moved, err := a.MoveTile(ctx, &gridwellv1.MoveTileRequest{TileId: row.Id, X: 9, Y: 9, W: 1, H: 1, Claim: seen})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: moved.Tile.Id, Claim: seen})
	wantConflict(t, "delete over a move it never saw", err)
	if _, err := a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: moved.Tile.Id}); err != nil {
		t.Fatalf("the refused row did not stand: %v", err)
	}
	now := &gridwellv1.Placement{GridId: row.GridId, X: 9, Y: 9, W: 1, H: 1}
	// This plugin serves no Delete, so the claim passing is the source's
	// own answer, never the conflict.
	_, err = a.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: moved.Tile.Id, Claim: now})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("delete of what the writer saw: %v, want it handed to the plugin", err)
	}
}

func TestAPluginRowsFreezeClaimsWhatTheWriterSaw(t *testing.T) {
	ctx := context.Background()
	a, rows := claimAdapter(t)
	page := rows[rpc.KindURL]
	no, yes := false, true
	frozen, err := a.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: page.Id, UrlFrozen: &yes, ClaimFrozen: &no})
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	id := frozen.Tile.Id
	if _, err := a.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: id, UrlFrozen: &no, ClaimFrozen: &yes}); err != nil {
		t.Fatalf("unfreeze: %v", err)
	}
	_, err = a.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: id, UrlFrozen: &yes, ClaimFrozen: &yes})
	wantConflict(t, "freeze over an unfreeze it never saw", err)
	got, err := a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.Tile.UrlFrozen || got.Tile.Version != 0 {
		t.Fatalf("frozen=%v version %d, want the unfreeze standing at version 0", got.Tile.UrlFrozen, got.Tile.Version)
	}
}
