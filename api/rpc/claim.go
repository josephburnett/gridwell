package rpc

import (
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// UnclaimedWrite is why req, a write of something the user made or placed,
// may not cross a door: it names no claim. It answers "" for a request that
// carries its claim or claims nothing. Both doors refuse what it names and
// Client never sends it; the node's own writers (a reap, a sweep) call their
// namespace directly and never meet it. WriteContent and a rename claim by
// shape, their version being a plain number every write carries. A select is
// the decided exception: it names what it adds and removes, and claims
// nothing.
func UnclaimedWrite(req proto.Message) string {
	switch r := req.(type) {
	case *pb.PlaceTileRequest:
		if r.GetClaim() == nil {
			return "PlaceTile names no claim: a move claims the placement it saw"
		}
	case *pb.DeleteTileRequest:
		if r.GetClaim() == nil {
			return "DeleteTile names no claim: a delete claims the version and placement it saw"
		}
	case *pb.SetTileRequest:
		if r.UrlFrozen != nil && r.ClaimFrozen == nil {
			return "SetTile url_frozen names no claim_frozen: a freeze claims the freeze it saw"
		}
	}
	return ""
}

// DeleteOf is the delete of t as the writer saw it.
func DeleteOf(t *pb.Tile) *pb.DeleteTileRequest {
	return &pb.DeleteTileRequest{TileId: t.GetId(), ClaimVersion: t.GetVersion(), Claim: PlacementOf(t)}
}
