package server

import (
	"context"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// contentRoute resolves the namespace and local id that serve a tile's content.
// It is the one link-resolution point, so every caller inherits the resolution
// instead of reimplementing it. Two facts keep it a single step: resolution
// happens only where the link row lives, so a transit-owned id forwards as-is;
// and a link never targets another link, so there is no chain to walk. Writes
// never resolve, because a link owns no content and the store refuses a write
// to a link row.
func (s *Server) contentRoute(ctx context.Context, qualifiedID string) (namespace.Namespace, string, error) {
	if _, _, ok := rpc.SplitID(qualifiedID); !ok {
		return nil, "", status.Errorf(gcodes.InvalidArgument, "unqualified id %q", qualifiedID)
	}
	c, local, _, transit, found := s.resolve(qualifiedID)
	if !found {
		return nil, "", undeclared(qualifiedID)
	}
	if transit {
		return c, local, nil
	}
	tr, err := c.GetTile(ctx, &pb.GetTileRequest{TileId: local})
	if err != nil {
		return nil, "", err
	}
	target := tr.GetTile().GetLinkTargetId()
	if target == "" {
		return c, local, nil
	}
	if _, _, ok := rpc.SplitID(target); !ok {
		return nil, "", status.Errorf(gcodes.Internal, "link target %q is not qualified", target)
	}
	// A leaf plugin stores its target already qualified from this node's
	// perspective, so it routes like any other id, through resolve. Peeling
	// the first segment by hand would answer home for a connection-chained
	// target, which the transport owns.
	tc, tlocal, _, _, found := s.resolve(target)
	if !found {
		return nil, "", undeclared(target)
	}
	return tc, tlocal, nil
}
