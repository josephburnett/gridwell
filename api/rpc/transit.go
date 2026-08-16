package rpc

import (
	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The TRANSIT qualification rule — how a hop that fronts a whole namespace
// (a node mount's server-side stamp, or the ssh plugin's per-connection
// sub-namespace) prepends one segment to ids that are already qualified from
// the far side's perspective. It lives here, next to the id codec, because
// two layers apply it: the server (qualifyTilesTransit / qualifyEvent) and
// the multi-connection ssh plugin, which peels a connection segment exactly
// as a node peels a plugin segment. One implementation, so the two hops can
// never disagree about what a chain looks like.

// TransitQualifyTiles rewrites ids from a transit namespace: every id gets
// prefix prepended — including an already-qualified child, which is a
// reference within the far namespace reachable only through this hop. The
// wire Reference bit is trusted verbatim: the far side already decided what
// is a link and what is owned content, and a remote plugin's interior well
// must stay solid (owned) even though its child id contains "/". Chains
// compose: each hop prepends exactly one segment.
func TransitQualifyTiles(prefix string, tiles []*pb.Tile) []*pb.Tile {
	out := make([]*pb.Tile, len(tiles))
	for i, t := range tiles {
		qt := *t
		qt.Id = QualifyID(prefix, t.Id)
		qt.GridId = QualifyID(prefix, t.GridId)
		if t.ChildGridId != "" {
			qt.ChildGridId = QualifyID(prefix, t.ChildGridId)
		}
		if t.LinkTargetId != "" {
			// A leaf link's target chains exactly like a qualified child: the
			// far side's "<uuid>/<tile>" is reachable only through this hop,
			// so prepend one segment. The wire Reference bit rides verbatim.
			qt.LinkTargetId = QualifyID(prefix, t.LinkTargetId)
		}
		out[i] = &qt
	}
	return out
}

// QualifyEventIDs prepends prefix to every id in a change event, applying
// qualifyTile to a TileChanged payload (the one place the leaf and transit
// rules differ — the caller injects its rule). GridId/TileId are plain
// prepends either way — chains compose by concatenation. The plugin uuid in
// a health event is an id like any other: one segment prepended per hop, so
// a far namespace's health transitions stay addressable on this side.
func QualifyEventIDs(prefix string, ev *pb.Event, qualifyTile func(*pb.Tile) *pb.Tile) *pb.Event {
	switch p := ev.Payload.(type) {
	case *pb.Event_GridChanged:
		return &pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{
			GridId: QualifyID(prefix, p.GridChanged.GridId),
		}}}
	case *pb.Event_TileChanged:
		return &pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{
			Tile: qualifyTile(p.TileChanged.Tile),
		}}}
	case *pb.Event_TileRemoved:
		return &pb.Event{Payload: &pb.Event_TileRemoved{TileRemoved: &pb.TileRemoved{
			GridId: QualifyID(prefix, p.TileRemoved.GridId),
			TileId: QualifyID(prefix, p.TileRemoved.TileId),
		}}}
	case *pb.Event_PluginHealth:
		return &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{
			PluginUuid: QualifyID(prefix, p.PluginHealth.PluginUuid),
			Healthy:    p.PluginHealth.Healthy,
			Detail:     p.PluginHealth.Detail,
		}}}
	}
	return ev
}

// TransitQualifyEvent applies the transit rule to a whole event: ids
// prepended, tiles by TransitQualifyTiles.
func TransitQualifyEvent(prefix string, ev *pb.Event) *pb.Event {
	return QualifyEventIDs(prefix, ev, func(t *pb.Tile) *pb.Tile {
		return TransitQualifyTiles(prefix, []*pb.Tile{t})[0]
	})
}
