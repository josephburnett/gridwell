// Package pluginhost adapts a plugin.v1 plugin to the full Gridwell service,
// joining the plugin's content answers with its namespace of the node's store
// (ids, placement, framing): presentation verbs terminate here and content
// verbs pass through. Listing mints nothing; every entry answers under its
// derived address (rpc.EntryTileID) and a row appears only at Adapter.mint.
// The row is bookkeeping, resolved on the way in (Adapter.resolveTile) and
// never handed out, since a mint that renamed the entry would take the id out
// from under whoever stood on it; and GetTile is one List of the context
// named, since a key→context index would copy the plugin's structure. A dark
// source, or a listing the plugin answered from memory, is published as this
// namespace's health and minted rows still read; a dark plugin fails the read,
// since a local subprocess has no remembered answers to serve.
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
	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/eventhub"
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

	hub *eventhub.Hub[*gridwellv1.Event]

	// The source's state as the listings and the Watch stream found it, held
	// only to announce its transitions; setSource is the one writer. liveOff
	// is the Watch stream's verdict, which is not darkness: a source that
	// lists but cannot watch still answers live.
	srcMu     sync.Mutex
	srcDark   bool
	srcDetail string
	liveOff   string

	// shown is this plugin's share of the node's interest, as contexts, and
	// links is, per context, the contexts its last live listing links into.
	// The Watch stream's scope derives from both (scopeLocked); moved closes
	// when it changes. SetInterest writes shown, synthesize writes links.
	scopeMu sync.Mutex
	shown   []string
	links   map[string][]string
	scope   []string
	moved   chan struct{}

	// served is, per grid address, each distinct listing GetGrid has answered
	// since the grid was last announced: what a client may hold. See
	// checkAdded; emitGridChanged clears an entry.
	servedMu sync.Mutex
	served   map[string][]listingSum
}

var _ namespace.Namespace = (*Adapter)(nil)

// New builds the adapter; the caller owns both halves' lifecycles.
func New(cp pluginv1.PluginClient, mem *store.Namespace, sup Supervisor) *Adapter {
	return &Adapter{cp: cp, mem: mem, sup: sup, hub: eventhub.New(rpc.EventKey), moved: make(chan struct{})}
}

// Info translates the plugin handshake, resolving each declared collection to
// a grid id and its persisted framing. It declares no root: a plugin is not a
// place.
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
		out := &gridwellv1.MenuEntry{
			Id: m.Id, Label: m.Label, Glyph: m.Glyph,
		}
		if m.Context != "" {
			out.GridId = rpc.EntryGridID(m.Context)
			v, err := a.contextFraming(m.Context)
			if err != nil {
				return nil, err
			}
			out.ViewCx, out.ViewCy, out.ViewZoom = v.Wire()
		}
		resp.MenuEntries = append(resp.MenuEntries, out)
	}
	return resp, nil
}

// declaredEntries is the one place root_context is still read: a plugin that
// declares no entries gets one derived from it, so an old binary keeps
// presenting; declared entries always win.
func declaredEntries(ci *pluginv1.InfoResponse) []*pluginv1.MenuEntry {
	if len(ci.MenuEntries) > 0 || ci.RootContext == "" {
		return ci.MenuEntries
	}
	return []*pluginv1.MenuEntry{{Id: ci.RootContext, Context: ci.RootContext}}
}

// contextFraming is the framing remembered for one context's grid. An
// unreadable store is an error, not a silent none.
func (a *Adapter) contextFraming(ckey string) (rpc.View, error) {
	gid, ok, err := a.mem.LookupContext(ckey)
	if err != nil || !ok {
		return rpc.View{}, err
	}
	return a.mem.RootFraming(gid)
}

// Subscribe serves this namespace's event stream: health of the subprocess and
// the source, GridChanged, GridFramingChanged and TileChanged. A subscriber
// arriving mid-outage, or while live updates are off, is told at once; a
// healthy plugin announces nothing, because a health event costs the client a
// full resync.
func (a *Adapter) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, send func(*gridwellv1.Event) error) error {
	ch, detach := a.hub.Subscribe()
	defer detach()
	up := true
	if a.sup != nil {
		cancel := a.sup.OnHealth(func(healthy bool, detail string) { a.emitHealth(healthy, detail) })
		defer cancel()
		var detail string
		if up, detail = a.sup.Health(); !up {
			if err := send(rpc.HealthEvent("", false, detail)); err != nil {
				return err
			}
		}
	}
	if s := a.source(); up && s != (sourceState{}) {
		if err := send(s.event()); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := send(ev); err != nil {
				return err
			}
		}
	}
}

func (a *Adapter) emitHealth(healthy bool, detail string) {
	a.hub.Publish(rpc.HealthEvent("", healthy, detail))
}

// noteSource records what a listing found and announces the transition. A
// dead subprocess is the supervisor's news, so it is not recorded here.
func (a *Adapter) noteSource(dark bool, detail string) {
	if !a.processUp() {
		return
	}
	a.setSource(func() { a.srcDark, a.srcDetail = dark, detail })
}

// noteWatch records the Watch stream's verdict, "" for none, even while the
// process is down.
func (a *Adapter) noteWatch(liveOff string) {
	a.setSource(func() { a.liveOff = liveOff })
}

// setSource applies one edit and announces the transition, never while the
// process is down: that is the supervisor's news.
func (a *Adapter) setSource(edit func()) {
	a.srcMu.Lock()
	was := a.sourceLocked()
	edit()
	now := a.sourceLocked()
	a.srcMu.Unlock()
	if (now.dark != was.dark || now.liveOff != was.liveOff) && a.processUp() {
		a.hub.Publish(now.event())
	}
}

func (a *Adapter) processUp() bool {
	if a.sup == nil {
		return true
	}
	healthy, _ := a.sup.Health()
	return healthy
}

// sourceState is the source as one health event tells it; the zero value is a
// light source with live updates on.
type sourceState struct {
	dark            bool
	detail, liveOff string
}

func (s sourceState) event() *gridwellv1.Event {
	ev := rpc.HealthEvent("", !s.dark, s.detail)
	ev.GetPluginHealth().LiveUpdatesOff = s.liveOff
	return ev
}

func (a *Adapter) source() sourceState {
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	return a.sourceLocked()
}

func (a *Adapter) sourceLocked() sourceState {
	s := sourceState{dark: a.srcDark, liveOff: a.liveOff}
	if s.dark {
		s.detail = a.srcDetail
	}
	return s
}

// sourceDetail is where the user is told the source, not the process, is out.
func sourceDetail(err error) string {
	if err == nil {
		return ""
	}
	return "the source is not answering: " + err.Error()
}

// emitGridChanged announces a grid under its derived address.
func (a *Adapter) emitGridChanged(gridID string) {
	if gridID == "" {
		return
	}
	a.servedMu.Lock()
	delete(a.served, gridID)
	a.servedMu.Unlock()
	a.hub.Publish(&gridwellv1.Event{Payload: &gridwellv1.Event_GridChanged{
		GridChanged: &gridwellv1.GridChanged{GridId: gridID},
	}})
}

// retiredPresentationRendered meant document only, with no way back to the
// source bytes. A plugin never hides those, so the door reads it as "both".
const retiredPresentationRendered = "rendered"

// acceptEntries is the one door a plugin's entries enter by: it refuses a shape
// the node cannot present (serves_page off a url entry, an unknown
// text_presentation, a link it cannot draw) and maps a retired declaration
// onto the live vocabulary. context is the context listed.
func acceptEntries(context string, entries []*pluginv1.Entry) error {
	for _, e := range entries {
		if lt := e.GetLinkTarget(); lt != nil {
			switch {
			case lt.Context == "" || lt.Key == "":
				return status.Errorf(codes.InvalidArgument,
					"plugin: entry %q links to context %q key %q; a link_target names both", e.Key, lt.Context, lt.Key)
			case e.Kind == rpc.KindWell:
				return status.Errorf(codes.InvalidArgument,
					"plugin: entry %q is a well with a link_target; a well opens its child_context and has no link variant", e.Key)
			case lt.Context == context && lt.Key == e.Key:
				return status.Errorf(codes.InvalidArgument, "plugin: entry %q links to itself", e.Key)
			}
		}
		if e.ServesPage && e.Kind != rpc.KindURL {
			return status.Errorf(codes.InvalidArgument,
				"plugin: entry %q declares kind %q and serves_page; only a url entry serves a page, the node deriving its address — declare kind %q, or drop serves_page and serve a document body instead",
				e.Key, e.Kind, rpc.KindURL)
		}
		switch e.TextPresentation {
		case "", rpc.TextPresentationPlain, rpc.TextPresentationBoth:
		case retiredPresentationRendered:
			e.TextPresentation = rpc.TextPresentationBoth
		default:
			return status.Errorf(codes.InvalidArgument,
				"plugin: entry %q declares text_presentation %q; the vocabulary is %q, %q, or nothing at all",
				e.Key, e.TextPresentation, rpc.TextPresentationPlain, rpc.TextPresentationBoth)
		}
	}
	return nil
}

// buildTiles joins the overlay's rows with the listing's content facts, naming
// each by its derived address, a well's child grid included: the id a well
// hands the client must be the id GetGrid answers under.
func buildTiles(gridID, context string, tiles []store.ExtTile, entries []*pluginv1.Entry, rowContext func(int64) (string, error)) ([]*gridwellv1.Tile, error) {
	byKey := map[string]*pluginv1.Entry{}
	for _, e := range entries {
		byKey[e.Key] = e
	}
	out := make([]*gridwellv1.Tile, 0, len(tiles))
	for _, t := range tiles {
		pt := proto.Clone(t.Tile).(*gridwellv1.Tile)
		pt.Id = rpc.EntryTileID(context, t.Key)
		pt.GridId = gridID
		pt.ChildGridId = ""
		e, listed := byKey[t.Key]
		switch {
		case listed && e.ChildContext != "":
			pt.ChildGridId = rpc.EntryGridID(e.ChildContext)
		case t.ChildGridID != 0:
			// A minted well the listing does not carry: its context is read
			// back so the name handed out is still the address.
			ck, err := rowContext(t.ChildGridID)
			if err != nil {
				return nil, err
			}
			pt.ChildGridId = rpc.EntryGridID(ck)
		}
		switch {
		case listed && pt.LinkTargetId != "":
			// A link's content facts are its target's, read through it.
			pt.StatusDetail = e.StatusDetail
		case listed:
			pt.ServesPage = e.ServesPage
			pt.TextPresentation = e.TextPresentation
			pt.PreviewBlobId = faceKey(pt.PreviewBlobId, e.PreviewStamp)
			pt.StatusDetail = e.StatusDetail
			pt.UrlString = e.UrlString
		}
		out = append(out, pt)
	}
	return out, nil
}

// faceKey is a plugin tile's preview key, exactly an internet tile's rule: the
// row's screenshot once one exists, until the next visit replaces it, and the
// plugin's own picture only before the first. The plugin's stamp is negated so
// the two number spaces never meet in the client's (tile, key) cache.
func faceKey(blobID, stamp int64) int64 {
	switch {
	case blobID != 0:
		return blobID
	case stamp > 0:
		return -stamp
	}
	return 0
}

// synthesized is one grid as the adapter derives it; rows[i] matches tiles[i].
type synthesized struct {
	grid    *gridwellv1.Grid
	context string
	gid     int64 // the grid's row, 0 when the context is untouched
	rows    []store.ExtTile
	tiles   []*gridwellv1.Tile
	entries []*pluginv1.Entry
	// dark and authoritative are what the listing was; see absent.
	dark, authoritative bool
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
		context, _, isTile, _ := rpc.SplitEntryID(gridID)
		if isTile {
			return 0, "", status.Errorf(codes.InvalidArgument, "plugin: %q names a tile, not a grid", gridID)
		}
		gid, _, err = a.mem.LookupContext(context)
		return gid, context, err
	default:
		return 0, "", status.Errorf(codes.InvalidArgument, "plugin: invalid grid_id %q", gridID)
	}
}

// synthesize is one grid with its join kept, so GetGrid, Search's key lookup
// and the mint's derived placement read the same tiles.
func (a *Adapter) synthesize(ctx context.Context, gridID string) (*synthesized, error) {
	gid, ckey, err := a.resolveGrid(gridID)
	if err != nil {
		return nil, err
	}
	// A transport failure is "not right now", not a verdict: nothing retires.
	dark := false
	resp, err := a.cp.List(ctx, &pluginv1.ListRequest{Context: ckey})
	if err != nil {
		if gwerr.IsAbandoned(ctx, err) || !gwerr.IsTransport(err) {
			return nil, err
		}
		dark, resp = true, &pluginv1.ListResponse{}
	}
	outage := sourceDetail(err)
	// A memory answer serves and refreshes rows like a live one, but a memory
	// is never a verdict, whatever it claims: nothing retires, and its reason
	// is the source's health as a transport failure's is.
	memory := resp.Unreachable != ""
	if memory {
		outage, resp.Authoritative = resp.Unreachable, false
	}
	a.noteSource(outage != "", outage)
	if err := acceptEntries(ckey, resp.Entries); err != nil {
		return nil, err
	}
	if !dark {
		a.noteLinks(ckey, resp.Entries)
	}
	// An authoritative listing is a verdict on every key, so rows it does not
	// mention retire. An untouched entry has nothing to retire.
	if !dark && resp.Authoritative && gid != 0 {
		present := map[string]bool{}
		for _, e := range resp.Entries {
			present[e.Key] = true
		}
		if err := a.mem.Sweep(gid, present); err != nil {
			return nil, err
		}
	}
	if !dark && gid != 0 {
		if err := a.mem.Refresh(gid, resp.Entries); err != nil {
			return nil, err
		}
	}
	tiles, err := a.mem.Overlay(gid, resp.Entries)
	if err != nil {
		return nil, err
	}
	// A non-authoritative listing sweeps by arbitration: only a definitive
	// GONE from Probe retires an unlisted row.
	if !dark && !memory && !resp.Authoritative {
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
			pr, perr := a.cp.Probe(ctx, &pluginv1.ProbeRequest{Key: t.Key, Context: ckey})
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
	// host_content and glyph ride the grid: a grid reached through a mount has
	// no local row. source_label is the listing's own, so it is as fresh as
	// the rows.
	ci, err := a.cp.Info(ctx, &pluginv1.InfoRequest{})
	if err != nil {
		return nil, err
	}
	addr := rpc.EntryGridID(ckey)
	g := &gridwellv1.Grid{
		Id:          addr,
		HostContent: ci.HostContent,
		Glyph:       ci.Glyph,
		SourceLabel: resp.SourceLabel,
	}
	wire, err := buildTiles(addr, ckey, tiles, resp.Entries, a.mem.ContextKey)
	if err != nil {
		return nil, err
	}
	return &synthesized{grid: g, context: ckey, gid: gid, rows: tiles, tiles: wire, entries: resp.Entries,
		dark: dark, authoritative: resp.Authoritative}, nil
}

func (a *Adapter) GetGrid(ctx context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	s, err := a.synthesize(ctx, req.GridId)
	if err != nil {
		return nil, err
	}
	a.noteServed(s)
	return &gridwellv1.GetGridResponse{Grid: s.grid, Tiles: s.tiles}, nil
}

// tileRef is a resolved tile; id is 0 when untouched.
type tileRef struct {
	id      int64
	gid     int64
	context string
	key     string
}

// resolveTile reads either wire tile id shape, the one place both are read.
func (a *Adapter) resolveTile(tileID string) (tileRef, error) {
	switch rpc.ShapeOf(tileID) {
	case rpc.ShapeRow:
		id, _ := strconv.ParseInt(tileID, 10, 64)
		gid, key, tomb, err := a.mem.TileKey(id)
		// Ids are never reassigned, so a retired or unknown row is dead.
		if errors.Is(err, store.ErrNotFound) || tomb {
			return tileRef{}, gwerr.DeadRef("", "plugin: no tile %d", id)
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
		ckey, key, isTile, _ := rpc.SplitEntryID(tileID)
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

// contentKey is the plugin key alone, all a content verb needs.
func (a *Adapter) contentKey(tileID string) (string, error) {
	ref, err := a.resolveTile(tileID)
	if err != nil {
		return "", err
	}
	return ref.key, nil
}

// mint is the one place a plugin tile becomes a row, at the placement GetGrid
// already answers, so minting never moves anything.
func (a *Adapter) mint(ctx context.Context, tileID string) (int64, error) {
	ref, err := a.resolveTile(tileID)
	if err != nil {
		return 0, err
	}
	if ref.id != 0 {
		return ref.id, nil
	}
	s, err := a.synthesize(ctx, rpc.EntryGridID(ref.context))
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
	return a.mem.Mint(gid, entry, child, row.X, row.Y, row.W, row.H)
}

// MintRef is the router's canonicalizer. A plugin's canonical id is its
// derived address, so this mints nothing; a row id (an older reference) is
// tried as a tile row, then as a grid row.
func (a *Adapter) MintRef(_ context.Context, localID string) (string, error) {
	switch rpc.ShapeOf(localID) {
	case rpc.ShapeRow:
		if ref, err := a.resolveTile(localID); err == nil {
			return rpc.EntryTileID(ref.context, ref.key), nil
		}
		if _, ckey, err := a.resolveGrid(localID); err == nil {
			return rpc.EntryGridID(ckey), nil
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
	s, err := a.synthesize(ctx, rpc.EntryGridID(ref.context))
	if err != nil {
		return nil, err
	}
	if t := s.tileForKey(ref.key); t != nil {
		return t, nil
	}
	return nil, a.absent(ctx, s, ref.key, tileID)
}

// absent is the one answer for a key the listing does not carry: dead only
// when the source has said it is gone, by an authoritative listing or a
// definitive probe; a dark source is never dead.
func (a *Adapter) absent(ctx context.Context, s *synthesized, key, tileID string) error {
	if s.dark {
		return status.Errorf(codes.Unavailable, "plugin: source dark and %q not remembered", tileID)
	}
	if s.authoritative {
		return gwerr.DeadRef("", "plugin: %q is gone", tileID)
	}
	pr, err := a.cp.Probe(ctx, &pluginv1.ProbeRequest{Key: key, Context: s.context})
	if err != nil {
		return err
	}
	if pr.Presence == pluginv1.ProbeResponse_PRESENCE_GONE {
		return gwerr.DeadRef("", "plugin: %q is gone", tileID)
	}
	return status.Errorf(codes.NotFound, "plugin: %q is not listed", tileID)
}

// Search turns each hit into a place (tile plus well chain) through the
// synthesis GetGrid runs, dropping hits it cannot place. An id: locate is
// refused: there is no parent index here.
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
		s, err := a.synthesize(ctx, rpc.EntryGridID(key))
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
			well := parent.tileOpening(rpc.EntryGridID(r.ContextPath[i]))
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

// tileOpening answers the well tile whose descent is the grid, or nil.
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
		_, want, err := a.resolveGrid(req.GridId)
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
	// A placement is part of the grid's answer, so it announces the grid.
	resp, err := a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: strconv.FormatInt(id, 10)})
	if err != nil {
		return nil, err
	}
	a.emitGridChanged(resp.GetTile().GetGridId())
	return resp, nil
}

// SetTile terminates the framing and capture arms at the store. Rename is
// refused (the name is the source's) and a url writeback keeps only its
// screenshot (the address is the plugin's).
func (a *Adapter) SetTile(ctx context.Context, req *gridwellv1.SetTileRequest) (*gridwellv1.TileResponse, error) {
	if req.Rename != "" {
		return nil, status.Error(codes.InvalidArgument, "plugin: tiles derive their names from the source")
	}
	if t := req.GetTile(); t.GetKind() == rpc.KindURL && req.ContentZoom == nil && req.UrlFrozen == nil {
		if t.UrlString != "" || t.AltText != "" || t.UrlHistory != "" {
			return nil, status.Error(codes.InvalidArgument,
				"plugin: a url tile's address, title and history are its plugin's; the writeback carries the screenshot alone")
		}
		if len(req.Preview) == 0 {
			return nil, status.Error(codes.InvalidArgument, "plugin: a url writeback with no screenshot writes nothing")
		}
	}
	var zoom rpc.ContentZoom
	if req.ContentZoom != nil {
		z, err := rpc.NewContentZoom(*req.ContentZoom)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		zoom = z
	}
	id, err := a.mint(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	switch {
	case req.ContentZoom != nil:
		err = a.mem.SetContentZoom(id, zoom)
	case req.UrlFrozen != nil:
		err = a.mem.SetFrozen(id, *req.UrlFrozen)
	default:
		t := req.GetTile()
		switch t.GetKind() {
		case rpc.KindText:
			err = a.mem.SetTextView(id, t.GetTextX(), t.GetTextY(), t.GetTextW(), t.GetTextH(), t.GetTextMode())
		case rpc.KindURL:
			err = a.mem.SetURLPreview(id, req.Preview)
		default:
			return nil, status.Errorf(codes.InvalidArgument, "plugin: unsupported SetTile kind %q", t.GetKind())
		}
	}
	if err != nil {
		return nil, gwerr.ToStatus(err)
	}
	t, err := a.changedTile(ctx, id)
	if err != nil {
		return nil, err
	}
	return &gridwellv1.TileResponse{Tile: t}, nil
}

// changedTile reads back and announces a write to one tile row that changes
// no listing, so a client applies it in place.
func (a *Adapter) changedTile(ctx context.Context, id int64) (*gridwellv1.Tile, error) {
	resp, err := a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: strconv.FormatInt(id, 10)})
	if err != nil {
		return nil, err
	}
	a.hub.Publish(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
		TileChanged: &gridwellv1.TileChanged{Tile: resp.GetTile()},
	}})
	return resp.GetTile(), nil
}

// SetFraming persists framing on a doorway tile row or a context's grid row.
func (a *Adapter) SetFraming(ctx context.Context, req *gridwellv1.SetFramingRequest) (*gridwellv1.SetFramingResponse, error) {
	f, err := rpc.FramingOf(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if req.RootGridId != "" {
		_, ckey, err := a.resolveGrid(req.RootGridId)
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
		a.hub.Publish(rpc.FramingEvent(rpc.EntryGridID(ckey), f))
		return &gridwellv1.SetFramingResponse{}, nil
	}
	id, err := a.mint(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	if err := a.mem.SetFraming(id, 0, f); err != nil {
		return nil, err
	}
	t, err := a.changedTile(ctx, id)
	if err != nil {
		return nil, err
	}
	return &gridwellv1.SetFramingResponse{Tile: t}, nil
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

// GetTilePreview answers the face faceKey named.
func (a *Adapter) GetTilePreview(ctx context.Context, req *gridwellv1.GetTilePreviewRequest) (*gridwellv1.GetTilePreviewResponse, error) {
	ref, err := a.resolveTile(req.TileId)
	if err != nil {
		return nil, err
	}
	if ref.id != 0 {
		jpeg, err := a.mem.Preview(ref.id)
		if err != nil {
			return nil, gwerr.ToStatus(err)
		}
		if len(jpeg) > 0 {
			return &gridwellv1.GetTilePreviewResponse{Jpeg: jpeg}, nil
		}
	}
	resp, err := a.cp.GetPreview(ctx, &pluginv1.GetPreviewRequest{Key: ref.key})
	if err != nil {
		return nil, err
	}
	return &gridwellv1.GetTilePreviewResponse{Jpeg: resp.Jpeg}, nil
}

func (a *Adapter) Probe(ctx context.Context, req *gridwellv1.ProbeRequest) (*gridwellv1.ProbeResponse, error) {
	// An id this namespace cannot read at all is GONE. A derived address
	// always resolves to a key, and the plugin says whether the key is there.
	ref, err := a.resolveTile(req.TileId)
	if err != nil {
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	resp, err := a.cp.Probe(ctx, &pluginv1.ProbeRequest{Key: ref.key, Context: ref.context})
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

// DeleteTile hands the gesture to the plugin, which says what deleting means,
// and retires the row only on a definitive GONE: a delete may transform rather
// than remove, and retiring a live thing would kill every link to it.
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
		pr, perr := a.cp.Probe(ctx, &pluginv1.ProbeRequest{Key: ref.key, Context: ref.context})
		if perr == nil && pr.Presence == pluginv1.ProbeResponse_PRESENCE_GONE {
			if err := a.mem.Retire(ref.id); err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, fmt.Errorf("plugin: source deleted but row not retired: %w", err)
			}
		}
	}
	// Either way the source changed and the client must look again.
	a.emitGridChanged(rpc.EntryGridID(ref.context))
	return &gridwellv1.DeleteTileResponse{}, nil
}
