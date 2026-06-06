package server

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/store"
)

// connectHandler implements gridwellv1connect.GridwellHandler. It is the
// thin proto↔rpc bridge: every method converts the wire request into a
// Go rpc.* value, calls the store, and packs the result into a wire
// response. The Go rpc types remain the internal source of truth.
type connectHandler struct {
	gridwellv1connect.UnimplementedGridwellHandler
	srv *Server
}

func newConnectHandler(srv *Server) *connectHandler {
	return &connectHandler{srv: srv}
}

// tileResp packs a store-produced tile into the wire-shaped response.
// Used by every Create / Move / Clone / Resize / Set / Update method.
func tileResp(t *rpc.Tile, err error) (*connect.Response[pb.TileResponse], error) {
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(&pb.TileResponse{Tile: rpc.TileToProto(t)}), nil
}

func (h *connectHandler) Bootstrap(ctx context.Context, _ *connect.Request[pb.BootstrapRequest]) (*connect.Response[pb.BootstrapResponse], error) {
	id, err := h.srv.store.RootGridID(ctx)
	if err != nil {
		return nil, asConnectError(err)
	}
	cx, cy, zoom, err := h.srv.store.RootView(ctx)
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(&pb.BootstrapResponse{
		RootGridId: id,
		RootViewCx: cx,
		RootViewCy: cy,
		RootZoom:   zoom,
	}), nil
}

func (h *connectHandler) GetGrid(ctx context.Context, req *connect.Request[pb.GetGridRequest]) (*connect.Response[pb.GetGridResponse], error) {
	r, err := h.srv.store.GetGrid(ctx, req.Msg.GridId)
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(&pb.GetGridResponse{
		Grid:  rpc.GridToProto(&r.Grid),
		Tiles: rpc.TilesToProto(r.Tiles),
	}), nil
}

func (h *connectHandler) GetBlob(ctx context.Context, req *connect.Request[pb.GetBlobRequest]) (*connect.Response[pb.GetBlobResponse], error) {
	data, err := h.srv.store.GetBlob(ctx, req.Msg.BlobId)
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(&pb.GetBlobResponse{Data: data}), nil
}

func (h *connectHandler) GetTilePreview(ctx context.Context, req *connect.Request[pb.GetTilePreviewRequest]) (*connect.Response[pb.GetTilePreviewResponse], error) {
	jpeg, err := h.srv.store.GetTilePreview(ctx, req.Msg.TileId)
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(&pb.GetTilePreviewResponse{Jpeg: jpeg}), nil
}

func (h *connectHandler) CreateWell(ctx context.Context, req *connect.Request[pb.CreateWellRequest]) (*connect.Response[pb.TileResponse], error) {
	return tileResp(h.srv.store.CreateWell(ctx, rpc.CreateWellFromProto(req.Msg)))
}
func (h *connectHandler) CreateText(ctx context.Context, req *connect.Request[pb.CreateTextRequest]) (*connect.Response[pb.TileResponse], error) {
	return tileResp(h.srv.store.CreateText(ctx, rpc.CreateTextFromProto(req.Msg)))
}
func (h *connectHandler) CreateURL(ctx context.Context, req *connect.Request[pb.CreateURLRequest]) (*connect.Response[pb.TileResponse], error) {
	return tileResp(h.srv.store.CreateURL(ctx, rpc.CreateURLFromProto(req.Msg)))
}
func (h *connectHandler) CreateBlackHole(ctx context.Context, req *connect.Request[pb.CreateBlackHoleRequest]) (*connect.Response[pb.TileResponse], error) {
	return tileResp(h.srv.store.CreateBlackHole(ctx, rpc.CreateBlackHoleFromProto(req.Msg)))
}
func (h *connectHandler) CreateFileWell(ctx context.Context, req *connect.Request[pb.CreateFileWellRequest]) (*connect.Response[pb.TileResponse], error) {
	return tileResp(h.srv.store.CreateFileWell(ctx, rpc.CreateFileWellFromProto(req.Msg)))
}
func (h *connectHandler) CreateProcessWell(ctx context.Context, req *connect.Request[pb.CreateProcessWellRequest]) (*connect.Response[pb.TileResponse], error) {
	return tileResp(h.srv.store.CreateProcessWell(ctx, rpc.CreateProcessWellFromProto(req.Msg)))
}

func (h *connectHandler) MoveTile(ctx context.Context, req *connect.Request[pb.MoveTileRequest]) (*connect.Response[pb.TileResponse], error) {
	return tileResp(h.srv.store.MoveTile(ctx, rpc.MoveTileFromProto(req.Msg)))
}
func (h *connectHandler) CloneTile(ctx context.Context, req *connect.Request[pb.CloneTileRequest]) (*connect.Response[pb.TileResponse], error) {
	return tileResp(h.srv.store.CloneTile(ctx, rpc.CloneTileFromProto(req.Msg)))
}
func (h *connectHandler) ResizeTile(ctx context.Context, req *connect.Request[pb.ResizeTileRequest]) (*connect.Response[pb.TileResponse], error) {
	return tileResp(h.srv.store.ResizeTile(ctx, rpc.ResizeTileFromProto(req.Msg)))
}
func (h *connectHandler) SetWellView(ctx context.Context, req *connect.Request[pb.SetWellViewRequest]) (*connect.Response[pb.TileResponse], error) {
	return tileResp(h.srv.store.SetWellView(ctx, rpc.SetWellViewFromProto(req.Msg)))
}
func (h *connectHandler) SetTextView(ctx context.Context, req *connect.Request[pb.SetTextViewRequest]) (*connect.Response[pb.TileResponse], error) {
	return tileResp(h.srv.store.SetTextView(ctx, rpc.SetTextViewFromProto(req.Msg)))
}
func (h *connectHandler) SetRootView(ctx context.Context, req *connect.Request[pb.SetRootViewRequest]) (*connect.Response[pb.SetRootViewResponse], error) {
	if err := h.srv.store.SetRootView(ctx, rpc.SetRootViewFromProto(req.Msg)); err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(&pb.SetRootViewResponse{}), nil
}
func (h *connectHandler) UpdateText(ctx context.Context, req *connect.Request[pb.UpdateTextRequest]) (*connect.Response[pb.TileResponse], error) {
	return tileResp(h.srv.store.UpdateText(ctx, rpc.UpdateTextFromProto(req.Msg)))
}
func (h *connectHandler) DeleteTile(ctx context.Context, req *connect.Request[pb.DeleteTileRequest]) (*connect.Response[pb.DeleteTileResponse], error) {
	if err := h.srv.store.DeleteTile(ctx, rpc.DeleteTileFromProto(req.Msg)); err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(&pb.DeleteTileResponse{}), nil
}

// Subscribe is a server-streaming RPC. Each event from the store is
// converted and pushed onto the wire until the client disconnects or
// the store closes its subscriber channel.
func (h *connectHandler) Subscribe(ctx context.Context, _ *connect.Request[pb.SubscribeRequest], stream *connect.ServerStream[pb.Event]) error {
	ch, cancel := h.srv.store.SubscribeEvents()
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
		case <-ctx.Done():
			return nil
		}
	}
}

// asConnectError maps store sentinel errors to Connect status codes so
// the wire surface matches the prior HTTP status mapping.
func asConnectError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, store.ErrInvalidArgument),
		errors.Is(err, store.ErrInvalidPath),
		errors.Is(err, store.ErrNotURLTile),
		errors.Is(err, store.ErrNotTextTile),
		errors.Is(err, store.ErrNotWellTile):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, store.ErrOverlap),
		errors.Is(err, store.ErrVersionConflict):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
