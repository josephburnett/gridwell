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
func qualifyTiles(uuid string, tiles []*pb.Tile) []*pb.Tile {
	out := make([]*pb.Tile, len(tiles))
	for i, t := range tiles {
		qt := *t
		qt.Id = qualifyID(uuid, t.Id)
		qt.GridId = qualifyID(uuid, t.GridId)
		if t.ChildGridId != "" {
			if _, _, already := splitPluginID(t.ChildGridId); !already {
				qt.ChildGridId = qualifyID(uuid, t.ChildGridId)
			}
		}
		out[i] = &qt
	}
	return out
}
