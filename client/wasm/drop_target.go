//go:build js && wasm

package main

import (
	"slices"

	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/zoomtrans"
	"github.com/josephburnett/gridwell/internal/rpc"
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
	rect     pane.Rect
	gridID   int64
	path     []int64
	cellSize float64
	originX  float64
	originY  float64
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
func (a *App) dropTargetAt(sx, sy float64, excludeTileID int64) (*dropTarget, bool) {
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil, false
	}
	if p.TextFocus != 0 {
		return nil, false
	}
	parentCell := cellPx * p.Zoom

	// Parent-grid origin (top-left of cell (0, 0) in screen coords).
	ps := paneToDragdrop(p, r)
	parentOriginX, parentOriginY := ps.CellToScreen(0, 0)

	// Look for an open well under the cursor — that promotes the
	// target to the well's child grid. Skip the promotion when the
	// well *is* the source being dragged; that would be a drop into
	// self and creates a parent/child cycle on the server.
	cellX, cellY := cellAtScreen(p, r, sx, sy)
	if n := a.tileAtCell(p, cellX, cellY); n != nil &&
		rpc.IsWellKind(n.Kind) &&
		n.ChildGridID != 0 &&
		n.ID != excludeTileID {
		// Well preview math. Effective ratio resolves the unvisited
		// fallback in one place so the child cell size is computed
		// the same way here as in the renderer.
		ratio := zoomtrans.EffectiveViewZoom(n.ViewZoom, zoomtrans.DefaultWellViewZoom)
		cp := dragdrop.ChildPreviewFor(ps, struct {
			X, Y, W, H, ViewX, ViewY int64
		}{X: n.X, Y: n.Y, W: n.W, H: n.H, ViewX: n.ViewX, ViewY: n.ViewY},
			ratio)
		path := append(slices.Clone(p.Path), n.ID)
		return &dropTarget{
			pane:     p,
			rect:     r,
			gridID:   n.ChildGridID,
			path:     path,
			cellSize: cp.CellPx,
			originX:  cp.OriginX,
			originY:  cp.OriginY,
		}, true
	}

	// Parent-grid drop.
	return &dropTarget{
		pane:     p,
		rect:     r,
		gridID:   a.gridIDForPath(p.Path),
		path:     slices.Clone(p.Path),
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
func (a *App) gridSourceKind(gridID int64) string {
	g, ok := a.c.Grid(gridID)
	if !ok {
		return ""
	}
	return g.Meta.SourceKind
}

// dropForbiddenForMove reports whether a left-drag (move) gesture from
// the dragState's source grid to t's destination grid would be rejected
// by the server. Today the server refuses any cross-grid move whose
// endpoints have different source kinds — a source-grid tile can't
// migrate into a regular grid (right-drag clones it instead), and
// regular tiles can't move into a source-backed grid (host directories
// aren't a placement medium). The UI flags those gestures so the cursor
// and the ghost render "no entry" instead of inviting the failed drop.
//
// Same-grid drops are always allowed by this check (positional moves
// inside one grid never cross the source/regular boundary).
func (a *App) dropForbiddenForMove(d *dragState, t *dropTarget) bool {
	if d == nil || t == nil {
		return false
	}
	return dragdrop.MoveForbidden(
		d.srcGridID == t.gridID,
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
	if !rpc.IsWellKind(well.Kind) || well.ChildGridID == 0 {
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
	for _, n := range g.Tiles {
		if dragdrop.TileContainsCell(n.X, n.Y, n.W, n.H, cellX, cellY) {
			return tileCopy(&n)
		}
	}
	return nil
}
