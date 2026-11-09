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

// Copy across namespaces. What a copy of a tile becomes is rpc.CopyOf's; this
// is its executor across sources: the top-level copy enters deepCopyTile at
// the destination cell, the walk inside a copied door at each source cell.
// The router reads the source subtree through the namespace interface and
// materializes it in the destination namespace: create, recurse, framing and
// preview through the one writeback.
//
// Creation is necessarily top-down, because an interior door's child grid is
// allocated by its create. So the destination door appears immediately and
// fills in, and a mid-copy failure leaves a partial subtree that is visible and
// deletable with the error surfaced: never a silent half-state, and never a
// rollback that could destroy what the user already sees.
//
// The mechanics of each verdict:
//   - a deep door's framing is preserved through SetFraming;
//   - into a directory only a text tile copies, made a markdown file there
//     (rpc.IntoDirectory);
//   - text and workspace bytes go ReadContent to the create, so the copy is born
//     with them and no reader sees it bodiless; a pane layout stays
//     owner-frame-relative, which is a link's semantics in bytes;
//   - a url copies its url_string, and every leaf rpc.CopyFacts by rpc.CopyWrites;
//   - a source that never answered degrades to a link (CLAUDE.md, 2026-10-06).

// deepCopyDoor reads the source child grid before anything is created, so an
// unreachable grid answers with nothing created and deepCopyTile links instead
// of leaving an empty solid door pretending to be a copy.
func (rt *router) deepCopyDoor(ctx context.Context, src namespace.Namespace, srcTransit bool, srcUUID string, srcLocalTile *pb.Tile, dst copyDst, dstGrid string, x, y int64) (*pb.TileResponse, error) {
	srcChild := srcLocalTile.ChildGridId
	g, err := src.GetGrid(ctx, &pb.GetGridRequest{GridId: srcChild})
	if err != nil {
		return nil, fmt.Errorf("read source grid %s: %w", srcChild, err)
	}

	created, err := rt.createCopy(ctx, dst, dstGrid,
		&pb.Tile{Kind: rpc.KindDoor, X: x, Y: y, W: srcLocalTile.W, H: srcLocalTile.H,
			AltText: srcLocalTile.AltText, Named: srcLocalTile.Named}, nil)
	if err != nil {
		return nil, err
	}
	// The door's framing is at once the preview, the descent target and the
	// ascent return; a source never visited has none to copy. The
	// writeback's response is the current row, so return that rather than
	// the pre-framing create response.
	if f, ok := rpc.ViewOf(srcLocalTile.ViewCx, srcLocalTile.ViewCy, srcLocalTile.ViewZoom).Framing(); ok {
		framed, err := dst.SetFraming(ctx, &pb.SetFramingRequest{
			TileId: created.GetTile().GetId(), Cx: f.Cx(), Cy: f.Cy(), Zoom: f.Zoom(),
		})
		if err != nil {
			return created, fmt.Errorf("door framing: %w", err)
		}
		created = &pb.TileResponse{Tile: framed.GetTile()}
	}

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
// the row it created. A failure once a door's copy exists answers with that
// partial, so the caller can say the partial remains; every other failure
// answers nil.
func (rt *router) deepCopyTile(ctx context.Context, src namespace.Namespace, srcTransit bool, srcUUID string, t *pb.Tile, dst copyDst, dstGrid string, x, y int64) (*pb.TileResponse, error) {
	// The qualification every wire response gets decides whether this tile is
	// a link, so the copy and a fresh read cannot disagree.
	q := qualifyTilesFor(srcTransit, srcUUID, []*pb.Tile{t})[0]
	if why := rpc.IntoDirectory(q); why != "" && rpc.IsFilesID(dst.holder) {
		return nil, status.Error(gcodes.InvalidArgument, why)
	}
	// A grid that does not answer says nothing here: the deep arm reads it
	// again and links if it is dark.
	accepts := func() bool {
		g, err := rt.GetGrid(ctx, &pb.GetGridRequest{GridId: q.ChildGridId})
		return err != nil || g.GetGrid().GetAcceptsTiles()
	}
	// The walk runs where the ends part, or into a directory, which takes a
	// text alone, so nothing here copies as a shell.
	as, target := rpc.CopyOf(q, false, accepts)
	switch as {
	case rpc.CopyLinkGrid, rpc.CopyLinkTile:
		return rt.linkCopy(ctx, dst, dstGrid, t, x, y, as, target)
	case rpc.CopyDeep:
		created, err := rt.deepCopyDoor(ctx, src, srcTransit, srcUUID, t, dst, dstGrid, x, y)
		// Degrade only when nothing was created. Degrading with a partial in
		// place would stack a link on the cell it occupies, and the user
		// would get an overlap refusal on a grid they never touched.
		if created == nil && gwerr.IsTransport(err) {
			// Dark, not gone: a link (CLAUDE.md, 2026-10-06).
			return rt.linkCopy(ctx, dst, dstGrid, t, x, y, rpc.CopyLinkGrid, target)
		}
		return created, err
	case rpc.CopyLeaf:
		return rt.copyLeaf(ctx, src, t, q, dst, dstGrid, x, y)
	}
	return nil, status.Error(gcodes.InvalidArgument, "copy across namespaces: "+target)
}

// copyLeaf copies a tile that owns no grid: bytes for a body kind, the address
// for a url, and the facts and preview every copy carries.
func (rt *router) copyLeaf(ctx context.Context, src namespace.Namespace, t, q *pb.Tile, dst copyDst, dstGrid string, x, y int64) (*pb.TileResponse, error) {
	// Leaf bytes are read before the copy row is created, so an unreachable
	// source degrades to a link instead of an empty copy that looks whole. A
	// body kind is always asked: blob_id is the local store's own bookkeeping
	// and a file's row carries none, so gating on it would copy a file's
	// text as an empty tile.
	var body []byte
	if rpc.IsBodyKind(t.Kind) {
		var err error
		body, err = readAllContent(ctx, src, t.Id)
		if gwerr.IsTransport(err) {
			return rt.linkCopy(ctx, dst, dstGrid, t, x, y, rpc.CopyLinkTile, q.Id)
		}
		if err != nil {
			return nil, err
		}
	}

	created, err := rt.createCopy(ctx, dst, dstGrid,
		&pb.Tile{Kind: t.Kind, X: x, Y: y, W: t.W, H: t.H,
			AltText: t.AltText, Named: t.Named, UrlString: t.UrlString}, body)
	if err != nil {
		return nil, err
	}
	// An unreachable preview skips: it re-freezes on the next live visit, and a
	// link here would deny the copy the content the walk has.
	var preview []byte
	if rpc.WebContent(t) && t.PreviewBlobId != 0 {
		pv, err := src.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: t.Id})
		if err != nil && !gwerr.IsTransport(err) {
			return nil, err
		}
		preview = pv.GetJpeg()
	}
	for _, w := range rpc.CopyWrites(rpc.CopyFacts(t), preview) {
		w.TileId = created.GetTile().GetId()
		if created, err = dst.SetTile(ctx, w); err != nil {
			return nil, err
		}
	}
	return created, nil
}

// linkCopy is the copy a link becomes and the copy a dark source degrades
// to: exactly the door to another namespace or leaf link a left-drag would have made. A door
// links by the grid it opens and carries the framing the source was left at; a
// leaf links by its target.
func (rt *router) linkCopy(ctx context.Context, dst copyDst, dstGrid string, t *pb.Tile, x, y int64, as rpc.CopyAs, target string) (*pb.TileResponse, error) {
	tile := &pb.Tile{Kind: t.Kind, X: x, Y: y, W: t.W, H: t.H, AltText: t.AltText, Named: t.Named}
	if as == rpc.CopyLinkGrid {
		tile.ChildGridId = target
		tile.ViewCx, tile.ViewCy, tile.ViewZoom = t.ViewCx, t.ViewCy, t.ViewZoom
	} else {
		tile.LinkTargetId = target
	}
	return rt.createCopy(ctx, dst, dstGrid, tile, nil)
}

// copyDst is where a deep copy writes: the destination namespace, the peel into
// its frame, and holder, the grid the gesture named in this node's frame, whose
// node holds every link the copy writes.
type copyDst struct {
	namespace.Namespace
	hop    rpc.Hop
	holder string
}

// createCopy stores one copy row with the bytes it is born with. The create
// goes straight to the destination namespace rather than back through
// CreateTile, so the link it may carry is spelled for its holder and peeled
// into dst's frame here instead; see router.CreateTile.
func (rt *router) createCopy(ctx context.Context, dst copyDst, dstGrid string, tile *pb.Tile, content []byte) (*pb.TileResponse, error) {
	if err := rt.spellLinks(ctx, dst.holder, tile); err != nil {
		return nil, err
	}
	return dst.CreateTile(ctx, &pb.CreateTileRequest{GridId: dstGrid, Tile: dst.hop.PeelTile(tile), Content: content})
}
