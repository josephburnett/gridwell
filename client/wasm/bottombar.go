//go:build js && wasm

package main

import (
	"slices"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/wsbar"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// The bottom bar (issue #212): the always-reserved band above the notice
// strip. It carries the workspace crumbs (named rectangles, outermost
// first) and the focused pane's descent chain (square tile previews, root
// inclusive). Geometry comes from wsbar so the click hit-test reads the
// identical layout — render and input cannot disagree. The chain is
// DERIVED per frame from the focused pane's own facts (pane.DescentChain);
// nothing here stores a second copy of where the pane is.

// bottomBarTop returns the bar band's top edge: the bar sits directly
// above the notice strip (which keeps the very bottom).
func (a *App) bottomBarTop() float64 {
	return a.height - errsurface.StripHeight(a.errs.Len()) - wsbar.Height()
}

// bottomBarChain returns the focused pane and its descent chain (nil chain
// for a boot-blank pane).
func (a *App) bottomBarChain() (*pane.Pane, []pane.Crumb) {
	p := a.tree.FocusedPane()
	if p == nil {
		return nil, nil
	}
	return p, pane.DescentChain(p)
}

// bottomBarSegments lays out the bar for the current stack + chain.
func (a *App) bottomBarSegments(chain []pane.Crumb) []wsbar.Segment {
	return wsbar.Layout(a.ws.Depth(), len(chain), a.width)
}

// drawBottomBar paints the band: workspace crumbs, then the chain squares.
func (a *App) drawBottomBar() {
	c := a.cctx
	top := a.bottomBarTop()
	c.Set("fillStyle", colorPaneTileFill)
	c.Call("fillRect", 0, top, a.width, wsbar.RowH)

	_, chain := a.bottomBarChain()
	segs := a.bottomBarSegments(chain)
	names := a.ws.Names()
	depth := a.ws.Depth()
	c.Set("font", "12px system-ui, sans-serif")
	c.Set("textBaseline", "middle")
	for _, s := range segs {
		switch s.Kind {
		case wsbar.KindWorkspace:
			// Crumb face: the current (innermost) workspace reads brightest.
			if s.Index == depth {
				c.Set("fillStyle", colorPaneTileBorder)
			} else {
				c.Set("fillStyle", "#1d4a4a")
			}
			c.Call("fillRect", s.X+2, top+3, s.W-4, wsbar.RowH-6)
			c.Set("fillStyle", "#dff4f4")
			label := names[s.Index-1]
			if label == "" {
				label = "workspace"
			}
			c.Call("save")
			c.Call("beginPath")
			c.Call("rect", s.X+2, top, s.W-4, wsbar.RowH)
			c.Call("clip")
			c.Call("fillText", label, s.X+10, top+wsbar.RowH/2)
			c.Call("restore")
		case wsbar.KindChain:
			a.drawChainCrumb(chain[s.Index], s, top)
		}
	}
	c.Set("fillStyle", "#dff4f4")
	c.Call("fillRect", 0, top, a.width, 1) // hairline above the band
	a.drawBarTitle(top)
	a.drawBarSlot()
}

// barTitleGeom computes the centered current-pane title: the pane's name
// (bubbleLabel/bubbleDecorate — the one owners), centered in the bar and
// clamped so it never overlaps the crumbs on its left or the circle slot on
// its right. Render, hit-test, and the rename input all read this one rect.
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
	a.cctx.Set("font", "12px system-ui, sans-serif")
	w = a.cctx.Call("measureText", label).Get("width").Float() + 24
	x = (a.width - w) / 2
	_, chain := a.bottomBarChain()
	segs := a.bottomBarSegments(chain)
	crumbsEnd := 0.0
	if n := len(segs); n > 0 {
		crumbsEnd = segs[n-1].X + segs[n-1].W
	}
	if x < crumbsEnd+8 {
		x = crumbsEnd + 8
	}
	if maxW := a.width - wsbar.SlotW - 8 - x; w > maxW {
		w = maxW
	}
	if w < 24 {
		return
	}
	ok = true
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
				a.drawURLNoLiveButton()
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
// prevFocus gating is unnecessary). RIGHT (or middle) click ascends one
// level — the corner circle's ascent gesture, now a plain click.
func (a *App) barSlotClick(button int) {
	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	if button == 1 || button == 2 {
		a.menu.Close()
		if a.canAscend(p) {
			a.ascendPane(p)
		}
		a.draw()
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
			// The slashed button: this host can't place a live view.
			// Explain instead of a silent dead tap (charter §6).
			a.reportErr(caps.GoLiveNotice())
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

// drawChainCrumb paints one descent-chain square: the tile's own preview
// via the same drawer the parent grid and markdown embeds use — so each
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
		a.drawPluginGlyph(a.pluginKind(cr.Anchor), x, y, side, side)
		c.Set("strokeStyle", colorFocusBorder)
		c.Set("lineWidth", 1.0)
		c.Call("strokeRect", x+0.5, y+0.5, side-1, side-1)
	} else if t := a.chainCrumbTile(cr); t != nil {
		cells := float64(max(t.W, t.H))
		if cells < 1 {
			cells = 1
		}
		r := pane.Rect{X: x, Y: y, W: side, H: side}
		a.drawNodeWithPreview(t, x, y, side, side, side/cells, r, false, false, isLinkTile(t), "")
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

// bottomBarClick consumes a click in the bar band. A workspace crumb is
// the workspace's universal handle: LEFT-click renames it inline,
// RIGHT-click LEAVES workspace k and everything deeper. A chain crumb is a
// place: LEFT-click ascends the focused pane all the way back to that
// level (issue #212). Returns true when the click was in the band, whether
// or not it hit a crumb, so the click never falls through to a pane below.
func (a *App) bottomBarClick(sx, sy float64, button int) bool {
	top := a.bottomBarTop()
	if sy < top || sy >= top+wsbar.RowH {
		return false
	}
	if sx >= a.width-wsbar.SlotW {
		a.barSlotClick(button)
		return true
	}
	// The centered title is the pane's universal handle (the old name
	// bubble's contract, issues #118/#213; buttons per the 2026-07-30
	// tweak): LEFT-click toggles the tmux-style pane zoom, RIGHT-click
	// renames what's here.
	if tx, tw, _, _, _, ok := a.barTitleGeom(); ok && sx >= tx && sx < tx+tw {
		switch button {
		case 0:
			a.togglePaneZoom()
		case 2:
			a.openRenameInput()
		}
		return true
	}
	p, chain := a.bottomBarChain()
	seg, ok := wsbar.At(a.bottomBarSegments(chain), sx)
	if !ok {
		return true
	}
	// LEFT-click ascends on every crumb kind alike (2026-07-30 tweak: one
	// gesture for "go there"); RIGHT-click renames a workspace crumb.
	switch seg.Kind {
	case wsbar.KindWorkspace:
		switch button {
		case 0:
			a.ascendWorkspaceLevels(a.ws.PopCountForCrumb(seg.Index))
		case 2:
			a.openWorkspaceRenameInput(seg.Index)
		}
	case wsbar.KindChain:
		if p != nil && button == 0 {
			a.ascendToChainCrumb(p, chain[seg.Index])
		}
	}
	return true
}

// openWorkspaceRenameInput opens the shared inline rename input over crumb
// `level` — the same input the pane name bubble uses, committing via the
// same user-owned versioned rename.
func (a *App) openWorkspaceRenameInput(level int) {
	f := a.ws.At(level)
	if f == nil {
		return
	}
	_, chain := a.bottomBarChain()
	seg, ok := wsbar.WorkspaceSegment(a.bottomBarSegments(chain), level)
	if !ok {
		return
	}
	top := a.bottomBarTop()
	a.openNameInputAt(f.Name, seg.W-28, func(st js.Value) {
		st.Set("left", pxOf(seg.X+2))
		st.Set("top", pxOf(top+4))
	}, func(val string) {
		a.commitWorkspaceRename(level, val)
	})
}

// openRenameInput swaps the centered bar title for the shared inline input
// (issue #213 — the name lives in the bar, not a pill over pane content, so
// this works identically over live views with no native help). Enter
// commits the versioned rename; Escape or blur cancels. A no-op on
// read-only contexts.
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
	if w < 160 {
		// The input needs typing room; grow around the title's center but
		// stay off the slot.
		grown := 160.0
		x = x + w/2 - grown/2
		if max := a.width - wsbar.SlotW - 8; x+grown > max {
			x = max - grown
		}
		w = grown
	}
	top := a.bottomBarTop()
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
// ascendPane so the final landing keeps the familiar animation and embed
// restore. Clicking the crumb you are already on is a no-op.
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
// animated twin's writebacks; embed-return restores are deliberately
// skipped — the user asked for a level ABOVE the embed origin, so the
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
