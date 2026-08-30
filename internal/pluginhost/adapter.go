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
// through.
//
// Outages split by WHOSE fact is missing (docs/simplify-plan.md S7). A
// dark SOURCE (the plugin answers; its directory, its API, its process
// table does not) costs only what the source says: the adapter merges an
// empty non-authoritative listing, so every row it minted still reads —
// same ids, same placement, same labels, whatever the user has since
// moved — stamped stale, retiring nothing. A dark PLUGIN (the subprocess
// is gone) costs the node-side answer too, and THAT is what
// internal/sourcecache remembers, one layer up. The adapter keeps no
// memory of its own: the durable rows are the node's memory of what it
// minted, and the cache is the memory of what the source said.
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

// Adapter implements namespace.Namespace over one plugin + its namespace
// of the node's store (docs/one-node.md §2.6): the router calls it as a Go
// value, and the ONE gRPC hop left underneath is the plugin.v1 subprocess
// — the third-party door (charter, 2026-08-15).
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
		if f, ok, err := a.mem.RootFraming(gid); err == nil && ok {
			resp.RootViewCx, resp.RootViewCy, resp.RootViewZoom = f.Cx, f.Cy, f.Zoom
		}
	}
	return resp, nil
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
	// The listing is the source's half. When it fails transport-shaped —
	// "not right now", not a verdict — the adapter carries on with an
	// EMPTY, non-authoritative one: nothing is authoritatively absent, so
	// Merge retires nothing and answers the rows the node minted. That is
	// the whole degradation; there is no remembered listing to serve,
	// because the rows ARE the remembered answer and they are durable.
	stale := false
	resp, err := a.cp.List(ctx, &pluginv1.ListRequest{Context: key})
	if err != nil {
		if !gwerr.IsTransport(err) {
			return nil, err
		}
		stale, resp = true, &pluginv1.ListResponse{}
	}
	tiles, err := a.mem.Merge(gid, engineEntries(resp.Entries), resp.Authoritative)
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
	// The plugin's declared kind stamps the grid. A dark PLUGIN fails
	// here, and the source cache one layer up answers the whole read from
	// what this namespace last said.
	ci, err := a.cp.Info(ctx, &pluginv1.InfoRequest{})
	if err != nil {
		return nil, err
	}
	kind := ci.Kind
	gridID := strconv.FormatInt(gid, 10)
	g := &gridwellv1.Grid{Id: gridID, SourceKind: kind, SourceId: resp.SourceLabel, Stale: stale}
	return &synthesized{grid: g, rows: tiles, tiles: buildTiles(gridID, tiles, resp.Entries)}, nil
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

// SetFraming persists framing into this plugin's memory — the ONE
// framing write, aimed at a doorway tile row or a context's ROOT grid
// row. Framing-class: the node's memory of the user's view, never the
// plugin's content.
func (a *Adapter) SetFraming(ctx context.Context, req *gridwellv1.SetFramingRequest) (*gridwellv1.SetFramingResponse, error) {
	f := rpc.Framing{Cx: req.Cx, Cy: req.Cy, Zoom: req.Zoom}
	if req.RootGridId != "" {
		gid, err := strconv.ParseInt(req.RootGridId, 10, 64)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "plugin: invalid root_grid_id %q", req.RootGridId)
		}
		if err := a.mem.SetFraming(0, gid, f); err != nil {
			return nil, err
		}
		return &gridwellv1.SetFramingResponse{}, nil
	}
	id, err := strconv.ParseInt(req.TileId, 10, 64)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "plugin: invalid tile_id %q", req.TileId)
	}
	if err := a.mem.SetFraming(id, 0, f); err != nil {
		return nil, err
	}
	t, err := a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: req.TileId})
	if err != nil {
		return nil, err
	}
	return &gridwellv1.SetFramingResponse{Tile: t.GetTile()}, nil
}

func (a *Adapter) ReadContent(ctx context.Context, req *gridwellv1.ReadContentRequest, send func(*gridwellv1.ContentChunk) error) error {
	_, key, err := a.key(req.TileId)
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
		// Plugin content is not version-edited: version 0, the legacy
		// fs/proc wire fact.
		if serr := send(&gridwellv1.ContentChunk{Data: chunk.Data, MediaType: chunk.MediaType}); serr != nil {
			return serr
		}
	}
}

func (a *Adapter) ServeContent(ctx context.Context, req *gridwellv1.ServeContentRequest, send func(*gridwellv1.ServeContentChunk) error) error {
	_, key, err := a.key(req.TileId)
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
