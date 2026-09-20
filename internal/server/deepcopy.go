package server

import (
	"context"
	"fmt"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// Cross-plugin copy. deepCopyTile is the one owner of what a copy of a tile
// into another namespace is: the top-level clone enters through it at the
// destination cell, the walk inside a copied well at each source cell, so the
// rule is written once and holds at every depth. The router reads the source
// subtree through the namespace interface and materializes it in the
// destination plugin: create, recurse, bytes through the one content door,
// framing and face through the one writeback.
//
// Creation is necessarily top-down, because an interior well's child grid is
// allocated by its create. So the destination well appears immediately and
// fills in, and a mid-copy failure leaves a partial subtree that is visible and
// deletable with the error surfaced: never a silent half-state, and never a
// rollback that could destroy what the user already sees.
//
// What copies as what:
//   - a solid well becomes a new well plus a recursive copy of its child grid,
//     with its framing preserved through SetFraming;
//   - an exit well or leaf link copies as a reference;
//   - text and pane bytes go ReadContent to WriteContent; a pane layout stays
//     owner-frame-relative, which is cross-plugin link semantics in bytes;
//   - a url copies its url_string plus the frozen preview and history;
//   - a shell becomes a fresh shell, since a PTY session is namespace-local;
//   - a source that never answered degrades to a link.

// deepCopyWell reads the source child grid before anything is created, so an
// unreachable room degrades to a link (sourceUnreachable with a nil out) rather
// than to an empty solid well pretending to be a copy.
func (rt *router) deepCopyWell(ctx context.Context, src namespace.Namespace, srcTransit bool, srcUUID string, srcLocalTile *pb.Tile, dst namespace.Namespace, dstGrid string, x, y int64) (*pb.TileResponse, error) {
	srcChild := srcLocalTile.ChildGridId
	g, err := src.GetGrid(ctx, &pb.GetGridRequest{GridId: srcChild})
	if err != nil {
		return nil, fmt.Errorf("read source grid %s: %w", srcChild, err)
	}

	created, err := rt.createCopy(ctx, dst, dstGrid,
		&pb.Tile{Kind: rpc.KindWell, X: x, Y: y, W: srcLocalTile.W, H: srcLocalTile.H,
			AltText: srcLocalTile.AltText})
	if err != nil {
		return nil, err
	}
	// The well's framing is at once the preview, the descent target and the
	// ascent return. The writeback's response is the current row, so return
	// that rather than the pre-framing create response.
	framed, err := dst.SetFraming(ctx, &pb.SetFramingRequest{
		TileId: created.GetTile().GetId(),
		Cx:     srcLocalTile.ViewCx, Cy: srcLocalTile.ViewCy, Zoom: srcLocalTile.ViewZoom,
	})
	if err != nil {
		return created, fmt.Errorf("well framing: %w", err)
	}
	created = &pb.TileResponse{Tile: framed.GetTile()}

	dstChild := created.GetTile().GetChildGridId()
	for _, child := range g.Tiles {
		// A child keeps the cell it sits in; only the top-level copy lands
		// where the gesture dropped it.
		if _, err := rt.deepCopyTile(ctx, src, srcTransit, srcUUID, child, dst, dstChild, child.X, child.Y); err != nil {
			return created, fmt.Errorf("copy tile %s: %w", child.Id, err)
		}
	}
	return created, nil
}

// deepCopyTile copies one source tile into dstGrid at (x, y) and answers with
// the row it created. A failure once a well's copy exists answers with that
// partial, so the caller can say the partial remains; every other failure
// answers nil.
func (rt *router) deepCopyTile(ctx context.Context, src namespace.Namespace, srcTransit bool, srcUUID string, t *pb.Tile, dst namespace.Namespace, dstGrid string, x, y int64) (*pb.TileResponse, error) {
	// The qualification every wire response gets decides whether this tile is
	// a reference, so the copy and a fresh read cannot disagree.
	q := qualifyTilesFor(srcTransit, srcUUID, []*pb.Tile{t})[0]

	switch {
	case rpc.IsWellKind(q.Kind) && q.Reference:
		// A reference copies as a reference: the shared child, qualified.
		return rt.linkCopy(ctx, dst, dstGrid, t, x, y, q.ChildGridId)
	case rpc.IsWellKind(q.Kind):
		created, err := rt.deepCopyWell(ctx, src, srcTransit, srcUUID, t, dst, dstGrid, x, y)
		// Degrade only when nothing was created. Degrading with a partial in
		// place would stack a link on the cell it occupies, and the user
		// would get an overlap refusal on a grid they never touched.
		if created == nil && gwerr.IsTransport(err) {
			// The room is dark, not gone, so degrade to a link: the dashed
			// border already means "lives elsewhere", which beats failing the
			// walk or leaving an empty well that lies about being a copy.
			return rt.linkCopy(ctx, dst, dstGrid, t, x, y, q.ChildGridId)
		}
		return created, err
	case q.LinkTargetId != "":
		// The tile being copied is a reference, so the copy is one too.
		return rt.linkCopy(ctx, dst, dstGrid, t, x, y, q.LinkTargetId)
	case rpc.IsBodyKind(t.Kind), t.Kind == rpc.KindURL, t.Kind == rpc.KindShell:
		return rt.copyLeaf(ctx, src, t, q, dst, dstGrid, x, y)
	}
	return nil, status.Errorf(gcodes.InvalidArgument,
		"cross-plugin clone: unsupported tile kind %q", t.Kind)
}

// copyLeaf copies a tile that owns no grid: bytes for a body kind, the address
// and frozen face for a url, a fresh session for a shell.
func (rt *router) copyLeaf(ctx context.Context, src namespace.Namespace, t, q *pb.Tile, dst namespace.Namespace, dstGrid string, x, y int64) (*pb.TileResponse, error) {
	// Leaf bytes are read before the copy row is created, so an unreachable
	// source degrades to a link instead of an empty copy that looks whole. A
	// body kind is always asked: blob_id is the local store's own bookkeeping
	// and a plugin's rows carry none, so gating on it would copy a plugin's
	// text as an empty tile.
	var body []byte
	if rpc.IsBodyKind(t.Kind) {
		var err error
		body, err = readAllContent(ctx, src, t.Id)
		if gwerr.IsTransport(err) {
			return rt.linkCopy(ctx, dst, dstGrid, t, x, y, q.Id)
		}
		if err != nil {
			return nil, err
		}
	}

	created, err := rt.createCopy(ctx, dst, dstGrid,
		&pb.Tile{Kind: t.Kind, X: x, Y: y, W: t.W, H: t.H,
			AltText: t.AltText, UrlString: t.UrlString})
	if err != nil {
		return nil, err
	}
	id := created.GetTile().GetId()
	version := created.GetTile().GetVersion()

	switch {
	case rpc.IsBodyKind(t.Kind):
		if len(body) == 0 {
			return created, nil
		}
		// Not atomic with the create: a failure leaves a visible, deletable
		// empty copy and surfaces, never a silent half-state.
		if _, err := writeAllContent(ctx, dst, id, version, body); err != nil {
			return nil, err
		}
	case t.Kind == rpc.KindURL || t.Kind == rpc.KindShell:
		// The frozen face travels with the copy. An unreachable preview skips:
		// the copy's own facts are present and the face re-freezes on the next
		// live visit, so a link here would deny the copy content the walk has.
		if t.PreviewBlobId == 0 {
			return created, nil
		}
		pv, err := src.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: t.Id})
		if gwerr.IsTransport(err) {
			return created, nil
		}
		if err != nil {
			return nil, err
		}
		if len(pv.GetJpeg()) == 0 {
			return created, nil
		}
		if _, err := dst.SetTile(ctx, &pb.SetTileRequest{
			TileId: id, Version: version,
			Tile:    &pb.Tile{Kind: t.Kind, UrlString: t.UrlString, UrlHistory: t.UrlHistory},
			Preview: pv.GetJpeg(),
		}); err != nil {
			return nil, err
		}
	default:
		return created, nil
	}
	// A second write moved the row, so the answer is a fresh read of it.
	return freshCopy(ctx, dst, created), nil
}

// linkCopy is the copy a reference becomes and the copy a dark source degrades
// to: exactly the exit well or leaf link a left-drag would have made. A well
// links by the grid it opens and carries the framing the source was left at; a
// leaf links by its target.
func (rt *router) linkCopy(ctx context.Context, dst namespace.Namespace, dstGrid string, t *pb.Tile, x, y int64, target string) (*pb.TileResponse, error) {
	tile := &pb.Tile{Kind: t.Kind, X: x, Y: y, W: t.W, H: t.H, AltText: t.AltText}
	if rpc.IsWellKind(t.Kind) {
		tile.ChildGridId = target
		tile.ViewCx, tile.ViewCy, tile.ViewZoom = t.ViewCx, t.ViewCy, t.ViewZoom
	} else {
		tile.LinkTargetId = target
	}
	return rt.createCopy(ctx, dst, dstGrid, tile)
}

// createCopy stores one copy row. The create goes straight to the destination
// namespace rather than back through CreateTile, so the reference it may carry
// is canonicalized here instead; see router.mintReferences.
func (rt *router) createCopy(ctx context.Context, dst namespace.Namespace, dstGrid string, tile *pb.Tile) (*pb.TileResponse, error) {
	if err := rt.mintReferences(ctx, tile); err != nil {
		return nil, err
	}
	return dst.CreateTile(ctx, &pb.CreateTileRequest{GridId: dstGrid, Tile: tile})
}

// freshCopy re-reads the copy so the answer carries the version and the face
// the follow-up write left. An unreadable re-read keeps the create's row: the
// copy landed, and failing it here would say otherwise.
func freshCopy(ctx context.Context, dst namespace.Namespace, created *pb.TileResponse) *pb.TileResponse {
	fresh, err := dst.GetTile(ctx, &pb.GetTileRequest{TileId: created.GetTile().GetId()})
	if err != nil {
		return created
	}
	return fresh
}
