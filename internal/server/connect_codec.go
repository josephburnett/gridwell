package server

// The browser codec: gridwellv1connect.GridwellHandler over the one
// in-process router. It routes nothing and decides nothing — it unwraps a
// connect.Request, calls the router, and wraps the answer. The federation
// door's codec is namespace.Server over the same router value, so the two
// surfaces cannot drift: there is no second implementation to drift from.
//
// The codec is deliberately not total. Info, Probe, and OpenShell are
// federation verbs: the browser learns the node's identity from Handshake on
// its own door, and its shell bytes ride the /shell WebSocket. Unimplemented
// here means "not a browser verb", not "missing".

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

// unary lifts one router verb into the Connect shapes: unwrap, call, wrap, and
// map the router's gRPC status code through gwerr's one table. It is written
// once so no verb can grow its own error mapping.
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

func (h *connectHandler) Search(ctx context.Context, req *connect.Request[pb.SearchRequest]) (*connect.Response[pb.SearchResponse], error) {
	return unary(h.rt.Search)(ctx, req)
}

func (h *connectHandler) CreateTile(ctx context.Context, req *connect.Request[pb.CreateTileRequest]) (*connect.Response[pb.TileResponse], error) {
	return unary(h.rt.CreateTile)(ctx, req)
}

func (h *connectHandler) SetTile(ctx context.Context, req *connect.Request[pb.SetTileRequest]) (*connect.Response[pb.TileResponse], error) {
	return unary(h.rt.SetTile)(ctx, req)
}

func (h *connectHandler) PlaceTile(ctx context.Context, req *connect.Request[pb.PlaceTileRequest]) (*connect.Response[pb.TileResponse], error) {
	return unary(h.rt.PlaceTile)(ctx, req)
}

func (h *connectHandler) CloneTile(ctx context.Context, req *connect.Request[pb.CloneTileRequest]) (*connect.Response[pb.TileResponse], error) {
	return unary(h.rt.CloneTile)(ctx, req)
}

func (h *connectHandler) DeleteTile(ctx context.Context, req *connect.Request[pb.DeleteTileRequest]) (*connect.Response[pb.DeleteTileResponse], error) {
	return unary(h.rt.DeleteTile)(ctx, req)
}

func (h *connectHandler) SetFraming(ctx context.Context, req *connect.Request[pb.SetFramingRequest]) (*connect.Response[pb.SetFramingResponse], error) {
	return unary(h.rt.SetFraming)(ctx, req)
}

func (h *connectHandler) ShellSessionAlive(ctx context.Context, req *connect.Request[pb.ShellSessionAliveRequest]) (*connect.Response[pb.ShellSessionAliveResponse], error) {
	return unary(h.rt.ShellSessionAlive)(ctx, req)
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

func (h *connectHandler) Subscribe(ctx context.Context, _ *connect.Request[pb.SubscribeRequest], stream *connect.ServerStream[pb.Event]) error {
	return asConnectError(h.rt.Subscribe(ctx, &pb.SubscribeRequest{}, stream.Send))
}

// asConnectError maps an error returned from a namespace, or a raw store
// sentinel, to a Connect status code. Namespace errors arrive as gRPC status
// errors, because a namespace translates store sentinels into codes, so
// NotFound, InvalidArgument, overlap, and version conflicts survive the
// routing hop. The code crosses through gwerr's one gRPC-to-Connect table so
// every code survives, and a transport failure two mounts away still reads as
// transport. A non-gRPC error falls through to the same gwerr.ClassifyError
// categorization the raw-HTTP endpoints use.
func asConnectError(err error) error {
	if err == nil {
		return nil
	}
	if st, ok := status.FromError(err); ok {
		return connect.NewError(gwerr.ConnectCode(st.Code()), errors.New(st.Message()))
	}
	switch gwerr.ClassifyError(err) {
	case gwerr.ClassNotFound:
		return connect.NewError(connect.CodeNotFound, err)
	case gwerr.ClassInvalidArgument:
		return connect.NewError(connect.CodeInvalidArgument, err)
	case gwerr.ClassConflict:
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
