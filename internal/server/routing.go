package server

import (
	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The id codec (QualifyID / SplitID / UUIDOf) lives once, in internal/rpc —
// this file only APPLIES it to plugin responses. Do not re-implement the
// split or join here; a local copy is how the format drifts.

// qualifyGrid rewrites all IDs in a proto Grid returned by a plugin so they
// are globally qualified with the plugin's UUID.
func qualifyGrid(uuid string, g *pb.Grid) *pb.Grid {
	if g == nil {
		return nil
	}
	out := *g
	out.Id = rpc.QualifyID(uuid, g.Id)
	return &out
}

// qualifyTiles rewrites all IDs in a slice of proto Tiles returned by a
// plugin so they are globally qualified with the plugin's UUID. ChildGridId
// is only qualified if it is not already a qualified cross-plugin reference
// (i.e. it does not already contain a "/").
//
// A child that arrived ALREADY qualified is a cross-plugin reference — the
// well is a LINK, not owned content (a mounted plugin, a file/process well, a
// cross-plugin clone). That same "already qualified" fact is what the store's
// delete/clone key on (a qualified child never cascades / is shared, never
// duplicated), so surfacing it here as Tile.reference is the one authoritative
// "is a link" signal both render and store read — they can't disagree. Note a
// same-plugin mount (the localdb mounted into its own grid) is still a
// reference even though its child uuid matches the grid uuid, which a bare
// uuid comparison (IsExitWell) would miss; "arrived qualified" catches it.
func qualifyTiles(uuid string, tiles []*pb.Tile) []*pb.Tile {
	out := make([]*pb.Tile, len(tiles))
	for i, t := range tiles {
		qt := *t
		qt.Id = rpc.QualifyID(uuid, t.Id)
		qt.GridId = rpc.QualifyID(uuid, t.GridId)
		if t.ChildGridId != "" {
			if _, _, already := rpc.SplitID(t.ChildGridId); already {
				qt.Reference = true
			} else {
				qt.ChildGridId = rpc.QualifyID(uuid, t.ChildGridId)
			}
		}
		// A leaf link (text/url/shell/pane with a link_target_id) is a
		// reference by construction: the store only accepts a QUALIFIED
		// target, so there is no bare-id arm — the same one derived
		// Reference bit covers both link shapes (exit well, leaf link).
		if t.LinkTargetId != "" {
			qt.Reference = true
		}
		out[i] = &qt
	}
	return out
}

// qualifyTilesTransit rewrites ids from a TRANSIT plugin (a node mount — the
// ssh plugin proxying a whole remote gridwell). The rule itself lives in
// internal/rpc (rpc.TransitQualifyTiles) because the ssh plugin's
// per-connection sub-namespace applies the SAME prepend one level down —
// one implementation, so the hops can never disagree about chain shape.
func qualifyTilesTransit(uuid string, tiles []*pb.Tile) []*pb.Tile {
	return rpc.TransitQualifyTiles(uuid, tiles)
}

// qualifyTilesFor picks the per-plugin qualification rule: transit plugins
// (node mounts) prepend onto chains and trust the wire Reference bit; leaf
// plugins get bare-int qualification and reference derivation.
func qualifyTilesFor(transit bool, uuid string, tiles []*pb.Tile) []*pb.Tile {
	if transit {
		return qualifyTilesTransit(uuid, tiles)
	}
	return qualifyTiles(uuid, tiles)
}
