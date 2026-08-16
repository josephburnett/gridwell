//go:build js && wasm

package main

import (
	"slices"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/wsbar"
)

// The bottom bar (issues #212/#220/#245): the band at the bottom of the
// ACTIVE pane — its contents are per-pane facts, so it rides the focused
// pane and unfocused panes keep their full height. It carries the ONE nav
// chain (the complete path from the root: outer chains, pane-tile
// boundary crumbs, the current chain — square previews truncated from the
// LEFT on overflow), the centered title, and the circle slot. Geometry
// comes from wsbar so the click hit-test reads the identical layout —
// render and input cannot disagree. The chain is DERIVED per frame from
// the workspace stack + each tree's own facts (pane.DescentChain);
// nothing here stores a second copy of where anything is.

// bottomBarRect returns the bar band: a RowH strip INSIDE the focused
// pane's border, flush above the bottom edge (issues #220/#223 — the
// border wraps all the way around; the band never paints over it).
// ok=false with no pane or a degenerate rect. Native surfaces on the
// focused pane carve the band out of their content boxes (panebox.BarInset)
// so they can never occlude it; their content box's bottom edge is exactly
// this band's top.
func (a *App) bottomBarRect() (x, top, w float64, ok bool) {
	p := a.tree.FocusedPane()
	if p == nil {
		return 0, 0, 0, false
	}
	r := a.paneRectByID(p.ID)
	if r.W <= 2*paneBorderPx || r.H <= wsbar.RowH+paneBorderPx {
		return 0, 0, 0, false
	}
	return r.X + paneBorderPx, r.Y + r.H - paneBorderPx - wsbar.RowH, r.W - 2*paneBorderPx, true
}

// barTheme returns the band and button shades for the focused pane: a
// subtle dark of the pane's color family for the band, the family's
// saturated hue for the buttons (issue #223). Same classifier as the pane
// border (pane.FamilyOf via borderInputFor) — one fact, two shades.
func (a *App) barTheme() (band, button string) {
	p := a.tree.FocusedPane()
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
	case pane.FamilyRoot, pane.FamilyExit:
		return "#241e12", colorPluginBorder
	case pane.FamilyEphemeral:
		return "#1d1f24", colorEphemeralBorder
	}
	return "#151b2e", colorFocusBorder
}

// navCrumb is one link of the COMPLETE nav chain (issue #245): the whole
// path from the root in one breadcrumb. Each workspace frame contributes
// its ORIGIN pane's descent chain in the outer tree it will restore, then
// the pane tile itself as a boundary crumb; the current tree's
// focused-pane chain ends it. Clicking any crumb GOES THERE — the last
// crumb is where you are, so it does nothing.
type navCrumb struct {
	// paneTile marks a workspace boundary: wsLevel is the 1-based stack
	// level, tileID the pane tile (preview square + rename target).
	paneTile bool
	wsLevel  int
	tileID   string
	// Chain crumbs: crumb is the descent-chain entry; treeLevel is the
	// workspace depth at which its tree is CURRENT (0 = the session tree,
	// Depth = the live tree) — a click pops to treeLevel, then ascends.
	treeLevel int
	crumb     pane.Crumb
	// closeOnly: the leading ROOT crumb while inside a view (owner tweak
	// 2026-08-04 on #245): its click CLOSES all views — pop to the
	// session, never an in-tree ascent (mutating a far-away tree's state
	// from the bar read badly; the session restores exactly as left).
	closeOnly bool
}

// navChain assembles the chain, outermost first: the ROOT crumb (click =
// close all views), one boundary bar per open view, then the CURRENT
// pane's full chain. The intermediate trees' tile crumbs are deliberately
// NOT shown (owner tweak 2026-08-04 on #245): clicking them mutated a
// far-away tree's state from the bar, and the last of them duplicated
// "go to the previous view" — the boundary bars already say that.
func (a *App) navChain() []navCrumb {
	var out []navCrumb
	depth := a.ws.Depth()
	if depth > 0 {
		// The root crumb wears the session origin's ROOT face (namespace
		// glyph) when it is known; a boot-restored frame has none and the
		// crumb draws as the muted placeholder. Either way the click only
		// closes views.
		root := navCrumb{treeLevel: 0, closeOnly: true}
		if f := a.ws.At(1); f != nil && f.OuterTree != nil && f.OriginPane != "" {
			if op := f.OuterTree.FindPane(f.OriginPane); op != nil {
				if chain := pane.DescentChain(op); len(chain) > 0 {
					root.crumb = chain[0]
				}
			}
		}
		out = append(out, root)
		for k := 1; k <= depth; k++ {
			f := a.ws.At(k)
			if f == nil {
				continue
			}
			out = append(out, navCrumb{paneTile: true, wsLevel: k, tileID: f.TileID})
		}
	}
	if p := a.tree.FocusedPane(); p != nil {
		for _, c := range pane.DescentChain(p) {
			out = append(out, navCrumb{treeLevel: depth, crumb: c})
		}
	}
	return out
}

// bottomBarSegments lays out the visible suffix of the chain (wsbar's
// left-truncation), relative to the band's left edge: squares for chain
// crumbs, the wide named bar for workspace boundaries.
func (a *App) bottomBarSegments(chain []navCrumb) []wsbar.Segment {
	_, _, w, ok := a.bottomBarRect()
	if !ok {
		return nil
	}
	widths := make([]float64, len(chain))
	for i, nc := range chain {
		if nc.paneTile {
			widths[i] = wsbar.BoundaryW
		} else {
			widths[i] = wsbar.RowH
		}
	}
	return wsbar.Layout(widths, w)
}

// drawBottomBar paints the band: the one nav chain, then title and slot.
func (a *App) drawBottomBar() {
	bx, top, bw, ok := a.bottomBarRect()
	if !ok {
		return
	}
	band, button := a.barTheme()
	c := a.cctx
	c.Set("fillStyle", band)
	c.Call("fillRect", bx, top, bw, wsbar.RowH)

	chain := a.navChain()
	segs := a.bottomBarSegments(chain)
	c.Set("font", "12px system-ui, sans-serif")
	c.Set("textBaseline", "middle")
	for _, s := range segs {
		shifted := s
		shifted.X += bx
		if nc := chain[s.Index]; nc.paneTile {
			a.drawBoundaryCrumb(nc.wsLevel, shifted, top)
		} else {
			a.drawChainCrumb(nc.crumb, shifted, top)
		}
	}
	c.Set("fillStyle", button)
	c.Call("fillRect", bx, top, bw, 1) // hairline above the band, kind-hued
	a.drawBarTitle(top)
	a.drawBarSlot()
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
	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	label, editable, muted = a.bubbleLabel(p)
	label = a.bubbleDecorate(p, label)
	if label == "" {
		return
	}
	bx, _, bw, rectOK := a.bottomBarRect()
	if !rectOK {
		return
	}
	a.cctx.Set("font", "12px system-ui, sans-serif")
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

// drawBarTitle paints the current pane's name centered in the band (issue
// #213 tweak 2026-07-30: a centered title, not a crumb extension). Hidden
// while the rename input replaces it in place.
func (a *App) drawBarTitle(top float64) {
	if a.renameEditing {
		return
	}
	x, w, label, _, muted, ok := a.barTitleGeom()
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

// drawBarSlot paints the bar's right-end circle for the FOCUSED pane's
// mode (issue #214, the corner circle's new home): URL descent shows back
// (live) / refresh (frozen) / the slashed no-live button; a frozen shell
// shows refresh; a grid shows the + menu button (and the trashcan during a
// tile drag). The node grid and a markdown descent draw nothing — the
// first is read-only (and must offer no drag-delete target), the second's
// slot is occupied by the DOM text-mode toggle at the same center.
func (a *App) drawBarSlot() {
	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	if p.TextFocus != "" {
		switch {
		case a.isURLDescent(p):
			if a.urlViewFor(p.ID) != nil {
				a.drawURLBackButton()
			} else if a.caps.LiveURL {
				a.drawURLRefreshButton()
			} else {
				a.drawURLOpenTabButton()
			}
		case a.isShellDescent(p) && !a.hasShellStream(p.ID):
			// Frozen shell descent: refresh either creates a fresh tmux
			// session (no snapshot yet) or attaches to the existing one.
			// Hidden when the session is gone — the JPEG is all that
			// remains. shellRefreshButtonVisible decides (and kicks off the
			// ShellSessionAlive probe if the answer isn't cached yet).
			if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
				if file, ok := g.Tiles[p.TextFocus]; ok && a.shellRefreshButtonVisible(&file) {
					a.drawURLRefreshButton()
				}
			}
		}
		return
	}
	if !a.isNodeGridPane(p) || a.tileDragInFlight() {
		a.drawPlusButton(p)
	}
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
				if tile, ok := g.Tiles[p.TextFocus]; ok && a.shellRefreshButtonVisible(&tile) {
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
				if tile, ok := g.Tiles[p.TextFocus]; ok {
					a.openURLStream(p, tile.ID)
				}
			}
		}
	case p.TextFocus != "":
		// A markdown descent's slot is the DOM toggle button, which handles
		// its own clicks; a canvas click reaching here just missed it.
	case !a.isNodeGridPane(p):
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
		return nil
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
	bx, top, bw, ok := a.bottomBarRect()
	if !ok || sy < top || sy >= top+wsbar.RowH || sx < bx || sx >= bx+bw {
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
		if seg, segOK := wsbar.At(a.bottomBarSegments(chain), sx-bx); segOK && chain[seg.Index].paneTile {
			a.openWorkspaceRenameInput(chain[seg.Index].wsLevel)
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
	if nc.paneTile || nc.closeOnly {
		// GO THERE: be inside view wsLevel — or, for the root crumb, back
		// in the session (closeOnly: views close; the session's own state
		// is never touched from the bar).
		if n := a.ws.PopCountTo(nc.wsLevel); n > 0 {
			a.ascendWorkspaceLevels(n)
		}
		return true
	}
	// A current-chain crumb: ascend within the live tree.
	if p := a.tree.FocusedPane(); p != nil {
		a.ascendToChainCrumb(p, nc.crumb)
	}
	return true
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
		if nc.paneTile && nc.wsLevel == level {
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

// ascendToChainCrumb ascends pane p back to crumb c's level: instant
// single-level ascents — each performing the SAME writebacks its animated
// twin does (text/framing saves, well-view persistence, portal root views,
// panestate pops) — until one ordinary ascent remains, which runs through
// ascendPane so the final landing keeps the familiar animation and any
// stashed-descent restore. Clicking the crumb you are already on is a no-op.
func (a *App) ascendToChainCrumb(p *pane.Pane, c pane.Crumb) {
	if !pane.DeeperThan(p, c) {
		return
	}
	// A bounded loop: each step strictly decreases the pane's depth key
	// (pane.DeeperThan's order), so this converges; the bound is a backstop.
	for i := 0; i < 64 && pane.DeeperThan(p, c); i++ {
		if pane.OneAscentReaches(p, c) {
			a.ascendPane(p)
			return
		}
		a.ascendOneLevelInstant(p)
	}
	a.refreshFileOverlay()
	a.draw()
	a.scheduleURLUpdate()
}

// ascendOneLevelInstant pops exactly one descent level with no animation:
// the intermediate step of a multi-level crumb jump. Each arm mirrors its
// animated twin's writebacks; stashed-descent restores are deliberately
// skipped — the user asked for a level ABOVE the stash origin, so the
// stashed return is consumed and discarded, not re-descended into.
func (a *App) ascendOneLevelInstant(p *pane.Pane) {
	switch {
	case p.TextFocus != "":
		a.exitTextInstant(p, false)
	case len(p.Path) > 0:
		parentPath := slices.Clone(p.Path[:len(p.Path)-1])
		if g, ok := a.c.Grid(a.gridIDForPathFrom(p.Anchor, parentPath)); ok {
			if w, ok := g.Tiles[p.Path[len(p.Path)-1]]; ok {
				well := w
				a.saveWellViewBeforeAscent(p, &well, parentPath)
			}
		}
		saved := a.popPaneState(p.ID)
		p.Path = parentPath
		if saved != nil {
			p.Cx, p.Cy, p.Zoom = saved.Cx, saved.Cy, saved.Zoom
		} else if cx, cy, zoom, ok := a.persistedGridView(p, p.Anchor, parentPath); ok {
			// Post-reload ascent: the parent's persisted framing, never an
			// arbitrary origin (see persistedGridView).
			p.Cx, p.Cy, p.Zoom = cx, cy, zoom
		} else {
			p.Cx, p.Cy, p.Zoom = 0, 0, 1.0
		}
		a.clearSelected(p.ID)
	case len(p.Up) > 0:
		f, _ := p.TopFrame()
		// The same face-#3 writeback a portal ascent performs: onto the
		// containing link tile when it resolves, else the plugin root view.
		if well := a.portalWellForFrame(p, f); well != nil {
			a.persistWellView(p, well, f.Anchor, slices.Clone(f.Path))
		} else {
			a.persistPluginRootView(p)
		}
		p.PopFrame()
		a.fetchGrid(a.gridIDForPane(p))
	}
}
