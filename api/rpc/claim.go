package rpc

import (
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// UnclaimedWrite is why req, a write of something the user made or placed,
// may not cross an API: it names no claim. It answers "" for a request that
// carries its claim or claims nothing. Both APIs refuse what it names and
// Client never sends it; the node's own writers (a reap, a sweep) call their
// namespace directly and never meet it. WriteContent and a rename claim by
// shape, their version being a plain number every write carries. A select is
// the decided exception: it names what it adds and removes, and claims
// nothing.
func UnclaimedWrite(req proto.Message) string {
	switch r := req.(type) {
	case *pb.MoveTileRequest:
		if r.GetClaim() == nil {
			return "MoveTile names no claim: a move claims the placement it saw"
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

// CopyFacts is what a copy of t carries beside its bytes, name, placement and
// framing: store.insertTileCopy writes it, and CopyWrites across a namespace.
func CopyFacts(t *pb.Tile) *pb.Tile {
	return &pb.Tile{Kind: t.Kind, TextX: t.TextX, TextY: t.TextY, TextMode: t.TextMode,
		ContentZoom: t.ContentZoom, UrlHistory: t.UrlHistory, UrlFrozen: t.UrlFrozen}
}

// CopyWrites are the writes that put c, a CopyFacts, onto a fresh row, one
// SetTile arm each: preview, the source's picture, rides the capture arm, and
// the freeze claims the fresh row's unfrozen.
func CopyWrites(c *pb.Tile, preview []byte) []*pb.SetTileRequest {
	var out []*pb.SetTileRequest
	if c.TextX != 0 || c.TextY != 0 || c.TextMode != "" {
		out = append(out, &pb.SetTileRequest{Tile: &pb.Tile{Kind: c.Kind, TextX: c.TextX, TextY: c.TextY, TextMode: c.TextMode}})
	}
	if c.ContentZoom != 0 {
		out = append(out, &pb.SetTileRequest{ContentZoom: proto.Float64(c.ContentZoom)})
	}
	if c.UrlHistory != "" || len(preview) > 0 {
		out = append(out, &pb.SetTileRequest{Tile: &pb.Tile{Kind: c.Kind, UrlHistory: c.UrlHistory}, Preview: preview})
	}
	if c.UrlFrozen {
		out = append(out, &pb.SetTileRequest{UrlFrozen: proto.Bool(true), ClaimFrozen: proto.Bool(false)})
	}
	return out
}
