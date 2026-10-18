// Package claimtest owns how a test that is not about the claim names one: it
// reads the row first, as a press would, because both APIs refuse a move or
// delete that names none (rpc.UnclaimedWrite).
package claimtest

import (
	"context"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// TileReader is where a claim is read from: rpc.Client.
type TileReader interface {
	GetTile(ctx context.Context, tileID string) (*pb.Tile, error)
}

// Claimed fills req's claim with the tile as cl reads it now.
func Claimed[R *pb.PlaceTileRequest | *pb.DeleteTileRequest](t testing.TB, cl TileReader, req R) R {
	t.Helper()
	read := func(id string) *pb.Tile {
		row, err := cl.GetTile(context.Background(), id)
		if err != nil {
			t.Fatalf("read %s to claim it: %v", id, err)
		}
		return row
	}
	switch r := any(req).(type) {
	case *pb.PlaceTileRequest:
		r.Claim = rpc.PlacementOf(read(r.TileId))
	case *pb.DeleteTileRequest:
		row := read(r.TileId)
		r.ClaimVersion, r.Claim = row.Version, rpc.PlacementOf(row)
	}
	return req
}
