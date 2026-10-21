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
// into another namespace is: the top-level copy enters through it at the
// destination cell, the walk inside a copied door at each source cell, so the
// rule is written once and holds at every depth. The router reads the source
// subtree through the namespace interface and materializes it in the
// destination plugin: create, recurse, framing and preview through the one
// writeback.
//
// Creation is necessarily top-down, because an interior door's child grid is
// allocated by its create. So the destination door appears immediately and
// fills in, and a mid-copy failure leaves a partial subtree that is visible and
// deletable with the error surfaced: never a silent half-state, and never a
// rollback that could destroy what the user already sees.
//
// What copies as what:
//   - a solid door becomes a new door plus a recursive copy of its child grid,
//     with its framing preserved through SetFraming;
//   - a door to another namespace or leaf link copies as a link (CLAUDE.md, 2026-10-06);
//   - a plugin's door copies as a link to its grid (rpc.CopiesAsLink);
//   - text and workspace bytes go ReadContent to the create, so the copy is born
//     with them and no reader sees it bodiless; a pane layout stays
//     owner-frame-relative, which is cross-plugin link semantics in bytes;
//   - a url copies its url_string plus the frozen preview and history;
//   - a shell copies as a link to its source, since a copy shares its
//     source's tmux and a tmux is namespace-local;
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

	switch {
	case rpc.IsDoorKind(q.Kind) && q.Link:
		// The link names the shared child, qualified.
		return rt.linkCopy(ctx, dst, dstGrid, t, x, y, q.ChildGridId)
	case rt.copiesAsLink(ctx, q):
		return rt.linkCopy(ctx, dst, dstGrid, t, x, y, q.ChildGridId)
	case rpc.IsDoorKind(q.Kind):
		created, err := rt.deepCopyDoor(ctx, src, srcTransit, srcUUID, t, dst, dstGrid, x, y)
		// Degrade only when nothing was created. Degrading with a partial in
		// place would stack a link on the cell it occupies, and the user
		// would get an overlap refusal on a grid they never touched.
		if created == nil && gwerr.IsTransport(err) {
			// Dark, not gone: a link (CLAUDE.md, 2026-10-06).
			return rt.linkCopy(ctx, dst, dstGrid, t, x, y, q.ChildGridId)
		}
		return created, err
	case q.LinkTargetId != "":
		return rt.linkCopy(ctx, dst, dstGrid, t, x, y, q.LinkTargetId)
	case t.Kind == rpc.KindShell:
		// A copy shares its source's tmux, and a tmux lives on the
		// node that started it, so across namespaces the copy is a link.
		return rt.linkCopy(ctx, dst, dstGrid, t, x, y, q.Id)
	case rpc.IsBodyKind(t.Kind), t.Kind == rpc.KindURL:
		return rt.copyLeaf(ctx, src, t, q, dst, dstGrid, x, y)
	}
	return nil, status.Errorf(gcodes.InvalidArgument,
		"cross-plugin copy: unsupported tile kind %q", t.Kind)
}

// copiesAsLink is rpc.CopiesAsLink over the router's read of the grid q
// opens, the stamp the client drew its ghost from (transit-normalized,
// disabled-stamped). A grid that does not answer says nothing here: the door
// arm reads it again and degrades to a link if it is dark.
func (rt *router) copiesAsLink(ctx context.Context, q *pb.Tile) bool {
	if !rpc.IsDoorKind(q.Kind) || q.Link {
		return false
	}
	g, err := rt.GetGrid(ctx, &pb.GetGridRequest{GridId: q.ChildGridId})
	return err == nil && rpc.CopiesAsLink(q, g.GetGrid().GetAcceptsTiles())
}

// copyLeaf copies a tile that owns no grid: bytes for a body kind, the address
// and frozen preview for a url.
func (rt *router) copyLeaf(ctx context.Context, src namespace.Namespace, t, q *pb.Tile, dst copyDst, dstGrid string, x, y int64) (*pb.TileResponse, error) {
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
			AltText: t.AltText, Named: t.Named, UrlString: t.UrlString}, body)
	if err != nil {
		return nil, err
	}
	id := created.GetTile().GetId()
	version := created.GetTile().GetVersion()

	switch {
	case t.Kind == rpc.KindURL:
		// The frozen preview travels with the copy. An unreachable preview skips:
		// the copy's own facts are present and the preview re-freezes on the next
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
			Tile:    &pb.Tile{Kind: t.Kind, UrlHistory: t.UrlHistory},
			Preview: pv.GetJpeg(),
		}); err != nil {
			return nil, err
		}
	default:
		return created, nil
	}
	// The preview write moved the row, so the answer is a fresh read of it.
	return freshCopy(ctx, dst, created), nil
}

// linkCopy is the copy a link becomes and the copy a dark source degrades
// to: exactly the door to another namespace or leaf link a left-drag would have made. A door
// links by the grid it opens and carries the framing the source was left at; a
// leaf links by its target.
func (rt *router) linkCopy(ctx context.Context, dst copyDst, dstGrid string, t *pb.Tile, x, y int64, target string) (*pb.TileResponse, error) {
	tile := &pb.Tile{Kind: t.Kind, X: x, Y: y, W: t.W, H: t.H, AltText: t.AltText, Named: t.Named}
	if rpc.IsDoorKind(t.Kind) {
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
// CreateTile, so the link it may carry is canonicalized, spelled for its
// holder and peeled into dst's frame here instead; see router.CreateTile.
func (rt *router) createCopy(ctx context.Context, dst copyDst, dstGrid string, tile *pb.Tile, content []byte) (*pb.TileResponse, error) {
	if err := rt.mintLinks(ctx, tile); err != nil {
		return nil, err
	}
	if err := rt.spellLinks(ctx, dst.holder, tile); err != nil {
		return nil, err
	}
	return dst.CreateTile(ctx, &pb.CreateTileRequest{GridId: dstGrid, Tile: dst.hop.PeelTile(tile), Content: content})
}

// freshCopy re-reads the copy so the answer carries the version and the preview
// the follow-up write left. An unreadable re-read keeps the create's row: the
// copy landed, and failing it here would say otherwise.
func freshCopy(ctx context.Context, dst namespace.Namespace, created *pb.TileResponse) *pb.TileResponse {
	fresh, err := dst.GetTile(ctx, &pb.GetTileRequest{TileId: created.GetTile().GetId()})
	if err != nil {
		return created
	}
	return fresh
}
