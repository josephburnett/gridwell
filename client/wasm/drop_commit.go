//go:build js && wasm

package main

import (
	"context"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/dragdrop"
)

// This file commits a left-button drag and animates the ghost that shows
// it. finishLeftDrag is the one commit path: both the mouseup the handler
// saw and the release recoverLostRelease infers resolve through it, so a
// drag cannot land two ways. The verdict is dragdrop.DecideDrop's over the
// gather in drop_target.go; this file only executes it. The right button's
// twin is commitRightClone in right_button.go, and it shares the landing
// and snap-back below so the two cannot place the same drop differently.

// Animation durations in milliseconds. Tuned for "stone settling" feel.
const (
	snapMs     = 110.0
	snapBackMs = 220.0
)

// finishLeftDrag commits the armed left-button drag at the release point: the
// move, the pan, the palette template, or the bar's promote drag. It is the
// left drag's own commit path, the twin of finishRightDrag and
// finishLeftResize, so both ways a left drag can end — the mouseup we saw, and
// the one recoverLostRelease infers from a button that is already up — resolve
// through exactly the same code. It reports whether it consumed the drag.
func (a *App) finishLeftDrag(sx, sy float64) bool {
	if a.dragging == nil {
		return false
	}
	// A right-button drag — a copy or a link — commits only through the
	// right-button release path (finishRightDrag → commitRightClone), which
	// clears a.dragging before reaching here. Reaching the move-commit with
	// one still armed means a non-right button came up mid-drag (e.g. the
	// user pressed and released the left button while right-dragging).
	// Ignore it so the gesture is never silently committed as a move — it
	// stays armed and the eventual right-button release still creates.
	if a.dragging.intent.Creates() {
		return false
	}
	d := a.dragging
	a.dragging = nil
	// Reset any drag-time cursor change (e.g. "not-allowed" from
	// hovering a doc with the left button).
	a.canvas.Get("style").Set("cursor", "")

	// A palette swatch released without a drag is the MENU's gesture, and it
	// is answered here, by the one table, never below. The drop verdict's
	// bare-click arm is the canvas's gesture, at the canvas's coordinates —
	// and the popover floats over a live pane, so a swatch click that fell
	// through to it descended into, or selected, whatever tile happened to sit
	// behind the swatch.
	if d.isTemplate && !d.started {
		a.clickTemplate(d)
		return true
	}

	// Snapshot every world-read the drop decision needs, once, using the
	// local d, since a.dragging is already nil above. DecideDrop then picks
	// the action and the switch executes the side effects. onMouseMove
	// gathers the same DropInput for the ghost preview, so preview and commit
	// cannot diverge. The flavor is d.intent, which is IntentMove here: a
	// right-button drag commits via the right path above.
	in, t, dropX, dropY := a.dropInputAt(d, sx, sy, true /* placement */)

	verdict := dragdrop.DecideDrop(in)
	switch verdict {
	case dragdrop.DropFocusOnly:
		// The click's only job was moving focus (done at mousedown).
		a.draw()
		return true

	case dragdrop.DropNavigate, dragdrop.DropNavigateSplit:
		// Bare click (no movement) on an already-focused pane: navigation.
		// The split flavor is the same click with ctrl held at press: the
		// descent lands in a new pane below; everything else is identical.
		focused := a.tree.FindPane(d.originPaneID)
		if focused == nil {
			a.draw()
			return true
		}
		r := paneRectFor(a, focused)
		// Try descent/ascent first — a click on a well, a content
		// tile, or in the edge band kicks off navigation. Selection
		// only applies to other cases (e.g., clicking a tile to
		// outline it without descending).
		if a.attemptDescentOrAscent(focused, r, sx, sy,
			verdict == dragdrop.DropNavigateSplit) {
			a.scheduleURLUpdate()
			return true
		}
		cellX, cellY := cellAtScreen(focused, r, sx, sy)
		if n := a.tileAtCell(focused, cellX, cellY); n != nil {
			a.local(focused.ID).Selected = n.ID
		} else {
			a.clearSelected(focused.ID)
		}
		a.draw()
		a.scheduleURLUpdate()
		return true

	case dragdrop.DropCreateTemplate:
		// Template-drag drop: turn the synthetic ghost into a real node by
		// asking the server to create it at the snapped cell. The target and
		// the cell are the ones gathered above — the verdict's own — so the
		// create can never land somewhere the verdict did not allow.
		a.commitTemplateDrop(d, t, dropX, dropY)
		return true

	case dragdrop.DropPanEnd:
		// Pan drag end: just persist viewport state (the URL now; the grid
		// framing via the draw()-armed settle persister).
		a.scheduleURLUpdate()
		a.draw()
		return true

	case dragdrop.DropDelete:
		// Dropping the dragged tile on the bar slot's trashcan deletes it.
		// It resolves against that button, not the grid under the cursor, so
		// it works wherever the cursor happens to be.
		a.runDeleteTile(d, nil)
		a.ghost = nil
		a.draw()
		return true

	case dragdrop.DropRejected:
		// No target, a forbidden cross-grid move, the same cell, or an
		// occupied one: snap back without a doomed round trip.
		a.cancelDragSnapBack(d)
		return true

	case dragdrop.DropLink:
		// A cross-namespace left-drag: the destination gains a link and the
		// source stays put, because there is no cross-plugin move. The ghost
		// previewed this with the dashed chain badge.
		if a.ghost != nil {
			// The source was hidden for a would-be move; it stays — unhide it
			// now so the world reads "source intact + link appearing".
			a.ghost.hiddenTileID = ""
			a.ghost.hiddenPaneID = ""
		}
		a.landGhostAtCell(t, dropX, dropY)
		a.commitLinkDrop(d, t, dropX, dropY)
		a.draw()
		return true
	}

	// DropMove: animate ghost to the snapped cell in the target grid's coords.
	a.landGhostAtCell(t, dropX, dropY)

	dstGridID := t.gridID
	srcGridID := d.srcGridID

	// A same-namespace left-drag is a move; a clone goes through the
	// right-drag path (commitRightClone in right_button.go) and never
	// reaches here. PlaceTile is the one placement writeback: an id plus the
	// full (grid, x, y, w, h) fact, with no descent path — the
	// well-into-own-subtree refusal is the server's own ancestor walk — and
	// no version claim, since placement is layout and last-writer-wins, with
	// the overlap check protecting the grid.
	req := &rpc.PlaceTileRequest{
		TileID: d.tileID,
		GridID: dstGridID,
		X:      dropX,
		Y:      dropY,
		W:      d.snapshotTile.W,
		H:      d.snapshotTile.H,
	}
	// A drag carries no parked value: the ghost is presentation, and snapping
	// it back to its origin is the honest reconcile the user can see.
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

// commitLinkDrop creates the link a DropLink verdict asks for — a ctrl +
// right-drag anywhere, or a cross-namespace left-drag, which have no reason
// to make two different kinds of reference: an exit well for a dragged well,
// with the same qualified child grid, framing, and label a + menu
// plugin-swatch drop produces, or a leaf link for a text, url, shell, or pane
// tile, whose link_target_id names the dragged tile — or, when the dragged
// tile is itself a leaf link, its target, so links never chain through
// middleman tiles. The ids the client already holds are qualified in every
// namespace, home included, so the same request shape links inside one
// namespace and across one. The source tile is not touched.
func (a *App) commitLinkDrop(d *dragState, t *dropTarget, dropX, dropY int64) {
	src := d.snapshotTile
	dstGridID := t.gridID
	if rpc.IsWellKind(src.Kind) {
		req := &rpc.CreateWellRequest{
			GridID: dstGridID, X: dropX, Y: dropY, W: src.W, H: src.H,
			ChildGridID: src.ChildGridID, Label: src.AltText,
			Framing: rpc.Framing{Cx: src.ViewCx, Cy: src.ViewCy, Zoom: src.ViewZoom},
		}
		a.postTileMutate("CreateWell", dstGridID, func(ctx context.Context) (*rpc.Tile, error) {
			return a.cl.CreateWell(ctx, req)
		}, nil)
		return
	}
	// A link to a link points at the content, never at the middle row: the
	// same read-through every content operation takes.
	target := src.ContentID()
	req := &rpc.CreateLeafLinkRequest{
		GridID: dstGridID, X: dropX, Y: dropY, W: src.W, H: src.H,
		Kind: src.Kind, LinkTargetID: target, Label: src.AltText,
	}
	a.postTileMutate("CreateLeafLink", dstGridID, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateLeafLink(ctx, req)
	}, nil)
}

// occupiedForDrop reports whether the dropped footprint (x, y, w, h) in
// gridID overlaps any cached tile other than excludeID. A move passes the
// dragged tile's own id, mirroring the server's PlaceTile self-exclusion, so
// the preflight cannot reject a placement the server would accept: a large
// tile dragged a short distance crosses its own old footprint, which is not a
// collision. A clone passes "", because the source tile is a real neighbor
// there.
func (a *App) occupiedForDrop(gridID string, x, y, w, h int64, excludeID string) bool {
	g, ok := a.c.Grid(gridID)
	if !ok {
		return false
	}
	for _, n := range g.Tiles {
		if n.ID == excludeID {
			continue
		}
		if dragdrop.RectsOverlap(n.X, n.Y, n.W, n.H, x, y, w, h) {
			return true
		}
	}
	return false
}

// landGhostAtCell lands the ghost on the drop target's cell (dropX, dropY):
// the target pane, the target's cell size, and the screen position of that
// cell. The one landing every drop commit uses — move, link, and clone — so
// the three cannot place the same drop differently.
func (a *App) landGhostAtCell(t *dropTarget, dropX, dropY int64) {
	a.landGhost(t.pane.ID, t.cellSize,
		t.originX+float64(dropX)*t.cellSize, t.originY+float64(dropY)*t.cellSize)
}

// landGhost is the one drop landing: the ghost belongs to paneID, drawn at
// that pane's cell size when cellSize > 0, and snaps to the screen cell
// (toX, toY).
func (a *App) landGhost(paneID string, cellSize, toX, toY float64) {
	if a.ghost != nil {
		a.ghost.paneID = paneID
		if cellSize > 0 {
			a.ghost.targetCellSize = cellSize
		}
	}
	a.startSnap(toX, toY, snapMs)
}

// startSnap animates the active ghost from its current position to (toX, toY)
// over the given duration. Replaces any prior animation.
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

// cancelDragSnapBack runs the snap-back-to-origin animation when a drop is
// abandoned (released outside any pane, or the source pane vanished).
func (a *App) cancelDragSnapBack(d *dragState) {
	if a.ghost == nil {
		// Drag never crossed the threshold — nothing to animate.
		a.draw()
		return
	}
	a.snapBackToOrigin(d)
}

// snapBackToOrigin starts an animation from the ghost's current position
// back to the original location of the dragged tile. Used both for failed
// server commits and for drops outside any pane. Position animates on
// the anim.Animation; the ghost's displayedCellSize is independently
// lerped toward srcCellSize each frame so the ghost grows or shrinks
// back to its original size as it returns.
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
