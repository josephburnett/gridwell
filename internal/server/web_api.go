package server

// The web API: gridwellv1connect.GridwellHandler over the one in-process
// router. It routes nothing and decides nothing, and the connection API is
// namespace.Server over the same router value, so there is no second
// implementation to drift from. It is deliberately not total: Info, Probe and
// OpenShell are connection API verbs, so unimplemented here means "not a
// browser verb", not "missing".

import (
	"context"
	"errors"
	"io"

	"connectrpc.com/connect"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/api/gwerr"
)

type connectHandler struct {
	gridwellv1connect.UnimplementedGridwellHandler
	rt *router
}

func newConnectHandler(rt *router) *connectHandler { return &connectHandler{rt: rt} }

// unary lifts one router verb into the Connect shapes, mapping the router's
// gRPC status code through gwerr's one table, so no verb grows its own error
// mapping.
func unary[Req, Resp any](call func(context.Context, *Req) (*Resp, error)) func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error) {
	return func(ctx context.Context, req *connect.Request[Req]) (*connect.Response[Resp], error) {
		resp, err := call(ctx, req.Msg)
		if err != nil {
			return nil, asConnectError(err)
		}
		return connect.NewResponse(resp), nil
	}
}

func (h *connectHandler) Handshake(ctx context.Context, req *connect.Request[pb.HandshakeRequest]) (*connect.Response[pb.HandshakeResponse], error) {
	return unary(h.rt.Handshake)(ctx, req)
}

func (h *connectHandler) GetGrid(ctx context.Context, req *connect.Request[pb.GetGridRequest]) (*connect.Response[pb.GetGridResponse], error) {
	return unary(h.rt.GetGrid)(ctx, req)
}

func (h *connectHandler) GetTile(ctx context.Context, req *connect.Request[pb.GetTileRequest]) (*connect.Response[pb.TileResponse], error) {
	return unary(h.rt.GetTile)(ctx, req)
}

func (h *connectHandler) GetTilePreview(ctx context.Context, req *connect.Request[pb.GetTilePreviewRequest]) (*connect.Response[pb.GetTilePreviewResponse], error) {
	return unary(h.rt.GetTilePreview)(ctx, req)
}

func (h *connectHandler) Find(ctx context.Context, req *connect.Request[pb.FindRequest]) (*connect.Response[pb.FindResponse], error) {
	return unary(h.rt.Find)(ctx, req)
}

func (h *connectHandler) CreateTile(ctx context.Context, req *connect.Request[pb.CreateTileRequest]) (*connect.Response[pb.TileResponse], error) {
	return unary(h.rt.CreateTile)(ctx, req)
}

func (h *connectHandler) SetTile(ctx context.Context, req *connect.Request[pb.SetTileRequest]) (*connect.Response[pb.TileResponse], error) {
	return unary(h.rt.SetTile)(ctx, req)
}

func (h *connectHandler) MoveTile(ctx context.Context, req *connect.Request[pb.MoveTileRequest]) (*connect.Response[pb.TileResponse], error) {
	return unary(h.rt.MoveTile)(ctx, req)
}

func (h *connectHandler) CopyTile(ctx context.Context, req *connect.Request[pb.CopyTileRequest]) (*connect.Response[pb.TileResponse], error) {
	return unary(h.rt.CopyTile)(ctx, req)
}

func (h *connectHandler) DeleteTile(ctx context.Context, req *connect.Request[pb.DeleteTileRequest]) (*connect.Response[pb.DeleteTileResponse], error) {
	return unary(h.rt.DeleteTile)(ctx, req)
}

func (h *connectHandler) SetFraming(ctx context.Context, req *connect.Request[pb.SetFramingRequest]) (*connect.Response[pb.SetFramingResponse], error) {
	return unary(h.rt.SetFraming)(ctx, req)
}

func (h *connectHandler) SetSelection(ctx context.Context, req *connect.Request[pb.SetSelectionRequest]) (*connect.Response[pb.SetSelectionResponse], error) {
	return unary(h.rt.SetSelection)(ctx, req)
}

func (h *connectHandler) ShellTmuxAlive(ctx context.Context, req *connect.Request[pb.ShellTmuxAliveRequest]) (*connect.Response[pb.ShellTmuxAliveResponse], error) {
	return unary(h.rt.ShellTmuxAlive)(ctx, req)
}

func (h *connectHandler) ReadContent(ctx context.Context, req *connect.Request[pb.ReadContentRequest], stream *connect.ServerStream[pb.ContentChunk]) error {
	return asConnectError(h.rt.ReadContent(ctx, req.Msg, stream.Send))
}

func (h *connectHandler) ServeContent(ctx context.Context, req *connect.Request[pb.ServeContentRequest], stream *connect.ServerStream[pb.ServeContentChunk]) error {
	return asConnectError(h.rt.ServeContent(ctx, req.Msg, stream.Send))
}

func (h *connectHandler) WriteContent(ctx context.Context, stream *connect.ClientStream[pb.WriteContentRequest]) (*connect.Response[pb.TileResponse], error) {
	resp, err := h.rt.WriteContent(ctx, func() (*pb.WriteContentRequest, error) {
		if stream.Receive() {
			return stream.Msg(), nil
		}
		if err := stream.Err(); err != nil {
			return nil, err // a broken inbound stream: the owner never commits
		}
		return nil, io.EOF
	})
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(resp), nil
}

func (h *connectHandler) DisableSource(ctx context.Context, req *connect.Request[pb.DisableSourceRequest]) (*connect.Response[pb.DisableSourceResponse], error) {
	return unary(h.rt.DisableSource)(ctx, req)
}

func (h *connectHandler) SetView(ctx context.Context, req *connect.Request[pb.SetViewRequest]) (*connect.Response[pb.SetViewResponse], error) {
	return unary(h.rt.SetView)(ctx, req)
}

func (h *connectHandler) Subscribe(ctx context.Context, req *connect.Request[pb.SubscribeRequest], stream *connect.ServerStream[pb.Event]) error {
	return asConnectError(h.rt.Subscribe(ctx, req.Msg, stream.Send))
}

// asConnectError maps a namespace error, or a raw store sentinel, to a Connect
// status code through gwerr's two tables, gRPC code to Connect code and class
// to gRPC code, so a transport failure two hops away still reads as
// transport and a sentinel reads as home would answer it.
func asConnectError(err error) error {
	if err == nil {
		return nil
	}
	if st, ok := status.FromError(err); ok {
		return gwerr.ConnectDetails(err, connect.NewError(gwerr.ConnectCode(st.Code()), errors.New(st.Message())))
	}
	return connect.NewError(gwerr.ConnectCode(gwerr.StatusCode(gwerr.ClassifyError(err))), err)
}
