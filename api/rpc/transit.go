package rpc

import (
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The transit qualification rule: a hop that fronts a whole namespace prepends
// one segment to ids already qualified from the far side. Two layers apply it,
// so it lives here and the hops cannot disagree about a chain's shape.

// TransitQualifyTiles prepends prefix to every id in a tile, an
// already-qualified child included. The wire Reference bit rides verbatim,
// because the far side already decided what is a link and a remote plugin's
// interior well stays owned though its child id contains "/".
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
			// A leaf link's target chains exactly like a qualified child.
			qt.LinkTargetId = QualifyID(prefix, t.LinkTargetId)
		}
		out[i] = qt
	}
	return out
}

// TransitQualifyGrid prepends prefix to a Grid's own id, its scratch grid,
// node_ns and its menu entries' targets; everything else rides verbatim,
// because the far node already stamped its plugin's facts.
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

// QualifySearchResponse rewrites every id a search answer carries through the
// caller's tile rule, the one place leaf and transit differ.
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

// QualifyEventIDs prepends prefix to every id in a change event. A health
// event's plugin uuid is an id like any other, so a far namespace's health
// stays addressable here.
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
		// An empty uuid means the namespace this event rode in from: the
		// cache layer reports its own store health without knowing the uuid
		// the registry gave it, and the prefix is exactly that uuid.
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

// TransitQualifyPluginList prepends one hop segment to every id a forwarded
// Handshake response carries. The content token and node identity are zeroed,
// being capabilities of the handshake with the node asked directly;
// shells_disabled and per-plugin InfoError ride verbatim. It is the one reader
// of the retired connections list: a far node built before the fold still
// answers with one, and its rows join plugins here as ConnectionRow's shape.
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
	for _, c := range resp.Connections {
		root := ""
		if c.RootGridId != "" {
			root = QualifyID(prefix, c.RootGridId)
		}
		out.Plugins = append(out.Plugins, ConnectionRow(QualifyID(prefix, c.Uuid), c.Label, root, c.StatusDetail,
			Framing{Cx: c.RootViewCx, Cy: c.RootViewCy, Zoom: c.RootViewZoom}))
	}
	return out
}

// QualifyMenuEntries prepends one hop segment to a plugin's menu entry
// targets, returning fresh values so a hop never mutates what it forwards.
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
