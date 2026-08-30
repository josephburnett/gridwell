//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/wsbar"
)

// The bottom bar (issues #212/#220/#245, #267): a band at the bottom of
// EVERY pane — its contents are per-pane facts (owner decision
// 2026-08-21, reversing #220's focused-only band: content resizing on
// every focus change was distracting; focus shows in the border color).
// It carries the ONE nav chain (the complete path from the root: outer
// chains, pane-tile boundary crumbs, the pane's chain — square previews
// truncated from the LEFT on overflow), the centered title, and the
// circle slot. Geometry comes from wsbar so the click hit-test reads the
// identical layout — render and input cannot disagree. The chain is
// DERIVED per frame from the workspace stack + each tree's own facts
// (pane.DescentChain); nothing here stores a second copy of where
// anything is. Clicks ACT only in the focused pane; in an unfocused one
// the band click moves focus, nothing else (the click-focus family).

// bottomBarRect is bottomBarRectFor of the focused pane — the band the
// rename input, the palette anchor, and the testhook read.
func (a *App) bottomBarRect() (x, top, w float64, ok bool) {
	return a.bottomBarRectFor(a.tree.FocusedPane())
}

// bottomBarRectFor returns pane p's bar band: a RowH strip INSIDE the
// pane's border, flush above the bottom edge (issues #220/#223 — the
// border wraps all the way around; the band never paints over it).
// ok=false with no pane or a degenerate rect. Native surfaces carve the
// band out of their content boxes (panebox.BarInset) so they can never
// occlude it; their content box's bottom edge is exactly this band's top.
func (a *App) bottomBarRectFor(p *pane.Pane) (x, top, w float64, ok bool) {
	if p == nil {
		return 0, 0, 0, false
	}
	r := a.paneRectByID(p.ID)
	if r.W <= 2*paneBorderPx || r.H <= wsbar.RowH+paneBorderPx {
		return 0, 0, 0, false
	}
	return r.X + paneBorderPx, r.Y + r.H - paneBorderPx - wsbar.RowH, r.W - 2*paneBorderPx, true
}

// barTheme is barThemeFor of the focused pane (the testhook's shade).
func (a *App) barTheme() (band, button string) {
	return a.barThemeFor(a.tree.FocusedPane())
}

// barThemeFor returns the band and button shades for pane p: a subtle
// dark of the pane's color family for the band, the family's saturated
// hue for the buttons (issue #223). Same classifier as the pane border
// (pane.FamilyOf via borderInputFor) — one fact, two shades.
func (a *App) barThemeFor(p *pane.Pane) (band, button string) {
	if p == nil {
		return colorBg, colorFocusBorder
	}
	g, gridOK := a.c.Grid(a.gridIDForPane(p))
	in := a.borderInputFor(p, g, gridOK, true, a.urlViewFor(p.ID) != nil)
	switch pane.FamilyOf(in) {
	case pane.FamilyText:
		return "#1b2213", colorMarkdownLine
	case pane.FamilyURL:
		return colorURLFill, colorURLLine
	case pane.FamilyURLLive:
		return colorURLFill, colorURLLiveLine
	case pane.FamilyShell:
		return colorShellFill, colorShellBorder
	case pane.FamilyExit:
		return "#241e12", colorPluginBorder
	case pane.FamilyEphemeral:
		return "#1d1f24", colorEphemeralBorder
	}
	return "#151b2e", colorFocusBorder
}

// navCrumb is pane.NavCrumb; navChain is the stack's NavChain for the
// focused pane (the decision is pure and unit-tested there).
type navCrumb = pane.NavCrumb

func (a *App) navChain() []navCrumb {
	return a.navChainFor(a.tree.FocusedPane())
}

func (a *App) navChainFor(p *pane.Pane) []navCrumb {
	return a.ws.NavChain(p)
}

func (a *App) bottomBarSegments(chain []navCrumb) []wsbar.Segment {
	return a.bottomBarSegmentsFor(a.tree.FocusedPane(), chain)
}

func (a *App) bottomBarSegmentsFor(p *pane.Pane, chain []navCrumb) []wsbar.Segment {
	_, _, w, ok := a.bottomBarRectFor(p)
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

// drawBottomBars paints every leaf pane's band (#267): the pane's nav
// chain, title, and slot — the same bar everywhere, so content never
// resizes when focus moves.
func (a *App) drawBottomBars() {
	pane.WalkLeaves(a.tree.Root, func(p *pane.Pane) { a.drawBottomBarFor(p) })
}

// drawBottomBarFor paints pane p's band: the one nav chain, then title
// and slot.
func (a *App) drawBottomBarFor(p *pane.Pane) {
	bx, top, bw, ok := a.bottomBarRectFor(p)
	if !ok {
		return
	}
	band, button := a.barThemeFor(p)
	c := a.cctx
	c.Set("fillStyle", band)
	c.Call("fillRect", bx, top, bw, wsbar.RowH)

	chain := a.navChainFor(p)
	segs := a.bottomBarSegmentsFor(p, chain)
	c.Set("font", "12px system-ui, sans-serif")
	c.Set("textBaseline", "middle")
	for _, s := range segs {
		shifted := s
		shifted.X += bx
		if nc := chain[s.Index]; nc.PaneTile {
			a.drawBoundaryCrumb(nc.WsLevel, shifted, top)
		} else {
			a.drawChainCrumb(nc.Crumb, shifted, top)
		}
	}
	c.Set("fillStyle", button)
	c.Call("fillRect", bx, top, bw, 1) // hairline above the band, kind-hued
	a.drawBarTitleFor(p, top)
	a.drawStaleChipFor(p, bx, top, bw)
	a.drawBarSlotFor(p)
}

// drawStaleChipFor marks a pane whose grid is a cache-served MEMORY (the
// wire-level stale bit, issue #256): a small amber chip at the bar's
// right, beside the slot. Bar chrome only — staleness must never move or
// restyle tiles; the room renders exactly as remembered, and this is the
// one quiet sign it is a remembering.
func (a *App) drawStaleChipFor(p *pane.Pane, bx, top, bw float64) {
	if p == nil {
		return
	}
	g, ok := a.c.Grid(a.gridIDForPane(p))
	if !ok || !g.Meta.Stale {
		return
	}
	const chipW, chipH = 52.0, 16.0
	x := bx + bw - wsbar.SlotW - chipW - 8
	y := top + (wsbar.RowH-chipH)/2
	c := a.cctx
	c.Set("fillStyle", "#8a6d2f")
	c.Call("fillRect", x, y, chipW, chipH)
	c.Set("fillStyle", "#f4e3b2")
	c.Set("font", "10px system-ui, sans-serif")
	c.Set("textAlign", "center")
	c.Set("textBaseline", "middle")
	c.Call("fillText", "offline", x+chipW/2, y+chipH/2)
	c.Set("textAlign", "start")
}

// drawBoundaryCrumb paints a workspace-boundary crumb as the light-blue
// NAMED bar (owner tweak 2026-08-04 on #245: a tile-preview square didn't
// stand out — this crumb is the thing you're working on, a whole desktop
// of state, and the wide face is the obvious rename target). The current
// (innermost) workspace reads brightest.
func (a *App) drawBoundaryCrumb(level int, s wsbar.Segment, top float64) {
	c := a.cctx
	if level == a.ws.Depth() {
		c.Set("fillStyle", colorPaneTileBorder)
	} else {
		c.Set("fillStyle", "#1d4a4a")
	}
	c.Call("fillRect", s.X+2, top+3, s.W-4, wsbar.RowH-6)
	label := ""
	if f := a.ws.At(level); f != nil {
		label = f.Name
	}
	if label == "" {
		label = "workspace"
	}
	c.Set("fillStyle", "#dff4f4")
	c.Call("save")
	c.Call("beginPath")
	c.Call("rect", s.X+2, top, s.W-4, wsbar.RowH)
	c.Call("clip")
	c.Call("fillText", label, s.X+10, top+wsbar.RowH/2)
	c.Call("restore")
}

// barTitleGeom computes the centered current-pane title: the pane's name
// (bubbleLabel/bubbleDecorate — the one owners), centered by wsbar.TitleSpan
// in the free space between the crumbs and the circle slot (issue #230).
// Render, hit-test, and the rename input all read this one rect.
func (a *App) barTitleGeom() (x, w float64, label string, editable, muted, ok bool) {
	return a.barTitleGeomFor(a.tree.FocusedPane())
}

func (a *App) barTitleGeomFor(p *pane.Pane) (x, w float64, label string, editable, muted, ok bool) {
	if p == nil {
		return
	}
	label, editable, muted = a.bubbleLabel(p)
	label = a.bubbleDecorate(p, label)
	if label == "" {
		return
	}
	bx, _, bw, rectOK := a.bottomBarRectFor(p)
	if !rectOK {
		return
	}
	a.cctx.Set("font", "12px system-ui, sans-serif")
	textW := a.cctx.Call("measureText", label).Get("width").Float() + 24
	segs := a.bottomBarSegmentsFor(p, a.navChainFor(p))
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

// drawBarTitleFor paints pane p's name centered in its band (issue #213
// tweak 2026-07-30: a centered title, not a crumb extension). Hidden in
// the focused pane while the rename input replaces it in place.
func (a *App) drawBarTitleFor(p *pane.Pane, top float64) {
	if a.renameEditing && p != nil && p.ID == a.tree.Focus {
		return
	}
	x, w, label, _, muted, ok := a.barTitleGeomFor(p)
	if !ok {
		return
	}
	c := a.cctx
	color := "#dff4f4"
	if muted {
		color = colorMuted
	}
	c.Set("fillStyle", color)
	c.Set("font", "12px system-ui, sans-serif")
	c.Set("textBaseline", "middle")
	c.Set("textAlign", "center")
	c.Call("save")
	c.Call("beginPath")
	c.Call("rect", x, top, w, wsbar.RowH)
	c.Call("clip")
	c.Call("fillText", label, x+w/2, top+wsbar.RowH/2)
	c.Call("restore")
	c.Set("textAlign", "start")
}

// drawBarSlotFor paints the bar's right-end circle for pane p's mode
// (issue #214, the corner circle's new home): URL descent shows back
// (live) / refresh (frozen) / the slashed no-live button; a frozen shell
// shows refresh; a grid shows the + menu button (and the trashcan during a
// tile drag). A markdown descent draws nothing — its slot is occupied by
// the DOM text-mode toggle at the same center.
func (a *App) drawBarSlotFor(p *pane.Pane) {
	if p == nil {
		return
	}
	if p.ContentID() != "" {
		switch {
		case a.isURLDescent(p):
			if a.urlViewFor(p.ID) != nil {
				a.drawURLBackButton(p)
			} else if a.caps.LiveURL {
				a.drawURLRefreshButton(p)
			} else {
				a.drawURLOpenTabButton(p)
			}
		case a.isShellDescent(p) && !a.hasShellStream(p.ID):
			// Frozen shell descent: refresh either creates a fresh tmux
			// session (no snapshot yet) or attaches to the existing one.
			// Hidden when the session is gone — the JPEG is all that
			// remains. shellRefreshButtonVisible decides (and kicks off the
			// ShellSessionAlive probe if the answer isn't cached yet).
			if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
				if file, ok := g.Tiles[p.ContentID()]; ok && a.shellRefreshButtonVisible(&file) {
					a.drawURLRefreshButton(p)
				}
			}
		}
		return
	}
	a.drawPlusButton(p)
}

// barSlotClick dispatches a click on the bar's circle slot, always acting
// on the FOCUSED pane (the slot never transfers focus, so the old
// prevFocus gating is unnecessary). LEFT-click only: the ascent gesture is
// clicking the previous crumb (owner decision 2026-07-30, issue #222 —
// the old right-click-to-ascend on the slot and on empty bar space are
// gone; middle-click on a pane remains the in-pane shortcut).
func (a *App) barSlotClick(button int) {
	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	if button != 0 {
		return
	}
	switch {
	case a.isShellDescent(p):
		if !a.hasShellStream(p.ID) {
			if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
				if tile, ok := g.Tiles[p.ContentID()]; ok && a.shellRefreshButtonVisible(&tile) {
					a.openShellStream(p, tile.ID)
				}
			}
		}
	case a.isURLDescent(p):
		if a.urlViewFor(p.ID) != nil {
			bridgeGoBack(p.ID)
		} else if !a.caps.LiveURL {
			// A browser host can't place a live view; the next-best descent
			// is the browser's own: open the address in a NEW TAB (owner
			// decision 2026-08-09). The tile stays frozen and untouched —
			// nothing is persisted by this gesture. Synchronous within the
			// click, so the popup rides the user-gesture allowance.
			a.openURLInNewTab(p)
		} else {
			// Frozen: go live (place the native view).
			if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
				if tile, ok := g.Tiles[p.ContentID()]; ok {
					a.openURLStream(p, tile.ID)
				}
			}
		}
	case p.ContentID() != "":
		// A markdown descent's slot is the DOM toggle button, which handles
		// its own clicks; a canvas click reaching here just missed it.
	default:
		a.menu.Toggle(p.ID)
		a.draw()
	}
}

// openURLInNewTab opens the focused pane's web-content address in a new
// browser tab — the frozen-host answer to "descend live". The address is
// the tile's own: a url tile's frozen URLString or a serves_page tile's
// derived /content/ door URL (a link resolves to its target's); a tile
// with no address yet says so instead of a silent dead tap (§6).
func (a *App) openURLInNewTab(p *pane.Pane) {
	t, ok := a.descendedTile(p)
	if !ok {
		return
	}
	url := a.webAddress(&t)
	if url == "" {
		if ct := a.cachedTileByID(a.contentKey(t.ID)); ct != nil {
			url = a.webAddress(ct)
		}
	}
	if url == "" {
		a.reportErr(errsurface.Info, "urlopen", "this url tile has no address yet")
		return
	}
	js.Global().Get("window").Call("open", url, "_blank", "noopener")
}

// drawChainCrumb paints one descent-chain square: the tile's own preview
// via the same drawer the parent grid uses — so each
// crumb carries its GRID appearance, kind border included (blue wells,
// text green, url purple…; 2026-07-30 tweak). A 1px margin is the only
// chrome: the rightmost crumb is always the current one, so it needs no
// highlight.
func (a *App) drawChainCrumb(cr pane.Crumb, s wsbar.Segment, top float64) {
	c := a.cctx
	square := min(s.W, wsbar.RowH)
	side := square - 2
	if side < 4 {
		return
	}
	x := s.X + (square-side)/2
	y := top + (wsbar.RowH-side)/2

	c.Call("save")
	c.Call("beginPath")
	c.Call("rect", x, y, side, side)
	c.Call("clip")
	if cr.Anchor != "" {
		// A root crumb: the namespace's identity glyph — the same drawing as
		// its menu swatch — bordered in the grid blue, like the grid it is.
		c.Set("fillStyle", colorBg)
		c.Call("fillRect", x, y, side, side)
		a.drawPluginGlyph(a.pluginGlyph(cr.Anchor), x, y, side, side)
		c.Set("strokeStyle", colorFocusBorder)
		c.Set("lineWidth", 1.0)
		c.Call("strokeRect", x+0.5, y+0.5, side-1, side-1)
	} else if t := a.chainCrumbTile(cr); t != nil {
		cells := float64(max(t.W, t.H))
		if cells < 1 {
			cells = 1
		}
		a.drawNodeWithPreview(t, x, y, side, side, side/cells, false, false, isLinkTile(t), "")
	} else {
		// Row not cached (a stale portal level, a fetch in flight): a muted
		// placeholder square; the fetch kicked by chainCrumbTile fills it in.
		c.Set("strokeStyle", "#1d4a4a")
		c.Set("lineWidth", 1.0)
		c.Call("strokeRect", x+1, y+1, side-2, side-2)
	}
	c.Call("restore")
}

// chainCrumbTile resolves a tile crumb's row from the cache, kicking a
// fetch of its containing grid on a miss.
func (a *App) chainCrumbTile(cr pane.Crumb) *rpc.Tile {
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
		// An EPHEMERAL visit lives in the scratch grid, not the pane's:
		// resolve it by id so the crumb shows its live face (it drew a
		// grey placeholder until 2026-08-27).
		return a.findTileByID(cr.TileID)
	}
	return &t
}

// bottomBarClick consumes a click in the bar band. Every crumb of the one
// nav chain (issue #245) answers LEFT-click with GO THERE: a pane-tile
// crumb lands you INSIDE that workspace (deeper ones close; the current
// boundary is where you are — a no-op), and a chain crumb pops to its
// tree and ascends within it — one verb, whether the target is above,
// beside, or outside the current workspace. RIGHT-click renames (the
// title, or a pane-tile crumb's workspace) — and on the circle slot of a
// LIVE url descent it pops the view's context menu (Freeze Page): the
// page can hijack contextmenu inside the view, but the circle sits on
// the canvas, so this door always opens. Left clicks in the band never
// fall through to a pane below; right clicks outside those surfaces DO,
// so the pane border gestures under the band stay reachable (#220).
func (a *App) bottomBarClick(sx, sy float64, button int) bool {
	// Every pane wears a band (#267); resolve the one under the cursor.
	bp, _, pOK := a.paneAtScreen(sx, sy)
	if !pOK {
		return false
	}
	bx, top, bw, ok := a.bottomBarRectFor(bp)
	if !ok || sy < top || sy >= top+wsbar.RowH || sx < bx || sx >= bx+bw {
		return false
	}
	if bp.ID != a.tree.Focus {
		// A band click in an UNFOCUSED pane moves focus, nothing else —
		// the same click-focus family as pane content (DropFocusOnly).
		// Right clicks fall through so border gestures stay reachable.
		if button == 0 {
			a.focusToPane(bp)
			return true
		}
		return false
	}
	chain := a.navChain()
	if button == 2 {
		if sx >= bx+bw-wsbar.SlotW {
			if p := a.tree.FocusedPane(); p != nil && a.isURLDescent(p) && a.urlViewFor(p.ID) != nil {
				bridgeShowMenu(p.ID)
				return true
			}
			return false
		}
		if tx, tw, _, _, _, tOK := a.barTitleGeom(); tOK && sx >= tx && sx < tx+tw {
			a.openRenameInput()
			return true
		}
		if seg, segOK := wsbar.At(a.bottomBarSegments(chain), sx-bx); segOK && chain[seg.Index].PaneTile {
			a.openWorkspaceRenameInput(chain[seg.Index].WsLevel)
			return true
		}
		return false
	}
	if sx >= bx+bw-wsbar.SlotW {
		a.barSlotClick(button)
		return true
	}
	// The centered title is the pane's universal handle (the old name
	// bubble's contract, issues #118/#213): LEFT-click toggles the
	// tmux-style pane zoom (right-click rename was handled above).
	if tx, tw, _, _, _, ok := a.barTitleGeom(); ok && sx >= tx && sx < tx+tw {
		if button == 0 {
			a.togglePaneZoom()
		}
		return true
	}
	seg, segOK := wsbar.At(a.bottomBarSegments(chain), sx-bx)
	if !segOK {
		return true // empty band space swallows clicks (no gesture, #222)
	}
	if button != 0 {
		return true
	}
	nc := chain[seg.Index]
	if nc.PaneTile || nc.CloseOnly {
		// GO THERE: be inside view wsLevel — or, for the root crumb, back
		// in the session (closeOnly: views close; the session's own state
		// is never touched from the bar).
		a.ascendLevels(a.ws.PopCountTo(nc.WsLevel))
		return true
	}
	// The CURRENT crumb of an ephemeral url visit is a drag handle: onto
	// another pane's grid it PROMOTES the visit to a persistent tile
	// there (2026-08-27). Armed here on the press; onMouseUp decides
	// click (nothing — this is where you are) vs drop.
	if seg.Index == len(chain)-1 {
		if p := a.tree.FocusedPane(); p != nil {
			if t, ok := a.descendedTile(p); ok && t.Kind == rpc.KindURL && a.isEphemeralTile(p, &t) {
				a.startPromoteDrag(p, t, seg, bx, top, sx, sy)
				return true
			}
		}
	}
	// A current-chain crumb: ascend the focused pane to that level. How many
	// ascents that is, is the crumb's own arithmetic (pane.AscentsTo) — the
	// last hop animates, the ones above it are instant.
	if p := a.tree.FocusedPane(); p != nil {
		a.ascend(p, p.AscentsTo(nc.Crumb), true)
	}
	return true
}

// startPromoteDrag arms the promote drag from the bar's current crumb:
// a template-shaped drag (the drop creates a tile) whose item carries the
// origin pane, ghosting the visit's own url tile at the crumb's square.
func (a *App) startPromoteDrag(p *pane.Pane, t rpc.Tile, seg wsbar.Segment, bx, top, sx, sy float64) {
	square := min(seg.W, wsbar.RowH)
	ghost := t
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
// pane-tile crumb of workspace `level` — the same input the pane name
// bubble uses, committing via the same user-owned versioned rename. The
// crumb is a square now (issue #245), so the input grows rightward from
// it to a typeable width, clamped off the slot.
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
		return // truncated off the left edge; rename via the title instead
	}
	bx, top, _, rectOK := a.bottomBarRect()
	if !rectOK {
		return
	}
	a.openNameInputAt(f.Name, seg.W-28, func(st js.Value) {
		st.Set("left", pxOf(bx+seg.X+2))
		st.Set("top", pxOf(top+4))
	}, func(val string) {
		a.commitWorkspaceRename(level, val)
	})
}

// openRenameInput swaps the centered bar title for the shared inline input
// (issue #213 — the name lives in the bar, not a pill over pane content, so
// this works identically over live views with no native help). Enter or
// blur commits the versioned rename; Escape cancels. A no-op on read-only
// contexts.
func (a *App) openRenameInput() {
	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	target, ok := a.renameTarget(p)
	if !ok {
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
		// The input needs typing room; grow around the title's center but
		// stay off the slot.
		grown := 160.0
		x = x + w/2 - grown/2
		if max := bx + bw - wsbar.SlotW - 8; x+grown > max {
			x = max - grown
		}
		w = grown
	}
	tileID := target.ID
	a.openNameInputAt(target.AltText, w-24, func(st js.Value) {
		st.Set("left", pxOf(x))
		st.Set("top", pxOf(top+4))
	}, func(val string) {
		a.commitRename(tileID, val)
	})
}
