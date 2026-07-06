package server

import (
	"strings"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// splitPluginID splits a possibly-qualified id of the form "<uuid>/<local>"
// into its two parts. Returns ("", id, false) when the id has no prefix
// (i.e. it belongs to the local store).
func splitPluginID(id string) (uuid, local string, ok bool) {
	if i := strings.IndexByte(id, '/'); i > 0 {
		return id[:i], id[i+1:], true
	}
	return "", "", false
}

// qualifyID returns "<uuid>/<local>".
func qualifyID(uuid, local string) string { return uuid + "/" + local }

// qualifyGrid rewrites all IDs in a proto Grid returned by a plugin so they
// are globally qualified with the plugin's UUID.
func qualifyGrid(uuid string, g *pb.Grid) *pb.Grid {
	if g == nil {
		return nil
	}
	out := *g
	out.Id = qualifyID(uuid, g.Id)
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
		qt.Id = qualifyID(uuid, t.Id)
		qt.GridId = qualifyID(uuid, t.GridId)
		if t.ChildGridId != "" {
			if _, _, already := splitPluginID(t.ChildGridId); already {
				qt.Reference = true
			} else {
				qt.ChildGridId = qualifyID(uuid, t.ChildGridId)
			}
		}
		out[i] = &qt
	}
	return out
}

// qualifyTilesTransit rewrites ids from a TRANSIT plugin (a node mount — the
// ssh plugin proxying a whole remote gridwell). A transit plugin speaks ids
// already qualified from the REMOTE node's perspective (chains), so every id
// gets this hop's uuid prepended — including an already-qualified child, which
// is a reference within the remote's namespace reachable only through this
// connection. The wire Reference bit is trusted verbatim: the remote node
// already decided what is a link and what is owned content, and a remote
// plugin's interior well must stay solid (owned) even though its child id
// contains "/". Chains compose: each hop prepends exactly one segment.
func qualifyTilesTransit(uuid string, tiles []*pb.Tile) []*pb.Tile {
	out := make([]*pb.Tile, len(tiles))
	for i, t := range tiles {
		qt := *t
		qt.Id = qualifyID(uuid, t.Id)
		qt.GridId = qualifyID(uuid, t.GridId)
		if t.ChildGridId != "" {
			qt.ChildGridId = qualifyID(uuid, t.ChildGridId)
		}
		out[i] = &qt
	}
	return out
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
