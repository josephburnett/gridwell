package server

import (
	"context"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// contentRoute resolves the namespace and local id that serve a tile's
// content: its body bytes and its preview. It is the one link-resolution
// point: when the tile is a leaf link, the route follows link_target_id at the
// serving node, so every caller — the client, a CLI, a remote mounter —
// inherits the resolution instead of reimplementing it. Every content-serving
// door resolves through here.
//
// Two structural facts keep this a single step:
//   - resolution happens only where the link row LIVES: a transit-owned id
//     forwards as-is, and the remote node — running this same code — resolves
//     its own links one hop over;
//   - a link never targets another link (a link dragged onward links to the
//     original target, never the middleman), so there is no chain to walk.
//
// Writes never resolve: a link owns no content, and content writes address
// the target the caller names explicitly (the store refuses a write to a
// link row).
func (s *Server) contentRoute(ctx context.Context, qualifiedID string) (namespace.Namespace, string, error) {
	if _, _, ok := rpc.SplitID(qualifiedID); !ok {
		return nil, "", status.Errorf(gcodes.InvalidArgument, "unqualified id %q", qualifiedID)
	}
	c, local, uuid, transit, found := s.resolve(qualifiedID)
	if !found {
		return nil, "", status.Errorf(gcodes.NotFound, "no plugin %q", uuid)
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
	// A leaf plugin stores its target already qualified from this node's
	// perspective, so it routes like any other id.
	tuuid, tlocal, ok := rpc.SplitID(target)
	if !ok {
		return nil, "", status.Errorf(gcodes.Internal, "link target %q is not qualified", target)
	}
	tc, found := s.routeClient(tuuid)
	if !found {
		return nil, "", status.Errorf(gcodes.NotFound, "no plugin %q for link target %q", tuuid, target)
	}
	return tc, tlocal, nil
}
