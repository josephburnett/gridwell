package server

import (
	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"google.golang.org/protobuf/proto"
)

// The id codec — QualifyID, SplitID, UUIDOf — lives once, in api/rpc. This
// file only applies it to namespace responses. Do not re-implement the split
// or join here; a local copy is how the format drifts.

// qualifyGrid rewrites all IDs in a proto Grid returned by a plugin so they
// are globally qualified with the plugin's UUID.
func qualifyGrid(uuid string, g *pb.Grid) *pb.Grid {
	if g == nil {
		return nil
	}
	out := proto.Clone(g).(*pb.Grid)
	out.Id = rpc.QualifyID(uuid, g.Id)
	return out
}

// qualifyTiles rewrites every id in a slice of proto Tiles returned by a
// namespace so they are globally qualified with that namespace's uuid.
// ChildGridId is qualified only when it is not already a qualified
// cross-plugin reference, that is, when it contains no "/".
//
// A child that arrived already qualified is a cross-plugin reference, so the
// well is a link rather than owned content: a mounted plugin, a file or
// process well, a cross-plugin clone. The same "already qualified" fact is
// what the store's delete and clone key on — a qualified child never cascades
// and is shared, never duplicated — so surfacing it here as Tile.reference
// makes one authoritative "is a link" signal that render and store both read
// and cannot disagree on. A same-namespace mount is still a reference even
// though its child uuid matches the grid uuid, which a bare uuid comparison
// would miss; "arrived qualified" catches it.
func qualifyTiles(uuid string, tiles []*pb.Tile) []*pb.Tile {
	out := make([]*pb.Tile, len(tiles))
	for i, t := range tiles {
		qt := proto.Clone(t).(*pb.Tile)
		qt.Id = rpc.QualifyID(uuid, t.Id)
		qt.GridId = rpc.QualifyID(uuid, t.GridId)
		if t.ChildGridId != "" {
			if _, _, already := rpc.SplitID(t.ChildGridId); already {
				qt.Reference = true
			} else {
				qt.ChildGridId = rpc.QualifyID(uuid, t.ChildGridId)
			}
		}
		// A leaf link — a text, url, shell, or pane tile with a
		// link_target_id — is a reference by construction: the store accepts
		// only a qualified target, so there is no bare-id arm, and the one
		// derived Reference bit covers both link shapes.
		if t.LinkTargetId != "" {
			qt.Reference = true
		}
		out[i] = qt
	}
	return out
}

// qualifyTilesTransit rewrites ids from a transit namespace: a mount of a
// whole remote node. The rule lives in api/rpc, as rpc.TransitQualifyTiles,
// because the transport's per-connection sub-namespace applies the same
// prepend one level down. One implementation, so the hops cannot disagree
// about chain shape.
func qualifyTilesTransit(uuid string, tiles []*pb.Tile) []*pb.Tile {
	return rpc.TransitQualifyTiles(uuid, tiles)
}

// qualifyTilesFor picks the per-namespace qualification rule: a transit
// namespace prepends onto chains and trusts the wire Reference bit, while a
// leaf gets bare-id qualification and reference derivation.
func qualifyTilesFor(transit bool, uuid string, tiles []*pb.Tile) []*pb.Tile {
	if transit {
		return qualifyTilesTransit(uuid, tiles)
	}
	return qualifyTiles(uuid, tiles)
}
