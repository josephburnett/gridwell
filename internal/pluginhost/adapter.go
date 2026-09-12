// Package pluginhost adapts a plugin.v1 plugin to the full Gridwell service. It
// joins the plugin's content answers with the plugin's namespace of the node's
// store, which holds the ids, the placement and the framing. Every merge
// decision lives in the store and every content derivation in the plugin, so
// presentation verbs terminate here and content verbs pass through.
//
// Listing writes nothing: a grid's answer is a join of the plugin's List with
// store.Namespace.Overlay, and every entry is answered under its derived
// address (address.go), row or no row. A row appears only when the user makes
// a durable fact about an entry, which is Adapter.mint.
//
// Outages split by whose fact is missing. A dark source costs only what the
// source says: every minted row still reads, stamped stale, while an entry
// with no row is absent. A dark plugin fails the read, because nothing fronts
// a plugin: a subprocess on this machine is a call away, so there are no
// remembered answers to serve.
package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// Supervisor is whoever owns the plugin's subprocess. The adapter never decides
// liveness itself and keeps no state about it; nil still has a stream, just no
// health on it.
type Supervisor interface {
	// Health is the current state: up, or down with the reason.
	Health() (healthy bool, detail string)
	// OnHealth registers a listener for every transition until cancel runs.
	// The listener must not block.
	OnHealth(func(healthy bool, detail string)) (cancel func())
}

// Adapter implements namespace.Namespace over one plugin and its namespace of
// the node's store. The router calls it as a Go value; the one gRPC hop
// underneath is the plugin.v1 subprocess.
type Adapter struct {
	namespace.Unimplemented
	cp  pluginv1.PluginClient
	mem *store.Namespace
	sup Supervisor

	// subs are this namespace's event subscribers. The stream carries the
	// supervisor's health and the grids the adapter's own writes changed.
	subsMu sync.Mutex
	subs   map[int]chan *gridwellv1.Event
	subSeq int
}

// A plugin reaches the router as a Go value; the compiler is what says so.
var _ namespace.Namespace = (*Adapter)(nil)

// New builds the adapter; the caller owns both halves' lifecycles.
func New(cp pluginv1.PluginClient, mem *store.Namespace, sup Supervisor) *Adapter {
	return &Adapter{cp: cp, mem: mem, sup: sup, subs: map[int]chan *gridwellv1.Event{}}
}

// Info translates the plugin handshake, resolving each declared collection's
// context to a grid id and reading that grid's persisted viewport. The node
// declares no root of its own: a plugin is not a place, it contributes
// doorways, and the node has no landing to choose among them.
func (a *Adapter) Info(ctx context.Context, _ *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	ci, err := a.cp.Info(ctx, &pluginv1.InfoRequest{})
	if err != nil {
		return nil, err
	}
	resp := &gridwellv1.InfoResponse{
		DisplayName: ci.DisplayName,
		Glyph:       ci.Glyph,
		// Writable describes the door this adapter opens, not the plugin's
		// answer: false because there is no WriteContent here and passing the
		// plugin's through would offer editing that is then refused.
		Writable: false,
	}
	for _, m := range declaredEntries(ci) {
		// A collection's context becomes a grid id the node can serve, and the
		// framing the node remembers rides along, so re-entering lands where
		// the user left it.
		out := &gridwellv1.MenuEntry{
			Id: m.Id, Label: m.Label, Glyph: m.Glyph, Color: m.Color,
		}
		if m.Context != "" {
			id, err := a.canonicalGridID(m.Context)
			if err != nil {
				return nil, err
			}
			out.GridId = id
			out.ViewCx, out.ViewCy, out.ViewZoom = a.contextFraming(m.Context)
		}
		resp.MenuEntries = append(resp.MenuEntries, out)
	}
	return resp, nil
}

// declaredEntries is the one place root_context is still read. A plugin written
// before collections were declared has one collection, so its root becomes one
// entry wearing the plugin's own name and face; once a plugin declares entries,
// a root_context it still sends gets no privilege among them. Retiring the
// field is the node's job, so an old third-party binary keeps presenting.
func declaredEntries(ci *pluginv1.InfoResponse) []*pluginv1.MenuEntry {
	if len(ci.MenuEntries) > 0 || ci.RootContext == "" {
		return ci.MenuEntries
	}
	return []*pluginv1.MenuEntry{{Id: ci.RootContext, Context: ci.RootContext}}
}

// contextFraming is the framing the node remembers for one context's grid, the
// one read behind every doorway the handshake declares, so a collection cannot
// get a rule of its own.
func (a *Adapter) contextFraming(ckey string) (cx, cy, zoom float64) {
	gid, ok, err := a.mem.LookupContext(ckey)
	if err != nil || !ok {
		return 0, 0, 0
	}
	f, ok, err := a.mem.RootFraming(gid)
	if err != nil || !ok {
		return 0, 0, 0
	}
	return f.Cx, f.Cy, f.Zoom
}

// Subscribe serves this namespace's event stream: the supervisor's health, and
// a GridChanged for a grid the adapter's own writes changed, so a second pane
// repaints instead of holding a placement the user has moved. A subscriber
// arriving while the plugin is down is told at once, since nothing else would
// tell it until recovery; a healthy plugin announces nothing, because a health
// event costs the client a full resync.
func (a *Adapter) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, send func(*gridwellv1.Event) error) error {
	id, ch := a.addSub()
	defer a.removeSub(id)
	if a.sup != nil {
		cancel := a.sup.OnHealth(func(healthy bool, detail string) { a.emitHealth(healthy, detail) })
		defer cancel()
		if healthy, detail := a.sup.Health(); !healthy {
			if err := send(healthEvent(healthy, detail)); err != nil {
				return err
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-ch:
			if err := send(ev); err != nil {
				return err
			}
		}
	}
}

func (a *Adapter) addSub() (int, chan *gridwellv1.Event) {
	ch := make(chan *gridwellv1.Event, 64)
	a.subsMu.Lock()
	defer a.subsMu.Unlock()
	a.subSeq++
	a.subs[a.subSeq] = ch
	return a.subSeq, ch
}

func (a *Adapter) removeSub(id int) {
	a.subsMu.Lock()
	defer a.subsMu.Unlock()
	delete(a.subs, id)
}

// emit hands one event to every subscriber. A subscriber too far behind loses
// it rather than blocking the writer: every event here is a cue to look again,
// never a fact only it carries.
func (a *Adapter) emit(ev *gridwellv1.Event) {
	a.subsMu.Lock()
	defer a.subsMu.Unlock()
	for _, ch := range a.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// healthEvent is one health state as this namespace announces it. The uuid
// rides empty; the server's fan-in fills it (rpc.QualifyEventIDs).
func healthEvent(healthy bool, detail string) *gridwellv1.Event {
	return &gridwellv1.Event{Payload: &gridwellv1.Event_PluginHealth{
		PluginHealth: &gridwellv1.EventPluginHealth{Healthy: healthy, Detail: detail},
	}}
}

func (a *Adapter) emitHealth(healthy bool, detail string) { a.emit(healthEvent(healthy, detail)) }

// emitGridChanged announces that a grid this adapter serves has changed,
// under the canonical address (canonicalGridID), so a listener does not have
// to know whether the write minted anything.
func (a *Adapter) emitGridChanged(gridID string) {
	if gridID == "" {
		return
	}
	a.emit(&gridwellv1.Event{Payload: &gridwellv1.Event_GridChanged{
		GridChanged: &gridwellv1.GridChanged{GridId: gridID},
	}})
}

// checkEntries refuses a shape the node cannot present, at the one door where a
// plugin's entries enter, so it never reaches the store, the wire or a client.
// The one shape refused today is kind "url" together with serves_page: every
// reader answers the url arm first, so the page would never serve.
func checkEntries(entries []*pluginv1.Entry) error {
	for _, e := range entries {
		if e.Kind == rpc.KindURL && e.ServesPage {
			return status.Errorf(codes.InvalidArgument,
				"plugin: entry %q declares kind url and serves_page; a url entry opens url_string, a page has no address of its own — declare one or the other",
				e.Key)
		}
	}
	return nil
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

// buildTiles joins the overlay's rows with the listing's content facts, naming
// each by its derived address, a well's child grid included: the id a well
// hands the client must be the id GetGrid answers under.
func buildTiles(gridID, context string, tiles []store.ExtTile, entries []*pluginv1.Entry, childGrid func(string) (string, error), rowContext func(int64) (string, error)) ([]*gridwellv1.Tile, error) {
	byKey := map[string]*pluginv1.Entry{}
	for _, e := range entries {
		byKey[e.Key] = e
	}
	out := make([]*gridwellv1.Tile, 0, len(tiles))
	for _, t := range tiles {
		pt := &gridwellv1.Tile{
			Id:          tileAddr(context, t.Key),
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
		case listed && e.ChildContext != "":
			cg, err := childGrid(e.ChildContext)
			if err != nil {
				return nil, err
			}
			pt.ChildGridId = cg
		case t.ChildGridID != 0:
			// A minted well the listing does not carry, from a dark or stale
			// source. The row remembers which child grid, and its context is
			// read back so the name handed out is still the address.
			ck, err := rowContext(t.ChildGridID)
			if err != nil {
				return nil, err
			}
			cg, err := childGrid(ck)
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

// synthesized is one grid as the adapter derives it: the wire grid, the joined
// rows carrying the plugin keys, the wire tiles with row i matching tile i,
// and the listing the mint reads back for an entry's content snapshot.
type synthesized struct {
	grid    *gridwellv1.Grid
	context string
	gid     int64 // the grid's row, 0 when the context is untouched
	rows    []store.ExtTile
	tiles   []*gridwellv1.Tile
	entries []*pluginv1.Entry
}

// resolveGrid reads a wire grid id as the context it names plus the grid row
// backing it, 0 when nobody has touched the context. Both shapes resolve.
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

// canonicalGridID stays the derived address even after the store mints a row:
// the address is the name, the row is storage. Both still resolve on the way in
// (resolveGrid), so a reference stored before this rule keeps working.
func (a *Adapter) canonicalGridID(context string) (string, error) {
	return gridAddr(context), nil
}

// grid is GetGrid's core, shared with GetTile so the two cannot disagree.
func (a *Adapter) grid(ctx context.Context, gridID string) (*gridwellv1.Grid, []*gridwellv1.Tile, error) {
	s, err := a.synthesize(ctx, gridID)
	if err != nil {
		return nil, nil, err
	}
	return s.grid, s.tiles, nil
}

// synthesize is grid() keeping the join, so Search's key lookup and the mint's
// derived placement read the same tile GetGrid answers.
func (a *Adapter) synthesize(ctx context.Context, gridID string) (*synthesized, error) {
	gid, ckey, err := a.resolveGrid(gridID)
	if err != nil {
		return nil, err
	}
	// A transport-shaped failure is "not right now", not a verdict, so the
	// adapter carries on with an empty non-authoritative listing and nothing
	// retires. The rows are the whole remembered answer, so an entry with no
	// row is absent for as long as the source is.
	stale := false
	resp, err := a.cp.List(ctx, &pluginv1.ListRequest{Context: ckey})
	if err != nil {
		if !gwerr.IsTransport(err) {
			return nil, err
		}
		stale, resp = true, &pluginv1.ListResponse{}
	}
	if err := checkEntries(resp.Entries); err != nil {
		return nil, err
	}
	// An authoritative listing is a verdict on every key, so rows it does not
	// mention retire. An untouched entry has nothing to retire.
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
	// The rows' outage snapshot follows what the source last said. A listed
	// entry reads by the join instead, so this writes only where the source
	// changed something.
	if !stale && gid != 0 {
		if err := a.mem.Refresh(gid, entries); err != nil {
			return nil, err
		}
	}
	tiles, err := a.mem.Overlay(gid, entries)
	if err != nil {
		return nil, err
	}
	// A live non-authoritative listing sweeps by arbitration: rows it did not
	// include are probed, and only a definitive GONE retires them. An
	// untouched entry never reaches this arm.
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
	// host_content and glyph ride the grid rather than a plugin-list lookup,
	// because a grid reached through a mount has no local row. A dark plugin
	// fails the whole read here: nothing else can supply the declared face.
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
	wire, err := buildTiles(canonical, ckey, tiles, resp.Entries, a.canonicalGridID, a.mem.ContextKey)
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
// its row, 0 when untouched. Every verb needing more than the key resolves
// here, so the two id shapes are read in one place.
type tileRef struct {
	id      int64
	gid     int64
	context string
	key     string
}

// resolveTile reads a wire tile id. Digits are a row, which says which key and
// context it stands for; a key form carries both itself and picks up a row id
// once the entry is minted, so an id a client held from before a move keeps
// answering as the same tile.
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

// contentKey is the plugin key alone, all a content verb needs. A derived
// address carries its key, so an untouched entry reads with no store hit.
func (a *Adapter) contentKey(tileID string) (string, error) {
	ref, err := a.resolveTile(tileID)
	if err != nil {
		return "", err
	}
	return ref.key, nil
}

// mint is the one place a plugin tile becomes a row, and only where a durable
// fact needs one. The row takes the placement the entry is already answered at,
// out of the same join GetGrid runs, so minting never moves anything.
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
	// well row needs its child grid to exist.
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

// MintRef is the router's canonicalizer, called before a reference to a plugin
// tile or grid is stored. A plugin's canonical id is its derived address, so
// this mints nothing: a reference holding anything else would give the same
// document a second name and a second live surface. A row id arriving here is
// a reference made under the older rule being re-stored, and digits do not say
// whether they name a tile row or a grid row, so both are tried, in that
// order.
func (a *Adapter) MintRef(_ context.Context, localID string) (string, error) {
	switch rpc.ShapeOf(localID) {
	case rpc.ShapeRow:
		if ref, err := a.resolveTile(localID); err == nil {
			return tileAddr(ref.context, ref.key), nil
		}
		if _, ckey, err := a.resolveGrid(localID); err == nil {
			return gridAddr(ckey), nil
		}
		// A row nothing here answers to, retired or never this namespace's,
		// answers itself: what it names is not this call's verdict to make.
		return localID, nil
	case rpc.ShapeKey:
		return localID, nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "plugin: invalid id %q", localID)
	}
}

// tileByID resolves one tile through the same grid synthesis GetGrid uses.
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

// Search turns each hit into a place the way the store's Search does: the tile
// plus its containing-well chain, both through the same grid synthesis GetGrid
// runs. A hit the synthesis cannot place is dropped, because a result is a
// promise you can go there. An id: locate is refused: the store keeps no parent
// index here, so the path would be a wrong place, not a missing one.
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

// tileForKey answers the wire tile for a plugin key, nil when there is none.
func (s *synthesized) tileForKey(key string) *gridwellv1.Tile {
	for i, row := range s.rows {
		if row.Key == key {
			return s.tiles[i]
		}
	}
	return nil
}

// tileOpening answers the well tile whose descent is the grid, nil when there
// is none.
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
	return a.changed(a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: strconv.FormatInt(id, 10)}))
}

// resolveGridContext is resolveGrid's context half, minting nothing.
func (a *Adapter) resolveGridContext(gridID string) (string, error) {
	_, ckey, err := a.resolveGrid(gridID)
	return ckey, err
}

// SetTile terminates the framing arms at the store. Rename is refused because
// a plugin tile's name is its source name.
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
		case rpc.KindText:
			if err := a.mem.SetTextView(id, t.GetTextX(), t.GetTextY(), t.GetTextW(), t.GetTextH(), t.GetTextMode()); err != nil {
				return nil, err
			}
		default:
			return nil, status.Errorf(codes.InvalidArgument, "plugin: unsupported SetTile kind %q", t.GetKind())
		}
	}
	return a.changed(a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: strconv.FormatInt(id, 10)}))
}

// changed announces the grid a write landed in, on the way back out with the
// write's own answer, so no write can forget to say what it moved.
func (a *Adapter) changed(resp *gridwellv1.TileResponse, err error) (*gridwellv1.TileResponse, error) {
	if err == nil {
		a.emitGridChanged(resp.GetTile().GetGridId())
	}
	return resp, err
}

// SetFraming persists framing into this plugin's namespace of the store,
// aimed at a doorway tile row or a context's root grid row. It is the node's
// memory of the user's view, never the plugin's content.
func (a *Adapter) SetFraming(ctx context.Context, req *gridwellv1.SetFramingRequest) (*gridwellv1.SetFramingResponse, error) {
	f := rpc.Framing{Cx: req.Cx, Cy: req.Cy, Zoom: req.Zoom}
	if req.RootGridId != "" {
		ckey, err := a.resolveGridContext(req.RootGridId)
		if err != nil {
			return nil, err
		}
		// Framing a root grid is a durable fact about it, so the grid gets its
		// row here.
		gid, err := a.mem.ContextID(ckey)
		if err != nil {
			return nil, err
		}
		if err := a.mem.SetFraming(0, gid, f); err != nil {
			return nil, err
		}
		a.emitGridChanged(gridAddr(ckey))
		return &gridwellv1.SetFramingResponse{}, nil
	}
	id, err := a.mint(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	if err := a.mem.SetFraming(id, 0, f); err != nil {
		return nil, err
	}
	t, err := a.changed(a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: strconv.FormatInt(id, 10)}))
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
	// An id this namespace cannot read at all is GONE. A derived address
	// always resolves to a key, and the plugin says whether the key is there.
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

// DeleteTile hands the gesture to the plugin, the only one that says what
// deleting its thing means, and retires the row only if the key went with it.
//
// Delete is not removal by definition: one plugin unlinks the file, another
// marks the todo done and the tile stays. So the row's fate is settled by the
// same arbitration synthesize runs, where only a definitive GONE retires:
// retiring a row whose thing is still there would snap the tile back under a
// fresh id and kill every link to it.
//
// A tile_id that does not resolve is answered with the failure resolveTile
// classified, as home answers the same ids: an empty success would tell the
// client the delete happened, and the tile it refetches is still sitting
// there with nothing said about why.
func (a *Adapter) DeleteTile(ctx context.Context, req *gridwellv1.DeleteTileRequest) (*gridwellv1.DeleteTileResponse, error) {
	ref, err := a.resolveTile(req.TileId)
	if err != nil {
		return nil, err
	}
	if _, err := a.cp.Delete(ctx, &pluginv1.DeleteRequest{Key: ref.key}); err != nil {
		return nil, err
	}
	// Only a row can be retired. Deleting an untouched entry leaves no id to
	// retire, and the next listing simply does not name it.
	if ref.id != 0 {
		pr, perr := a.cp.Probe(ctx, &pluginv1.ProbeRequest{Key: ref.key})
		if perr == nil && pr.Presence == pluginv1.ProbeResponse_PRESENCE_GONE {
			if err := a.mem.Retire(ref.id); err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, fmt.Errorf("plugin: source deleted but row not retired: %w", err)
			}
		}
	}
	// Either way the source changed and the client must look again: for a
	// delete that transforms, this refetch repaints the new state.
	a.emitGridChanged(gridAddr(ref.context))
	return &gridwellv1.DeleteTileResponse{}, nil
}
