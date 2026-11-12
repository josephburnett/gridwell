package server

import (
	"cmp"
	"context"
	"errors"
	"io"
	"sync"

	"connectrpc.com/connect"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// The router's content streams and the placement verb. Reads route through
// contentRoute, a page through servePage; writes take plain id routing.

// ReadContent streams a tile's bytes from the namespace that owns it, or a
// list's, each id read alone (readOne). A list's chunks carry the id as the
// caller spelled it, so nothing needs re-qualification on the way back.
func (rt *router) ReadContent(ctx context.Context, req *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	if (req.TileId == "") == (len(req.TileIds) == 0) {
		return status.Error(gcodes.InvalidArgument, "a read names tile_id or tile_ids, one of them")
	}
	if req.TileId != "" {
		return rt.readOne(ctx, req.TileId, req.Info, send)
	}
	return rt.readList(ctx, req.TileIds, req.Info, send)
}

func (rt *router) readOne(ctx context.Context, id string, info bool, send func(*pb.ContentChunk) error) error {
	c, local, _, err := rt.srv.contentRoute(ctx, id)
	if err != nil {
		return err
	}
	return c.ReadContent(ctx, &pb.ReadContentRequest{TileId: local, Info: info}, send)
}

// readFanOut is how many ids of a list are read at once. The namespaces
// answer one id each, and a far id is a forward over the connection's one
// multiplexed transport.
const readFanOut = 8

// readList reads each id through readOne, readFanOut at a time, and sends
// each id's chunks whole under one lock, so they stay contiguous.
func (rt *router) readList(ctx context.Context, ids []string, info bool, send func(*pb.ContentChunk) error) error {
	var (
		mu      sync.Mutex
		sendErr error
		wg      sync.WaitGroup
	)
	slots := make(chan struct{}, readFanOut)
	for _, id := range ids {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			chunks := rt.readChunks(ctx, id, info)
			mu.Lock()
			defer mu.Unlock()
			for _, c := range chunks {
				if sendErr == nil {
					c.TileId = id
					sendErr = send(c)
				}
			}
		}()
	}
	wg.Wait()
	return cmp.Or(sendErr, ctx.Err())
}

// readChunks is one id's answer as a list sends it: its chunks, copied
// because the list tags them, or its verdict alone.
func (rt *router) readChunks(ctx context.Context, id string, info bool) []*pb.ContentChunk {
	var chunks []*pb.ContentChunk
	err := rt.readOne(ctx, id, info, func(c *pb.ContentChunk) error {
		chunks = append(chunks, proto.CloneOf(c))
		return nil
	})
	if err != nil {
		return []*pb.ContentChunk{{Verdict: verdictOf(err)}}
	}
	if len(chunks) == 0 {
		return []*pb.ContentChunk{{}}
	}
	return chunks
}

// verdictOf is the verdict a failed id of a list carries: the status
// asConnectError gives the browser for a single read's err, so the client
// rebuilds the error it would have had.
func verdictOf(err error) *pb.ReadVerdict {
	var ce *connect.Error
	if !errors.As(asConnectError(err), &ce) {
		return &pb.ReadVerdict{Code: int32(connect.CodeUnknown), Message: err.Error()}
	}
	v := &pb.ReadVerdict{Code: int32(ce.Code()), Message: ce.Message()}
	for _, d := range ce.Details() {
		v.Details = append(v.Details, &anypb.Any{TypeUrl: "type.googleapis.com/" + d.Type(), Value: d.Bytes()})
	}
	return v
}

// ServeContent forwards a web-content request one hop. HTTP terminates at the
// local API and the request rides this verb through the tunnel, which is how
// a far node's pages are served.
func (rt *router) ServeContent(ctx context.Context, req *pb.ServeContentRequest, send func(*pb.ServeContentChunk) error) error {
	return rt.srv.servePage(ctx, req, send)
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
	if err := rt.refuseDisabled(first.TileId); err != nil {
		return nil, err
	}
	bound := proto.CloneOf(first)
	bound.TileId = local
	resp, err := c.WriteContent(ctx, namespace.Rebound(bound, recv))
	return rt.tileResp(ctx, uuid, transit, resp, err)
}

// MoveTile is the single placement writeback, refused before either side is
// asked by rpc.MoveRefusal; a Gridwell tile onto a directory is home's move,
// over a cell the directory's grid shows free (placeRoute).
func (rt *router) MoveTile(ctx context.Context, req *pb.MoveTileRequest) (*pb.TileResponse, error) {
	if err := refuseUnclaimed(req); err != nil {
		return nil, err
	}
	if why := rpc.MoveRefusal(req); why != "" {
		return nil, status.Error(gcodes.InvalidArgument, why)
	}
	c, local, uuid, transit, err := rt.route(req.TileId)
	if err != nil {
		return nil, err
	}
	if !transit && !rpc.OnDisk(req.TileId) && req.GridId != "" {
		_, _, _, taken, err := rt.placeRoute(ctx, req.GridId, false)
		if err != nil {
			return nil, err
		}
		if err := cellFree(taken, local, req.X, req.Y, req.W, req.H); err != nil {
			return nil, err
		}
	}
	if err := rt.refuseDisabled(req.TileId, req.GridId); err != nil {
		return nil, err
	}
	resp, err := c.MoveTile(ctx, rpc.PeelRequest(rt.hop(req.TileId, transit), req))
	return rt.tileResp(ctx, uuid, transit, resp, err)
}
