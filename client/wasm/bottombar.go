//go:build js && wasm

package main

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"syscall/js"

	"google.golang.org/protobuf/proto"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/barslot"
	"github.com/josephburnett/gridwell/client/bartitle"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/wsbar"
)

// The one bar at the bottom of the window, riding the focused pane, carrying
// the complete nav chain, the centered title and the circle slot. Geometry
// comes from wsbar, so the hit-test reads the identical layout, and the band
// is reserved layout so no pane can paint over it. What the bar shows is
// derived per frame, never stored.

// barFont is the bar's lettering: the title measures with it and every crumb
// draws in it, so the centered span cannot be sized from a different face.
const barFont = "12px system-ui, sans-serif"

// bottomBarRect is the bar's drawn rectangle, spanning the focused pane.
// wsbar.Rect owns the geometry. ok=false when there is no pane to sit under
// or no room for the band, and the row is then plain background.
func (a *App) bottomBarRect() (x, top, w float64, ok bool) {
	p := a.tree.FocusedPane()
	if p == nil {
		return 0, 0, 0, false
	}
	r := a.paneRectByID(p.ID)
	return wsbar.Rect(a.width, a.height, errsurface.StripHeight(a.errs.Len()), r.X, r.W)
}

// barTheme returns the band and button shades for the focused pane, from the
// same classifier as the pane border: one fact, two shades.
func (a *App) barTheme() (band, button string) {
	p := a.tree.FocusedPane()
	if p == nil {
		return a.pal.Bg, a.pal.FocusBorder
	}
	g, gridOK := a.c.Grid(a.gridIDForPane(p))
	in := a.borderInputFor(p, g, gridOK, true, a.urlViewFor(p.ID) != nil)
	switch pane.FamilyOf(in) {
	case pane.FamilyText:
		return a.pal.BarTextBand, a.pal.MarkdownLine
	case pane.FamilyURL:
		return a.pal.URLFill, a.pal.URLLine
	case pane.FamilyURLLive:
		return a.pal.URLFill, a.pal.URLLiveLine
	case pane.FamilyShell:
		return a.pal.ShellFill, a.pal.ShellBorder
	case pane.FamilyExit:
		return a.pal.BarPluginBand, a.pal.PluginBorder
	case pane.FamilyEphemeral:
		return a.pal.BarEphemeralBand, a.pal.EphemeralBorder
	}
	return a.pal.BarGridBand, a.pal.FocusBorder
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
		if nc.PaneTile {
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
	c.Set("fillStyle", band)
	c.Call("fillRect", bx, top, bw, wsbar.RowH)

	chain := a.navChain()
	segs := a.bottomBarSegments(chain)
	for _, s := range segs {
		shifted := s
		shifted.X += bx
		if nc := chain[s.Index]; nc.PaneTile {
			a.drawBoundaryCrumb(nc.WsLevel, shifted, top)
		} else {
			a.drawChainCrumb(nc.Crumb, shifted, top)
		}
	}
	a.drawBarTitle(top)
	a.drawMemoryChip(bx, top, bw)
	a.drawBarSlot()
}

// drawMemoryChip marks a focused pane whose room is a memory: the source
// serving it is not answering, so what is on screen is the node's remembering
// of it. Bar chrome only — darkness never moves or restyles tiles, so the room
// renders exactly as remembered.
func (a *App) drawMemoryChip(bx, top, bw float64) {
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
	c.Set("fillStyle", a.pal.CachedChipBg)
	c.Call("fillRect", x, y, chipW, chipH)
	drawLabel(c, "cached", x+chipW/2, y+chipH/2, labelOpts{
		font: "10px system-ui, sans-serif", fill: a.pal.CachedChipFg,
		align: "center", baseline: "middle",
	})
}

// drawBoundaryCrumb paints a pane-tile boundary crumb as a wide named bar,
// standing out from the preview squares as the obvious rename target.
func (a *App) drawBoundaryCrumb(level int, s wsbar.Segment, top float64) {
	c := a.cctx
	if level == a.ws.Depth() {
		c.Set("fillStyle", a.pal.CrumbHere)
	} else {
		c.Set("fillStyle", a.pal.CrumbIdle)
	}
	c.Call("fillRect", s.X+2, top+3, s.W-4, wsbar.RowH-6)
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
}

// barTitleGeom is the centered current-pane title. Render, hit-test and the
// rename input all read this one rect.
func (a *App) barTitleGeom() (x, w float64, label string, editable, muted, ok bool) {
	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	v, _ := a.barTitle(p)
	label, editable, muted = a.bubbleDecorate(p, v.Label), v.Editable, v.Muted
	if label == "" {
		return
	}
	bx, _, bw, rectOK := a.bottomBarRect()
	if !rectOK {
		return
	}
	a.cctx.Set("font", barFont)
	textW := a.cctx.Call("measureText", label).Get("width").Float() + 24
	segs := a.bottomBarSegments(a.navChain())
	crumbsEnd := 0.0
	if n := len(segs); n > 0 {
		crumbsEnd = segs[n-1].X + segs[n-1].W
	}
	tx, tw, spanOK := wsbar.TitleSpan(crumbsEnd, bw, textW)
	if !spanOK {
		return
	}
	x, w, ok = bx+tx, tw, true
	return
}

// drawBarTitle paints the focused pane's name centered in the band, hidden
// while the rename input replaces it in place.
func (a *App) drawBarTitle(top float64) {
	if a.overlays.renameEditing {
		return
	}
	x, w, label, _, muted, ok := a.barTitleGeom()
	if !ok {
		return
	}
	c := a.cctx
	color := a.pal.BarInk
	if muted {
		color = a.pal.Muted
	}
	withClip(c, x, top, w, wsbar.RowH, func() {
		drawLabel(c, label, x+w/2, top+wsbar.RowH/2, labelOpts{
			font: barFont, fill: color, align: "center", baseline: "middle",
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
	if in.Content == rpc.DescentShell {
		// The pane's own grid, with no scratch fallback: an ephemeral visit
		// resolves to nothing, which is exactly what Durable means.
		t, ok := a.descendedGridTile(p)
		in.Durable = ok
		if ok && !in.ShellLive {
			in.ShellRefreshVisible = a.shellRefreshButtonVisible(t)
		}
	}
	return barslot.Decide(in)
}

// descendedGridTile resolves the descended row from the pane's own grid, with
// no scratch-grid fallback, so an ephemeral visit has nothing for the slot's
// go-live and refresh actions to open.
func (a *App) descendedGridTile(p *pane.Pane) (*gridwellv1.Tile, bool) {
	g, ok := a.c.Grid(a.gridIDForPane(p))
	if !ok {
		return nil, false
	}
	t, ok := g.Tiles[p.ContentID()]
	return t, ok
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
		if t, ok := a.descendedGridTile(p); ok {
			if a.isShellDescent(p) {
				a.openShellStream(p, t.Id)
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
	// handles its own clicks; an ephemeral shell visit has no row to freeze.
}

// openURLInNewTab is the frozen host's answer to "descend live". A tile with
// no address yet says so, instead of a silent dead tap.
func (a *App) openURLInNewTab(p *pane.Pane) {
	t, ok := a.descendedTile(p)
	if !ok {
		return
	}
	url := a.webAddress(t)
	if url == "" {
		if ct := a.cachedTileByID(a.contentKey(t.Id)); ct != nil {
			url = a.webAddress(ct)
		}
	}
	if url == "" {
		a.reportErr(errsurface.Info, "urlopen", "this url tile has no address yet")
		return
	}
	js.Global().Get("window").Call("open", url, "_blank", "noopener")
}

// drawChainCrumb paints one descent-chain square through the same drawer the
// parent grid uses, so each crumb carries its grid appearance and kind
// border. The rightmost crumb is the current one and needs no highlight.
func (a *App) drawChainCrumb(cr pane.Crumb, s wsbar.Segment, top float64) {
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
			c.Set("fillStyle", a.pal.Bg)
			c.Call("fillRect", x, y, side, side)
			a.drawPluginGlyph(a.pluginGlyph(cr.Anchor), x, y, side, side)
			c.Set("strokeStyle", a.pal.FocusBorder)
			c.Set("lineWidth", 1.0)
			c.Call("strokeRect", x+0.5, y+0.5, side-1, side-1)
		} else if t := a.chainCrumbTile(cr); t != nil {
			cells := float64(max(t.W, t.H))
			if cells < 1 {
				cells = 1
			}
			a.drawNodeWithPreview(t, x, y, side, side, side/cells, false, false, isLinkTile(t), "")
		} else {
			// The row is not cached, so draw a placeholder; the fetch kicked
			// by chainCrumbTile fills it in.
			c.Set("strokeStyle", a.pal.CrumbIdle)
			c.Set("lineWidth", 1.0)
			c.Call("strokeRect", x+1, y+1, side-2, side-2)
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

// bottomBarClick consumes a press in the bar's band, always on the focused
// pane, and the background either side swallows presses too. wsbar.RouteClick
// says where the press goes; this gathers the world facts and runs the effect.
// They are gathered on the ZoneBar arm alone, so a press anywhere else in the
// window never walks the caches.
func (a *App) bottomBarClick(sx, sy float64, button int) bool {
	bx, top, bw, ok := a.bottomBarRect()
	if !ok {
		return false
	}
	p := a.tree.FocusedPane()
	in := wsbar.Click{Button: button, X: sx - bx, BarW: bw,
		Zone: wsbar.Where(sx, sy, bx, top, bw)}
	var visit *gridwellv1.Tile
	if in.Zone == wsbar.ZoneBar {
		in.Chain = a.navChain()
		in.Segments = a.bottomBarSegments(in.Chain)
		if tx, tw, _, _, _, tOK := a.barTitleGeom(); tOK {
			in.TitleX, in.TitleW, in.TitleOK = tx-bx, tw, true
		}
		if p != nil {
			in.SlotMenu = a.slotHasMenu(p)
			if t, ok := a.descendedTile(p); ok && t.Kind == rpc.KindURL &&
				a.certainlyEphemeral(p, t) {
				visit, in.Promote = t, true
			}
		}
	}
	hit := wsbar.RouteClick(in)
	switch hit.Action {
	case wsbar.ActionPass:
		return false
	case wsbar.ActionSlotMenu:
		a.openCircleMenu(p)
	case wsbar.ActionSlot:
		a.barSlotClick(button)
	case wsbar.ActionRename:
		a.openRenameInput()
	case wsbar.ActionZoom:
		a.togglePaneZoom()
	case wsbar.ActionWorkspaceRename:
		a.openWorkspaceRenameInput(in.Chain[hit.Segment.Index].WsLevel)
	case wsbar.ActionLeaveLevels:
		// Be inside level wsLevel, or for the root crumb back in the
		// session, whose own state the bar never touches.
		a.runGesture(nav.Gesture{Kind: nav.GestureLeaveLevels,
			Count: a.ws.PopCountTo(in.Chain[hit.Segment.Index].WsLevel)})
	case wsbar.ActionPromote:
		a.startPromoteDrag(p, visit, hit.Segment, bx, top, sx, sy)
	case wsbar.ActionAscend:
		// Only the focused pane's own frame stack yields an ascending crumb
		// (pane.Levels.NavChain), so there is a pane here. How many ascents
		// the crumb is is pane.AscentsTo's arithmetic; the last hop animates
		// and the ones above it are instant.
		a.ascend(p, p.AscentsTo(in.Chain[hit.Segment.Index].Crumb), true)
	}
	return true
}

// startPromoteDrag arms the promote drag from the bar's current crumb, as a
// template-shaped drag whose item carries the origin pane.
func (a *App) startPromoteDrag(p *pane.Pane, t *gridwellv1.Tile, seg wsbar.Segment, bx, top, sx, sy float64) {
	square := min(seg.W, wsbar.RowH)
	// cache.Grid hands out the cached rows themselves, so the ghost is a
	// clone: shaping it to 1x1 through t would resize the visit the crumb is
	// standing on.
	ghost := proto.CloneOf(t)
	ghost.W, ghost.H = 1, 1
	a.dragging = &dragState{
		originPaneID:  p.ID,
		originFocused: true,
		isTemplate:    true,
		item:          paletteItem{primitive: tplURL, promotePane: p.ID},
		menuNS:        a.paneNodeNS(p),
		startScreenX:  sx,
		startScreenY:  sy,
		curScreenX:    sx,
		curScreenY:    sy,
		cellOffsetX:   0.5,
		cellOffsetY:   0.5,
		snapshotTile:  ghost,
		originScreenX: bx + seg.X,
		originScreenY: top,
		srcCellSize:   square,
	}
}

// openWorkspaceRenameInput opens the shared inline rename input over the
// pane-tile crumb of `level`, growing rightward to a typeable width.
func (a *App) openWorkspaceRenameInput(level int) {
	f := a.ws.At(level)
	if f == nil {
		return
	}
	chain := a.navChain()
	idx := -1
	for i, nc := range chain {
		if nc.PaneTile && nc.WsLevel == level {
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
	x, w, _, _, _, geomOK := a.barTitleGeom()
	if !geomOK {
		return
	}
	bx, top, bw, rectOK := a.bottomBarRect()
	if !rectOK {
		return
	}
	if w < 160 {
		// Typing room: grow around the title's center but stay off the
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
