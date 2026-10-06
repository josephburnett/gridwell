package server

import (
	"context"
	"errors"
	"io"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The router's content streams and the placement verb. Reads route through
// contentRoute; writes take plain id routing.

// ReadContent streams a tile's bytes from the namespace that owns it. Chunks
// carry no ids, so nothing needs re-qualification on the way back.
func (rt *router) ReadContent(ctx context.Context, req *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	c, local, err := rt.srv.contentRoute(ctx, req.TileId)
	if err != nil {
		return err
	}
	return c.ReadContent(ctx, &pb.ReadContentRequest{TileId: local}, send)
}

// ServeContent forwards a web-content request one hop. HTTP terminates at the
// local door and the request rides this verb through the tunnel, which is how
// a mounted node's pages are served.
func (rt *router) ServeContent(ctx context.Context, req *pb.ServeContentRequest, send func(*pb.ServeContentChunk) error) error {
	c, local, err := rt.srv.contentRoute(ctx, req.TileId)
	if err != nil {
		return err
	}
	return c.ServeContent(ctx, &pb.ServeContentRequest{TileId: local, Subpath: req.Subpath}, send)
}

// WriteContent preserves commit-at-close: the owner commits only after a clean
// end-of-stream, and a broken caller stream errors before any commit, so
// nothing is written torn.
func (rt *router) WriteContent(ctx context.Context, recv func() (*pb.WriteContentRequest, error)) (*pb.TileResponse, error) {
	first, err := recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, status.Error(gcodes.InvalidArgument, "write: empty stream")
		}
		return nil, status.Error(gcodes.InvalidArgument, err.Error())
	}
	c, local, uuid, transit, err := rt.route(first.TileId)
	if err != nil {
		return nil, err
	}
	bound := proto.CloneOf(first)
	bound.TileId = local
	sentBind := false
	resp, err := c.WriteContent(ctx, func() (*pb.WriteContentRequest, error) {
		if !sentBind {
			sentBind = true
			return bound, nil
		}
		return recv()
	})
	return rt.tileResp(uuid, transit, resp, err)
}

// PlaceTile is the single placement writeback.
func (rt *router) PlaceTile(ctx context.Context, req *pb.PlaceTileRequest) (*pb.TileResponse, error) {
	c, _, uuid, transit, err := rt.route(req.TileId)
	if err != nil {
		return nil, err
	}
	resp, err := c.PlaceTile(ctx, rpc.PeelRequest(rt.hop(req.TileId, transit), req))
	return rt.tileResp(uuid, transit, resp, err)
}
