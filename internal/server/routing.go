package server

import (
	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"google.golang.org/protobuf/proto"
)

// The id codec lives once, in api/rpc; this file only applies it to namespace
// responses.

// qualifyGrid qualifies every id in a Grid with the plugin's uuid.
func qualifyGrid(uuid string, g *pb.Grid) *pb.Grid {
	if g == nil {
		return nil
	}
	out := proto.Clone(g).(*pb.Grid)
	out.Id = rpc.QualifyID(uuid, g.Id)
	return out
}

// qualifyTiles qualifies every id in a namespace's tiles. A ChildGridId that
// arrived already qualified is a cross-plugin reference, so the well is a link
// and is left alone; surfacing that here as Tile.reference gives render and
// store one "is a link" signal. A bare uuid comparison would miss a
// same-namespace mount, which "arrived qualified" catches.
func qualifyTiles(uuid string, tiles []*pb.Tile) []*pb.Tile {
	out := make([]*pb.Tile, len(tiles))
	for i, t := range tiles {
		qt := proto.Clone(t).(*pb.Tile)
		rpc.QualifyOwnIDs(uuid, qt)
		if t.ChildGridId != "" {
			if _, _, already := rpc.SplitID(t.ChildGridId); already {
				qt.Reference = true
			} else {
				qt.ChildGridId = rpc.QualifyID(uuid, t.ChildGridId)
			}
		}
		// A leaf link is a reference by construction, however its target is
		// spelled, so the one derived Reference bit covers both link shapes.
		if t.LinkTargetId != "" {
			qt.LinkTargetId = rpc.QualifyLinkTarget(uuid, t.LinkTargetId)
			qt.Reference = true
		}
		out[i] = qt
	}
	return out
}

// qualifyTilesTransit rewrites ids from a transit namespace; the rule lives in
// api/rpc because the transport applies the same prepend one level down.
func qualifyTilesTransit(uuid string, tiles []*pb.Tile) []*pb.Tile {
	return rpc.TransitQualifyTiles(uuid, tiles)
}

// qualifyTilesFor picks the rule: transit prepends onto chains and trusts the
// wire Reference bit, a leaf qualifies bare ids and derives the bit.
func qualifyTilesFor(transit bool, uuid string, tiles []*pb.Tile) []*pb.Tile {
	if transit {
		return qualifyTilesTransit(uuid, tiles)
	}
	return qualifyTiles(uuid, tiles)
}
