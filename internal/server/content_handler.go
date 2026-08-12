package server

import (
	"context"
	"errors"
	"io"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The Connect front door for the content streams and the placement verb
// (2026-07-26 redesign). Routing follows contentRoute for reads (link
// resolution at the serving node) and plain id routing for writes (a link
// owns no content; the store refuses a write to a link row).

func (h *connectHandler) ReadContent(ctx context.Context, req *connect.Request[pb.ReadContentRequest], stream *connect.ServerStream[pb.ContentChunk]) error {
	c, local, err := h.srv.contentRoute(ctx, req.Msg.TileId)
	if err != nil {
		return asConnectError(err)
	}
	up, err := c.ReadContent(ctx, &pb.ReadContentRequest{TileId: local})
	if err != nil {
		return asConnectError(err)
	}
	for {
		chunk, err := up.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return asConnectError(err)
		}
		if err := stream.Send(chunk); err != nil {
			return err
		}
	}
}

// ServeContent on the Connect surface mirrors the HTTP /content/ door's RPC
// hop (the door itself calls the plugin client directly; this front keeps
// the Connect data plane total, so a CLI or test can drive the same verb).
func (h *connectHandler) ServeContent(ctx context.Context, req *connect.Request[pb.ServeContentRequest], stream *connect.ServerStream[pb.ServeContentChunk]) error {
	c, local, err := h.srv.contentRoute(ctx, req.Msg.TileId)
	if err != nil {
		return asConnectError(err)
	}
	up, err := c.ServeContent(ctx, &pb.ServeContentRequest{TileId: local, Subpath: req.Msg.Subpath})
	if err != nil {
		return asConnectError(err)
	}
	for {
		chunk, err := up.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return asConnectError(err)
		}
		if err := stream.Send(chunk); err != nil {
			return err
		}
	}
}

// WriteContent relays the client stream to the owning plugin, preserving
// commit-at-close: the upstream CloseAndRecv happens only after the inbound
// stream ended cleanly, and a broken inbound returns without closing
// upstream, so the plugin never commits a torn write.
func (h *connectHandler) WriteContent(ctx context.Context, stream *connect.ClientStream[pb.WriteContentRequest]) (*connect.Response[pb.TileResponse], error) {
	if !stream.Receive() {
		err := stream.Err()
		if err == nil {
			err = errors.New("write: empty stream")
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	first := stream.Msg()
	c, local, uuid, err := h.route(first.TileId)
	if err != nil {
		return nil, err
	}
	up, err := c.WriteContent(ctx)
	if err != nil {
		return nil, asConnectError(err)
	}
	if err := up.Send(&pb.WriteContentRequest{TileId: local, Version: first.Version, Data: first.Data}); err != nil {
		return nil, asConnectError(err)
	}
	for stream.Receive() {
		if err := up.Send(stream.Msg()); err != nil {
			return nil, asConnectError(err)
		}
	}
	if err := stream.Err(); err != nil {
		return nil, asConnectError(err) // broken inbound: upstream is never closed cleanly, no commit
	}
	resp, err := up.CloseAndRecv()
	return h.tileResp(uuid, resp, err)
}

// PlaceTile is the single placement writeback (⇐ MoveTile + ResizeTile).
func (h *connectHandler) PlaceTile(ctx context.Context, req *connect.Request[pb.PlaceTileRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.GridId = stripUUID(m.GridId, uuid)
	resp, err := c.PlaceTile(ctx, m)
	return h.tileResp(uuid, resp, err)
}
