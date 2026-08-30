// Package pluginhost adapts a v2 Plugin to the full Gridwell
// service (docs/v2-design.md §4): the node-side half of the split. The
// adapter joins the plugin's content answers (keys, kinds, labels,
// bytes) with the external's memory DB (ids, placement, framing — the
// layout engine) and registers in the plugin registry like any plugin,
// so the router, the client, and federation never know the difference.
//
// This is THE seam of the v2 design — the one place two owners' facts
// meet — which is why it stays thin: every merge decision lives in
// the store's externals engine (store.Namespace, unit-tested), every content
// derivation lives in the plugin, and the adapter only converts and
// forwards. Presentation verbs terminate here; content verbs pass
// through; a plugin outage degrades to the remembered listing (the
// read-through cache, tenet 6 — I12 as node machinery).
package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// Adapter implements gridwellv1.GridwellServer over one plugin + its
// namespace of the node's store (docs/one-node.md §2.6).
type Adapter struct {
	gridwellv1.UnimplementedGridwellServer
	cp  pluginv1.PluginClient
	mem *store.Namespace

	// kind memoizes the plugin's declared Kind after the first
	// successful handshake. Identity-stable for the plugin's lifetime
	// — remembered so a DARK plugin (crashed subprocess) degrades to
	// the cached listing instead of failing every GetGrid on the Info
	// call (tenet 6: the remembered answer, stamped stale).
	kindMu sync.Mutex
	kind   string
}

// New builds the adapter. The caller owns both halves' lifecycles.
func New(cp pluginv1.PluginClient, mem *store.Namespace) *Adapter {
	return &Adapter{cp: cp, mem: mem}
}

// Info translates the plugin handshake, minting the root context's
// grid id and reading its persisted viewport from the memory DB.
func (a *Adapter) Info(ctx context.Context, _ *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	ci, err := a.cp.Info(ctx, &pluginv1.InfoRequest{})
	if err != nil {
		return nil, err
	}
	resp := &gridwellv1.InfoResponse{
		Kind:        ci.Kind,
		DisplayName: ci.DisplayName,
		Glyph:       ci.Glyph,
		// Watch and Writable are the ADAPTER's declarations, not the
		// plugin's: the node-facing Info describes the doors this
		// adapter opens, and it has no Subscribe (a passed-through
		// watch:true sent the server's watchPlugin into Unimplemented
		// retries forever) and no WriteContent (a passed-through
		// writable:true offered editing that was then refused). Both
		// stay false until the adapter carries them — Subscribe over
		// cp.Watch (ContextChanged → GridChanged by ContextID,
		// EntryRemoved → TileRemoved by the id map) and WriteContent
		// forwarding by key — at which point they follow ci again.
		Watch:    false,
		Writable: false,
	}
	for _, m := range ci.MenuEntries {
		// A menu entry names an extra plugin ROOT: the context it
		// targets becomes a grid id the node can serve. (The
		// creation-entry half — mint a tool tile from a drop — was
		// removed 2026-08-29; the adapter had stripped it since #258
		// landed, so no client ever saw one.)
		out := &gridwellv1.MenuEntry{
			Id: m.Id, Label: m.Label, Glyph: m.Glyph, Color: m.Color,
		}
		if m.Context != "" {
			gid, err := a.mem.ContextID(m.Context)
			if err != nil {
				return nil, err
			}
			out.GridId = strconv.FormatInt(gid, 10)
		}
		resp.MenuEntries = append(resp.MenuEntries, out)
	}
	if ci.RootContext != "" {
		gid, err := a.mem.ContextID(ci.RootContext)
		if err != nil {
			return nil, err
		}
		resp.RootGridId = strconv.FormatInt(gid, 10)
		if cx, cy, zoom, ok, err := a.mem.RootView(gid); err == nil && ok {
			resp.RootViewCx, resp.RootViewCy, resp.RootViewZoom = cx, cy, zoom
		}
	}
	return resp, nil
}

// listing fetches a context's listing, degrading to the remembered
// answer on a transport-shaped failure. stale reports a cache serve.
// facts is the entry-fact lookup: the live entries, UNIONED (for a
// non-authoritative listing) with previously remembered ones — a pid
// unreadable this pass still has its kind and label from the last pass
// it was seen (the legacy stored-row behavior, now cache behavior).
func (a *Adapter) listing(ctx context.Context, gid int64, key string) (resp *pluginv1.ListResponse, facts []*pluginv1.Entry, stale bool, err error) {
	prev := a.cachedListing(gid)
	resp, err = a.cp.List(ctx, &pluginv1.ListRequest{Context: key})
	if err == nil {
		facts = resp.Entries
		if !resp.Authoritative && prev != nil {
			// A remembered key that was RETIRED and is not in the live
			// listing stays retired: unioning it back would re-mint a
			// fresh id every read (mint → probe → GONE → retire → cache
			// remembers → mint …) — unbounded id burn on a read path.
			// A retired key that IS live again (a recycled pid) enters
			// as a live entry and mints fresh, which is the identity
			// rule for a recreated thing.
			remembered := prev.Entries
			if retired, rerr := a.mem.RetiredKeys(gid); rerr == nil && len(retired) > 0 {
				liveKeys := map[string]bool{}
				for _, e := range resp.Entries {
					liveKeys[e.Key] = true
				}
				kept := make([]*pluginv1.Entry, 0, len(remembered))
				for _, e := range remembered {
					if retired[e.Key] && !liveKeys[e.Key] {
						continue
					}
					kept = append(kept, e)
				}
				remembered = kept
			}
			facts = unionEntries(resp.Entries, remembered)
		}
		// Remember the UNION so facts survive repeated unreadable
		// passes. A cache write failing must not fail the read.
		toCache := resp
		if !resp.Authoritative {
			toCache = &pluginv1.ListResponse{Entries: facts, Authoritative: false, SourceLabel: resp.SourceLabel}
		}
		if blob, merr := proto.Marshal(toCache); merr == nil {
			_ = a.mem.CacheListing(gid, blob, resp.Authoritative)
		}
		return resp, facts, false, nil
	}
	if !gwerr.IsTransport(err) {
		return nil, nil, false, err
	}
	if prev == nil {
		return nil, nil, false, err // no remembered answer: the failure stands
	}
	return prev, prev.Entries, true, nil
}

// cachedListing loads the remembered listing, nil when none/corrupt.
func (a *Adapter) cachedListing(gid int64) *pluginv1.ListResponse {
	blob, _, ok, err := a.mem.CachedListing(gid)
	if err != nil || !ok {
		return nil
	}
	cached := &pluginv1.ListResponse{}
	if uerr := proto.Unmarshal(blob, cached); uerr != nil {
		return nil
	}
	return cached
}

// unionEntries returns live entries plus remembered ones live didn't
// include (live wins on a shared key).
func unionEntries(live, remembered []*pluginv1.Entry) []*pluginv1.Entry {
	seen := map[string]bool{}
	for _, e := range live {
		seen[e.Key] = true
	}
	out := append([]*pluginv1.Entry(nil), live...)
	for _, e := range remembered {
		if !seen[e.Key] {
			out = append(out, e)
		}
	}
	return out
}

// engineEntries converts listing entries for the layout engine.
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

// buildTiles joins merged layout tiles with the listing's content facts.
func buildTiles(gridID string, tiles []store.ExtTile, entries []*pluginv1.Entry) []*gridwellv1.Tile {
	byKey := map[string]*pluginv1.Entry{}
	for _, e := range entries {
		byKey[e.Key] = e
	}
	out := make([]*gridwellv1.Tile, 0, len(tiles))
	for _, t := range tiles {
		pt := &gridwellv1.Tile{
			Id:          strconv.FormatInt(t.ID, 10),
			GridId:      gridID,
			Kind:        t.Kind,
			X:           t.X,
			Y:           t.Y,
			W:           t.W,
			H:           t.H,
			ViewX:       t.ViewX,
			ViewY:       t.ViewY,
			ViewZoom:    t.ViewZoom,
			TextX:       t.TextX,
			TextY:       t.TextY,
			TextW:       t.TextW,
			TextH:       t.TextH,
			TextMode:    t.TextMode,
			ContentZoom: t.ContentZoom,
			AltText:     t.Label,
		}
		if t.ChildGridID != 0 {
			pt.ChildGridId = strconv.FormatInt(t.ChildGridID, 10)
		}
		if e, ok := byKey[t.Key]; ok {
			pt.ServesPage = e.ServesPage
			pt.TextPresentation = e.TextPresentation
			pt.PreviewBlobId = e.PreviewStamp
			pt.StatusDetail = e.StatusDetail
			pt.UrlString = e.UrlString
		}
		out = append(out, pt)
	}
	return out
}

// sourceKind answers the plugin's declared Kind, from the memo when
// the plugin is unreachable — a read that already degraded to the
// remembered listing must not fail on this identity-stable stamp.
func (a *Adapter) sourceKind(ctx context.Context) (string, error) {
	a.kindMu.Lock()
	memo := a.kind
	a.kindMu.Unlock()
	ci, err := a.cp.Info(ctx, &pluginv1.InfoRequest{})
	if err != nil {
		if memo != "" && gwerr.IsTransport(err) {
			return memo, nil
		}
		return "", err
	}
	a.kindMu.Lock()
	a.kind = ci.Kind
	a.kindMu.Unlock()
	return ci.Kind, nil
}

// synthesized is one grid as the adapter derives it: the wire grid, the
// merged rows (which carry the plugin keys), and the wire tiles, row i
// ↔ tile i.
type synthesized struct {
	grid  *gridwellv1.Grid
	rows  []store.ExtTile
	tiles []*gridwellv1.Tile
}

// grid fetches, merges, and builds one grid — GetGrid's core, shared
// with GetTile so the two can never disagree.
func (a *Adapter) grid(ctx context.Context, gid int64) (*gridwellv1.Grid, []*gridwellv1.Tile, error) {
	s, err := a.synthesize(ctx, gid)
	if err != nil {
		return nil, nil, err
	}
	return s.grid, s.tiles, nil
}

// synthesize is grid() keeping the merged rows: Search resolves a
// plugin key to its tile through the rows, so a hit is the SAME tile a
// GetGrid mints (never a parallel derivation).
func (a *Adapter) synthesize(ctx context.Context, gid int64) (*synthesized, error) {
	key, err := a.mem.ContextKey(gid)
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "plugin: no grid %d", gid)
	}
	if err != nil {
		return nil, err
	}
	resp, facts, stale, err := a.listing(ctx, gid, key)
	if err != nil {
		return nil, err
	}
	// A stale (remembered) listing must never retire rows — the source
	// didn't answer; nothing is authoritatively absent.
	authoritative := resp.Authoritative && !stale
	tiles, err := a.mem.Merge(gid, engineEntries(facts), authoritative)
	if err != nil {
		return nil, err
	}
	// A LIVE non-authoritative listing sweeps by arbitration: rows the
	// listing didn't include are probed, and only a definitive GONE
	// retires them (the legacy proc reconcile, as adapter machinery).
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
			kept = append(kept, t) // uncertain or alive: keep (I12)
		}
		tiles = kept
	}
	kind, err := a.sourceKind(ctx)
	if err != nil {
		return nil, err
	}
	gridID := strconv.FormatInt(gid, 10)
	g := &gridwellv1.Grid{Id: gridID, SourceKind: kind, SourceId: resp.SourceLabel, Stale: stale}
	return &synthesized{grid: g, rows: tiles, tiles: buildTiles(gridID, tiles, facts)}, nil
}

func (a *Adapter) GetGrid(ctx context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	gid, err := strconv.ParseInt(req.GridId, 10, 64)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "plugin: invalid grid_id %q", req.GridId)
	}
	g, tiles, err := a.grid(ctx, gid)
	if err != nil {
		return nil, err
	}
	return &gridwellv1.GetGridResponse{Grid: g, Tiles: tiles}, nil
}

// tileByID resolves one tile through the SAME grid synthesis GetGrid
// uses (never a parallel derivation).
func (a *Adapter) tileByID(ctx context.Context, tileID string) (*gridwellv1.Tile, error) {
	id, err := strconv.ParseInt(tileID, 10, 64)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "plugin: invalid tile_id %q", tileID)
	}
	gid, _, tomb, err := a.mem.TileKey(id)
	if errors.Is(err, store.ErrNotFound) || tomb {
		return nil, status.Errorf(codes.NotFound, "plugin: no tile %d", id)
	}
	if err != nil {
		return nil, err
	}
	_, tiles, err := a.grid(ctx, gid)
	if err != nil {
		return nil, err
	}
	for _, t := range tiles {
		if t.Id == tileID {
			return t, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "plugin: no tile %d", id)
}

// Search forwards the query to the plugin and turns each hit into a
// PLACE the way the store's Search answers one: the tile, plus the
// containing-well chain from the plugin root. The plugin names a key
// and a context path; the adapter resolves both through the SAME grid
// synthesis GetGrid runs — one synthesis per distinct context per
// call — so a hit carries the id the memory DB minted, at the placement
// the user left it. A hit the synthesis cannot place (the key is not in
// its context's listing, a path step is not a well of the step before)
// is dropped: a result is a promise you can go there. An `id:` locate
// is refused: the memory DB keeps no parent index, and an empty or
// root-anchored path would be a wrong place, not a missing one.
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
		gid, err := a.mem.ContextID(key)
		if err != nil {
			return nil, err
		}
		s, err := a.synthesize(ctx, gid)
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
			cgid, err := a.mem.ContextID(r.ContextPath[i])
			if err != nil {
				return nil, err
			}
			well := parent.tileOpening(strconv.FormatInt(cgid, 10))
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

// tileForKey answers the wire tile minted for a plugin key, nil if the
// synthesis holds none.
func (s *synthesized) tileForKey(key string) *gridwellv1.Tile {
	for i, row := range s.rows {
		if row.Key == key {
			return s.tiles[i]
		}
	}
	return nil
}

// tileOpening answers the well tile whose descent is the grid, nil if
// none.
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

// key resolves a LIVE tile id to its plugin key.
func (a *Adapter) key(tileID string) (int64, string, error) {
	id, err := strconv.ParseInt(tileID, 10, 64)
	if err != nil {
		return 0, "", status.Errorf(codes.InvalidArgument, "plugin: invalid tile_id %q", tileID)
	}
	_, key, tomb, err := a.mem.TileKey(id)
	if errors.Is(err, store.ErrNotFound) || tomb {
		return 0, "", status.Errorf(codes.NotFound, "plugin: no tile %d", id)
	}
	if err != nil {
		return 0, "", err
	}
	return id, key, nil
}

// PlaceTile terminates at the memory DB: in-grid only, unversioned (the
// retired legacy plugins' semantics, carried verbatim).
func (a *Adapter) PlaceTile(ctx context.Context, req *gridwellv1.PlaceTileRequest) (*gridwellv1.TileResponse, error) {
	id, _, err := a.key(req.TileId)
	if err != nil {
		return nil, err
	}
	if req.GridId != "" {
		gid, _, _, kerr := a.mem.TileKey(id)
		if kerr != nil {
			return nil, kerr
		}
		if req.GridId != strconv.FormatInt(gid, 10) {
			return nil, status.Errorf(codes.InvalidArgument, "plugin: cross-grid placement not supported")
		}
	}
	if err := a.mem.Place(id, req.X, req.Y, req.W, req.H); err != nil {
		return nil, err
	}
	return a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: req.TileId})
}

// SetTile terminates the framing arms at the memory DB. Rename is
// refused (a plugin tile's name IS its source name); content arms
// don't exist for plugin tiles.
func (a *Adapter) SetTile(ctx context.Context, req *gridwellv1.SetTileRequest) (*gridwellv1.TileResponse, error) {
	id, _, err := a.key(req.TileId)
	if err != nil {
		return nil, err
	}
	switch {
	case req.Rename != "":
		return nil, status.Error(codes.InvalidArgument, "plugin: tiles derive their names from the source")
	case req.ContentZoom != nil:
		if err := a.mem.SetContentZoom(id, *req.ContentZoom); err != nil {
			return nil, err
		}
	default:
		t := req.GetTile()
		switch t.GetKind() {
		case "well":
			if err := a.mem.SetWellView(id, t.GetViewX(), t.GetViewY(), t.GetViewZoom()); err != nil {
				return nil, err
			}
		case "text":
			if err := a.mem.SetTextView(id, t.GetTextX(), t.GetTextY(), t.GetTextW(), t.GetTextH(), t.GetTextMode()); err != nil {
				return nil, err
			}
		default:
			return nil, status.Errorf(codes.InvalidArgument, "plugin: unsupported SetTile kind %q", t.GetKind())
		}
	}
	return a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: req.TileId})
}

// SetRootView persists a context's viewport (framing-class).
func (a *Adapter) SetRootView(_ context.Context, req *gridwellv1.SetRootViewRequest) (*gridwellv1.SetRootViewResponse, error) {
	gid, err := strconv.ParseInt(req.RootGridId, 10, 64)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "plugin: invalid root_grid_id %q", req.RootGridId)
	}
	if err := a.mem.SetRootView(gid, req.Cx, req.Cy, req.Zoom); err != nil {
		return nil, err
	}
	return &gridwellv1.SetRootViewResponse{}, nil
}

func (a *Adapter) ReadContent(req *gridwellv1.ReadContentRequest, stream grpc.ServerStreamingServer[gridwellv1.ContentChunk]) error {
	_, key, err := a.key(req.TileId)
	if err != nil {
		return err
	}
	cs, err := a.cp.ReadContent(stream.Context(), &pluginv1.ReadContentRequest{Key: key})
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
		// Plugin content is not version-edited: version 0, the legacy
		// fs/proc wire fact.
		if serr := stream.Send(&gridwellv1.ContentChunk{Data: chunk.Data, MediaType: chunk.MediaType}); serr != nil {
			return serr
		}
	}
}

func (a *Adapter) ServeContent(req *gridwellv1.ServeContentRequest, stream grpc.ServerStreamingServer[gridwellv1.ServeContentChunk]) error {
	_, key, err := a.key(req.TileId)
	if err != nil {
		return err
	}
	cs, err := a.cp.ServeContent(stream.Context(), &pluginv1.ServeContentRequest{Key: key, Subpath: req.Subpath})
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
		if serr := stream.Send(&gridwellv1.ServeContentChunk{Status: chunk.Status, MediaType: chunk.MediaType, Data: chunk.Data}); serr != nil {
			return serr
		}
	}
}

func (a *Adapter) GetTilePreview(ctx context.Context, req *gridwellv1.GetTilePreviewRequest) (*gridwellv1.GetTilePreviewResponse, error) {
	_, key, err := a.key(req.TileId)
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
	id, err := strconv.ParseInt(req.TileId, 10, 64)
	if err != nil {
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	_, key, tomb, err := a.mem.TileKey(id)
	if errors.Is(err, store.ErrNotFound) || tomb {
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	if err != nil {
		return nil, err
	}
	resp, err := a.cp.Probe(ctx, &pluginv1.ProbeRequest{Key: key})
	if err != nil {
		if gwerr.IsTransport(err) {
			return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_UNSPECIFIED}, nil
		}
		return nil, err
	}
	// The enums are defined identically; map by name to keep that a
	// checked fact rather than a numeric coincidence.
	switch resp.Presence {
	case pluginv1.ProbeResponse_PRESENCE_PRESENT:
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_PRESENT}, nil
	case pluginv1.ProbeResponse_PRESENCE_GONE:
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	default:
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_UNSPECIFIED}, nil
	}
}

// DeleteTile deletes the SOURCE thing (the plugin's verdict), then
// retires the row. Already-gone rows succeed — idempotent, like legacy.
func (a *Adapter) DeleteTile(ctx context.Context, req *gridwellv1.DeleteTileRequest) (*gridwellv1.DeleteTileResponse, error) {
	id, err := strconv.ParseInt(req.TileId, 10, 64)
	if err != nil {
		return &gridwellv1.DeleteTileResponse{}, nil
	}
	_, key, tomb, kerr := a.mem.TileKey(id)
	if errors.Is(kerr, store.ErrNotFound) || tomb {
		return &gridwellv1.DeleteTileResponse{}, nil
	}
	if kerr != nil {
		return nil, kerr
	}
	if _, err := a.cp.Delete(ctx, &pluginv1.DeleteRequest{Key: key}); err != nil {
		return nil, err
	}
	if err := a.mem.Retire(id); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("plugin: source deleted but row not retired: %w", err)
	}
	return &gridwellv1.DeleteTileResponse{}, nil
}
