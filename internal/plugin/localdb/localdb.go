// Package localdb implements a Gridwell plugin that wraps the local SQLite
// store. It satisfies gridwellv1.GridwellServer by delegating every RPC to
// store.Store and translating between the proto wire types and the internal
// rpc.* types via the existing rpc.ConvXxx functions.
//
// This is the "main" plugin: it owns the full Gridwell space — wells, text,
// URL, and shell tiles. The fs and proc plugins project external state; this
// plugin owns everything the user creates inside Gridwell.
package localdb

import (
	"context"
	"errors"
	"strconv"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SessionManager is the shell-session lifecycle surface. Wire in the real
// tmux controller in production; leave nil in tests (ShellSessionAlive
// returns false and DeleteTile skips the session cleanup).
type SessionManager interface {
	HasSession(tileID int64) (bool, error)
	Kill(tileID int64) error
}

// Plugin wraps store.Store as a gridwellv1.GridwellServer.
type Plugin struct {
	gridwellv1.UnimplementedGridwellServer
	st   *store.Store
	sess SessionManager // optional
}

// New wraps an open store. sess may be nil.
func New(st *store.Store, sess SessionManager) *Plugin {
	return &Plugin{st: st, sess: sess}
}

// Store returns the underlying store (used by the server to wire shell
// session cleanup and orphan detection at startup).
func (p *Plugin) Store() *store.Store { return p.st }

// Close closes the underlying store.
func (p *Plugin) Close() error { return p.st.Close() }

// ── Lifecycle ────────────────────────────────────────────────────────────────

// Info is the whole handshake: identity plus the default root grid (localdb's
// singleton root). No Attach/Detach — the gRPC connection is the lifecycle.
func (p *Plugin) Info(ctx context.Context, _ *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	id, err := p.st.RootGridID(ctx)
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.InfoResponse{
		Kind:          "localdb",
		DisplayName:   "local",
		SchemaVersion: 1,
		RootGridId:    id,
		HasSession:    true,
	}, nil
}

func (p *Plugin) Probe(ctx context.Context, req *gridwellv1.ProbeRequest) (*gridwellv1.ProbeResponse, error) {
	_, err := p.st.GetTile(ctx, req.TileId)
	if errors.Is(err, store.ErrNotFound) {
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_PRESENT}, nil
}

// ── Reads ────────────────────────────────────────────────────────────────────

func (p *Plugin) GetGrid(ctx context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	r, err := p.st.GetGrid(ctx, req.GridId)
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.GetGridResponse{
		Grid:  rpc.GridToProto(&r.Grid),
		Tiles: rpc.TilesToProto(r.Tiles),
	}, nil
}

func (p *Plugin) GetTilePreview(ctx context.Context, req *gridwellv1.GetTilePreviewRequest) (*gridwellv1.GetTilePreviewResponse, error) {
	jpeg, err := p.st.GetTilePreview(ctx, req.TileId)
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.GetTilePreviewResponse{Jpeg: jpeg}, nil
}

// GetTileContent returns a text tile's stored body bytes (the markdown source).
func (p *Plugin) GetTileContent(ctx context.Context, req *gridwellv1.GetTileContentRequest) (*gridwellv1.GetTileContentResponse, error) {
	tile, err := p.st.GetTile(ctx, req.TileId)
	if err != nil {
		return nil, errToStatus(err)
	}
	if tile.BlobID == 0 {
		return &gridwellv1.GetTileContentResponse{}, nil
	}
	data, err := p.st.GetBlob(ctx, tile.BlobID)
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.GetTileContentResponse{Data: data, MediaType: "text/markdown"}, nil
}

// GetTile reads a single tile's metadata.
func (p *Plugin) GetTile(ctx context.Context, req *gridwellv1.GetTileRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.GetTile(ctx, req.TileId))
}

// SetTileAlt stamps a tile's display label and returns the updated tile.
func (p *Plugin) SetTileAlt(ctx context.Context, req *gridwellv1.SetTileAltRequest) (*gridwellv1.TileResponse, error) {
	id, err := strconv.ParseInt(req.TileId, 10, 64)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tile_id")
	}
	if err := p.st.SetTileAlt(ctx, id, req.Alt); err != nil {
		return nil, errToStatus(err)
	}
	return tileResp(p.st.GetTile(ctx, req.TileId))
}

// ── Creates ──────────────────────────────────────────────────────────────────

// CreateTile is the single create: tile.kind selects which typed store create
// to run. The wire carries one create; localdb fans it back out here.
func (p *Plugin) CreateTile(ctx context.Context, req *gridwellv1.CreateTileRequest) (*gridwellv1.TileResponse, error) {
	t := req.Tile
	if t == nil {
		return nil, status.Error(codes.InvalidArgument, "create: nil tile")
	}
	path := rpc.PathFromProto(req.Path)
	switch t.Kind {
	case rpc.KindWell:
		// child_grid_id set → an exit well pointing at a grid owned by another
		// plugin (a mounted DB, an fs/proc grid). No interior child grid is
		// allocated; the cross-plugin reference is stored verbatim. alt_text is
		// the exit well's label.
		if t.ChildGridId != "" {
			return tileResp(p.st.CreateExitWell(ctx, path, req.GridId, t.X, t.Y, t.W, t.H, t.ChildGridId, t.AltText))
		}
		return tileResp(p.st.CreateWell(ctx, &rpc.CreateWellRequest{Path: path, GridID: req.GridId, X: t.X, Y: t.Y, W: t.W, H: t.H}))
	case rpc.KindText:
		return tileResp(p.st.CreateText(ctx, &rpc.CreateTextRequest{Path: path, GridID: req.GridId, X: t.X, Y: t.Y, W: t.W, H: t.H, Data: req.Data}))
	case rpc.KindURL:
		return tileResp(p.st.CreateURL(ctx, &rpc.CreateURLRequest{Path: path, GridID: req.GridId, X: t.X, Y: t.Y, W: t.W, H: t.H, URL: t.UrlString}))
	case rpc.KindShell:
		return tileResp(p.st.CreateShell(ctx, &rpc.CreateShellRequest{Path: path, GridID: req.GridId, X: t.X, Y: t.Y, W: t.W, H: t.H}))
	default:
		return nil, status.Errorf(codes.InvalidArgument, "create: unknown kind %q", t.Kind)
	}
}

// ── Mutations ────────────────────────────────────────────────────────────────

func (p *Plugin) MoveTile(ctx context.Context, req *gridwellv1.MoveTileRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.MoveTile(ctx, rpc.MoveTileFromProto(req)))
}

func (p *Plugin) CloneTile(ctx context.Context, req *gridwellv1.CloneTileRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.CloneTile(ctx, rpc.CloneTileFromProto(req)))
}

func (p *Plugin) ResizeTile(ctx context.Context, req *gridwellv1.ResizeTileRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.ResizeTile(ctx, rpc.ResizeTileFromProto(req)))
}

// SetTile is the single framing/preview writeback: tile.kind selects the one
// store operation that kind supports, and that mapping fixes the version
// semantics — well/text framing never bumps version, url/shell preview does.
func (p *Plugin) SetTile(ctx context.Context, req *gridwellv1.SetTileRequest) (*gridwellv1.TileResponse, error) {
	t := req.Tile
	if t == nil {
		return nil, status.Error(codes.InvalidArgument, "set: nil tile")
	}
	path := rpc.PathFromProto(req.Path)
	switch t.Kind {
	case rpc.KindWell:
		return tileResp(p.st.SetWellView(ctx, &rpc.SetWellViewRequest{Path: path, TileID: req.TileId, Version: req.Version, ViewX: t.ViewX, ViewY: t.ViewY, ViewZoom: t.ViewZoom}))
	case rpc.KindText:
		return tileResp(p.st.SetTextView(ctx, &rpc.SetTextViewRequest{Path: path, TileID: req.TileId, Version: req.Version, TextX: t.TextX, TextY: t.TextY, TextW: t.TextW, TextH: t.TextH, TextMode: t.TextMode}))
	case rpc.KindShell:
		return tileResp(p.st.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{Path: path, TileID: req.TileId, Version: req.Version, JPEG: req.Preview}))
	case rpc.KindURL:
		return tileResp(p.st.SetURLState(ctx, &rpc.SetURLStateRequest{Path: path, TileID: req.TileId, Version: req.Version, JPEG: req.Preview, URL: t.UrlString, Title: t.AltText}))
	default:
		return nil, status.Errorf(codes.InvalidArgument, "set: unknown kind %q", t.Kind)
	}
}

func (p *Plugin) ShellSessionAlive(_ context.Context, req *gridwellv1.ShellSessionAliveRequest) (*gridwellv1.ShellSessionAliveResponse, error) {
	if p.sess == nil {
		return &gridwellv1.ShellSessionAliveResponse{Alive: false}, nil
	}
	alive := false
	if id, err := strconv.ParseInt(req.TileId, 10, 64); err == nil {
		alive, _ = p.sess.HasSession(id)
	}
	return &gridwellv1.ShellSessionAliveResponse{Alive: alive}, nil
}

func (p *Plugin) UpdateText(ctx context.Context, req *gridwellv1.UpdateTextRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.UpdateText(ctx, rpc.UpdateTextFromProto(req)))
}

func (p *Plugin) DeleteTile(ctx context.Context, req *gridwellv1.DeleteTileRequest) (*gridwellv1.DeleteTileResponse, error) {
	tileID := req.TileId
	if err := p.st.DeleteTile(ctx, rpc.DeleteTileFromProto(req)); err != nil {
		return nil, errToStatus(err)
	}
	// Clean up any orphaned shell session for the deleted tile.
	if p.sess != nil {
		exists, err := p.st.ShellTileExists(ctx, tileID)
		if err == nil && !exists {
			if id, perr := strconv.ParseInt(tileID, 10, 64); perr == nil {
				_ = p.sess.Kill(id) // fire-and-forget; startup orphan sweep is the safety net
			}
		}
	}
	return &gridwellv1.DeleteTileResponse{}, nil
}

// ── Subscribe (server-streaming) ─────────────────────────────────────────────

func (p *Plugin) Subscribe(_ *gridwellv1.SubscribeRequest, stream grpc.ServerStreamingServer[gridwellv1.Event]) error {
	ch, cancel := p.st.SubscribeEvents()
	defer cancel()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(rpc.EventToProto(ev)); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func tileResp(t *rpc.Tile, err error) (*gridwellv1.TileResponse, error) {
	if err != nil {
		return nil, errToStatus(err)
	}
	return &gridwellv1.TileResponse{Tile: rpc.TileToProto(t)}, nil
}

// errToStatus maps a store sentinel error to a gRPC status code so the
// classification survives the routing hop to the server (which maps the code
// to a Connect status). Mirrors the server's classifyStoreError; an unmatched
// error passes through (grpc wraps it as codes.Unknown → CodeInternal).
func errToStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, store.ErrInvalidArgument),
		errors.Is(err, store.ErrInvalidPath),
		errors.Is(err, store.ErrNotURLTile),
		errors.Is(err, store.ErrNotTextTile),
		errors.Is(err, store.ErrNotWellTile),
		errors.Is(err, store.ErrNotShellTile):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrOverlap),
		errors.Is(err, store.ErrVersionConflict):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return err
	}
}
