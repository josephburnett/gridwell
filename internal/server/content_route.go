package server

import (
	"context"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// contentRoute resolves the plugin client + local id that serves a tile's
// CONTENT — body bytes and preview. It is the ONE link-resolution point
// (owner decision 8, 2026-07-26): if the tile is a leaf link, the route
// follows link_target_id at the serving node, so every caller — our client,
// a CLI, a remote mounter — inherits the resolution instead of
// reimplementing it. Every content-serving door (Connect ReadContent and
// the node export) resolves through here.
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
func (s *Server) contentRoute(ctx context.Context, qualifiedID string) (pb.GridwellClient, string, error) {
	uuid, local, ok := rpc.SplitID(qualifiedID)
	if !ok {
		return nil, "", status.Errorf(gcodes.InvalidArgument, "unqualified id %q", qualifiedID)
	}
	c, found := s.routeClient(uuid)
	if !found {
		return nil, "", status.Errorf(gcodes.NotFound, "no plugin %q", uuid)
	}
	if s.pluginReg.Transit(uuid) {
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
