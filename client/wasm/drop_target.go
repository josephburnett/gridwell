//go:build js && wasm

package main

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"google.golang.org/protobuf/proto"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/traceevent"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// dropTarget is where the cursor points as a drop destination: either the
// pane's leaf grid or, when the cursor promoted into an open well, that
// well's child grid. origin{X,Y} is the screen coordinate of cell (0, 0) in
// that grid, so cellSize and the cursor give the target cell.
type dropTarget struct {
	pane     *pane.Pane
	gridID   string
	cellSize float64
	originX  float64
	originY  float64
}

// commitVerdict is the verdict of a release that commits, recorded once. The
// ghost's preview asks dragdrop.DecideDrop directly, being per pointer move.
func (a *App) commitVerdict(in dragdrop.DropInput, d *dragState, t *dropTarget) dragdrop.DropAction {
	v := dragdrop.DecideDrop(in)
	gridID := ""
	if t != nil {
		gridID = t.gridID
	}
	a.emit(traceevent.Drop(v, d.tileID, gridID))
	return v
}

// dropInputAt gathers every world-read a drop decision needs. The left
// commit, the right commit and the ghost preview all read it, so the three
// cannot disagree about the world they decide on. The flavor is d.intent, set
// by the press: Forbidden is move-only, because no creation is forbidden, and
// Occupied excludes the dragged tile on a move but nothing on a creation.
//
// placement asks for the drop cell too. The preview passes false, so it shows
// the snap-to-cell even over an occupied cell, while the commit does the
// authoritative overlap check. The two always share the action class.
func (a *App) dropInputAt(d *dragState, sx, sy float64, placement bool) (
	in dragdrop.DropInput, t *dropTarget, dropX, dropY int64) {

	in = dragdrop.DropInput{
		Started:       d.started,
		OriginFocused: d.originFocused,
		SplitNav:      d.splitNav,
		IsTemplate:    d.isTemplate,
		Intent:        d.intent,
		TileID:        d.tileID,
		OverDelete:    a.overDeleteButton(d, sx, sy),
	}
	t, in.HasTarget = a.dropTargetAt(sx, sy, d.tileID)
	if !in.HasTarget {
		return in, t, 0, 0
	}
	// Unknown is not read-only: a drop must not be refused because the
	// target grid's fetch has not landed.
	targetWritable, targetKnown := a.gridWritable(t.gridID)
	in.TargetReadOnly = targetKnown && !targetWritable
	in.SameGrid = t.gridID == d.srcGridID
	in.CrossPlugin = dropCrossNamespace(d, t)
	if !d.intent.Creates() {
		in.Forbidden = a.dropForbiddenForMove(d, t)
	}
	if !placement {
		return in, t, 0, 0
	}
	dropX, dropY = t.cellAtCursor(sx, sy, d.cellOffsetX, d.cellOffsetY)
	in.SameCell = in.SameGrid && dropX == d.snapshotTile.X && dropY == d.snapshotTile.Y
	exclude := d.tileID
	if d.intent.Creates() {
		exclude = ""
	}
	in.Occupied = a.occupiedForDrop(t.gridID, dropX, dropY,
		d.snapshotTile.W, d.snapshotTile.H, exclude)
	return in, t, dropX, dropY
}

// previewDrop updates the active ghost from the same DecideDrop verdict the
// commit uses, so a previewed action cannot diverge from the committed one.
// The flavor is d.intent, the same field the commit reads.
func (a *App) previewDrop(d *dragState, sx, sy float64) {
	if a.ghost == nil {
		return
	}
	in, t, _, _ := a.dropInputAt(d, sx, sy, false /* placement */)

	// The verdict picks the action and GhostPlanForDrop the styling, both in
	// client/dragdrop. The ghost rests in a different pane per verdict, so
	// feed all three candidate pane ids and sizes.
	var targetPaneID string
	var targetCellSize float64
	if t != nil {
		targetPaneID = t.pane.ID
		targetCellSize = t.cellSize
	}
	plan := dragdrop.GhostPlanForDrop(dragdrop.DecideDrop(in), in.Forbidden,
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

// dropTargetAt resolves the cursor to a drop target, false over a content
// descent or off-canvas. excludeTileID keeps a well at that row id from being
// a drop-into-well target, so dragging well X cannot drop X into its own
// child grid.
func (a *App) dropTargetAt(sx, sy float64, excludeTileID string) (*dropTarget, bool) {
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil, false
	}
	if p.ContentID() != "" {
		return nil, false
	}
	parentCell := cellPx * p.Zoom

	ps := paneToDragdrop(p, r)
	parentOriginX, parentOriginY := ps.CellToScreen(0, 0)

	// An open well under the cursor promotes the target to its child grid.
	// The rule is dragdrop.PromoteToWell's.
	cellX, cellY := cellAtScreen(p, r, sx, sy)
	if n := a.tileAtCell(p, cellX, cellY); n != nil &&
		dragdrop.PromoteToWell(rpc.IsWellKind(n.Kind), n.ChildGridId, n.Id, excludeTileID) {
		cp := wellPreviewFor(ps, n)
		return &dropTarget{
			pane:     p,
			gridID:   n.ChildGridId,
			cellSize: cp.CellPx,
			originX:  cp.OriginX,
			originY:  cp.OriginY,
		}, true
	}

	return &dropTarget{
		pane:     p,
		gridID:   a.gridIDForPane(p),
		cellSize: parentCell,
		originX:  parentOriginX,
		originY:  parentOriginY,
	}, true
}

// gridHostContent reports the grid's declared host_content, false for a
// Gridwell-owned or unknown grid.
func (a *App) gridHostContent(gridID string) bool {
	g, _ := a.c.Grid(gridID)
	return g.HostContent()
}

// dropCrossNamespace reports whether the source and destination grids live
// in different id namespaces, which is what makes a left-drag a link and
// refuses a solid well's right-drag. The one reader of rpc.NamespaceOf here,
// so nothing else can disagree about what cross-plugin means.
func dropCrossNamespace(d *dragState, t *dropTarget) bool {
	if d == nil || t == nil {
		return false
	}
	return rpc.NamespaceOf(d.srcGridID) != rpc.NamespaceOf(t.gridID)
}

// dropForbiddenForMove reports a left-drag rejected up front: a
// same-namespace cross-grid move with a host-content endpoint, since host mv
// is unimplemented and host directories are not a placement medium. A
// cross-namespace left-drag verdicts DropLink and is exempt; a read-only
// destination is the separate TargetReadOnly gate.
func (a *App) dropForbiddenForMove(d *dragState, t *dropTarget) bool {
	if d == nil || t == nil {
		return false
	}
	return dragdrop.MoveForbidden(
		d.srcGridID == t.gridID,
		dropCrossNamespace(d, t),
		a.gridHostContent(d.srcGridID),
		a.gridHostContent(t.gridID),
	)
}

// cellAtCursor returns the cell coord at the cursor, offset by the grab
// point on the source tile so the snap matches it.
func (t *dropTarget) cellAtCursor(sx, sy, cellOffsetX, cellOffsetY float64) (int64, int64) {
	cx := (sx-t.originX)/t.cellSize - cellOffsetX
	cy := (sy-t.originY)/t.cellSize - cellOffsetY
	return dragdrop.SnapToCell(cx), dragdrop.SnapToCell(cy)
}

// childTileAtScreen returns the preview tile under the cursor inside well's
// child preview, which is what decides whether a click on a well starts a
// pull-out gesture.
func (a *App) childTileAtScreen(p *pane.Pane, r pane.Rect, well *gridwellv1.Tile, sx, sy float64) *gridwellv1.Tile {
	if !rpc.IsWellKind(well.Kind) || well.ChildGridId == "" {
		return nil
	}
	g, ok := a.c.Grid(well.ChildGridId)
	if !ok {
		return nil
	}
	cp := wellPreviewFor(paneToDragdrop(p, r), well)
	// FloorCellAt floors toward -inf, the correct hit-test answer in a
	// well's negative quadrant, where int64() truncation would mis-target.
	cellX, cellY := dragdrop.FloorCellAt(cp.OriginX, cp.OriginY, cp.CellPx, sx, sy)
	for _, n := range g.Tiles {
		if dragdrop.TileContainsCell(n.X, n.Y, n.W, n.H, cellX, cellY) {
			// Caller-owned, because the pull-out gesture outlives the event
			// this row was read in and the cache may rewrite its map.
			return proto.CloneOf(n)
		}
	}
	return nil
}

// wellPreviewFor is the one way a well's stored framing becomes a child
// preview transform, with both halves resolved through zoomtrans' unvisited
// sentinel, so the drop target, the pull-out hit test and the renderer place
// a never-visited well's preview at the same pixels.
func wellPreviewFor(ps dragdrop.Pane, n *gridwellv1.Tile) dragdrop.ChildPreview {
	cx, cy := zoomtrans.EffectiveCenter(wellOf(n))
	return dragdrop.ChildPreviewFor(ps, struct {
		X, Y, W, H     int64
		ViewCx, ViewCy float64
	}{X: n.X, Y: n.Y, W: n.W, H: n.H, ViewCx: cx, ViewCy: cy},
		zoomtrans.EffectiveViewZoom(n.ViewZoom, zoomtrans.DefaultWellViewZoom))
}

// wellOf forwards to zoomtrans.WellOf. The local name is for the renderer's
// many call sites.
func wellOf(n *gridwellv1.Tile) zoomtrans.Well { return zoomtrans.WellOf(n) }
