package server

import (
	"context"
	"fmt"
	"github.com/josephburnett/gridwell/api/gwerr"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Cross-plugin DEEP COPY of a solid well (issue #200) — the standing punt
// from the 2026-07-19 link/clone decision, unblocked by the content streams.
// The router walks the source subtree through the plugin interface (reads by
// LOCAL id against the source client; reference targets re-expressed in the
// server-global frame via the same qualification every response gets) and
// materializes it in the destination plugin: create → recurse → bytes via
// the one content door → framing via the one writeback.
//
// Order is a decided property, not an accident: creation is necessarily
// TOP-DOWN — an interior well's child grid is allocated BY its create, so
// children cannot precede their parent. The destination well therefore
// appears immediately and fills in; a mid-copy failure leaves a PARTIAL
// SUBTREE that is visible and deletable, with the error surfaced — never a
// silent half-state, and never a rollback that could destroy what the user
// already sees (charter §6).
//
// What copies as what:
//   - solid interior well → new well + recursive copy of its child grid,
//     framing (view_*) preserved via SetTile;
//   - exit well / leaf link inside the subtree → copies AS a reference
//     (references stay references — the same rule as the top-level clone);
//   - text/pane bytes → ReadContent → WriteContent (pane layouts stay
//     owner-frame-relative, the cross-plugin link semantics in bytes);
//   - url → url_string + the frozen preview/history via the SetTile freeze;
//   - shell → a fresh shell (a PTY session is plugin-local) + its frozen
//     preview.

// deepCopyWell copies the qualified source well st (whose plugin-local row
// is srcLocalTile) into destination grid dstGrid (dest-local id) at (x, y).
// Returns the created well (dest-local response). The source child grid is
// read BEFORE anything is created: an unreachable room must degrade to a
// LINK (the caller's decision, keyed on sourceUnreachable of the nil-out
// error), not to an empty solid well pretending to be a copy.
func (h *connectHandler) deepCopyWell(ctx context.Context, src pb.GridwellClient, srcTransit bool, srcUUID string, srcLocalTile *pb.Tile, dst pb.GridwellClient, dstGrid string, x, y int64) (*pb.TileResponse, error) {
	srcChild := srcLocalTile.ChildGridId
	g, err := src.GetGrid(ctx, &pb.GetGridRequest{GridId: srcChild})
	if err != nil {
		return nil, fmt.Errorf("read source grid %s: %w", srcChild, err)
	}

	created, err := dst.CreateTile(ctx, &pb.CreateTileRequest{
		GridId: dstGrid,
		Tile: &pb.Tile{Kind: "well", X: x, Y: y, W: srcLocalTile.W, H: srcLocalTile.H,
			AltText: srcLocalTile.AltText},
	})
	if err != nil {
		return nil, err
	}
	// Preserve the well's framing (preview = descent target = ascent
	// return). Framing-class: no version bump. The writeback's response is
	// the current row — return THAT, not the pre-framing create response.
	framed, err := dst.SetTile(ctx, &pb.SetTileRequest{
		TileId: created.GetTile().GetId(), Version: created.GetTile().GetVersion(),
		Tile: &pb.Tile{Kind: "well", ViewCx: srcLocalTile.ViewCx, ViewCy: srcLocalTile.ViewCy, ViewZoom: srcLocalTile.ViewZoom},
	})
	if err != nil {
		return created, fmt.Errorf("well framing: %w", err)
	}
	created = framed

	dstChild := created.GetTile().GetChildGridId()
	for _, child := range g.Tiles {
		if err := h.deepCopyTile(ctx, src, srcTransit, srcUUID, child, dst, dstChild); err != nil {
			return created, fmt.Errorf("copy tile %s: %w", child.Id, err)
		}
	}
	return created, nil
}

// deepCopyTile copies one plugin-local source tile into dest-local grid
// dstGrid at the source's own coordinates.
func (h *connectHandler) deepCopyTile(ctx context.Context, src pb.GridwellClient, srcTransit bool, srcUUID string, t *pb.Tile, dst pb.GridwellClient, dstGrid string) error {
	// The server-global view of this tile decides its reference-ness and the
	// qualified targets a copied reference must carry — the SAME
	// qualification every wire response gets, so the copy and a fresh read
	// can never disagree about what a link is.
	q := qualifyTilesFor(srcTransit, srcUUID, []*pb.Tile{t})[0]

	switch {
	case q.Kind == "well" && q.Reference:
		// A reference copies as a reference: the shared child, qualified.
		_, err := dst.CreateTile(ctx, &pb.CreateTileRequest{
			GridId: dstGrid,
			Tile: &pb.Tile{Kind: "well", X: t.X, Y: t.Y, W: t.W, H: t.H,
				AltText: t.AltText, ChildGridId: q.ChildGridId,
				ViewCx: t.ViewCx, ViewCy: t.ViewCy, ViewZoom: t.ViewZoom},
		})
		return err
	case q.Kind == "well":
		created, err := h.deepCopyWell(ctx, src, srcTransit, srcUUID, t, dst, dstGrid, t.X, t.Y)
		// Degrade ONLY when nothing was created (the source grid was dark
		// before the copy began) — the same guard the top-level clone has
		// (out != nil). A failure with a partial in place must surface as
		// itself: firing the degrade there stacks a link on the cell the
		// partial already occupies, and the user gets an "overlap" refusal
		// pointing at a grid they never touched.
		if created == nil && gwerr.IsTransport(err) {
			// The room is DARK, not gone (offline-plan owner decision
			// 2026-08-14): degrade to a LINK to the original — the dashed
			// border says "lives elsewhere" in the vocabulary that already
			// means it, instead of failing the whole walk or leaving an
			// empty solid well that lies about being a copy. Back online,
			// the link resolves and a right-drag completes the copy.
			_, lerr := dst.CreateTile(ctx, &pb.CreateTileRequest{
				GridId: dstGrid,
				Tile: &pb.Tile{Kind: "well", X: t.X, Y: t.Y, W: t.W, H: t.H,
					AltText: t.AltText, ChildGridId: q.ChildGridId,
					ViewCx: t.ViewCx, ViewCy: t.ViewCy, ViewZoom: t.ViewZoom},
			})
			return lerr
		}
		return err
	case q.LinkTargetId != "":
		_, err := dst.CreateTile(ctx, &pb.CreateTileRequest{
			GridId: dstGrid,
			Tile: &pb.Tile{Kind: t.Kind, X: t.X, Y: t.Y, W: t.W, H: t.H,
				AltText: t.AltText, LinkTargetId: q.LinkTargetId},
		})
		return err
	}

	// Leaf bytes are read BEFORE the copy row is created, so an unreachable
	// source degrades to a link instead of leaving an empty copy that looks
	// whole (the silent-incompleteness class the offline decision forbids).
	var body []byte
	if (t.Kind == "text" || t.Kind == "pane") && t.BlobId != 0 {
		var err error
		body, err = readAllContent(ctx, src, t.Id)
		if gwerr.IsTransport(err) {
			_, lerr := dst.CreateTile(ctx, &pb.CreateTileRequest{
				GridId: dstGrid,
				Tile: &pb.Tile{Kind: t.Kind, X: t.X, Y: t.Y, W: t.W, H: t.H,
					AltText: t.AltText, LinkTargetId: q.Id},
			})
			return lerr
		}
		if err != nil {
			return err
		}
	}

	created, err := dst.CreateTile(ctx, &pb.CreateTileRequest{
		GridId: dstGrid,
		Tile: &pb.Tile{Kind: t.Kind, X: t.X, Y: t.Y, W: t.W, H: t.H,
			AltText: t.AltText, UrlString: t.UrlString},
	})
	if err != nil {
		return err
	}
	id := created.GetTile().GetId()
	version := created.GetTile().GetVersion()

	switch t.Kind {
	case "text", "pane":
		if len(body) == 0 {
			return nil
		}
		_, err = writeAllContent(ctx, dst, id, version, body)
		return err
	case "url", "shell":
		// The frozen face travels with the copy: preview jpeg (+ url
		// history) through the kind's own freeze writeback. An absent
		// preview skips — the writeback's empty-fields rule would treat a
		// zero-byte jpeg as "skip" anyway. An UNREACHABLE preview also
		// skips: the copy's fact (the address / the label) is present, the
		// face is derived and re-freezes on the next live visit — a link
		// here would deny the copy of content the walk actually has.
		if t.PreviewBlobId == 0 {
			return nil
		}
		pv, err := src.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: t.Id})
		if gwerr.IsTransport(err) {
			return nil
		}
		if err != nil || len(pv.GetJpeg()) == 0 {
			return err
		}
		_, err = dst.SetTile(ctx, &pb.SetTileRequest{
			TileId: id, Version: version,
			Tile:    &pb.Tile{Kind: t.Kind, UrlString: t.UrlString, UrlHistory: t.UrlHistory},
			Preview: pv.GetJpeg(),
		})
		return err
	}
	return nil
}
