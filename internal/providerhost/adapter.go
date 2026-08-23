// Package providerhost adapts a v2 ContentProvider to the full Gridwell
// service (docs/v2-design.md §4): the node-side half of the split. The
// adapter joins the provider's content answers (keys, kinds, labels,
// bytes) with the external's memory DB (ids, placement, framing — the
// layout engine) and registers in the plugin registry like any plugin,
// so the router, the client, and federation never know the difference.
//
// This is THE seam of the v2 design — the one place two owners' facts
// meet — which is why it stays thin: every merge decision lives in
// internal/layout (pure, exhaustively unit-tested), every content
// derivation lives in the provider, and the adapter only converts and
// forwards. Presentation verbs terminate here; content verbs pass
// through; a provider outage degrades to the remembered listing (the
// read-through cache, tenet 6 — I12 as node machinery).
package providerhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	cpv1 "github.com/josephburnett/gridwell/api/gen/contentprovider/v1"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/layout"
)

// Adapter implements gridwellv1.GridwellServer over one provider + its
// memory DB.
type Adapter struct {
	gridwellv1.UnimplementedGridwellServer
	cp  cpv1.ContentProviderClient
	mem *layout.DB
}

// New builds the adapter. The caller owns both halves' lifecycles.
func New(cp cpv1.ContentProviderClient, mem *layout.DB) *Adapter {
	return &Adapter{cp: cp, mem: mem}
}

// Info translates the provider handshake, minting the root context's
// grid id and reading its persisted viewport from the memory DB.
func (a *Adapter) Info(ctx context.Context, _ *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	ci, err := a.cp.Info(ctx, &cpv1.InfoRequest{})
	if err != nil {
		return nil, err
	}
	resp := &gridwellv1.InfoResponse{
		Kind:        ci.Kind,
		DisplayName: ci.DisplayName,
		Glyph:       ci.Glyph,
		Watch:       ci.Watch,
		Writable:    ci.Writable,
	}
	for _, m := range ci.MenuEntries {
		out := &gridwellv1.MenuEntry{
			Id: m.Id, Label: m.Label, Glyph: m.Glyph, Color: m.Color,
			Kind: m.Kind, ParamSchema: m.ParamSchema,
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

// notNow reports a transport-shaped failure — "the source didn't answer",
// as opposed to an answered verdict. Only these degrade to the cache.
func notNow(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled:
		return true
	}
	return false
}

// listing fetches a context's listing, degrading to the remembered
// answer on a transport-shaped failure. stale reports a cache serve.
func (a *Adapter) listing(ctx context.Context, gid int64, key string) (resp *cpv1.ListResponse, stale bool, err error) {
	resp, err = a.cp.List(ctx, &cpv1.ListRequest{Context: key})
	if err == nil {
		if blob, merr := proto.Marshal(resp); merr == nil {
			// A cache write failing must not fail the read.
			_ = a.mem.CacheListing(gid, blob, resp.Authoritative)
		}
		return resp, false, nil
	}
	if !notNow(err) {
		return nil, false, err
	}
	blob, _, ok, cerr := a.mem.CachedListing(gid)
	if cerr != nil || !ok {
		return nil, false, err // no remembered answer: the failure stands
	}
	cached := &cpv1.ListResponse{}
	if uerr := proto.Unmarshal(blob, cached); uerr != nil {
		return nil, false, err
	}
	return cached, true, nil
}

// engineEntries converts listing entries for the layout engine.
func engineEntries(entries []*cpv1.Entry) []layout.Entry {
	out := make([]layout.Entry, len(entries))
	for i, e := range entries {
		le := layout.Entry{Key: e.Key, Kind: e.Kind, Label: e.Label, ChildContext: e.ChildContext}
		if h := e.PlacementHint; h != nil {
			le.Hint = &layout.Hint{X: h.X, Y: h.Y, W: h.W, H: h.H}
		}
		out[i] = le
	}
	return out
}

// buildTiles joins merged layout tiles with the listing's content facts.
func buildTiles(gridID string, tiles []layout.Tile, entries []*cpv1.Entry) []*gridwellv1.Tile {
	byKey := map[string]*cpv1.Entry{}
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

// grid fetches, merges, and builds one grid — GetGrid's core, shared
// with GetTile so the two can never disagree.
func (a *Adapter) grid(ctx context.Context, gid int64) (*gridwellv1.Grid, []*gridwellv1.Tile, error) {
	key, err := a.mem.ContextKey(gid)
	if errors.Is(err, layout.ErrNotFound) {
		return nil, nil, status.Errorf(codes.NotFound, "provider: no grid %d", gid)
	}
	if err != nil {
		return nil, nil, err
	}
	resp, stale, err := a.listing(ctx, gid, key)
	if err != nil {
		return nil, nil, err
	}
	// A stale (remembered) listing must never retire rows — the source
	// didn't answer; nothing is authoritatively absent.
	authoritative := resp.Authoritative && !stale
	tiles, err := a.mem.Merge(gid, engineEntries(resp.Entries), authoritative)
	if err != nil {
		return nil, nil, err
	}
	ci, err := a.cp.Info(ctx, &cpv1.InfoRequest{})
	if err != nil {
		return nil, nil, err
	}
	gridID := strconv.FormatInt(gid, 10)
	g := &gridwellv1.Grid{Id: gridID, SourceKind: ci.Kind, SourceId: resp.SourceLabel, Stale: stale}
	return g, buildTiles(gridID, tiles, resp.Entries), nil
}

func (a *Adapter) GetGrid(ctx context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	gid, err := strconv.ParseInt(req.GridId, 10, 64)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "provider: invalid grid_id %q", req.GridId)
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
		return nil, status.Errorf(codes.InvalidArgument, "provider: invalid tile_id %q", tileID)
	}
	gid, _, tomb, err := a.mem.TileKey(id)
	if errors.Is(err, layout.ErrNotFound) || tomb {
		return nil, status.Errorf(codes.NotFound, "provider: no tile %d", id)
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
	return nil, status.Errorf(codes.NotFound, "provider: no tile %d", id)
}

func (a *Adapter) GetTile(ctx context.Context, req *gridwellv1.GetTileRequest) (*gridwellv1.TileResponse, error) {
	t, err := a.tileByID(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	return &gridwellv1.TileResponse{Tile: t}, nil
}

// key resolves a LIVE tile id to its provider key.
func (a *Adapter) key(tileID string) (int64, string, error) {
	id, err := strconv.ParseInt(tileID, 10, 64)
	if err != nil {
		return 0, "", status.Errorf(codes.InvalidArgument, "provider: invalid tile_id %q", tileID)
	}
	_, key, tomb, err := a.mem.TileKey(id)
	if errors.Is(err, layout.ErrNotFound) || tomb {
		return 0, "", status.Errorf(codes.NotFound, "provider: no tile %d", id)
	}
	if err != nil {
		return 0, "", err
	}
	return id, key, nil
}

// PlaceTile terminates at the memory DB: in-grid only, unversioned (the
// legacy griddb semantics the parity gate pins).
func (a *Adapter) PlaceTile(_ context.Context, req *gridwellv1.PlaceTileRequest) (*gridwellv1.TileResponse, error) {
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
			return nil, status.Errorf(codes.InvalidArgument, "provider: cross-grid placement not supported")
		}
	}
	if err := a.mem.Place(id, req.X, req.Y, req.W, req.H); err != nil {
		return nil, err
	}
	return a.GetTile(context.Background(), &gridwellv1.GetTileRequest{TileId: req.TileId})
}

// SetTile terminates the framing arms at the memory DB. Rename is
// refused (a provider tile's name IS its source name); content arms
// don't exist for provider tiles.
func (a *Adapter) SetTile(ctx context.Context, req *gridwellv1.SetTileRequest) (*gridwellv1.TileResponse, error) {
	id, _, err := a.key(req.TileId)
	if err != nil {
		return nil, err
	}
	switch {
	case req.Rename != "":
		return nil, status.Error(codes.InvalidArgument, "provider: tiles derive their names from the source")
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
			return nil, status.Errorf(codes.InvalidArgument, "provider: unsupported SetTile kind %q", t.GetKind())
		}
	}
	return a.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: req.TileId})
}

// SetRootView persists a context's viewport (framing-class).
func (a *Adapter) SetRootView(_ context.Context, req *gridwellv1.SetRootViewRequest) (*gridwellv1.SetRootViewResponse, error) {
	gid, err := strconv.ParseInt(req.RootGridId, 10, 64)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "provider: invalid root_grid_id %q", req.RootGridId)
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
	cs, err := a.cp.ReadContent(stream.Context(), &cpv1.ReadContentRequest{Key: key})
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
		// Provider content is not version-edited: version 0, the legacy
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
	cs, err := a.cp.ServeContent(stream.Context(), &cpv1.ServeContentRequest{Key: key, Subpath: req.Subpath})
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
	resp, err := a.cp.GetPreview(ctx, &cpv1.GetPreviewRequest{Key: key})
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
	if errors.Is(err, layout.ErrNotFound) || tomb {
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	if err != nil {
		return nil, err
	}
	resp, err := a.cp.Probe(ctx, &cpv1.ProbeRequest{Key: key})
	if err != nil {
		if notNow(err) {
			return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_UNSPECIFIED}, nil
		}
		return nil, err
	}
	// The enums are defined identically; map by name to keep that a
	// checked fact rather than a numeric coincidence.
	switch resp.Presence {
	case cpv1.ProbeResponse_PRESENCE_PRESENT:
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_PRESENT}, nil
	case cpv1.ProbeResponse_PRESENCE_GONE:
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	default:
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_UNSPECIFIED}, nil
	}
}

// DeleteTile deletes the SOURCE thing (the provider's verdict), then
// retires the row. Already-gone rows succeed — idempotent, like legacy.
func (a *Adapter) DeleteTile(ctx context.Context, req *gridwellv1.DeleteTileRequest) (*gridwellv1.DeleteTileResponse, error) {
	id, err := strconv.ParseInt(req.TileId, 10, 64)
	if err != nil {
		return &gridwellv1.DeleteTileResponse{}, nil
	}
	_, key, tomb, kerr := a.mem.TileKey(id)
	if errors.Is(kerr, layout.ErrNotFound) || tomb {
		return &gridwellv1.DeleteTileResponse{}, nil
	}
	if kerr != nil {
		return nil, kerr
	}
	if _, err := a.cp.Delete(ctx, &cpv1.DeleteRequest{Key: key}); err != nil {
		return nil, err
	}
	if err := a.mem.Retire(id); err != nil && !errors.Is(err, layout.ErrNotFound) {
		return nil, fmt.Errorf("provider: source deleted but row not retired: %w", err)
	}
	return &gridwellv1.DeleteTileResponse{}, nil
}
