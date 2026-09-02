package rpc

import (
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The transit qualification rule: how a hop that fronts a whole namespace
// prepends one segment to ids that are already qualified from the far
// side's perspective. It lives here, next to the id codec, because two
// layers apply it — the server's transit stamp and the connection transport,
// which peels a connection segment exactly as a node peels a plugin
// segment. One implementation, so the two hops can never disagree about
// what a chain looks like.

// TransitQualifyTiles rewrites ids from a transit namespace: every id gets
// prefix prepended, including an already-qualified child, which is a
// reference within the far namespace reachable only through this hop. The
// wire Reference bit is trusted verbatim — the far side already decided
// what is a link and what is owned content, and a remote plugin's interior
// well must stay owned even though its child id contains "/". Chains
// compose: each hop prepends exactly one segment.
func TransitQualifyTiles(prefix string, tiles []*pb.Tile) []*pb.Tile {
	out := make([]*pb.Tile, len(tiles))
	for i, t := range tiles {
		qt := proto.Clone(t).(*pb.Tile)
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
		out[i] = qt
	}
	return out
}

// TransitQualifyGrid rewrites a Grid's ids from a transit namespace: the
// grid's own id, its scratch grid, node_ns (the serving node is one segment
// further away), and the menu entries' root targets. Everything else rides
// verbatim, because the far node already stamped its owning plugin's facts.
// It is the one grid rule for both hops, the server's transit stamp and the
// connection transport.
func TransitQualifyGrid(prefix string, g *pb.Grid) *pb.Grid {
	if g == nil {
		return nil
	}
	out := proto.Clone(g).(*pb.Grid)
	out.Id = QualifyID(prefix, g.Id)
	if g.ScratchGridId != "" {
		out.ScratchGridId = QualifyID(prefix, g.ScratchGridId)
	}
	out.NodeNs = QualifyNS(prefix, g.NodeNs)
	out.MenuEntries = QualifyMenuEntries(prefix, g.MenuEntries)
	return out
}

// QualifySearchResponse rewrites every id a search answer carries —
// result tiles and their path chains alike — through the caller's tile
// rule (the one place leaf and transit differ, injected exactly like
// QualifyEventIDs). One implementation for the server's fan-out and the
// builtin transport's per-connection prepend.
func QualifySearchResponse(resp *pb.SearchResponse, qualifyTiles func([]*pb.Tile) []*pb.Tile) *pb.SearchResponse {
	out := &pb.SearchResponse{Results: make([]*pb.SearchResult, 0, len(resp.Results))}
	for _, r := range resp.Results {
		qr := &pb.SearchResult{Snippet: r.Snippet, Score: r.Score}
		if r.Tile != nil {
			qr.Tile = qualifyTiles([]*pb.Tile{r.Tile})[0]
		}
		qr.Path = qualifyTiles(r.Path)
		out.Results = append(out.Results, qr)
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
		// An empty uuid means "the namespace this event rode in from": the
		// cache layer reports its own store health without knowing the uuid
		// the registry gave it, and the fan-in's prefix is exactly that uuid.
		uuid := prefix
		if p.PluginHealth.PluginUuid != "" {
			uuid = QualifyID(prefix, p.PluginHealth.PluginUuid)
		}
		return &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{
			PluginUuid: uuid,
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

// TransitQualifyPluginList re-qualifies a forwarded Handshake response with
// one hop segment: every id the answer carries — the plugin namespaces
// themselves and their grid addresses — gains the hop prefix, so the
// receiving side holds ids routable from its own perspective. Node-local
// fields are zeroed: the content token, node identity, and node view answer
// only for the node asked directly, and are capabilities of that handshake,
// meaningless and unsafe to forward through a chain. shells_disabled and
// per-plugin InfoError ride verbatim, because they describe the answering
// node. Chains compose: each hop calls this once.
func TransitQualifyPluginList(prefix string, resp *pb.HandshakeResponse) *pb.HandshakeResponse {
	if resp == nil {
		return nil
	}
	out := &pb.HandshakeResponse{
		ShellsDisabled: resp.ShellsDisabled,
		HomeViewCx:     resp.HomeViewCx,
		HomeViewCy:     resp.HomeViewCy,
		HomeViewZoom:   resp.HomeViewZoom,
	}
	if resp.HomeGridId != "" {
		out.HomeGridId = QualifyID(prefix, resp.HomeGridId)
	}
	for _, c := range resp.Connections {
		q := &pb.ConnectionInfo{
			Uuid: QualifyID(prefix, c.Uuid), Label: c.Label, StatusDetail: c.StatusDetail,
			RootViewCx: c.RootViewCx, RootViewCy: c.RootViewCy, RootViewZoom: c.RootViewZoom,
		}
		if c.RootGridId != "" {
			q.RootGridId = QualifyID(prefix, c.RootGridId)
		}
		out.Connections = append(out.Connections, q)
	}
	for _, p := range resp.Plugins {
		q := &pb.PluginInfo{
			Uuid:         QualifyID(prefix, p.Uuid),
			Kind:         p.Kind,
			Label:        p.Label,
			Writable:     p.Writable,
			RootViewCx:   p.RootViewCx,
			RootViewCy:   p.RootViewCy,
			RootViewZoom: p.RootViewZoom,
			InfoError:    p.InfoError,
			Glyph:        p.Glyph,
		}
		if p.RootGridId != "" {
			q.RootGridId = QualifyID(prefix, p.RootGridId)
		}
		if p.ScratchGridId != "" {
			q.ScratchGridId = QualifyID(prefix, p.ScratchGridId)
		}
		q.MenuEntries = QualifyMenuEntries(prefix, p.MenuEntries)
		out.Plugins = append(out.Plugins, q)
	}
	return out
}

// QualifyMenuEntries prepends one hop segment to the grid targets of a
// plugin's menu entries. Root entries chain like every other id; creation
// entries ride verbatim. Returns fresh values so a hop never mutates the
// response it forwards.
func QualifyMenuEntries(prefix string, in []*pb.MenuEntry) []*pb.MenuEntry {
	if len(in) == 0 {
		return nil
	}
	out := make([]*pb.MenuEntry, len(in))
	for i, e := range in {
		q := proto.Clone(e).(*pb.MenuEntry)
		if q.GridId != "" {
			q.GridId = QualifyID(prefix, q.GridId)
		}
		out[i] = q
	}
	return out
}
