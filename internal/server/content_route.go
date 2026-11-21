package server

import (
	"context"

	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// servePage answers one page request from the namespace that owns its
// tile's content (contentRoute), asked for the bytes of the tile's page, or a
// range of them, and nothing else.
func (s *Server) servePage(ctx context.Context, req *pb.ServeContentRequest, send func(*pb.ServeContentChunk) error) error {
	c, local, _, err := s.contentRoute(ctx, req.TileId)
	if err != nil {
		return err
	}
	bound := proto.CloneOf(req)
	bound.TileId = local
	return c.ServeContent(ctx, bound, send)
}

// contentRoute resolves the namespace and local id that serve a tile's content.
// It is the one link-resolution point, so every caller inherits the resolution
// instead of reimplementing it. Two facts keep it a single step: resolution
// happens only where the link row lives, so a transit-owned id forwards as-is;
// and a link never targets another link, so there is no chain to walk. Writes
// never resolve, because a link owns no content and the store refuses a write
// to a link row. The row is the one that owns the content, nil when a hop
// forwards it.
func (s *Server) contentRoute(ctx context.Context, qualifiedID string) (namespace.Namespace, string, *pb.Tile, error) {
	c, local, uuid, transit, err := s.route(qualifiedID)
	if err != nil {
		return nil, "", nil, err
	}
	if transit {
		return c, local, nil, nil
	}
	tr, err := c.GetTile(ctx, &pb.GetTileRequest{TileId: local})
	if err != nil {
		return nil, "", nil, err
	}
	target := rpc.QualifyLinkTarget(uuid, tr.GetTile().GetLinkTargetId())
	if target == "" {
		return c, local, tr.GetTile(), nil
	}
	// The target is spelled from this node, so it routes like any other id.
	// Peeling the first segment by hand would answer home for a
	// connection-chained target, which the transport owns.
	tc, tlocal, _, ttransit, err := s.route(target)
	if err != nil {
		return nil, "", nil, err
	}
	// The target is checked as a directly addressed id is, so its owner's
	// verdict on a gone key is the answer whichever id the read named. A
	// transit target is the far router's to check.
	if ttransit {
		return tc, tlocal, nil, nil
	}
	ttr, err := tc.GetTile(ctx, &pb.GetTileRequest{TileId: tlocal})
	if err != nil {
		return nil, "", nil, err
	}
	return tc, tlocal, ttr.GetTile(), nil
}
