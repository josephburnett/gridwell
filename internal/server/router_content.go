package server

import (
	"context"
	"errors"
	"io"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The router's content streams and the placement verb (2026-07-26
// redesign). Routing follows contentRoute for reads (link resolution at the
// serving node) and plain id routing for writes (a link owns no content;
// the store refuses a write to a link row).

// ReadContent streams a tile's bytes from the namespace that owns it.
// Chunks carry no ids, so nothing needs re-qualification on the way back.
func (rt *router) ReadContent(ctx context.Context, req *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	c, local, err := rt.srv.contentRoute(ctx, req.TileId)
	if err != nil {
		return err
	}
	return c.ReadContent(ctx, &pb.ReadContentRequest{TileId: local}, send)
}

// ServeContent forwards a web-content request one hop, resolving links via
// contentRoute like ReadContent — this is how a mounted remote node's pages
// reach the local /content/ door: HTTP terminates at the LOCAL door and the
// request rides this verb through the tunnel.
func (rt *router) ServeContent(ctx context.Context, req *pb.ServeContentRequest, send func(*pb.ServeContentChunk) error) error {
	c, local, err := rt.srv.contentRoute(ctx, req.TileId)
	if err != nil {
		return err
	}
	return c.ServeContent(ctx, &pb.ServeContentRequest{TileId: local, Subpath: req.Subpath}, send)
}

// WriteContent relays the caller's messages to the owning namespace,
// preserving commit-at-close: the owner commits only after a clean
// end-of-stream, and a broken caller stream propagates as an error before
// any commit, so nothing is ever written torn. The TileResponse carries
// ids, so it is re-qualified like every tile-returning verb.
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
	bound := &pb.WriteContentRequest{TileId: local, Version: first.Version, Data: first.Data}
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

// PlaceTile is the single placement writeback (⇐ MoveTile + ResizeTile).
func (rt *router) PlaceTile(ctx context.Context, req *pb.PlaceTileRequest) (*pb.TileResponse, error) {
	m := req
	c, local, uuid, transit, err := rt.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.GridId = stripUUID(m.GridId, uuid)
	resp, err := c.PlaceTile(ctx, m)
	return rt.tileResp(uuid, transit, resp, err)
}
