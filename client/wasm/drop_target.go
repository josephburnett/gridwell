//go:build js && wasm

package main

import (
	"math"

	"github.com/josephburnett/ascent/client/dragdrop"
	"github.com/josephburnett/ascent/client/pane"
	"github.com/josephburnett/ascent/client/zoomtrans"
	"github.com/josephburnett/ascent/internal/rpc"
)

// dropTarget describes where the cursor is currently pointing as a
// destination for a drag-and-drop. Two flavors:
//
//   - parent-grid drop: inWell is nil; gridID is the focused leaf
//     grid of `pane`, cellSize is the parent cell size.
//   - well-child-grid drop: inWell is the open well that contains the
//     cursor; gridID is well.ChildGridID, cellSize is the preview
//     cell size, and the path/viewRect address the well's interior.
//
// origin{X,Y} is the screen coordinate of cell (0, 0) in the target
// grid — combined with cellSize and the cursor it's enough to compute
// the target cell at the cursor.
type dropTarget struct {
	pane     *pane.Pane
	paneRect paneRect
	inWell   *rpc.Node
	gridID   int64
	path     []int64
	cellSize float64
	originX  float64
	originY  float64
	viewRect rpc.ViewRect
}

// dropTargetAt resolves the cursor at (sx, sy) to a drop target.
// Returns false when the cursor is over a file-mode pane or off-canvas
// — neither is a valid drop destination.
//
// excludeNodeID, if non-zero, prevents a well at that row id from
// being treated as a drop-into-well target — used so a user dragging
// well X around can't accidentally drop X into its own child grid
// when the cursor is still on top of X. Pass d.nodeID from the
// dragState; it's a safe no-op when the source isn't a well.
func (a *App) dropTargetAt(sx, sy float64, excludeNodeID int64) (*dropTarget, bool) {
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil, false
	}
	if p.FileFocus != 0 {
		return nil, false
	}
	parentCell := cellPx * p.Zoom

	// Parent-grid origin (top-left of cell (0, 0) in screen coords).
	ps := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	parentOriginX, parentOriginY := ps.CellToScreen(0, 0)

	// Look for an open well under the cursor — that promotes the
	// target to the well's child grid. Skip the promotion when the
	// well *is* the source being dragged; that would be a drop into
	// self and creates a parent/child cycle on the server.
	cellX, cellY := cellAtScreen(p, r, sx, sy)
	if n := a.nodeAtCell(p, cellX, cellY); n != nil &&
		n.Type == "well" && !n.Capped && n.ChildGridID != 0 &&
		n.ID != excludeNodeID {
		// Well preview math.
		cp := dragdrop.ChildPreviewFor(ps, struct {
			X, Y, W, H, ViewX, ViewY int64
			ViewZoom                 float64
		}{X: n.X, Y: n.Y, W: n.W, H: n.H, ViewX: n.ViewX, ViewY: n.ViewY, ViewZoom: n.ViewZoom},
			zoomtrans.PreviewFactor)
		path := append(append([]int64(nil), p.Path...), n.ID)
		return &dropTarget{
			pane:     p,
			paneRect: r,
			inWell:   nodeCopy(n),
			gridID:   n.ChildGridID,
			path:     path,
			cellSize: cp.CellPx,
			originX:  cp.OriginX,
			originY:  cp.OriginY,
			viewRect: wellChildViewRect(n),
		}, true
	}

	// Parent-grid drop.
	return &dropTarget{
		pane:     p,
		paneRect: r,
		gridID:   a.gridIDForPath(p.Path),
		path:     append([]int64(nil), p.Path...),
		cellSize: parentCell,
		originX:  parentOriginX,
		originY:  parentOriginY,
		viewRect: a.paneViewRect(p, ps),
	}, true
}

// nodeCopy returns a copy of *n owned by the caller — the cache may
// rewrite its node map underneath us, so callers that retain a node
// across event boundaries should hold their own copy.
func nodeCopy(n *rpc.Node) *rpc.Node {
	c := *n
	return &c
}

// wellChildViewRect returns the visible region of a well's child grid
// in *child cell* coordinates, suitable for the server's locality
// check on cross-grid moves into / out of the well. The well stores
// W, H in parent cells; the visible child region is W*PreviewFactor
// cells wide centered on (ViewX + W/2, ViewY + H/2). A small ±1 pad
// matches paneViewRect's convention.
func wellChildViewRect(well *rpc.Node) rpc.ViewRect {
	pf := int64(zoomtrans.PreviewFactor)
	visW := well.W * pf
	visH := well.H * pf
	centerX := float64(well.ViewX) + float64(well.W)/2
	centerY := float64(well.ViewY) + float64(well.H)/2
	leftX := int64(math.Floor(centerX - float64(visW)/2))
	topY := int64(math.Floor(centerY - float64(visH)/2))
	return rpc.ViewRect{
		X: leftX - 1,
		Y: topY - 1,
		W: visW + 3,
		H: visH + 3,
	}
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
func (a *App) childTileAtScreen(p *pane.Pane, r paneRect, well *rpc.Node, sx, sy float64) *rpc.Node {
	if well.Type != "well" || well.Capped || well.ChildGridID == 0 {
		return nil
	}
	g, ok := a.c.Grid(well.ChildGridID)
	if !ok {
		return nil
	}
	ps := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	cp := dragdrop.ChildPreviewFor(ps, struct {
		X, Y, W, H, ViewX, ViewY int64
		ViewZoom                 float64
	}{X: well.X, Y: well.Y, W: well.W, H: well.H,
		ViewX: well.ViewX, ViewY: well.ViewY, ViewZoom: well.ViewZoom},
		zoomtrans.PreviewFactor)
	cxF, cyF := cp.ChildCellAtScreen(sx, sy)
	// Floor (which cell does the cursor sit in?), handling negatives.
	cellX := int64(cxF)
	if cxF < 0 && float64(cellX) != cxF {
		cellX--
	}
	cellY := int64(cyF)
	if cyF < 0 && float64(cellY) != cyF {
		cellY--
	}
	for _, n := range g.Nodes {
		if dragdrop.NodeContainsCell(n.X, n.Y, n.W, n.H, cellX, cellY) {
			return nodeCopy(&n)
		}
	}
	return nil
}
