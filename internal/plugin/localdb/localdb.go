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

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/store"
	"google.golang.org/grpc"
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

func (p *Plugin) Info(_ context.Context, _ *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	return &gridwellv1.InfoResponse{
		Kind:          "localdb",
		DisplayName:   "local",
		SchemaVersion: 1,
	}, nil
}

func (p *Plugin) Attach(ctx context.Context, _ *gridwellv1.AttachRequest) (*gridwellv1.AttachResponse, error) {
	id, err := p.st.RootGridID(ctx)
	if err != nil {
		return nil, err
	}
	return &gridwellv1.AttachResponse{
		RootGridId: id,
		Label:      "home",
		Caps: &gridwellv1.PluginCaps{
			Delete: true,
			Clone:  true,
			Move:   true,
			Write:  true,
			Live:   true,
			Accept: true,
		},
		HasSession: true,
	}, nil
}

func (p *Plugin) Detach(_ context.Context, _ *gridwellv1.DetachRequest) (*gridwellv1.DetachResponse, error) {
	return &gridwellv1.DetachResponse{}, nil
}

func (p *Plugin) Probe(ctx context.Context, req *gridwellv1.ProbeRequest) (*gridwellv1.ProbeResponse, error) {
	_, err := p.st.GetTile(ctx, req.TileId)
	if errors.Is(err, store.ErrNotFound) {
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	if err != nil {
		return nil, err
	}
	return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_PRESENT}, nil
}

// ── Bootstrap (backward compat) ──────────────────────────────────────────────

func (p *Plugin) Bootstrap(ctx context.Context, _ *gridwellv1.BootstrapRequest) (*gridwellv1.BootstrapResponse, error) {
	id, err := p.st.RootGridID(ctx)
	if err != nil {
		return nil, err
	}
	cx, cy, zoom, err := p.st.RootView(ctx)
	if err != nil {
		return nil, err
	}
	return &gridwellv1.BootstrapResponse{
		RootGridId: id,
		RootViewCx: cx,
		RootViewCy: cy,
		RootZoom:   zoom,
	}, nil
}

// ── Reads ────────────────────────────────────────────────────────────────────

func (p *Plugin) GetGrid(ctx context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	r, err := p.st.GetGrid(ctx, req.GridId)
	if err != nil {
		return nil, err
	}
	return &gridwellv1.GetGridResponse{
		Grid:  rpc.GridToProto(&r.Grid),
		Tiles: rpc.TilesToProto(r.Tiles),
	}, nil
}

func (p *Plugin) GetBlob(ctx context.Context, req *gridwellv1.GetBlobRequest) (*gridwellv1.GetBlobResponse, error) {
	data, err := p.st.GetBlob(ctx, req.BlobId)
	if err != nil {
		return nil, err
	}
	return &gridwellv1.GetBlobResponse{Data: data}, nil
}

func (p *Plugin) GetTilePreview(ctx context.Context, req *gridwellv1.GetTilePreviewRequest) (*gridwellv1.GetTilePreviewResponse, error) {
	jpeg, err := p.st.GetTilePreview(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	return &gridwellv1.GetTilePreviewResponse{Jpeg: jpeg}, nil
}

// ── Creates ──────────────────────────────────────────────────────────────────

func (p *Plugin) CreateWell(ctx context.Context, req *gridwellv1.CreateWellRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.CreateWell(ctx, rpc.CreateWellFromProto(req)))
}

func (p *Plugin) CreateText(ctx context.Context, req *gridwellv1.CreateTextRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.CreateText(ctx, rpc.CreateTextFromProto(req)))
}

func (p *Plugin) CreateURL(ctx context.Context, req *gridwellv1.CreateURLRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.CreateURL(ctx, rpc.CreateURLFromProto(req)))
}

func (p *Plugin) CreateFileWell(ctx context.Context, req *gridwellv1.CreateFileWellRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.CreateFileWell(ctx, rpc.CreateFileWellFromProto(req)))
}

func (p *Plugin) CreateProcessWell(ctx context.Context, req *gridwellv1.CreateProcessWellRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.CreateProcessWell(ctx, rpc.CreateProcessWellFromProto(req)))
}

func (p *Plugin) CreateShell(ctx context.Context, req *gridwellv1.CreateShellRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.CreateShell(ctx, rpc.CreateShellFromProto(req)))
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

func (p *Plugin) SetWellView(ctx context.Context, req *gridwellv1.SetWellViewRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.SetWellView(ctx, rpc.SetWellViewFromProto(req)))
}

func (p *Plugin) SetTextView(ctx context.Context, req *gridwellv1.SetTextViewRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.SetTextView(ctx, rpc.SetTextViewFromProto(req)))
}

func (p *Plugin) SetShellPreview(ctx context.Context, req *gridwellv1.SetShellPreviewRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.SetShellPreview(ctx, rpc.SetShellPreviewFromProto(req)))
}

func (p *Plugin) ShellSessionAlive(_ context.Context, req *gridwellv1.ShellSessionAliveRequest) (*gridwellv1.ShellSessionAliveResponse, error) {
	if p.sess == nil {
		return &gridwellv1.ShellSessionAliveResponse{Alive: false}, nil
	}
	alive, err := p.sess.HasSession(req.TileId)
	if err != nil {
		alive = false
	}
	return &gridwellv1.ShellSessionAliveResponse{Alive: alive}, nil
}

func (p *Plugin) SetRootView(ctx context.Context, req *gridwellv1.SetRootViewRequest) (*gridwellv1.SetRootViewResponse, error) {
	if err := p.st.SetRootView(ctx, rpc.SetRootViewFromProto(req)); err != nil {
		return nil, err
	}
	return &gridwellv1.SetRootViewResponse{}, nil
}

func (p *Plugin) SetURLState(ctx context.Context, req *gridwellv1.SetURLStateRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.SetURLState(ctx, rpc.SetURLStateFromProto(req)))
}

func (p *Plugin) UpdateText(ctx context.Context, req *gridwellv1.UpdateTextRequest) (*gridwellv1.TileResponse, error) {
	return tileResp(p.st.UpdateText(ctx, rpc.UpdateTextFromProto(req)))
}

func (p *Plugin) DeleteTile(ctx context.Context, req *gridwellv1.DeleteTileRequest) (*gridwellv1.DeleteTileResponse, error) {
	tileID := req.TileId
	if err := p.st.DeleteTile(ctx, rpc.DeleteTileFromProto(req)); err != nil {
		return nil, err
	}
	// Clean up any orphaned shell session for the deleted tile.
	if p.sess != nil {
		exists, err := p.st.ShellTileExists(ctx, tileID)
		if err == nil && !exists {
			_ = p.sess.Kill(tileID) // fire-and-forget; startup orphan sweep is the safety net
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
		return nil, err
	}
	return &gridwellv1.TileResponse{Tile: rpc.TileToProto(t)}, nil
}
