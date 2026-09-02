// Package pluginhost adapts a plugin.v1 plugin to the full Gridwell service:
// the node-side half of the split. The adapter joins the plugin's content
// answers — keys, kinds, labels, bytes — with the plugin's namespace of the
// node's store, which holds the ids, the placement, and the framing, and
// registers in the plugin registry, so the router, the client, and federation
// never know the difference.
//
// This is the seam where two owners' facts meet, which is why it stays thin:
// every merge decision lives in the store (store.Namespace, unit-tested),
// every content derivation lives in the plugin, and the adapter only converts
// and forwards. Presentation verbs terminate here; content verbs pass
// through.
//
// Listing writes nothing. A grid's answer is a JOIN: the plugin's List
// supplies the entries, store.Namespace.Overlay lays the minted rows over
// them, and an entry with no row is answered at a derived placement under a
// derived address (see address.go) rather than a row id. A row appears only
// when the user makes a durable fact — a move, a resize, a framing, a
// reference — and that is the one mint, Adapter.mint.
//
// Outages split by whose fact is missing. A dark source — the plugin answers,
// but its directory, its API, or its process table does not — costs only what
// the source says: the adapter overlays an empty non-authoritative listing, so
// every row it minted still reads, with the same ids, placement, and labels,
// stamped stale and retiring nothing. An entry with no row has nothing to
// answer from and is simply absent until the source speaks again. A dark
// plugin, whose subprocess is gone, costs the node-side answer too, and it
// fails honestly: nothing fronts a plugin, because a subprocess on this
// machine is a call away, so there are no remembered answers to serve. The
// durable rows are the node's memory of what it minted; what the source said
// is the source's to say again.
package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// Adapter implements namespace.Namespace over one plugin and its namespace of
// the node's store. The router calls it as a Go value, and the one gRPC hop
// underneath is the plugin.v1 subprocess, the third-party door.
type Adapter struct {
	namespace.Unimplemented
	cp  pluginv1.PluginClient
	mem *store.Namespace
}

// A plugin reaches the router as a Go value; the compiler is what says so.
var _ namespace.Namespace = (*Adapter)(nil)

// New builds the adapter. The caller owns both halves' lifecycles.
func New(cp pluginv1.PluginClient, mem *store.Namespace) *Adapter {
	return &Adapter{cp: cp, mem: mem}
}

// Info translates the plugin handshake, minting the root context's grid id
// and reading its persisted viewport from the store.
func (a *Adapter) Info(ctx context.Context, _ *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	ci, err := a.cp.Info(ctx, &pluginv1.InfoRequest{})
	if err != nil {
		return nil, err
	}
	resp := &gridwellv1.InfoResponse{
		Kind:        ci.Kind,
		DisplayName: ci.DisplayName,
		Glyph:       ci.Glyph,
		// Watch and Writable are the adapter's declarations, not the
		// plugin's: the node-facing Info describes the doors this adapter
		// opens. It has no Subscribe, so a passed-through watch:true would
		// send the server's watchPlugin into Unimplemented retries forever,
		// and no WriteContent, so a passed-through writable:true would offer
		// editing that is then refused. Both stay false until the adapter
		// carries them — Subscribe over cp.Watch, mapping ContextChanged to
		// GridChanged by context id and EntryRemoved to TileRemoved by the id
		// map, and WriteContent forwarding by key — at which point they
		// follow ci again. The cache layer that fronts every adapter
		// re-declares watch over its own stream (sourcecache.Layer.Subscribe,
		// the revalidation's GridChanged), so the fan-in still reaches a
		// plugin namespace; false here stays the adapter's own truth.
		Watch:    false,
		Writable: false,
	}
	for _, m := range ci.MenuEntries {
		// A menu entry names an extra plugin root: the context it targets
		// becomes a grid id the node can serve.
		out := &gridwellv1.MenuEntry{
			Id: m.Id, Label: m.Label, Glyph: m.Glyph, Color: m.Color,
		}
		if m.Context != "" {
			id, err := a.canonicalGridID(m.Context)
			if err != nil {
				return nil, err
			}
			out.GridId = id
		}
		resp.MenuEntries = append(resp.MenuEntries, out)
	}
	if ci.RootContext != "" {
		id, err := a.canonicalGridID(ci.RootContext)
		if err != nil {
			return nil, err
		}
		resp.RootGridId = id
		if gid, ok, err := a.mem.LookupContext(ci.RootContext); err == nil && ok {
			if f, ok, err := a.mem.RootFraming(gid); err == nil && ok {
				resp.RootViewCx, resp.RootViewCy, resp.RootViewZoom = f.Cx, f.Cy, f.Zoom
			}
		}
	}
	return resp, nil
}

// engineEntries converts listing entries for the store's merge.
func engineEntries(entries []*pluginv1.Entry) []store.Entry {
	out := make([]store.Entry, len(entries))
	for i, e := range entries {
		le := store.Entry{Key: e.Key, Kind: e.Kind, Label: e.Label, ChildContext: e.ChildContext, URL: e.UrlString}
		if h := e.PlacementHint; h != nil {
			le.Hint = &store.Hint{X: h.X, Y: h.Y, W: h.W, H: h.H}
		}
		out[i] = le
	}
	return out
}

// buildTiles joins the overlay's rows with the listing's content facts and
// names each one: a minted row by its row id, an untouched entry by its
// derived address. Both id positions — the tile's own and a well's child grid
// — canonicalize the same way, so a tile that has a row is never named by its
// key and a tile that has none is never named by a row that does not exist.
func buildTiles(gridID, context string, tiles []store.ExtTile, entries []*pluginv1.Entry, childGrid func(string) (string, error)) ([]*gridwellv1.Tile, error) {
	byKey := map[string]*pluginv1.Entry{}
	for _, e := range entries {
		byKey[e.Key] = e
	}
	out := make([]*gridwellv1.Tile, 0, len(tiles))
	for _, t := range tiles {
		id := tileAddr(context, t.Key)
		if t.ID != 0 {
			id = strconv.FormatInt(t.ID, 10)
		}
		pt := &gridwellv1.Tile{
			Id:          id,
			GridId:      gridID,
			Kind:        t.Kind,
			X:           t.X,
			Y:           t.Y,
			W:           t.W,
			H:           t.H,
			ViewCx:      t.ViewCx,
			ViewCy:      t.ViewCy,
			ViewZoom:    t.ViewZoom,
			TextX:       t.TextX,
			TextY:       t.TextY,
			TextW:       t.TextW,
			TextH:       t.TextH,
			TextMode:    t.TextMode,
			ContentZoom: t.ContentZoom,
			AltText:     t.Label,
		}
		e, listed := byKey[t.Key]
		switch {
		case t.ChildGridID != 0:
			// A minted well's child grid has a row: references at rest are
			// row ids, so this is the one the store already wrote.
			pt.ChildGridId = strconv.FormatInt(t.ChildGridID, 10)
		case listed && e.ChildContext != "":
			cg, err := childGrid(e.ChildContext)
			if err != nil {
				return nil, err
			}
			pt.ChildGridId = cg
		}
		if listed {
			pt.ServesPage = e.ServesPage
			pt.TextPresentation = e.TextPresentation
			pt.PreviewBlobId = e.PreviewStamp
			pt.StatusDetail = e.StatusDetail
			pt.UrlString = e.UrlString
		}
		out = append(out, pt)
	}
	return out, nil
}

// synthesized is one grid as the adapter derives it: the wire grid, the
// joined rows, which carry the plugin keys, the wire tiles, with row i
// matching tile i, and the listing that produced them, which the mint reads
// back to store an entry's content snapshot.
type synthesized struct {
	grid    *gridwellv1.Grid
	context string
	gid     int64 // the grid's row, 0 when the context is untouched
	rows    []store.ExtTile
	tiles   []*gridwellv1.Tile
	entries []*pluginv1.Entry
}

// resolveGrid reads a wire grid id as the context it names plus the grid row
// backing it, 0 when nobody has touched the context. Both shapes resolve:
// digits are a row, a key form is the context itself.
func (a *Adapter) resolveGrid(gridID string) (gid int64, context string, err error) {
	switch rpc.ShapeOf(gridID) {
	case rpc.ShapeRow:
		gid, _ = strconv.ParseInt(gridID, 10, 64)
		context, err = a.mem.ContextKey(gid)
		if errors.Is(err, store.ErrNotFound) {
			return 0, "", status.Errorf(codes.NotFound, "plugin: no grid %d", gid)
		}
		return gid, context, err
	case rpc.ShapeKey:
		context, _, isTile, _ := splitAddr(gridID)
		if isTile {
			return 0, "", status.Errorf(codes.InvalidArgument, "plugin: %q names a tile, not a grid", gridID)
		}
		gid, _, err = a.mem.LookupContext(context)
		return gid, context, err
	default:
		return 0, "", status.Errorf(codes.InvalidArgument, "plugin: invalid grid_id %q", gridID)
	}
}

// canonicalGridID is the one name a context answers to, and it is the derived
// address FOR GOOD — a grid keeps its name even after the store mints a row
// for it.
//
// A tile can be renamed by its mint because a grid answer replaces the whole
// tile set at once; a GRID cannot, because the client is standing in it. A
// pane holds its anchor grid id, and the moment the first tile in that grid is
// dragged — which is what mints the grid's row — a numeric answer would make
// the pane's anchor name a grid nothing answers to, and the room would go
// blank under the user's hand. The row is storage; the address is the name.
// Both still resolve on the way in (resolveGrid), so a reference stored before
// this rule keeps working.
func (a *Adapter) canonicalGridID(context string) (string, error) {
	return gridAddr(context), nil
}

// grid fetches, joins, and builds one grid. It is GetGrid's core, shared with
// GetTile so the two cannot disagree.
func (a *Adapter) grid(ctx context.Context, gridID string) (*gridwellv1.Grid, []*gridwellv1.Tile, error) {
	s, err := a.synthesize(ctx, gridID)
	if err != nil {
		return nil, nil, err
	}
	return s.grid, s.tiles, nil
}

// synthesize is grid() keeping the join: Search resolves a plugin key to its
// tile through the rows, and the mint reads the derived placement back out of
// them, so both are the same tile GetGrid answers and never a parallel
// derivation.
func (a *Adapter) synthesize(ctx context.Context, gridID string) (*synthesized, error) {
	gid, ckey, err := a.resolveGrid(gridID)
	if err != nil {
		return nil, err
	}
	// The listing is the source's half. When it fails transport-shaped — "not
	// right now", not a verdict — the adapter carries on with an empty,
	// non-authoritative one: nothing is authoritatively absent, so nothing
	// retires and the rows the node minted still answer. That is the whole
	// degradation. There is no remembered listing to serve, because the rows
	// are the remembered answer and they are durable; an entry with no row is
	// absent for as long as the source is.
	stale := false
	resp, err := a.cp.List(ctx, &pluginv1.ListRequest{Context: ckey})
	if err != nil {
		if !gwerr.IsTransport(err) {
			return nil, err
		}
		stale, resp = true, &pluginv1.ListResponse{}
	}
	// An authoritative listing is a verdict on every key: rows it does not
	// mention are gone, and their ids retire. Only rows are swept; an
	// untouched entry has nothing to retire and simply stops appearing.
	if !stale && resp.Authoritative && gid != 0 {
		present := map[string]bool{}
		for _, e := range resp.Entries {
			present[e.Key] = true
		}
		if err := a.mem.Sweep(gid, present); err != nil {
			return nil, err
		}
	}
	entries := engineEntries(resp.Entries)
	// The rows' outage snapshot follows what the source last said. It is not
	// what a listed entry reads by — the join takes those facts from the entry
	// — so this writes only where the source actually changed something.
	if !stale && gid != 0 {
		if err := a.mem.Refresh(gid, entries); err != nil {
			return nil, err
		}
	}
	tiles, err := a.mem.Overlay(gid, entries)
	if err != nil {
		return nil, err
	}
	// A live non-authoritative listing sweeps by arbitration: ROWS the
	// listing did not include are probed, and only a definitive GONE retires
	// them. An untouched entry never reaches this arm — absence from the
	// listing is its whole story.
	if !stale && !resp.Authoritative {
		live := map[string]bool{}
		for _, e := range resp.Entries {
			live[e.Key] = true
		}
		kept := tiles[:0]
		for _, t := range tiles {
			if live[t.Key] {
				kept = append(kept, t)
				continue
			}
			pr, perr := a.cp.Probe(ctx, &pluginv1.ProbeRequest{Key: t.Key})
			if perr == nil && pr.Presence == pluginv1.ProbeResponse_PRESENCE_GONE {
				if rerr := a.mem.Retire(t.ID); rerr != nil && !errors.Is(rerr, store.ErrNotFound) {
					return nil, rerr
				}
				continue // definitively gone: swept
			}
			kept = append(kept, t) // uncertain or alive: keep
		}
		tiles = kept
	}
	// The plugin's DECLARATIONS stamp the grid: host_content (these rows
	// project host state, so they wear the host treatment) and glyph (the
	// grid's identity face). Both ride the grid rather than a plugin-list
	// lookup, because a grid reached through a mount has no local row to look
	// up. A dark plugin fails here, and the whole read fails with it: the
	// declared face is the plugin's own fact and nothing else can supply it.
	ci, err := a.cp.Info(ctx, &pluginv1.InfoRequest{})
	if err != nil {
		return nil, err
	}
	canonical, err := a.canonicalGridID(ckey)
	if err != nil {
		return nil, err
	}
	g := &gridwellv1.Grid{
		Id:          canonical,
		HostContent: ci.HostContent,
		Glyph:       ci.Glyph,
		Stale:       stale,
	}
	wire, err := buildTiles(canonical, ckey, tiles, resp.Entries, a.canonicalGridID)
	if err != nil {
		return nil, err
	}
	return &synthesized{grid: g, context: ckey, gid: gid, rows: tiles, tiles: wire, entries: resp.Entries}, nil
}

func (a *Adapter) GetGrid(ctx context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	g, tiles, err := a.grid(ctx, req.GridId)
	if err != nil {
		return nil, err
	}
	return &gridwellv1.GetGridResponse{Grid: g, Tiles: tiles}, nil
}

// tileRef is a resolved tile: the context that lists it, its plugin key, and
// its row, 0 when the user has never touched it. Every verb that needs more
// than the key resolves through here, so the two id shapes are read in one
// place.
type tileRef struct {
	id      int64
	gid     int64
	context string
	key     string
}

// resolveTile reads a wire tile id. Digits are a row, and the row is the one
// that says which key and context it stands for; a key form carries both
// itself, and picks up a row id when the entry has since been minted, so an
// id the client is still holding from before a move keeps answering as the
// same tile.
func (a *Adapter) resolveTile(tileID string) (tileRef, error) {
	switch rpc.ShapeOf(tileID) {
	case rpc.ShapeRow:
		id, _ := strconv.ParseInt(tileID, 10, 64)
		gid, key, tomb, err := a.mem.TileKey(id)
		if errors.Is(err, store.ErrNotFound) || tomb {
			return tileRef{}, status.Errorf(codes.NotFound, "plugin: no tile %d", id)
		}
		if err != nil {
			return tileRef{}, err
		}
		ckey, err := a.mem.ContextKey(gid)
		if err != nil {
			return tileRef{}, err
		}
		return tileRef{id: id, gid: gid, context: ckey, key: key}, nil
	case rpc.ShapeKey:
		ckey, key, isTile, _ := splitAddr(tileID)
		if !isTile {
			return tileRef{}, status.Errorf(codes.InvalidArgument, "plugin: %q names a grid, not a tile", tileID)
		}
		gid, _, err := a.mem.LookupContext(ckey)
		if err != nil {
			return tileRef{}, err
		}
		id, _, err := a.mem.LiveTileID(gid, key)
		if err != nil {
			return tileRef{}, err
		}
		return tileRef{id: id, gid: gid, context: ckey, key: key}, nil
	default:
		return tileRef{}, status.Errorf(codes.InvalidArgument, "plugin: invalid tile_id %q", tileID)
	}
}

// contentKey is the cheap half of resolution: the plugin key alone, which is
// all a content verb needs. A derived address carries its key, so an untouched
// entry reads with no store hit at all.
func (a *Adapter) contentKey(tileID string) (string, error) {
	ref, err := a.resolveTile(tileID)
	if err != nil {
		return "", err
	}
	return ref.key, nil
}

// mint is the ONE place a plugin tile becomes a row, and it happens only
// where a durable fact needs one: a placement, a framing, a stored reference.
// The row takes the placement the entry is ALREADY being answered at — the
// derived one, read back out of the same join GetGrid runs — so minting never
// moves anything. It is idempotent: an id that already names a row is that
// row.
func (a *Adapter) mint(ctx context.Context, tileID string) (int64, error) {
	ref, err := a.resolveTile(tileID)
	if err != nil {
		return 0, err
	}
	if ref.id != 0 {
		return ref.id, nil
	}
	s, err := a.synthesize(ctx, gridAddr(ref.context))
	if err != nil {
		return 0, err
	}
	var row *store.ExtTile
	for i := range s.rows {
		if s.rows[i].Key == ref.key {
			row = &s.rows[i]
			break
		}
	}
	if row == nil {
		return 0, status.Errorf(codes.NotFound, "plugin: no entry %q in context %q", ref.key, ref.context)
	}
	var entry *pluginv1.Entry
	for _, e := range s.entries {
		if e.Key == ref.key {
			entry = e
			break
		}
	}
	if entry == nil {
		return 0, status.Errorf(codes.NotFound, "plugin: no entry %q in context %q", ref.key, ref.context)
	}
	// The grid row comes first: a tile row needs a grid to belong to, and a
	// well row needs its child grid to exist, so a stored reference is always
	// a row id.
	gid, err := a.mem.ContextID(ref.context)
	if err != nil {
		return 0, err
	}
	var child int64
	if entry.ChildContext != "" {
		if child, err = a.mem.ContextID(entry.ChildContext); err != nil {
			return 0, err
		}
	}
	return a.mem.Mint(gid, engineEntries([]*pluginv1.Entry{entry})[0], child, row.X, row.Y, row.W, row.H)
}

// MintRef is the node's door onto that mint: the router calls it before
// STORING a reference to a plugin tile or grid, because a reference at rest
// must name a row. It answers the canonical local id, which is what the
// reference then holds.
func (a *Adapter) MintRef(ctx context.Context, localID string) (string, error) {
	switch rpc.ShapeOf(localID) {
	case rpc.ShapeRow:
		return localID, nil
	case rpc.ShapeKey:
		_, _, isTile, _ := splitAddr(localID)
		if !isTile {
			// A grid keeps its address as its name (canonicalGridID), so a
			// reference to one is already canonical and durable: the context
			// key is as permanent as the plugin's keys, and a row would only
			// give it a second name the client could not follow.
			return localID, nil
		}
		id, err := a.mint(ctx, localID)
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(id, 10), nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "plugin: invalid id %q", localID)
	}
}

// tileByID resolves one tile through the same grid synthesis GetGrid uses,
// never a parallel derivation.
func (a *Adapter) tileByID(ctx context.Context, tileID string) (*gridwellv1.Tile, error) {
	ref, err := a.resolveTile(tileID)
	if err != nil {
		return nil, err
	}
	s, err := a.synthesize(ctx, gridAddr(ref.context))
	if err != nil {
		return nil, err
	}
	if t := s.tileForKey(ref.key); t != nil {
		return t, nil
	}
	return nil, status.Errorf(codes.NotFound, "plugin: no tile %q", tileID)
}

// Search forwards the query to the plugin and turns each hit into a place, the
// way the store's Search answers one: the tile plus the containing-well chain
// from the plugin root. The plugin names a key and a context path, and the
// adapter resolves both through the same grid synthesis GetGrid runs, one
// synthesis per distinct context per call, so a hit carries the id the store
// minted at the placement the user left it. A hit the synthesis cannot place —
// the key is not in its context's listing, or a path step is not a well of the
// step before — is dropped, because a result is a promise you can go there. An
// id: locate is refused: the store keeps no parent index for a plugin
// namespace, and an empty or root-anchored path would be a wrong place rather
// than a missing one.
func (a *Adapter) Search(ctx context.Context, req *gridwellv1.SearchRequest) (*gridwellv1.SearchResponse, error) {
	if q := rpc.ParseSearchQuery(req.Query); q.ID != "" {
		return nil, status.Error(codes.Unimplemented, "plugin: locate by id is not supported (no parent index in the memory DB)")
	}
	resp, err := a.cp.Search(ctx, &pluginv1.SearchRequest{Query: req.Query, Limit: req.Limit})
	if err != nil {
		return nil, err
	}
	grids := map[string]*synthesized{}
	synth := func(key string) (*synthesized, error) {
		if s, ok := grids[key]; ok {
			return s, nil
		}
		s, err := a.synthesize(ctx, gridAddr(key))
		if err != nil {
			return nil, err
		}
		grids[key] = s
		return s, nil
	}
	out := &gridwellv1.SearchResponse{}
	for _, r := range resp.Results {
		if r.Entry == nil || len(r.ContextPath) == 0 {
			continue
		}
		var path []*gridwellv1.Tile
		placed := true
		for i := 1; i < len(r.ContextPath); i++ {
			parent, err := synth(r.ContextPath[i-1])
			if err != nil {
				return nil, err
			}
			cgid, err := a.canonicalGridID(r.ContextPath[i])
			if err != nil {
				return nil, err
			}
			well := parent.tileOpening(cgid)
			if well == nil {
				placed = false
				break
			}
			path = append(path, well)
		}
		if !placed {
			continue
		}
		leaf, err := synth(r.ContextPath[len(r.ContextPath)-1])
		if err != nil {
			return nil, err
		}
		tile := leaf.tileForKey(r.Entry.Key)
		if tile == nil {
			continue
		}
		out.Results = append(out.Results, &gridwellv1.SearchResult{Tile: tile, Path: path, Snippet: r.Snippet, Score: r.Score})
	}
	return out, nil
}

// tileForKey answers the wire tile minted for a plugin key, or nil when the
// synthesis holds none.
func (s *synthesized) tileForKey(key string) *gridwellv1.Tile {
	for i, row := range s.rows {
		if row.Key == key {
			return s.tiles[i]
		}
	}
	return nil
}

// tileOpening answers the well tile whose descent is the grid, or nil when
// there is none.
func (s *synthesized) tileOpening(childGridID string) *gridwellv1.Tile {
	for _, t := range s.tiles {
		if t.ChildGridId == childGridID {
			return t
		}
	}
	return nil
}

func (a *Adapter) GetTile(ctx context.Context, req *gridwellv1.GetTileRequest) (*gridwellv1.TileResponse, error) {
	t, err := a.tileByID(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	return &gridwellv1.TileResponse{Tile: t}, nil
}

// PlaceTile terminates at the store: in-grid only, and unversioned. It is a
// durable fact about an entry, so it mints the row it writes into.
func (a *Adapter) PlaceTile(ctx context.Context, req *gridwellv1.PlaceTileRequest) (*gridwellv1.TileResponse, error) {
	ref, err := a.resolveTile(req.TileId)
	if err != nil {
		return nil, err
	}
	if req.GridId != "" {
		want, err := a.resolveGridContext(req.GridId)
		if err != nil {
			return nil, err
		}
		if want != ref.context {
			return nil, status.Errorf(codes.InvalidArgument, "plugin: cross-grid placement not supported")
		}
	}
	id, err := a.mint(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	if err := a.mem.Place(id, req.X, req.Y, req.W, req.H); err != nil {
		return nil, err
	}
	return a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: strconv.FormatInt(id, 10)})
}

// resolveGridContext is resolveGrid's context half, for the callers that only
// need to know WHICH grid an id names, minting nothing.
func (a *Adapter) resolveGridContext(gridID string) (string, error) {
	_, ckey, err := a.resolveGrid(gridID)
	return ckey, err
}

// SetTile terminates the framing arms at the store. Rename is refused,
// because a plugin tile's name is its source name, and the content arms do not
// exist for a plugin tile.
func (a *Adapter) SetTile(ctx context.Context, req *gridwellv1.SetTileRequest) (*gridwellv1.TileResponse, error) {
	if req.Rename != "" {
		return nil, status.Error(codes.InvalidArgument, "plugin: tiles derive their names from the source")
	}
	id, err := a.mint(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	switch {
	case req.ContentZoom != nil:
		if err := a.mem.SetContentZoom(id, *req.ContentZoom); err != nil {
			return nil, err
		}
	default:
		t := req.GetTile()
		switch t.GetKind() {
		case "text":
			if err := a.mem.SetTextView(id, t.GetTextX(), t.GetTextY(), t.GetTextW(), t.GetTextH(), t.GetTextMode()); err != nil {
				return nil, err
			}
		default:
			return nil, status.Errorf(codes.InvalidArgument, "plugin: unsupported SetTile kind %q", t.GetKind())
		}
	}
	return a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: strconv.FormatInt(id, 10)})
}

// SetFraming persists framing into this plugin's namespace of the store: the
// one framing write, aimed at a doorway tile row or a context's root grid row.
// It is framing-class — the node's memory of the user's view, never the
// plugin's content.
func (a *Adapter) SetFraming(ctx context.Context, req *gridwellv1.SetFramingRequest) (*gridwellv1.SetFramingResponse, error) {
	f := rpc.Framing{Cx: req.Cx, Cy: req.Cy, Zoom: req.Zoom}
	if req.RootGridId != "" {
		ckey, err := a.resolveGridContext(req.RootGridId)
		if err != nil {
			return nil, err
		}
		// Framing a root grid is a durable fact about it, so the grid gets
		// its row here — the same mint, one position up.
		gid, err := a.mem.ContextID(ckey)
		if err != nil {
			return nil, err
		}
		if err := a.mem.SetFraming(0, gid, f); err != nil {
			return nil, err
		}
		return &gridwellv1.SetFramingResponse{}, nil
	}
	id, err := a.mint(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	if err := a.mem.SetFraming(id, 0, f); err != nil {
		return nil, err
	}
	t, err := a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: strconv.FormatInt(id, 10)})
	if err != nil {
		return nil, err
	}
	return &gridwellv1.SetFramingResponse{Tile: t.GetTile()}, nil
}

func (a *Adapter) ReadContent(ctx context.Context, req *gridwellv1.ReadContentRequest, send func(*gridwellv1.ContentChunk) error) error {
	key, err := a.contentKey(req.TileId)
	if err != nil {
		return err
	}
	cs, err := a.cp.ReadContent(ctx, &pluginv1.ReadContentRequest{Key: key})
	if err != nil {
		return err
	}
	for {
		chunk, rerr := cs.Recv()
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
		// Plugin content is not version-edited, so version 0.
		if serr := send(&gridwellv1.ContentChunk{Data: chunk.Data, MediaType: chunk.MediaType}); serr != nil {
			return serr
		}
	}
}

func (a *Adapter) ServeContent(ctx context.Context, req *gridwellv1.ServeContentRequest, send func(*gridwellv1.ServeContentChunk) error) error {
	key, err := a.contentKey(req.TileId)
	if err != nil {
		return err
	}
	cs, err := a.cp.ServeContent(ctx, &pluginv1.ServeContentRequest{Key: key, Subpath: req.Subpath})
	if err != nil {
		return err
	}
	for {
		chunk, rerr := cs.Recv()
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
		if serr := send(&gridwellv1.ServeContentChunk{Status: chunk.Status, MediaType: chunk.MediaType, Data: chunk.Data}); serr != nil {
			return serr
		}
	}
}

func (a *Adapter) GetTilePreview(ctx context.Context, req *gridwellv1.GetTilePreviewRequest) (*gridwellv1.GetTilePreviewResponse, error) {
	key, err := a.contentKey(req.TileId)
	if err != nil {
		return nil, err
	}
	resp, err := a.cp.GetPreview(ctx, &pluginv1.GetPreviewRequest{Key: key})
	if err != nil {
		return nil, err
	}
	return &gridwellv1.GetTilePreviewResponse{Jpeg: resp.Jpeg}, nil
}

func (a *Adapter) Probe(ctx context.Context, req *gridwellv1.ProbeRequest) (*gridwellv1.ProbeResponse, error) {
	// An id that names nothing this namespace can read — a malformed shape,
	// a retired row — is GONE. A derived address always resolves to a key,
	// and the plugin is the one that says whether the key is still there.
	key, err := a.contentKey(req.TileId)
	if err != nil {
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	resp, err := a.cp.Probe(ctx, &pluginv1.ProbeRequest{Key: key})
	if err != nil {
		if gwerr.IsTransport(err) {
			return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_UNSPECIFIED}, nil
		}
		return nil, err
	}
	// The enums are defined identically. Map by name to keep that a checked
	// fact rather than a numeric coincidence.
	switch resp.Presence {
	case pluginv1.ProbeResponse_PRESENCE_PRESENT:
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_PRESENT}, nil
	case pluginv1.ProbeResponse_PRESENCE_GONE:
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	default:
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_UNSPECIFIED}, nil
	}
}

// DeleteTile deletes the source thing, which is the plugin's verdict, then
// retires the row. An already-gone row succeeds: the verb is idempotent.
func (a *Adapter) DeleteTile(ctx context.Context, req *gridwellv1.DeleteTileRequest) (*gridwellv1.DeleteTileResponse, error) {
	ref, err := a.resolveTile(req.TileId)
	if err != nil {
		return &gridwellv1.DeleteTileResponse{}, nil
	}
	if _, err := a.cp.Delete(ctx, &pluginv1.DeleteRequest{Key: ref.key}); err != nil {
		return nil, err
	}
	// Only a row can be retired. Deleting an untouched entry is the plugin's
	// verdict and nothing else: there is no id to retire, and the next
	// listing simply does not name it.
	if ref.id != 0 {
		if err := a.mem.Retire(ref.id); err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("plugin: source deleted but row not retired: %w", err)
		}
	}
	return &gridwellv1.DeleteTileResponse{}, nil
}
