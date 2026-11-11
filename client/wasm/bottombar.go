//go:build js && wasm

package main

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/barslot"
	"github.com/josephburnett/gridwell/client/bartitle"
	"github.com/josephburnett/gridwell/client/contentrow"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/scratch"
	"github.com/josephburnett/gridwell/client/traceevent"
	"github.com/josephburnett/gridwell/client/wsbar"
)

// The one bar at the bottom of the window, riding the focused pane, carrying
// the complete nav chain, the centered title and the circle slot. Geometry
// comes from wsbar, so the hit-test reads the identical layout, and the band
// is reserved layout so no pane can paint over it. What the bar shows is
// derived per draw, never stored.

// barFont is the bar's lettering: the title measures with it and every crumb
// draws in it, so the centered span cannot be sized from a different font.
const barFont = "12px system-ui, sans-serif"

// bottomBarRect is the bar's drawn rectangle, spanning the focused pane.
// wsbar.Rect owns the geometry. ok=false when there is no pane to sit under
// or no space for the band, and the row is then plain background.
func (a *App) bottomBarRect() (x, top, w float64, ok bool) {
	p := a.tree.FocusedPane()
	if p == nil {
		return 0, 0, 0, false
	}
	r := a.paneRectByID(p.ID)
	return wsbar.Rect(a.width, a.height, errsurface.NoticesHeight(a.errs.Len()), r.X, r.W)
}

// barTheme returns the band and button shades for the focused pane, from the
// same classifier as the pane border: one fact, two shades.
func (a *App) barTheme() (band, button string) {
	p := a.tree.FocusedPane()
	if p == nil {
		return a.pal.Bg, a.pal.FocusBorder
	}
	g, gridOK := a.c.Grid(a.gridIDForPane(p))
	c := a.pal.Family(pane.FamilyOf(a.borderInputFor(p, g, gridOK, true, a.urlViewFor(p.ID) != nil)))
	return c.Band, c.Line
}

// navCrumb is pane.NavCrumb; navChain is the stack's NavChain for the
// focused pane.
type navCrumb = pane.NavCrumb

func (a *App) navChain() []navCrumb {
	return a.ws.NavChain(a.tree.FocusedPane())
}

func (a *App) bottomBarSegments(chain []navCrumb) []wsbar.Segment {
	_, _, w, ok := a.bottomBarRect()
	if !ok {
		return nil
	}
	widths := make([]float64, len(chain))
	for i, nc := range chain {
		if nc.Workspace {
			widths[i] = wsbar.BoundaryW
		} else {
			widths[i] = wsbar.RowH
		}
	}
	return wsbar.Layout(widths, w)
}

// drawBottomBar paints the band. The top edge carries no rule, so the pane's
// own border is the only line there and the bar reads as that pane's
// footer.
func (a *App) drawBottomBar() {
	bx, top, bw, ok := a.bottomBarRect()
	if !ok {
		return
	}
	band, _ := a.barTheme()
	c := a.cctx
	fillRectC(c, bx, top, bw, wsbar.RowH, band)

	chain := a.navChain()
	segs := a.bottomBarSegments(chain)
	var picked []string
	for _, s := range segs {
		shifted := s
		shifted.X += bx
		nc := chain[s.Index]
		selected := a.crumbSelected(nc)
		if selected {
			id, _, _, _ := nc.SelectTarget()
			picked = append(picked, id)
		}
		if nc.Workspace {
			a.drawBoundaryCrumb(nc.WsLevel, shifted, top, selected)
		} else {
			a.drawChainCrumb(nc.Crumb, shifted, top, selected)
		}
	}
	a.e2e.crumbsSelected = picked
	a.drawBarTitle(top)
	a.drawCachedChip(bx, top, bw)
	a.drawBarSlot()
}

// drawCachedChip marks a focused pane whose grid is served from cache: the source
// serving it is not answering, so what is on screen is the node's cache
// of it. Bar chrome only — darkness never moves or restyles tiles, so the grid
// renders exactly as cached.
func (a *App) drawCachedChip(bx, top, bw float64) {
	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	if !a.c.SourceDark(a.gridIDForPane(p)) {
		return
	}
	const chipW, chipH = 52.0, 16.0
	x := bx + bw - wsbar.SlotW - chipW - 8
	y := top + (wsbar.RowH-chipH)/2
	c := a.cctx
	fillRectC(c, x, y, chipW, chipH, a.pal.CachedChipBg)
	drawLabel(c, "cached", x+chipW/2, y+chipH/2, labelOpts{
		font: "10px system-ui, sans-serif", fill: a.pal.CachedChipFg,
		align: "center", baseline: "middle",
	})
}

// drawBoundaryCrumb paints a workspace tile boundary crumb as a wide named bar,
// standing out from the preview squares as the obvious rename target.
func (a *App) drawBoundaryCrumb(level int, s wsbar.Segment, top float64, selected bool) {
	c := a.cctx
	band := a.pal.CrumbIdle
	if level == a.ws.Depth() {
		band = a.pal.CrumbHere
	}
	fillRectC(c, s.X+2, top+3, s.W-4, wsbar.RowH-6, band)
	label := ""
	if f := a.ws.At(level); f != nil {
		label = f.Name
	}
	if label == "" {
		label = "workspace"
	}
	withClip(c, s.X+2, top, s.W-4, wsbar.RowH, func() {
		drawLabel(c, label, s.X+10, top+wsbar.RowH/2, labelOpts{
			font: barFont, fill: a.pal.BarInk, baseline: "middle",
		})
	})
	if selected {
		a.drawSelectionBorder(c, s.X+2, top+3, s.W-4, wsbar.RowH-6)
	}
}

// barTitleGeom is the centered current-pane title. Render, hit-test and the
// rename input all read this one rect; textX is where render starts the text.
func (a *App) barTitleGeom() (x, w, textX float64, label string, editable, muted, ok bool) {
	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	v, _ := a.barTitle(p)
	label, editable, muted = a.bubbleDecorate(p, v.Text()), v.Editable, v.Muted
	if label == "" {
		return
	}
	bx, _, bw, rectOK := a.bottomBarRect()
	if !rectOK {
		return
	}
	a.cctx.Set("font", barFont)
	textW := a.cctx.Call("measureText", label).Get("width").Float()
	crumbsEnd := wsbar.CrumbsEnd(a.bottomBarSegments(a.navChain()))
	tx, tw, ttx, spanOK := wsbar.TitleSpan(crumbsEnd, bw, textW)
	if !spanOK {
		return
	}
	x, w, textX, ok = bx+tx, tw, bx+ttx, true
	return
}

// drawBarTitle paints the focused pane's name centered in the band, hidden
// while the rename input replaces it in place.
func (a *App) drawBarTitle(top float64) {
	if a.overlays.renameEditing {
		return
	}
	x, w, textX, label, _, muted, ok := a.barTitleGeom()
	if !ok {
		return
	}
	c := a.cctx
	color := a.pal.BarInk
	if muted {
		color = a.pal.Muted
	}
	withClip(c, x, top, w, wsbar.RowH, func() {
		drawLabel(c, label, textX, top+wsbar.RowH/2, labelOpts{
			font: barFont, fill: color, baseline: "middle",
		})
	})
}

// barSlotMode gathers the world facts barslot.Decide reads. The shell refresh
// button's visibility is resolved only on a frozen shell descent, because
// shellRefreshButtonVisible kicks a probe; Decide reads that field on the
// same arm alone, so the guard cannot change the verdict.
func (a *App) barSlotMode(p *pane.Pane) barslot.Mode {
	in := barslot.Input{
		Descent:    p.ContentID() != "",
		Content:    a.descentKind(p),
		URLLive:    a.urlViewFor(p.ID) != nil,
		ShellLive:  a.hasShellStream(p.ID),
		CanLiveURL: a.caps.LiveURL,
	}
	if in.Content == rpc.DescentURL && !in.CanLiveURL {
		t, _ := a.descendedTile(p)
		_, s := a.askContentRow(t)
		in.ContentPending = s == contentrow.Pending
	}
	if in.Content == rpc.DescentShell {
		if t, ok := a.descendedTile(p); ok && !in.ShellLive {
			in.ShellRefreshVisible = a.shellRefreshButtonVisible(t)
		}
	}
	return barslot.Decide(in)
}

// drawBarSlot paints the bar's right-end circle. barslot.Decide says which
// mode, so the glyph drawn is the same verdict barSlotClick acts on.
func (a *App) drawBarSlot() {
	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	switch a.barSlotMode(p) {
	case barslot.ModeURLBack:
		a.drawURLBackButton()
	case barslot.ModeGoLive:
		a.drawURLRefreshButton()
	case barslot.ModeURLOpenTab:
		a.drawURLOpenTabButton()
	case barslot.ModeFreeze:
		a.drawFreezeButton()
	case barslot.ModePlus:
		a.drawPlusButton(p)
	}
}

// barSlotClick dispatches a click on the circle slot, always on the focused
// pane; the slot never transfers focus. Left-click only. The mode is the same
// verdict drawBarSlot drew, so the button does what it shows.
func (a *App) barSlotClick(button int) {
	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	if button != 0 {
		return
	}
	switch a.barSlotMode(p) {
	case barslot.ModeURLBack:
		a.bridgeGoBack(p.ID)
	case barslot.ModeGoLive:
		// One verdict, one glyph; which stream reopens is the descended
		// tile's kind, which the pane already knows.
		if t, ok := a.descendedTile(p); ok {
			if a.isShellDescent(p) {
				// The verdict that drew this glyph resolved the key.
				if key, ok := a.shellKey(t, true); ok {
					a.openShellStream(p, t.Id, key)
				}
			} else {
				a.openURLStream(p, t.Id)
			}
		}
	case barslot.ModeFreeze:
		a.freezeShellPaneByIntent(p)
	case barslot.ModeURLOpenTab:
		// A browser host cannot place a live view, so the next-best descent
		// is a new tab. The tile stays frozen and this persists nothing.
		// Synchronous within the click, so the popup rides the user-gesture
		// allowance.
		a.openURLInNewTab(p)
	case barslot.ModePlus:
		a.menu.Toggle(p.ID)
		a.draw()
	}
	// ModeNothing: a markdown descent's slot is the DOM toggle button, which
	// handles its own clicks.
}

// openURLInNewTab is the frozen host's answer to "descend live". A tile with
// no address yet says so, instead of a silent dead tap; a link whose target is
// not read yet offers no tab at all (barslot.Input.ContentPending).
func (a *App) openURLInNewTab(p *pane.Pane) {
	t, ok := a.descendedTile(p)
	if !ok {
		return
	}
	url := a.webAddress(t)
	if url == "" {
		a.reportErr(errsurface.Info, "urlopen", "this url tile has no address yet")
		return
	}
	js.Global().Get("window").Call("open", url, "_blank", "noopener")
}

// drawChainCrumb paints one descent-chain square through the same drawer the
// parent grid uses, so each crumb carries its grid appearance and kind
// border. The rightmost crumb is the current one and needs no highlight. A
// selected tile's crumb wears the border its tile does.
func (a *App) drawChainCrumb(cr pane.Crumb, s wsbar.Segment, top float64, selected bool) {
	c := a.cctx
	square := min(s.W, wsbar.RowH)
	side := square - 2
	if side < 4 {
		return
	}
	x := s.X + (square-side)/2
	y := top + (wsbar.RowH-side)/2

	withClip(c, x, y, side, side, func() {
		if cr.Anchor != "" {
			// A root crumb: the namespace's identity glyph, the same drawing
			// as its menu swatch.
			line := a.pal.Family(pane.GridFamily(cr.Anchor)).Line
			fillRectC(c, x, y, side, side, a.pal.Bg)
			a.drawSourceGlyph(a.sourceGlyph(cr.Anchor), x, y, side, side, line)
			c.Set("strokeStyle", line)
			c.Set("lineWidth", 1.0)
			c.Call("strokeRect", x+0.5, y+0.5, side-1, side-1)
		} else if t := a.chainCrumbTile(cr); t != nil {
			a.drawNodeWithPreview(t, x, y, side, side, pane.SquareCell(side, t), false, pane.TileFamily(t), isLinkTile(t), "")
		} else {
			// The row is not cached, so draw a placeholder; the fetch kicked
			// by chainCrumbTile fills it in.
			c.Set("strokeStyle", a.pal.CrumbIdle)
			c.Set("lineWidth", 1.0)
			c.Call("strokeRect", x+1, y+1, side-2, side-2)
		}
		if selected {
			a.drawSelectionBorder(c, x, y, side, side)
		}
	})
}

// chainCrumbTile resolves a tile crumb's row from the cache, kicking a
// fetch of its containing grid on a miss.
func (a *App) chainCrumbTile(cr pane.Crumb) *gridwellv1.Tile {
	gid := a.gridIDForPathFrom(cr.ParentAnchor, cr.ParentPath)
	if gid == "" {
		return nil
	}
	g, ok := a.c.Grid(gid)
	if !ok {
		a.fetchGrid(gid)
		return nil
	}
	t, ok := g.Tiles[cr.TileID]
	if !ok {
		// An ephemeral visit lives in the scratch grid, not the pane's.
		return a.findTileByID(cr.TileID)
	}
	return t
}

// bottomBarClick consumes a press in the bar's band: on the bar it acts on
// the focused pane, and a left press beside it focuses the column above,
// which moves the bar there. wsbar.RouteClick
// says where the press goes; this gathers the world facts and runs the effect.
// They are gathered on the ZoneBar arm alone, so a press anywhere else in the
// window never walks the caches.
func (a *App) bottomBarClick(sx, sy float64, button int, mods traceevent.Mods) bool {
	bx, top, bw, ok := a.bottomBarRect()
	if !ok {
		return false
	}
	p := a.tree.FocusedPane()
	in := wsbar.Click{Button: button, Shift: mods.Shift, Ctrl: mods.Ctrl, Meta: mods.Meta,
		X: sx - bx, BarW: bw, Zone: wsbar.Where(sx, sy, bx, top, bw)}
	if in.Zone == wsbar.ZoneBar {
		in.Chain = a.navChain()
		in.Segments = a.bottomBarSegments(in.Chain)
		if tx, tw, _, _, _, _, tOK := a.barTitleGeom(); tOK {
			in.TitleX, in.TitleW, in.TitleOK = tx-bx, tw, true
		}
		if p != nil {
			in.SlotMenu = a.slotHasMenu(p)
			in.Visits = a.visitCrumbs(p, in.Chain)
		}
	}
	hit := wsbar.RouteClick(in)
	switch hit.Action {
	case wsbar.ActionPass:
		return false
	case wsbar.ActionFocusColumn:
		if id, ok := wsbar.BandFocus(a.layoutPanes(), a.tree.Recency(), sx); ok {
			a.focusToPane(a.tree.FindPane(id))
			a.takeKeyboard()
		}
	case wsbar.ActionSlotMenu:
		a.openCircleMenu(p)
	case wsbar.ActionSlot:
		a.barSlotClick(button)
	case wsbar.ActionRename:
		a.openRenameInput()
	case wsbar.ActionZoom:
		a.togglePaneZoom()
	case wsbar.ActionWorkspaceRename, wsbar.ActionLeaveLevels, wsbar.ActionAscend:
		a.crumbAct(p, hit.Action, in.Chain[hit.Segment.Index])
	case wsbar.ActionVisitDrag:
		if t := a.cachedTileByID(in.Visits[hit.Segment.Index]); t != nil {
			a.startVisitDrag(p, t, in.Chain[hit.Segment.Index], hit.Bare, hit.Segment, bx, top, sx, sy,
				button, mods.Ctrl)
		}
	case wsbar.ActionSelect:
		// A tile crumb is drawn only under a focused pane (NavChain).
		a.toggleCrumb(p.ID, in.Chain[hit.Segment.Index])
	}
	return true
}

// crumbAct runs a crumb's click: a press on it, or the bare release of a
// visit drag armed from it.
func (a *App) crumbAct(p *pane.Pane, act wsbar.Action, nc navCrumb) {
	switch act {
	case wsbar.ActionWorkspaceRename:
		a.openWorkspaceRenameInput(nc.WsLevel)
	case wsbar.ActionLeaveLevels:
		// Be inside level wsLevel, or for the root crumb back in the
		// session, whose own state the bar never touches.
		a.runGesture(nav.Gesture{Kind: nav.GestureLeaveLevels, Count: a.ws.PopCountTo(nc.WsLevel)})
	case wsbar.ActionAscend:
		// Only the focused pane's own frame stack yields an ascending crumb
		// (pane.Levels.NavChain), so there is a pane here. How many ascents
		// the crumb is is pane.AscentsTo's arithmetic; the last hop animates
		// and the ones above it are instant.
		a.ascend(p, p.AscentsTo(nc.Crumb), true)
	}
}

// visitCrumbs is wsbar.Visits over p's place: a frame's row is ephemeral by
// p's scratch rule, the open level's workspace tile by the rule of the pane
// it was entered from.
func (a *App) visitCrumbs(p *pane.Pane, chain []pane.NavCrumb) map[int]string {
	return wsbar.Visits(chain, p.Frames(), a.ws.Depth(), func(id string, level bool) bool {
		t := a.cachedTileByID(id)
		if t == nil {
			return false
		}
		if level {
			eph, known := scratch.Ephemeral(a.levelScratch(), t.GridId)
			return eph && known
		}
		return a.certainlyEphemeral(p, t)
	})
}

// startVisitDrag arms the drag of visit row t from its crumb nc, as any
// tile's drag is armed (startTemplateDrag): it carries the row from the
// scratch grid, so dragdrop.DecideDrop decides as for every tile. The right
// button copies, or with ctrl links, through the right-button release; a
// release with no drag is bare, the crumb's click. The ghost starts as the
// row drawn where the crumb is: the square drawChainCrumb paints, or a
// boundary's band, held at the press's place along it.
func (a *App) startVisitDrag(p *pane.Pane, t *gridwellv1.Tile, nc navCrumb, bare wsbar.Action, seg wsbar.Segment, bx, top, sx, sy float64, button int, ctrl bool) {
	square := min(seg.W, wsbar.RowH)
	side := square - 2
	x, y := bx+seg.X+(square-side)/2, top+(wsbar.RowH-side)/2
	if nc.Workspace {
		side = wsbar.RowH - 6
	}
	cell := pane.SquareCell(side, t)
	ox, oy := (sx-x)/cell, (sy-y)/cell
	if nc.Workspace {
		bandX, bandW := bx+seg.X+2, seg.W-4
		ox = float64(t.W) * min(max((sx-bandX)/bandW, 0), 1)
		oy = float64(t.H) * min(max((sy-top-3)/side, 0), 1)
	}
	intent := dragdrop.IntentMove
	if button == 2 {
		intent = rightDragIntent(ctrl)
		a.rightDrag = &rightDragState{kind: rightDragCarry, startX: sx, startY: sy, curX: sx, curY: sy}
	}
	a.dragging = &dragState{
		originPaneID: p.ID,
		visit:        &nc,
		visitBare:    bare,
		intent:       intent,
		startScreenX: sx,
		startScreenY: sy,
		curScreenX:   sx,
		curScreenY:   sy,
		srcGridID:    t.GridId,
		srcCellSize:  cell,
		carried:      []*gridwellv1.Tile{t},
	}
	a.dragging.grabTile(t, float64(t.X)+ox, float64(t.Y)+oy, sx-ox*cell, sy-oy*cell)
}

// openWorkspaceRenameInput opens the shared inline rename input over the
// workspace tile crumb of `level`, growing rightward to a typeable width.
func (a *App) openWorkspaceRenameInput(level int) {
	f := a.ws.At(level)
	if f == nil {
		return
	}
	chain := a.navChain()
	idx := -1
	for i, nc := range chain {
		if nc.Workspace && nc.WsLevel == level {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	seg, ok := wsbar.SegmentAt(a.bottomBarSegments(chain), idx)
	if !ok {
		return // truncated off the left edge: rename via the title
	}
	bx, top, _, rectOK := a.bottomBarRect()
	if !rectOK {
		return
	}
	a.openNameInputAt(f.Name, seg.W-28, func(st js.Value) {
		st.Set("left", pxf(bx+seg.X+2))
		st.Set("top", pxf(top+4))
	}, func(val string) {
		a.commitWorkspaceRename(level, val)
	})
}

// openRenameInput swaps the centered bar title for the shared inline input.
// The name lives in the bar, not over pane content, so this works over live
// views with no native help.
func (a *App) openRenameInput() {
	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	v, target := a.barTitle(p)
	if v.Rename == bartitle.RenameNone {
		return
	}
	x, w, _, _, _, _, geomOK := a.barTitleGeom()
	if !geomOK {
		return
	}
	bx, top, bw, rectOK := a.bottomBarRect()
	if !rectOK {
		return
	}
	if w < 160 {
		// Typing space: grow around the title's center but stay off the
		// slot.
		grown := 160.0
		x = x + w/2 - grown/2
		if max := bx + bw - wsbar.SlotW - 8; x+grown > max {
			x = max - grown
		}
		w = grown
	}
	tileID := target.Id
	a.openNameInputAt(target.AltText, w-24, func(st js.Value) {
		st.Set("left", pxf(x))
		st.Set("top", pxf(top+4))
	}, func(val string) {
		a.commitRename(tileID, val)
	})
}
