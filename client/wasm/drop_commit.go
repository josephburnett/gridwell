//go:build js && wasm

package main

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/paste"
	"github.com/josephburnett/gridwell/client/textedit"
	"github.com/josephburnett/gridwell/client/traceevent"
)

// Commits a left-button drag and animates the ghost. finishLeftDrag is the
// one commit path, so the mouseup the handler saw and the release
// gesture.RecoverRelease infers cannot land two ways. The verdict is
// dragdrop.DecideDrop's; commitRightCopy is the right button's twin and
// shares commitShared, the landing and the snap-back below.

// Animation durations in milliseconds. snapMs is a var so a spec can hold a
// drop's landing open (setSnapMs).
var snapMs = 110.0

const snapBackMs = 220.0

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

	// A menu swatch released without a drag is the menu's gesture, not the
	// verdict's bare-click arm, which is the canvas's. The popover floats
	// over a live pane, so falling through would act on the tile behind the
	// swatch.
	if d.menuPress && !d.started {
		a.clickMenuItem(d)
		return true
	}
	// Snapshot every world-read the decision needs, once. onMouseMove gathers
	// the same DropInput for the ghost preview, so preview and commit cannot
	// diverge.
	in, t, dropX, dropY := a.dropInputAt(d, sx, sy, true /* placement */)

	verdict := a.commitVerdict(in, d, t, dropX, dropY)
	switch verdict {
	case dragdrop.DropNavigate, dragdrop.DropNavigateSplit:
		// The split flavor is the same click asking for a split; focus
		// already followed the press.
		focused := a.tree.FindPane(d.originPaneID)
		if focused == nil {
			a.draw()
			return true
		}
		r := a.paneRect(focused.ID)
		// Descent and ascent come first; selection is what is left over.
		if a.attemptDescentOrAscent(focused, r, sx, sy,
			verdict == dragdrop.DropNavigateSplit) {
			a.scheduleURLUpdate()
			return true
		}
		cellX, cellY, shown := cellAtScreen(focused, r, sx, sy)
		if n := a.tileAtCell(focused, cellX, cellY); shown && n != nil {
			a.local(focused.ID).Selected = n.Id
		} else {
			a.clearSelected(focused.ID)
		}
		a.draw()
		a.scheduleURLUpdate()
		return true

	case dragdrop.DropTemplate:
		a.commitTemplateCopy(d, t)
		return true

	case dragdrop.DropFromMenu:
		// The target and the cell are the verdict's own, gathered above, so
		// the create cannot land somewhere the verdict did not allow.
		a.commitMenuDrop(d, t, dropX, dropY)
		return true

	case dragdrop.DropPanEnd:
		// The URL now; the grid framing through the draw()-armed settle
		// persister.
		a.scheduleURLUpdate()
		a.draw()
		return true
	}
	if a.commitShared(verdict, d, t, dropX, dropY) {
		return true
	}

	// DropMove.
	a.landGhostAtCell(t, dropX, dropY)
	a.commitMove(d, t, dropX, dropY)
	a.draw()
	return true
}

// crumbBare is a crumb released without a drag: the crumb's click.
func (a *App) crumbBare(d *dragState) {
	if p := a.tree.FindPane(d.originPaneID); p != nil {
		a.crumbAct(p, d.crumbBare, *d.crumb)
	}
	a.draw()
}

// commitShared commits the verdicts both buttons' drags share, reporting
// whether verdict was one: the trashcan, which resolves against the bar
// button wherever the cursor is; a snap back with no doomed round trip; and a
// link, made by a ctrl + right-drag or a left-drag that links, which
// leaves the sources a would-be move hid where they are.
func (a *App) commitShared(verdict dragdrop.DropAction, d *dragState, t *dropTarget, dropX, dropY int64) bool {
	switch verdict {
	case dragdrop.DropDelete:
		a.runDeleteTile(d, nil)
		a.ghost = nil
		a.draw()
	case dragdrop.DropRejected:
		a.cancelDragSnapBack(d)
	case dragdrop.DropPaste:
		a.cancelDragSnapBack(d)
		a.commitPaste(d, t)
	case dragdrop.DropLink:
		if a.ghost != nil {
			a.ghost.hidden, a.ghost.hiddenPaneID = nil, ""
		}
		a.landGhostAtCell(t, dropX, dropY)
		a.commitLinkDrop(d, t, dropX, dropY)
		a.draw()
	default:
		return false
	}
	return true
}

// commitPaste asks the node owning the shell to type what the carry names
// into its tmux, spelled under this page's origin, or inserts it into the
// text; a refusal is a notice. The paste's pane takes focus, so typing goes
// on where the paste went, to gesture.Keyboard's owner there (Joe,
// 2026-11-20); a text's must, since the textarea lives only over the focused
// pane.
func (a *App) commitPaste(d *dragState, t *dropTarget) {
	if t.into == paste.IntoText && !strings.HasPrefix(a.origin, "http") {
		a.reportErr(errsurface.Error, "paste", "this page has no web address to spell a reference under")
		return
	}
	a.focusToPane(t.pane)
	if t.into == paste.IntoText {
		a.pasteIntoText(d, t)
		return
	}
	ids := make([]string, len(d.carried))
	for i, row := range d.carried {
		ids[i] = row.Id
	}
	origin := a.origin
	if !strings.HasPrefix(origin, "http") {
		origin = ""
	}
	shell := rpc.ContentID(t.tile)
	a.post(write{label: "PasteShell", call: func(ctx context.Context) error {
		return a.cl.PasteShell(ctx, shell, ids, origin)
	}})
}

// pasteIntoText inserts the carry's references at the release through the
// editor, so the edit is the user's own: one undo step, and saved by the save
// chain under the version it claims. The text's body is held, since the pane
// painted it, and one that is not is refused rather than waited for.
func (a *App) pasteIntoText(d *dragState, t *dropTarget) {
	_, file, _, desc := a.focusedTextDescent()
	if _, held := a.tileBody(file); !held || desc.Mode != rpc.TextModeText || a.overlays.lastTextareaTileID != t.pane.ContentID() {
		a.reportErr(errsurface.Info, "paste", "the text has not loaded; drop again")
		return
	}
	ta := a.overlays.textTextarea
	e := textedit.InsertWords(ta.Get("value").String(), a.textareaOffsetAt(ta, t.atX, t.atY),
		paste.Text(d.carried, a.origin, paste.IntoText))
	if !a.applyTextareaEdit(ta, e) {
		a.reportErr(errsurface.Error, "paste", "the browser refused the paste")
	}
}

// textareaOffsetAt is the UTF-16 offset in ta under canvas point (sx, sy),
// by document.caretPositionFromPoint, which Chromium 128 and later answers
// with ta itself and the offset in its value. Where the call is absent or
// answers another node the caret stands in, so a paste always lands. The
// drop's ghost parks ta by a clip (overlayClip), and a clipped point hits
// nothing, so the clip is lifted for the read, within one task.
func (a *App) textareaOffsetAt(ta js.Value, sx, sy float64) int {
	rect := a.canvas.Call("getBoundingClientRect")
	if a.doc.Get("caretPositionFromPoint").Type() == js.TypeFunction {
		style := ta.Get("style")
		clip := style.Get("clipPath")
		style.Set("clipPath", "none")
		pos := a.doc.Call("caretPositionFromPoint", sx+rect.Get("left").Float(), sy+rect.Get("top").Float())
		style.Set("clipPath", clip)
		if pos.Truthy() && pos.Get("offsetNode").Equal(ta) {
			return pos.Get("offset").Int()
		}
	}
	return ta.Get("selectionStart").Int()
}

// commitMove places every carried tile by the grabbed tile's offset, each
// its own MoveTile claiming the placement the press saw, so a stale row is
// refused alone, its cache puts it back alone, and its ghost flies home alone
// (snapBackMember). They run one after another in dragdrop.Carry.MoveOrder,
// because the node checks each against the grid as the last one left it.
func (a *App) commitMove(d *dragState, t *dropTarget, dropX, dropY int64) {
	c := d.carry()
	to := c.At(dropX, dropY)
	gh := a.ghost
	ws := make([]write, 0, len(c))
	for _, i := range c.MoveOrder(dropX, dropY) {
		row := d.carried[i]
		snapBack := func() { a.snapBackMember(gh, d, i) }
		ws = append(ws, a.placementWrite(d.srcGridID, &gridwellv1.MoveTileRequest{
			TileId: row.Id,
			GridId: t.gridID,
			X:      to[i].X,
			Y:      to[i].Y,
			W:      row.W,
			H:      row.H,
			Claim:  rpc.PlacementOf(row),
		}, snapBack))
	}
	if d.crumb != nil {
		// Only a visit's crumb moves (dragdrop.KeepsVisit); its pane follows
		// the row once it lands.
		landed := ws[0].then
		ws[0].then = func() {
			landed()
			a.runGesture(nav.Gesture{Kind: nav.GestureVisitMoved, PaneID: d.originPaneID,
				DestPaneID: t.pane.ID, Door: a.cachedTileByID(d.tileID), TileID: t.door})
		}
	}
	a.postInOrder(ws)
}

// postPlacement is the one placement writeback, MoveTile: an id plus the
// full (grid, x, y, w, h) fact, claiming the placement the press saw, so a
// move that landed during the drag is refused rather than undone. The cache
// holds the placement from the release
// (cache.Place), so the tile is where the user put it before any event. A
// failure puts it back, and snapBack is the caller's animation of that; a
// conflict also re-reads the grid, so the tile lands where the node holds it.
// A placement parks nothing.
func (a *App) postPlacement(gid string, req *gridwellv1.MoveTileRequest, snapBack func()) {
	a.post(a.placementWrite(gid, req, snapBack))
}

// placementWrite holds the placement in the cache now and is the write that
// tells the node; see postPlacement.
func (a *App) placementWrite(gid string, req *gridwellv1.MoveTileRequest, snapBack func()) write {
	pl := a.c.Move(req.TileId, cache.Placement{GridID: req.GridId, X: req.X, Y: req.Y, W: req.W, H: req.H})
	var resp *gridwellv1.Tile
	return write{
		label: "MoveTile", gid: gid, optimistic: true,
		call: func(ctx context.Context) error {
			var err error
			resp, err = a.cl.MoveTile(ctx, req)
			return err
		},
		then: func() { pl.Landed(resp) },
		undo: func() {
			pl.Failed()
			if snapBack != nil {
				snapBack()
			}
			a.scheduleDraw(traceevent.WhyDrag)
		},
	}
}

// commitLinkDrop creates the links a DropLink verdict asks for, one per
// carried tile at its offset; what a link is, is rpc.LinkOf's. Ids are
// qualified in every namespace, so one request shape links inside one and
// across one.
func (a *App) commitLinkDrop(d *dragState, t *dropTarget, dropX, dropY int64) {
	to := d.carry().At(dropX, dropY)
	for i, row := range d.carried {
		label := "CreateLeafLink"
		if rpc.IsDoorKind(row.Kind) {
			label = "CreateDoor"
		}
		a.createTile(label, t.gridID, &gridwellv1.CreateTileRequest{GridId: t.gridID,
			Tile: rpc.LinkOf(row, to[i].X, to[i].Y)}, nil)
	}
}

// occupiedForDrop is dragdrop.Carry.Occupied over the target grid's cached
// rows: c landed with its first tile at (x, y). vacates is a move's, which
// mirrors the server's MoveTile self-exclusion.
func (a *App) occupiedForDrop(gridID string, c dragdrop.Carry, x, y int64, vacates bool) bool {
	g, ok := a.c.Grid(gridID)
	if !ok {
		return false
	}
	occupants := make([]dragdrop.Member, 0, len(g.Tiles))
	for _, n := range g.Tiles {
		occupants = append(occupants, memberOf(n))
	}
	return c.Occupied(x, y, occupants, vacates)
}

// landGhostAtCell lands the ghost on the drop target's cell. Move, link and
// copy all use it, so the three cannot place the same drop differently.
func (a *App) landGhostAtCell(t *dropTarget, dropX, dropY int64) {
	a.landGhost(t.cellSize,
		t.originX+float64(dropX)*t.cellSize, t.originY+float64(dropY)*t.cellSize)
}

// landGhost snaps the ghost to the screen cell (toX, toY) at cellSize.
func (a *App) landGhost(cellSize, toX, toY float64) {
	if a.ghost != nil {
		if cellSize > 0 {
			a.ghost.targetCellSize = cellSize
		}
	}
	a.startSnap(toX, toY, snapMs)
}

// startSnap lands the active ghost at (toX, toY).
func (a *App) startSnap(toX, toY, duration float64) {
	if a.ghost == nil {
		return
	}
	a.ghost.Land(toX, toY, nowMs(), duration)
	a.scheduleDraw(traceevent.WhyDrag)
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

// snapBackMember flies carried tile i of the drop gh landed home alone, from
// where gh draws it to where the press found it: the animation of a refused
// write, one per tile, so a lone tile and each tile of a set return the same
// way. A ghost that has ended or given way to a newer press is left be.
func (a *App) snapBackMember(gh *ghost, d *dragState, i int) {
	if gh == nil || gh != a.ghost {
		return
	}
	dx, dy := d.carry().Offset(i)
	cell := gh.displayedCellSize
	home := d.srcCellSize
	if home <= 0 {
		home = cell
	}
	gh.Return(i, gh.X+float64(dx)*cell, gh.Y+float64(dy)*cell, cell,
		d.originScreenX+float64(dx)*home, d.originScreenY+float64(dy)*home, home,
		nowMs(), snapBackMs)
	a.scheduleDraw(traceevent.WhyDrag)
}

// snapBackToOrigin animates the ghost back to the dragged tile's original
// location. Size returns separately: each draw lerps displayedCellSize
// toward srcCellSize.
func (a *App) snapBackToOrigin(d *dragState) {
	if a.ghost == nil {
		return
	}
	if d.srcCellSize > 0 {
		a.ghost.targetCellSize = d.srcCellSize
	}
	a.ghost.Land(d.originScreenX, d.originScreenY, nowMs(), snapBackMs)
	a.scheduleDraw(traceevent.WhyDrag)
}
