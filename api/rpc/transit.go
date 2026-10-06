package rpc

import (
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The id codec of a hop: outbound, a hop that fronts a whole namespace prepends
// one segment to ids already qualified from the far side; inbound, it peels
// that segment off every id it forwards. Two layers apply both directions, so
// they live here and the hops cannot disagree about a chain's shape.

// tileIDFields is every id a Tile carries, the one list both directions walk.
// An optional id stays empty rather than qualifying to a bare prefix. A ref
// names something elsewhere, a well's child or a leaf link's target: a leaf
// keeps it qualified, because arriving qualified is what makes it a link.
var tileIDFields = []struct {
	field         func(*pb.Tile) *string
	optional, ref bool
}{
	{func(t *pb.Tile) *string { return &t.Id }, false, false},
	{func(t *pb.Tile) *string { return &t.GridId }, false, false},
	{func(t *pb.Tile) *string { return &t.ChildGridId }, true, true},
	{func(t *pb.Tile) *string { return &t.LinkTargetId }, true, true},
	{func(t *pb.Tile) *string { return &t.ShellSession }, true, false},
}

func qualifyField(prefix string, id *string, optional bool) {
	if *id != "" || !optional {
		*id = QualifyID(prefix, *id)
	}
}

// TransitQualifyTiles prepends prefix to every id in a tile, an
// already-qualified child included. The wire Reference bit rides verbatim,
// because the far side already decided what is a link and a remote plugin's
// interior well stays owned though its child id contains "/".
func TransitQualifyTiles(prefix string, tiles []*pb.Tile) []*pb.Tile {
	out := make([]*pb.Tile, len(tiles))
	for i, t := range tiles {
		qt := proto.Clone(t).(*pb.Tile)
		for _, f := range tileIDFields {
			qualifyField(prefix, f.field(qt), f.optional)
		}
		out[i] = qt
	}
	return out
}

// QualifyOwnIDs qualifies, in place, every id in t that names something in
// t's own namespace. The refs are the leaf's to judge, since one that arrives
// qualified is a link.
func QualifyOwnIDs(uuid string, t *pb.Tile) {
	for _, f := range tileIDFields {
		if !f.ref {
			qualifyField(uuid, f.field(t), f.optional)
		}
	}
}

// Hop is one inbound peel, the inverse of the prepend. Seg is the segment the
// hop removes, and only from an id served through the namespace chain Via; any
// other id crosses verbatim, to be refused behind the hop rather than
// misresolved. Transit says the namespace behind the hop fronts other nodes'
// chains, where a reference is peeled like every id.
type Hop struct {
	Seg, Via string
	Transit  bool
}

// InboundHop is the peel for a request routed on the qualified id routed, at
// the node nodeID. Via is OwnerNamespaceOf's answer, so behind a node a
// reference is peeled only when it chains through the same connection as the
// routed id. A connection passes nodeID "": its namespace is the first segment.
func InboundHop(routed, nodeID string, transit bool) Hop {
	return Hop{Seg: UUIDOf(routed), Via: OwnerNamespaceOf(routed, nodeID), Transit: transit}
}

// PeelID removes the hop's segment from an id served through Via.
func (h Hop) PeelID(id string) string {
	if h.Seg != "" && ChainedThrough(id, h.Via) {
		return id[len(h.Seg)+1:]
	}
	return id
}

// PeelTile returns a copy of t with every id in tileIDFields peeled, a ref
// only at a transit hop.
func (h Hop) PeelTile(t *pb.Tile) *pb.Tile {
	if t == nil {
		return nil
	}
	out := proto.Clone(t).(*pb.Tile)
	for _, f := range tileIDFields {
		if !f.ref || h.Transit {
			id := f.field(out)
			*id = h.PeelID(*id)
		}
	}
	return out
}

// PeelSearchQuery peels an id: selector through the one grammar parser.
func (h Hop) PeelSearchQuery(query string) string {
	q := ParseSearchQuery(query)
	if q.ID == "" {
		return query
	}
	return "id:" + h.PeelID(q.ID)
}

// PeelRequest returns a copy of a request with every id it carries peeled, the
// routed one included. A request carrying one id is its route's alone and
// comes back an unchanged copy.
func PeelRequest[T proto.Message](h Hop, req T) T {
	out := proto.Clone(req).(T)
	switch r := any(out).(type) {
	case *pb.CreateTileRequest:
		r.GridId = h.PeelID(r.GridId)
		r.Tile = h.PeelTile(r.Tile)
	case *pb.PlaceTileRequest:
		r.TileId = h.PeelID(r.TileId)
		r.GridId = h.PeelID(r.GridId)
	case *pb.CloneTileRequest:
		r.TileId = h.PeelID(r.TileId)
		r.DestGridId = h.PeelID(r.DestGridId)
	case *pb.SetTileRequest:
		r.TileId = h.PeelID(r.TileId)
		r.Tile = h.PeelTile(r.Tile)
	case *pb.SetFramingRequest:
		r.TileId = h.PeelID(r.TileId)
		r.RootGridId = h.PeelID(r.RootGridId)
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
// caller's tile rule, the one place leaf and transit differ, and puts the hop's
// segment, prefix, before every skipped namespace.
func QualifySearchResponse(prefix string, resp *pb.SearchResponse, qualifyTiles func([]*pb.Tile) []*pb.Tile) *pb.SearchResponse {
	out := &pb.SearchResponse{Results: make([]*pb.SearchResult, 0, len(resp.Results))}
	for _, s := range resp.Skipped {
		out.Skipped = append(out.Skipped, &pb.SearchSkip{Namespace: QualifyNS(prefix, s.Namespace), Reason: s.Reason})
	}
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
	case *pb.Event_GridFramingChanged:
		f := proto.Clone(p.GridFramingChanged).(*pb.GridFramingChanged)
		f.GridId = QualifyID(prefix, f.GridId)
		return &pb.Event{Payload: &pb.Event_GridFramingChanged{GridFramingChanged: f}}
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
		h := proto.Clone(p.PluginHealth).(*pb.EventPluginHealth)
		h.PluginUuid = prefix
		if p.PluginHealth.PluginUuid != "" {
			h.PluginUuid = QualifyID(prefix, p.PluginHealth.PluginUuid)
		}
		return &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: h}}
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
	}
	if resp.HomeGridId != "" {
		out.HomeGridId = QualifyID(prefix, resp.HomeGridId)
	}
	for _, p := range resp.Plugins {
		q := &pb.PluginInfo{
			Uuid:         QualifyID(prefix, p.Uuid),
			Kind:         p.Kind,
			Label:        p.Label,
			RootViewCx:   p.RootViewCx,
			RootViewCy:   p.RootViewCy,
			RootViewZoom: p.RootViewZoom,
			InfoError:    p.InfoError,
			Glyph:        p.Glyph,
		}
		if p.RootGridId != "" {
			q.RootGridId = QualifyID(prefix, p.RootGridId)
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
			ViewOf(c.RootViewCx, c.RootViewCy, c.RootViewZoom)))
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
