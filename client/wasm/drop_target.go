//go:build js && wasm

package main

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// dropTarget describes where the cursor is currently pointing as a
// destination for a drag-and-drop. Two flavors:
//
//   - parent-grid drop: gridID is the focused leaf grid of `pane`,
//     cellSize is the parent cell size.
//   - well-child-grid drop: gridID is the open well's ChildGridID,
//     cellSize is the preview cell size, and path is the descent path
//     into the well's interior.
//
// origin{X,Y} is the screen coordinate of cell (0, 0) in the target
// grid — combined with cellSize and the cursor it's enough to compute
// the target cell at the cursor.
type dropTarget struct {
	pane     *pane.Pane
	gridID   string
	cellSize float64
	originX  float64
	originY  float64
}

// previewDrop updates the active ghost for an in-flight tile drag from
// the SAME dragdrop.DecideDrop verdict the commit path uses, so a
// previewed action can never diverge from the committed one (the
// trashcan-delete regression was exactly such a divergence). clone picks
// the right-drag flavor: Clone=true, Forbidden from CloneForbidden (a
// solid well can't deep-copy across a namespace), and no no-entry badge
// on a read-only doc (the clone preview never drew one). The left flavor
// feeds MoveForbidden and, across a namespace, previews the LINK (dashed
// ghost + chain badge — the teaching signal).
//
// SameCell/Occupied are intentionally NOT fed here: the preview is
// optimistic about placement (it shows the snap-to-cell even over an
// occupied cell, matching the pre-unification behavior); the commit does
// the authoritative overlap check and snaps back. The unification covers
// the action CLASS (delete/link/place/reject) — where the bug lived.
func (a *App) previewDrop(d *dragState, sx, sy float64, clone bool) {
	if a.ghost == nil {
		return
	}
	in := dragdrop.DropInput{
		Started:       d.started,
		OriginFocused: d.originFocused,
		IsTemplate:    d.isTemplate,
		Clone:         clone,
		TileID:        d.tileID,
		OverDelete:    a.overDeleteButton(d, sx, sy),
	}
	t, haveT := a.dropTargetAt(sx, sy, d.tileID)
	in.HasTarget = haveT
	if haveT {
		in.TargetReadOnly = a.gridKnownReadOnly(t.gridID)
		in.SameGrid = t.gridID == d.srcGridID
		in.CrossPlugin = dropCrossNamespace(d, t)
		if clone {
			in.Forbidden = dropForbiddenForClone(d, t)
		} else {
			in.Forbidden = a.dropForbiddenForMove(d, t)
		}
	}

	// The verdict picks the action; GhostPlanForDrop picks the styling. Both
	// are pure + tested in client/dragdrop, so preview and commit (and the
	// styling itself) can't drift. The ghost rests in a different pane per
	// verdict, so feed all three candidate pane ids + sizes.
	var targetPaneID string
	var targetCellSize float64
	if haveT {
		targetPaneID = t.pane.ID
		targetCellSize = t.cellSize
	}
	plan := dragdrop.GhostPlanForDrop(dragdrop.DecideDrop(in), in.Forbidden, clone,
		d.originPaneID, targetPaneID, d.srcCellSize, targetCellSize)
	a.ghost.paneID = plan.PaneID
	a.ghost.targetCellSize = plan.TargetCellSize
	a.ghost.targetFragmentation = plan.Fragmentation
	a.ghost.forbidden = plan.Forbidden
	a.ghost.link = plan.Link
	a.canvas.Get("style").Set("cursor", plan.Cursor)

	size := a.ghost.displayedCellSize
	a.ghost.screenX = sx - d.cellOffsetX*size
	a.ghost.screenY = sy - d.cellOffsetY*size
}

// dropTargetAt resolves the cursor at (sx, sy) to a drop target.
// Returns false when the cursor is over a file-mode pane or off-canvas
// — neither is a valid drop destination.
//
// excludeTileID, if non-zero, prevents a well at that row id from
// being treated as a drop-into-well target — used so a user dragging
// well X around can't accidentally drop X into its own child grid
// when the cursor is still on top of X. Pass d.tileID from the
// dragState; it's a safe no-op when the source isn't a well.
func (a *App) dropTargetAt(sx, sy float64, excludeTileID string) (*dropTarget, bool) {
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil, false
	}
	if p.TextFocus != "" {
		return nil, false
	}
	parentCell := cellPx * p.Zoom

	// Parent-grid origin (top-left of cell (0, 0) in screen coords).
	ps := paneToDragdrop(p, r)
	parentOriginX, parentOriginY := ps.CellToScreen(0, 0)

	// Look for an open well under the cursor — that promotes the target to
	// the well's child grid. The rule (enterable well, not the dragged tile
	// itself) is the tested dragdrop.PromoteToWell.
	cellX, cellY := cellAtScreen(p, r, sx, sy)
	if n := a.tileAtCell(p, cellX, cellY); n != nil &&
		dragdrop.PromoteToWell(rpc.IsWellKind(n.Kind), n.ChildGridID, n.ID, excludeTileID) {
		// Well preview math. Effective ratio resolves the unvisited
		// fallback in one place so the child cell size is computed
		// the same way here as in the renderer.
		ratio := zoomtrans.EffectiveViewZoom(n.ViewZoom, zoomtrans.DefaultWellViewZoom)
		cp := dragdrop.ChildPreviewFor(ps, struct {
			X, Y, W, H, ViewX, ViewY int64
		}{X: n.X, Y: n.Y, W: n.W, H: n.H, ViewX: n.ViewX, ViewY: n.ViewY},
			ratio)
		return &dropTarget{
			pane:     p,
			gridID:   n.ChildGridID,
			cellSize: cp.CellPx,
			originX:  cp.OriginX,
			originY:  cp.OriginY,
		}, true
	}

	// Parent-grid drop.
	return &dropTarget{
		pane:     p,
		gridID:   a.gridIDForPane(p),
		cellSize: parentCell,
		originX:  parentOriginX,
		originY:  parentOriginY,
	}, true
}

// tileCopy returns a copy of *n owned by the caller — the cache may
// rewrite its tile map underneath us, so callers that retain a tile
// across event boundaries should hold their own copy.
func tileCopy(n *rpc.Tile) *rpc.Tile {
	c := *n
	return &c
}

// gridSourceKind returns the grid's source kind (fs/proc), or "" for a
// regular Gridwell-owned grid or an unknown grid id. Wraps the cache
// lookup so callers can use the SourceKind comparison directly without
// repeating the Meta-field dance.
func (a *App) gridSourceKind(gridID string) string {
	g, ok := a.c.Grid(gridID)
	if !ok {
		return ""
	}
	return g.Meta.SourceKind
}

// dropCrossNamespace reports whether the drag's source grid and t's
// destination grid live in different id namespaces — the one predicate the
// 2026-07-19 gestures branch on (left-drag becomes a LINK, a solid well's
// right-drag is refused). One reader of NamespaceOf for all three gather
// sites (left preview, left commit, right commit) so they cannot disagree.
func dropCrossNamespace(d *dragState, t *dropTarget) bool {
	if d == nil || t == nil {
		return false
	}
	return rpc.NamespaceOf(d.srcGridID) != rpc.NamespaceOf(t.gridID)
}

// dropForbiddenForMove reports whether a left-drag from the dragState's
// source grid to t's destination grid is rejected up front: a SAME-namespace
// cross-grid move with a source-backed endpoint (host mv unimplemented; host
// dirs aren't a placement medium). A cross-namespace left-drag is never a
// move — it verdicts DropLink — so it is exempt here (the read-only
// destination case is the separate TargetReadOnly gate). The UI flags the
// forbidden gestures so the cursor and the ghost render "no entry" instead
// of inviting the failed drop.
func (a *App) dropForbiddenForMove(d *dragState, t *dropTarget) bool {
	if d == nil || t == nil {
		return false
	}
	return dragdrop.MoveForbidden(
		d.srcGridID == t.gridID,
		dropCrossNamespace(d, t),
		a.gridSourceKind(d.srcGridID),
		a.gridSourceKind(t.gridID),
	)
}

// dropForbiddenForClone reports whether a right-drag (clone) is rejected up
// front: a SOLID well cannot deep-copy across a namespace yet (the server
// refuses with unimplemented). Links clone anywhere — copying a link copies
// the reference.
func dropForbiddenForClone(d *dragState, t *dropTarget) bool {
	if d == nil || t == nil {
		return false
	}
	return dragdrop.CloneForbidden(
		dropCrossNamespace(d, t),
		rpc.IsWellKind(d.snapshotTile.Kind),
		d.snapshotTile.Reference,
	)
}

// cellAtCursorInTarget returns the (rounded) cell coord at the cursor
// for a given drop target, taking the dragState's cell offset into
// account so the snap point matches the grab point on the source tile.
func (t *dropTarget) cellAtCursor(sx, sy, cellOffsetX, cellOffsetY float64) (int64, int64) {
	cx := (sx-t.originX)/t.cellSize - cellOffsetX
	cy := (sy-t.originY)/t.cellSize - cellOffsetY
	return dragdrop.SnapToCell(cx), dragdrop.SnapToCell(cy)
}

// childTileAtScreen returns the preview tile under (sx, sy) inside
// well's child preview, or nil if no tile is there. Used at mousedown
// to decide whether a click on a well is starting a "pull out" gesture
// on a specific child tile.
func (a *App) childTileAtScreen(p *pane.Pane, r pane.Rect, well *rpc.Tile, sx, sy float64) *rpc.Tile {
	if !rpc.IsWellKind(well.Kind) || well.ChildGridID == "" {
		return nil
	}
	g, ok := a.c.Grid(well.ChildGridID)
	if !ok {
		return nil
	}
	ps := paneToDragdrop(p, r)
	ratio := zoomtrans.EffectiveViewZoom(well.ViewZoom, zoomtrans.DefaultWellViewZoom)
	cp := dragdrop.ChildPreviewFor(ps, struct {
		X, Y, W, H, ViewX, ViewY int64
	}{X: well.X, Y: well.Y, W: well.W, H: well.H,
		ViewX: well.ViewX, ViewY: well.ViewY},
		ratio)
	// Which child cell does the cursor sit in? FloorCellAt floors toward
	// -inf (math.Floor), the correct hit-test answer in a well's negative
	// quadrant — int64() truncates toward zero and would mis-target there.
	cellX, cellY := dragdrop.FloorCellAt(cp.OriginX, cp.OriginY, cp.CellPx, sx, sy)
	for _, n := range g.Tiles {
		if dragdrop.TileContainsCell(n.X, n.Y, n.W, n.H, cellX, cellY) {
			return tileCopy(&n)
		}
	}
	return nil
}
