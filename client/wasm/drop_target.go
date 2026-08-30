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

// dropInputAt gathers, at the cursor, every world-read a drop decision needs:
// one resolution of the drop target and one dragdrop.DropInput built from it,
// read by the left commit (onMouseUp), the right commit (commitRightClone)
// and the ghost preview (previewDrop) alike, so the three cannot disagree
// about the world they decide on.
//
// clone picks the gesture flavor, and the two flavors differ deliberately:
//
//   - Forbidden is a move-only input (dropForbiddenForMove): no clone is
//     forbidden — a solid well deep-copies and a link copies as a link — so
//     the clone flavor leaves it false and DecideDrop's forbidden branch
//     never fires for a right-drag.
//   - Occupied excludes the dragged tile itself on a move, mirroring the
//     server's PlaceTile self-exclusion, and excludes nothing on a clone,
//     where the source tile is a real neighbor the copy must not land on.
//
// SameGrid is fed on both flavors: DecideDrop reads it only through
// `TargetReadOnly && !(SameGrid && !Clone)`, where Clone already decides the
// branch, so a clone's SameGrid cannot change a verdict.
//
// placement asks for the drop cell as well — SameCell, Occupied, and the
// returned (dropX, dropY). The preview passes false: it is optimistic about
// placement and shows the snap-to-cell even over an occupied cell, while the
// commit does the authoritative overlap check and snaps back. What the two
// always share is the action class: delete, link, place, or reject.
func (a *App) dropInputAt(d *dragState, sx, sy float64, clone, placement bool) (
	in dragdrop.DropInput, t *dropTarget, dropX, dropY int64) {

	in = dragdrop.DropInput{
		Started:       d.started,
		OriginFocused: d.originFocused,
		IsTemplate:    d.isTemplate,
		Clone:         clone,
		TileID:        d.tileID,
		OverDelete:    a.overDeleteButton(d, sx, sy),
	}
	t, in.HasTarget = a.dropTargetAt(sx, sy, d.tileID)
	if !in.HasTarget {
		return in, t, 0, 0
	}
	in.TargetReadOnly = a.gridKnownReadOnly(t.gridID)
	in.SameGrid = t.gridID == d.srcGridID
	in.CrossPlugin = dropCrossNamespace(d, t)
	if !clone {
		in.Forbidden = a.dropForbiddenForMove(d, t)
	}
	if !placement {
		return in, t, 0, 0
	}
	dropX, dropY = t.cellAtCursor(sx, sy, d.cellOffsetX, d.cellOffsetY)
	in.SameCell = in.SameGrid && dropX == d.snapshotTile.X && dropY == d.snapshotTile.Y
	exclude := d.tileID
	if clone {
		exclude = ""
	}
	in.Occupied = a.occupiedForDrop(t.gridID, dropX, dropY,
		d.snapshotTile.W, d.snapshotTile.H, exclude)
	return in, t, dropX, dropY
}

// previewDrop updates the active ghost for an in-flight tile drag from the
// same dragdrop.DecideDrop verdict the commit path uses, so a previewed
// action cannot diverge from the committed one. clone picks the right-drag
// flavor: Clone=true, Forbidden never, and no no-entry badge on a read-only
// doc. The left flavor feeds MoveForbidden and, across a namespace, previews
// the link (dashed ghost plus chain badge — the teaching signal).
func (a *App) previewDrop(d *dragState, sx, sy float64, clone bool) {
	if a.ghost == nil {
		return
	}
	in, t, _, _ := a.dropInputAt(d, sx, sy, clone, false /* placement */)

	// The verdict picks the action; GhostPlanForDrop picks the styling. Both
	// are pure and tested in client/dragdrop, so preview, commit, and the
	// styling cannot drift. The ghost rests in a different pane per verdict,
	// so feed all three candidate pane ids and sizes.
	var targetPaneID string
	var targetCellSize float64
	if t != nil {
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

// dropTargetAt resolves the cursor at (sx, sy) to a drop target. Returns
// false when the cursor is over a content descent or off-canvas: neither is a
// valid drop destination.
//
// excludeTileID, when set, prevents a well at that row id from being treated
// as a drop-into-well target, so dragging well X cannot drop X into its own
// child grid while the cursor is still on top of X. Pass d.tileID from the
// dragState; it is a safe no-op when the source is not a well.
func (a *App) dropTargetAt(sx, sy float64, excludeTileID string) (*dropTarget, bool) {
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil, false
	}
	if p.ContentID() != "" {
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
		cp := wellPreviewFor(ps, n)
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

// tileCopy returns a copy of *n owned by the caller. The cache may rewrite
// its tile map underneath, so a caller retaining a tile across event
// boundaries holds its own copy.
func tileCopy(n *rpc.Tile) *rpc.Tile {
	c := *n
	return &c
}

// gridSourceKind returns the grid's source kind (fs or proc), or "" for a
// Gridwell-owned grid or an unknown grid id. It wraps the cache lookup so
// callers compare SourceKind directly.
func (a *App) gridSourceKind(gridID string) string {
	g, ok := a.c.Grid(gridID)
	if !ok {
		return ""
	}
	return g.Meta.SourceKind
}

// dropCrossNamespace reports whether the drag's source grid and t's
// destination grid live in different id namespaces — the one predicate the
// cross-plugin gestures branch on: a left-drag becomes a link, and a solid
// well's right-drag is refused. One reader of NamespaceOf for all three
// gather sites (left preview, left commit, right commit), so they cannot
// disagree.
func dropCrossNamespace(d *dragState, t *dropTarget) bool {
	if d == nil || t == nil {
		return false
	}
	return rpc.NamespaceOf(d.srcGridID) != rpc.NamespaceOf(t.gridID)
}

// dropForbiddenForMove reports whether a left-drag from the dragState's
// source grid to t's destination grid is rejected up front: a same-namespace
// cross-grid move with a source-backed endpoint, since host mv is
// unimplemented and host directories are not a placement medium. A
// cross-namespace left-drag is never a move — it verdicts DropLink — so it is
// exempt here, and the read-only destination case is the separate
// TargetReadOnly gate. The UI flags the forbidden gestures so the cursor and
// the ghost render "no entry" instead of inviting a drop that fails.
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
	cp := wellPreviewFor(paneToDragdrop(p, r), well)
	// Which child cell does the cursor sit in? FloorCellAt floors toward
	// -inf (math.Floor), the correct hit-test answer in a well's negative
	// quadrant; int64() truncates toward zero and would mis-target there.
	cellX, cellY := dragdrop.FloorCellAt(cp.OriginX, cp.OriginY, cp.CellPx, sx, sy)
	for _, n := range g.Tiles {
		if dragdrop.TileContainsCell(n.X, n.Y, n.W, n.H, cellX, cellY) {
			return tileCopy(&n)
		}
	}
	return nil
}

// wellPreviewFor is the one way a well's stored framing becomes a child
// preview transform: both halves of the framing resolved through zoomtrans'
// unvisited sentinel — the ratio (EffectiveViewZoom) and the center
// (EffectiveCenter) — so the drop target, the pull-out-of-well hit test, and
// the renderer place a never-visited well's preview at the same pixels
// instead of each remembering the fallback for itself.
func wellPreviewFor(ps dragdrop.Pane, n *rpc.Tile) dragdrop.ChildPreview {
	cx, cy := zoomtrans.EffectiveCenter(wellOf(n))
	return dragdrop.ChildPreviewFor(ps, struct {
		X, Y, W, H     int64
		ViewCx, ViewCy float64
	}{X: n.X, Y: n.Y, W: n.W, H: n.H, ViewCx: cx, ViewCy: cy},
		zoomtrans.EffectiveViewZoom(n.ViewZoom, zoomtrans.DefaultWellViewZoom))
}

// wellOf reads a tile row as the doorway zoomtrans reasons about: its
// footprint in the parent grid plus the framing it was left at.
func wellOf(n *rpc.Tile) zoomtrans.Well {
	return zoomtrans.Well{
		ID: n.ID, X: n.X, Y: n.Y, W: n.W, H: n.H,
		ViewCx: n.ViewCx, ViewCy: n.ViewCy, ViewZoom: n.ViewZoom,
	}
}
