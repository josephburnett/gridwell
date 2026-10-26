package server

import (
	"context"
	"net/http"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/trace"
)

// contentRoute resolves the namespace and local id that serve a tile's content.
// It is the one link-resolution point, so every caller inherits the resolution
// instead of reimplementing it. Two facts keep it a single step: resolution
// happens only where the link row lives, so a transit-owned id forwards as-is;
// and a link never targets another link, so there is no chain to walk. Writes
// never resolve, because a link owns no content and the store refuses a write
// to a link row.
func (s *Server) contentRoute(ctx context.Context, qualifiedID string) (namespace.Namespace, string, error) {
	c, local, _, err := s.contentRow(ctx, qualifiedID)
	return c, local, err
}

// pageRoute is contentRoute for a page: a url tile a plugin presents is that
// plugin's to serve, asked with no key, the subpath its site address, and a
// broken one is refused with its reason, so nothing loads in its place. A far
// row is its own node's to judge, behind the hop, and comes back nil.
func (s *Server) pageRoute(ctx context.Context, qualifiedID string) (namespace.Namespace, string, *pb.Tile, error) {
	c, local, row, err := s.contentRow(ctx, qualifiedID)
	if err != nil || row == nil {
		return c, local, nil, err
	}
	if s.stampPresented(ctx, row); row.PresentedBy == "" {
		return c, local, row, nil
	}
	if row.BrokenReason != "" {
		return nil, "", nil, status.Error(gcodes.Unavailable, row.BrokenReason)
	}
	p, found := s.routeClient(row.PresentedBy)
	if !found {
		return nil, "", nil, undeclared(row.PresentedBy)
	}
	return p, "", row, nil
}

// servePage answers one page request from whoever serves it (pageRoute),
// unless it is a document under a presented tile for an address its plugin
// does not take (leavesPage): that is a redirect to the site, so a browser
// tab, which nothing can ask first, follows the routes as a desktop view does,
// or for an address a plugin that does not answer takes, its reason.
func (s *Server) servePage(ctx context.Context, req *pb.ServeContentRequest, send func(*pb.ServeContentChunk) error) error {
	c, local, row, err := s.pageRoute(ctx, req.TileId)
	if err != nil {
		return err
	}
	if site, v, ok := s.leavesPage(ctx, row, req); ok {
		if v.Broken != "" {
			return status.Error(gcodes.Unavailable, v.Broken)
		}
		trace.Emit("webapi", "left", "a document its plugin does not take", map[string]string{"tile": req.TileId, "site": site})
		return send(&pb.ServeContentChunk{Status: http.StatusSeeOther, Headers: []*pb.Header{{Name: "Location", Value: site}}})
	}
	bound := proto.CloneOf(req)
	bound.TileId = local
	return c.ServeContent(ctx, bound, send)
}

// contentRow is contentRoute and the row that owns the content, nil when a
// hop forwards it.
func (s *Server) contentRow(ctx context.Context, qualifiedID string) (namespace.Namespace, string, *pb.Tile, error) {
	if _, _, ok := rpc.SplitID(qualifiedID); !ok {
		return nil, "", nil, status.Errorf(gcodes.InvalidArgument, "unqualified id %q", qualifiedID)
	}
	c, local, uuid, transit, found := s.resolve(qualifiedID)
	if !found {
		return nil, "", nil, undeclared(qualifiedID)
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
	// The target is spelled from this node, so it routes like any other id,
	// through resolve. Peeling the first segment by hand would answer home for
	// a connection-chained target, which the transport owns.
	tc, tlocal, _, ttransit, found := s.resolve(target)
	if !found {
		return nil, "", nil, undeclared(target)
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
