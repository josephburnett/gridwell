//go:build js && wasm

package main

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/dragdrop"
)

// Commits a left-button drag and animates the ghost. finishLeftDrag is the
// one commit path, so the mouseup the handler saw and the release
// recoverLostRelease infers cannot land two ways. The verdict is
// dragdrop.DecideDrop's; commitRightClone is the right button's twin and
// shares the landing and snap-back below.

// Animation durations in milliseconds.
const (
	snapMs     = 110.0
	snapBackMs = 220.0
)

// finishLeftDrag commits the armed left-button drag at the release point.
// Reports whether it consumed the drag.
func (a *App) finishLeftDrag(sx, sy float64) bool {
	if a.dragging == nil {
		return false
	}
	// A right-button drag commits only through finishRightDrag, which clears
	// a.dragging first, so one still armed here means another button came up
	// mid-drag. Leave it armed rather than commit the gesture as a move.
	if a.dragging.intent.Creates() {
		return false
	}
	d := a.dragging
	a.dragging = nil
	// Reset any drag-time cursor change.
	a.canvas.Get("style").Set("cursor", "")

	// A palette swatch released without a drag is the menu's gesture, not the
	// verdict's bare-click arm, which is the canvas's. The popover floats
	// over a live pane, so falling through would act on the tile behind the
	// swatch.
	if d.isTemplate && !d.started {
		a.clickTemplate(d)
		return true
	}

	// Snapshot every world-read the decision needs, once. onMouseMove gathers
	// the same DropInput for the ghost preview, so preview and commit cannot
	// diverge.
	in, t, dropX, dropY := a.dropInputAt(d, sx, sy, true /* placement */)

	verdict := dragdrop.DecideDrop(in)
	switch verdict {
	case dragdrop.DropFocusOnly:
		// Focus already moved at mousedown.
		a.draw()
		return true

	case dragdrop.DropNavigate, dragdrop.DropNavigateSplit:
		// Bare click on an already-focused pane. The split flavor is the
		// same click with ctrl held at press.
		focused := a.tree.FindPane(d.originPaneID)
		if focused == nil {
			a.draw()
			return true
		}
		r := paneRectFor(a, focused)
		// Descent and ascent come first; selection is what is left over.
		if a.attemptDescentOrAscent(focused, r, sx, sy,
			verdict == dragdrop.DropNavigateSplit) {
			a.scheduleURLUpdate()
			return true
		}
		cellX, cellY := cellAtScreen(focused, r, sx, sy)
		if n := a.tileAtCell(focused, cellX, cellY); n != nil {
			a.local(focused.ID).Selected = n.Id
		} else {
			a.clearSelected(focused.ID)
		}
		a.draw()
		a.scheduleURLUpdate()
		return true

	case dragdrop.DropCreateTemplate:
		// The target and the cell are the verdict's own, gathered above, so
		// the create cannot land somewhere the verdict did not allow.
		a.commitTemplateDrop(d, t, dropX, dropY)
		return true

	case dragdrop.DropPanEnd:
		// The URL now; the grid framing through the draw()-armed settle
		// persister.
		a.scheduleURLUpdate()
		a.draw()
		return true

	case dragdrop.DropDelete:
		// The trashcan resolves against the bar button, not the grid under
		// the cursor, so it works wherever the cursor happens to be.
		a.runDeleteTile(d, nil)
		a.ghost = nil
		a.draw()
		return true

	case dragdrop.DropRejected:
		// Snap back without a doomed round trip.
		a.cancelDragSnapBack(d)
		return true

	case dragdrop.DropLink:
		// A cross-namespace left-drag links, because there is no
		// cross-plugin move.
		if a.ghost != nil {
			// The source was hidden for a would-be move, but it stays.
			a.ghost.hiddenTileID = ""
			a.ghost.hiddenPaneID = ""
		}
		a.landGhostAtCell(t, dropX, dropY)
		a.commitLinkDrop(d, t, dropX, dropY)
		a.draw()
		return true
	}

	// DropMove.
	a.landGhostAtCell(t, dropX, dropY)

	dstGridID := t.gridID
	srcGridID := d.srcGridID

	// PlaceTile is the one placement writeback: an id plus the full (grid, x,
	// y, w, h) fact, with no descent path and no version claim, since
	// placement is layout and last-writer-wins.
	req := &gridwellv1.PlaceTileRequest{
		TileId: d.tileID,
		GridId: dstGridID,
		X:      dropX,
		Y:      dropY,
		W:      d.snapshotTile.W,
		H:      d.snapshotTile.H,
	}
	// A drag carries no parked value: snapping the ghost back to its origin
	// is the reconcile the user can see.
	a.post(write{
		label: "PlaceTile", gid: srcGridID, alsoGID: dstGridID, refetchOnOK: true,
		call: func(ctx context.Context) error {
			_, err := a.cl.PlaceTile(ctx, req)
			return err
		},
		undo: func() { a.snapBackToOrigin(d) },
	})
	a.draw()
	return true
}

// commitLinkDrop creates the link a DropLink verdict asks for: an exit well
// for a dragged well, a leaf link otherwise. A link to a link names the
// content, so links never chain through middleman tiles. Ids are qualified in
// every namespace, so one request shape links inside one and across one.
func (a *App) commitLinkDrop(d *dragState, t *dropTarget, dropX, dropY int64) {
	src := d.snapshotTile
	dstGridID := t.gridID
	if rpc.IsWellKind(src.Kind) {
		a.createTile("CreateWell", dstGridID, &gridwellv1.CreateTileRequest{GridId: dstGridID,
			Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: dropX, Y: dropY, W: src.W, H: src.H,
				ChildGridId: src.ChildGridId, AltText: src.AltText,
				ViewCx: src.ViewCx, ViewCy: src.ViewCy, ViewZoom: src.ViewZoom}}, nil)
		return
	}
	// The same read-through every content operation takes.
	target := rpc.ContentID(src)
	a.createTile("CreateLeafLink", dstGridID, &gridwellv1.CreateTileRequest{GridId: dstGridID,
		Tile: &gridwellv1.Tile{Kind: src.Kind, X: dropX, Y: dropY, W: src.W, H: src.H,
			LinkTargetId: target, AltText: src.AltText}}, nil)
}

// occupiedForDrop reports whether the dropped footprint overlaps a cached
// tile other than excludeID. A move passes the dragged tile's own id,
// mirroring the server's PlaceTile self-exclusion; a clone passes "", because
// the source tile is a real neighbor there.
func (a *App) occupiedForDrop(gridID string, x, y, w, h int64, excludeID string) bool {
	g, ok := a.c.Grid(gridID)
	if !ok {
		return false
	}
	for _, n := range g.Tiles {
		if n.Id == excludeID {
			continue
		}
		if dragdrop.RectsOverlap(n.X, n.Y, n.W, n.H, x, y, w, h) {
			return true
		}
	}
	return false
}

// landGhostAtCell lands the ghost on the drop target's cell. Move, link and
// clone all use it, so the three cannot place the same drop differently.
func (a *App) landGhostAtCell(t *dropTarget, dropX, dropY int64) {
	a.landGhost(t.pane.ID, t.cellSize,
		t.originX+float64(dropX)*t.cellSize, t.originY+float64(dropY)*t.cellSize)
}

// landGhost snaps the ghost to the screen cell (toX, toY) as a tile of
// paneID.
func (a *App) landGhost(paneID string, cellSize, toX, toY float64) {
	if a.ghost != nil {
		a.ghost.paneID = paneID
		if cellSize > 0 {
			a.ghost.targetCellSize = cellSize
		}
	}
	a.startSnap(toX, toY, snapMs)
}

// startSnap animates the active ghost to (toX, toY), replacing any prior
// animation.
func (a *App) startSnap(toX, toY, duration float64) {
	if a.ghost == nil {
		return
	}
	a.animation = &anim.Animation{
		FromX:      a.ghost.screenX,
		FromY:      a.ghost.screenY,
		ToX:        toX,
		ToY:        toY,
		StartMs:    nowMs(),
		DurationMs: duration,
	}
	a.scheduleFrame()
}

// cancelDragSnapBack runs the snap-back when a drop is abandoned.
func (a *App) cancelDragSnapBack(d *dragState) {
	if a.ghost == nil {
		// The drag never crossed the threshold.
		a.draw()
		return
	}
	a.snapBackToOrigin(d)
}

// snapBackToOrigin animates the ghost back to the dragged tile's original
// location. Size returns separately: each frame lerps displayedCellSize
// toward srcCellSize.
func (a *App) snapBackToOrigin(d *dragState) {
	if a.ghost == nil {
		return
	}
	a.ghost.paneID = d.originPaneID
	if d.srcCellSize > 0 {
		a.ghost.targetCellSize = d.srcCellSize
	}
	a.animation = &anim.Animation{
		FromX:      a.ghost.screenX,
		FromY:      a.ghost.screenY,
		ToX:        d.originScreenX,
		ToY:        d.originScreenY,
		StartMs:    nowMs(),
		DurationMs: snapBackMs,
	}
	a.scheduleFrame()
}
