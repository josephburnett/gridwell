//go:build js && wasm

package main

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"math"
	"strconv"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/palette"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/tilebanner"
	"github.com/josephburnett/gridwell/client/wsbar"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

const (
	colorBg          = "#0c0d11"
	colorFileInnerBg = "#1c1f26"
	// Error rows read as alarm; Info rows, an expected reconciliation, take the
	// focus-blue family so they read as a note.
	colorErrStripBg    = "#3a1216"
	colorErrStripText  = "#ff9a9a"
	colorInfoStripBg   = "#16203a"
	colorInfoStripText = "#9ab0ff"
	colorPaneBorder    = "#1f2229"
	colorFocusBorder   = "#4a6fff"
	// colorFocusBorderFaded is the outline for a descended but unfocused pane,
	// so the focused one pops while the others stay visibly inside something.
	colorFocusBorderFaded = "#2c3d70"
	// colorPluginBorder is the warm brown of plugin and host identity: an
	// earth-tone ground reading as a boundary, not a grid you can place in.
	colorPluginBorder      = "#7a6a4a"
	colorPluginBorderFaded = "#4a4233"
	// colorPluginFill is host content's body, so it does not read as editable.
	colorPluginFill = "#2a2419"
	// Grid lines are uniformly blue: every grid is a grid, whatever owns it.
	colorGridLineInterior = "#1c2540"
	// Each kind has its own identity: text olive green, url purple.
	colorMarkdownFill      = "#2c3a1a"
	colorMarkdownLine      = "#8aa05a"
	colorMarkdownLineFaded = "#4a5a3a"
	colorURLFill           = "#2b1a3a"
	colorURLLine           = "#7a5a9a"
	colorURLLineFaded      = "#4a3a5a"
	// colorURLLiveLine is a url tile with a native view attached: the same
	// purple, brighter, and its faded variant stays brighter than the frozen
	// one, so live against frozen reads across unfocused panes.
	colorURLLiveLine      = "#a07acc"
	colorURLLiveLineFaded = "#5c4478"
	// colorShellBorder: bash runs outside Gridwell's data world, so it gets its
	// own warm hue, not plugin brown.
	colorShellBorder      = "#d4863a"
	colorShellBorderFaded = "#6e4a22"
	// colorShellFill is the body behind a shell tile's preview or glyph.
	colorShellFill = "#2e220f"

	// Pane tiles: teal, a hue no other kind uses.
	colorPaneTileFill   = "#10282b"
	colorPaneTileBorder = "#3aa8a8"
	// colorEphemeralBorder overrides the kind color, because ascending deletes
	// the tile, a shell's tmux session included, and the border is the warning.
	colorEphemeralBorder      = "#8b8e96"
	colorEphemeralBorderFaded = "#4b4d52"
	// colorSourceLabelBg is dark enough that a label reads over any preview.
	colorSourceLabelBg = "rgba(20, 12, 8, 0.78)"
	// colorNoEntry{Fill,Stroke} draw the no-entry badge on a rejected drop.
	colorNoEntryFill   = "#c93030"
	colorNoEntryStroke = "#f6f6f6"
	colorLocked        = "#26262a"
	colorSelected      = "#e3b16f"
	// colorTrace is brighter than the gold selection, so both can show at once.
	colorTrace    = "#ffd94a"
	colorEdgeDot  = "#5a6a8a"
	colorPlusBg   = "#23252d"
	colorPlusBgHi = "#2d3140"
	// colorPlusBgDelete confirms that a release over the trashcan deletes.
	colorPlusBgDelete = "#6e2b22"
	colorPlusFg       = "#c8c9ce"
	// colorNoLiveFg dims the slashed go-live glyph where caps.LiveURL is false.
	colorNoLiveFg   = "#787b84"
	colorMenuBg     = "#16181f"
	colorMenuItemHi = "#e8e9ee"
	colorMuted      = "#6c6f78"
	// Launcher tints for a non-enterable plugin (client/pluginhealth). Broken,
	// every failure whatever the reason, takes the red alarm family; waiting
	// takes neutral gray, because nothing has gone wrong yet.
	colorLauncherBrokenTint  = "rgba(180, 40, 40, 0.38)"
	colorLauncherWaitingTint = "rgba(40, 40, 46, 0.55)"
	// A dead link (client/deadref) is a state, not a failure, so it gets no
	// alarm color: the veil fades the tile back toward the background, and the
	// outline and label are redrawn muted, keeping the dash and the name.
	colorDeadLinkVeil = "rgba(12, 13, 17, 0.72)"
	colorDeadLink     = "#5c5f68"
)

const (
	// paneBorderPx is the inset around a pane's live content view. It is
	// load-bearing: the strip between two live-tile panes is the only surface
	// that can grab the divider, since a WebContentsView eats input over its own.
	paneBorderPx = panebox.LiveViewInsetPx
	// tileBorderPx sits entirely inside the tile rect, so the banner label and
	// the neighbouring cell cannot overlap it.
	tileBorderPx = 2.0
)

// withClip writes the save/clip/restore sequence once, so a paint that returns
// early can never leave an unbalanced save behind.
func withClip(c js.Value, x, y, w, h float64, paint func()) {
	c.Call("save")
	c.Call("beginPath")
	c.Call("rect", x, y, w, h)
	c.Call("clip")
	paint()
	c.Call("restore")
}

// strokeTileBorder keeps the outline entirely inside (x, y, w, h): canvas
// centers a stroke, so the rect is inset by half the line width.
func strokeTileBorder(c js.Value, x, y, w, h float64, color string, borderPx float64) {
	c.Set("strokeStyle", color)
	c.Set("lineWidth", borderPx)
	half := borderPx / 2
	c.Call("strokeRect", x+half, y+half, w-borderPx, h-borderPx)
}

// previewBorderPxFor keeps a child-grid preview's borders proportional at a
// distance. previewCell is one child cell in px.
func previewBorderPxFor(previewCell float64) float64 {
	const ref = cellPx // full-zoom parent-cell reference
	bp := tileBorderPx * previewCell / ref
	if bp > tileBorderPx {
		return tileBorderPx
	}
	if bp < 0.5 {
		return 0.5
	}
	return bp
}

// drawSelectedTileOutline sits just outside the cell, so it is independent of
// the kind-specific border.
func drawSelectedTileOutline(c js.Value, x, y, w, h float64) {
	c.Set("strokeStyle", colorSelected)
	c.Set("lineWidth", 2.0)
	c.Call("strokeRect", x-1, y-1, w+2, h+2)
	c.Set("lineWidth", 1.0)
}

// strokeTileFrame is the coda every full-size tile renderer ends with, in one
// order for all of them, so no renderer can drift into a different one.
func strokeTileFrame(c js.Value, x, y, w, h float64, color string, dashed, selected bool) {
	if dashed {
		setTileDash(c)
	}
	strokeTileBorder(c, x, y, w, h, color, tileBorderPx)
	if dashed {
		clearTileDash(c)
	}
	if selected {
		drawSelectedTileOutline(c, x, y, w, h)
	}
}

// drawDeadLinkFace paints over the tile already drawn: nothing is asked for a
// dead link and nothing is said about it, so the tile carries the news alone.
func (a *App) drawDeadLinkFace(n *gridwellv1.Tile, x, y, w, h float64) {
	if !a.deadLink(n) {
		return
	}
	a.cctx.Set("fillStyle", colorDeadLinkVeil)
	a.cctx.Call("fillRect", x, y, w, h)
	strokeTileFrame(a.cctx, x, y, w, h, colorDeadLink, true, false)
	a.drawTileBannerLabelIn(n, x, y, w, h, colorDeadLink)
}

// drawTraceOutline is the selection outline's geometry, thicker and faded.
func drawTraceOutline(c js.Value, x, y, w, h, alpha float64) {
	if alpha <= 0 {
		return
	}
	c.Call("save")
	c.Set("globalAlpha", alpha)
	c.Set("strokeStyle", colorTrace)
	c.Set("lineWidth", 3.0)
	c.Call("strokeRect", x-2, y-2, w+4, h+4)
	c.Call("restore")
}

// plusButtonRadius is read once from its owner, palette.Default(), so the
// canvas draws and the DOM toggle cannot disagree about the button's size.
var plusButtonRadius = palette.Default().PlusRadius

// templateKind identifies one built-in tile primitive. Order matters:
// primitiveKinds sets the popover layout and the hit-test indices.
type templateKind int

const (
	tplWell templateKind = iota
	tplMarkdown
	tplURL
	// tplShell starts frozen; the user refreshes to spawn the PTY.
	tplShell
	// tplPane is a stored split-pane layout you descend into, created
	// never-arranged, so the first descent installs the default single pane.
	tplPane
)

// primitive is one row per built-in kind, so a kind cannot be half-added and
// every reader derives from the same order.
type primitive struct {
	kind  templateKind
	name  string
	ghost *gridwellv1.Tile
	glyph func(a *App, x, y, w, h float64)
	// create fires into gridID, the drop target's grid, which is an open well's
	// child grid when the cursor promoted into one, never re-derived here.
	create func(a *App, gridID string, cellX, cellY int64)
	// click is what a bare click on the swatch does, with no destination cell.
	// It is a column rather than an arm per kind, because a kind with no arm
	// let the click fall through to the canvas behind the popover.
	click func(a *App, p *pane.Pane)
}

// primitives is the palette layout order, left to right; primitiveKinds is its
// order, derived, never a second list. Both fill in init because the create and
// glyph rows close over App methods, and that reference graph is a cycle.
var (
	primitives     []primitive
	primitiveKinds []templateKind
)

func init() {
	primitives = []primitive{
		{
			kind: tplWell, name: "well",
			ghost:  &gridwellv1.Tile{Kind: rpc.KindWell, W: 1, H: 1},
			glyph:  func(a *App, x, y, w, h float64) { drawWellGlyph(a.cctx, x, y, w, h, colorFocusBorder) },
			create: func(a *App, gid string, cellX, cellY int64) { a.createWellAtCell(gid, cellX, cellY) },
		},
		{
			kind: tplMarkdown, name: "markdown",
			ghost:  &gridwellv1.Tile{Kind: rpc.KindText, W: 1, H: 1},
			glyph:  func(a *App, x, y, w, h float64) { drawDocumentGlyph(a.cctx, x, y, w, h, colorMarkdownLine) },
			create: func(a *App, gid string, cellX, cellY int64) { a.createTextAtCell(gid, []byte{}, cellX, cellY) },
		},
		{
			kind: tplURL, name: "url",
			ghost:  &gridwellv1.Tile{Kind: rpc.KindURL, W: 1, H: 1},
			glyph:  func(a *App, x, y, w, h float64) { drawGlobeGlyph(a.cctx, x, y, w, h, colorURLLine) },
			create: func(a *App, gid string, cellX, cellY int64) { a.createURLAtCell(gid, cellX, cellY) },
			click:  func(a *App, p *pane.Pane) { a.visitURLFromMenu(p) },
		},
		{
			kind: tplShell, name: "shell",
			ghost:  &gridwellv1.Tile{Kind: rpc.KindShell, W: 1, H: 1, AltText: "shell"},
			glyph:  func(a *App, x, y, w, h float64) { drawShellGlyph(a.cctx, x, y, w, h, colorShellBorder) },
			create: func(a *App, gid string, cellX, cellY int64) { a.createShellAtCell(gid, cellX, cellY) },
			click:  func(a *App, p *pane.Pane) { a.visitShellFromMenu(p) },
		},
		{
			kind: tplPane, name: "pane",
			ghost:  &gridwellv1.Tile{Kind: rpc.KindPane, W: 1, H: 1, AltText: "workspace"},
			glyph:  func(a *App, x, y, w, h float64) { drawPaneGlyph(a.cctx, x, y, w, h, colorPaneTileBorder) },
			create: func(a *App, gid string, cellX, cellY int64) { a.createPaneAtCell(gid, cellX, cellY) },
		},
	}
	primitiveKinds = make([]templateKind, len(primitives))
	for i, pr := range primitives {
		primitiveKinds[i] = pr.kind
	}
}

// primitiveFor's not-ok is an unknown kind; every caller does nothing for it.
func primitiveFor(k templateKind) (primitive, bool) {
	for _, pr := range primitives {
		if pr.kind == k {
			return pr, true
		}
	}
	return primitive{}, false
}

// paletteItem is a configured plugin or a built-in primitive; isPlugin says
// which of the two fields carries meaning.
type paletteItem struct {
	isPlugin  bool
	plugin    *gridwellv1.PluginInfo // when isPlugin (also set for a root ENTRY's owner)
	primitive templateKind           // when !isPlugin
	// entry is the declared menu entry this pseudo-plugin swatch came from.
	entry *gridwellv1.MenuEntry
	// promotePane marks a promote drag: the ephemeral url visit shown in that
	// pane, dragged off the bar's crumb, whose drop creates a persistent tile.
	promotePane string
}

// paletteItems returns pane p's palette entries in display order: the doorway
// section, then the tile primitives where the grid is writable. A folded
// section contributes no items, and every consumer reads this one list, so a
// swatch that is not shown cannot be clicked either.
func (a *App) paletteItems(p *pane.Pane) []paletteItem {
	items, _ := a.paletteView(p)
	return items
}

// paletteView returns the palette's items and the section decision they were
// composed under. palette.Show is the one owner of that decision.
func (a *App) paletteView(p *pane.Pane) ([]paletteItem, palette.Shown) {
	plugins, primitives := a.paletteGroups(p)
	show := palette.Show(palette.Section{
		Plugins:    len(plugins),
		Primitives: len(primitives),
		Expanded:   a.menu.PluginsExpanded(),
	})
	if !show.Plugins {
		plugins = nil
	}
	return append(plugins, primitives...), show
}

// paletteGroups builds the two groups unfiltered by the fold, so the counts
// palette.Show sees are what the node declares.
func (a *App) paletteGroups(p *pane.Pane) (plugins, primitives []paletteItem) {
	// The menu belongs to the pane's node: a remote pane's top row is what a
	// direct client of that node sees. "", local or uncached, is the handshake.
	ctx := a.menuCtx(p)
	// palette.Doorways is the one owner of the composition: a home is a place
	// and gets a row; a plugin is not, and gets a swatch per collection. Every
	// swatch is a pseudo-plugin, so every downstream flow is one path.
	sw := palette.Doorways(ctx.plugins)
	items := make([]paletteItem, 0, len(sw))
	for _, s := range sw {
		items = append(items, paletteItem{isPlugin: true, plugin: s.Plugin, entry: s.Entry})
	}
	prims := make([]paletteItem, 0, len(primitiveKinds))
	// Unknown is not writable: no swatch on a guess a click would then refuse.
	if writable, _ := a.gridWritable(a.gridIDForPane(p)); writable {
		for _, k := range primitiveKinds {
			// The shell swatch obeys the context node's policy alone: the PTY
			// rides the web door, so local caps would be a second, wrong owner.
			if k == tplShell && ctx.shellsDisabled {
				continue
			}
			prims = append(prims, paletteItem{primitive: k})
		}
	}
	return items, prims
}

// paletteTopRow is the layout's row split, derived from the one item list.
func paletteTopRow(items []paletteItem) int {
	n := 0
	for _, it := range items {
		if it.isPlugin {
			n++
		}
	}
	return n
}

// ghostSizeLerpAlpha gives about a 120 ms time constant at 60 fps.
const ghostSizeLerpAlpha = 0.20

// draw clears and redraws every pane fully, so it is cheap to call repeatedly.
func (a *App) draw() {
	if a.ghost != nil {
		// Snap when close enough, so the ghost does not jitter at the target.
		ds := a.ghost.displayedCellSize
		ts := a.ghost.targetCellSize
		if ts > 0 && math.Abs(ts-ds) > 0.5 {
			a.ghost.displayedCellSize = ds + (ts-ds)*ghostSizeLerpAlpha
			a.scheduleFrame()
		} else if ts > 0 {
			a.ghost.displayedCellSize = ts
		}
		// Drag onto a black hole and the ghost shatters in; drag out and it
		// reassembles.
		df := a.ghost.displayedFragmentation
		tf := a.ghost.targetFragmentation
		if math.Abs(tf-df) > 0.01 {
			a.ghost.displayedFragmentation = df + (tf-df)*ghostSizeLerpAlpha
			a.scheduleFrame()
		} else {
			a.ghost.displayedFragmentation = tf
		}
	}

	a.cctx.Set("fillStyle", colorBg)
	a.cctx.Call("fillRect", 0, 0, a.width, a.height)

	rects := a.layoutPanes()
	for paneID, r := range rects {
		p := a.tree.FindPane(paneID)
		if p == nil {
			continue
		}
		a.drawPane(p, r)
	}

	// Last, so a neighbour it overflows into cannot paint over it.
	if a.menu.IsOpen() {
		if mp := a.tree.FindPane(a.menu.PaneID()); mp != nil {
			if _, ok := rects[mp.ID]; ok {
				a.drawPalette(mp)
			}
		}
	}

	// Above every pane, below the DOM overlays.
	if a.rightDrag != nil {
		a.drawRightDragPreview()
	}
	// The layout crushes live; this adds the release-closes-this-side warning.
	if a.leftResize != nil {
		a.drawLeftResizePreview(a.leftResize)
	}

	// Every overlay tracks its pane's rect per frame; native views also park
	// off-screen during a canvas gesture.
	a.syncTextOverlayPosition()
	a.refreshRenderedOverlay()
	a.syncShellOverlayPosition()
	a.syncURLViews()
	// The reserved bottom bands, drawn last so nothing paints over them.
	a.drawBottomBar()
	a.drawErrStrip()
	// Inside a pane tile, a teal line wraps the window. It owns a reserved
	// gutter, since rootLayoutRect insets the panes by wsOutlinePx.
	if a.ws.Depth() > 0 {
		// The same height the layout used, so the outline stays off the bands.
		h := a.paneAreaH()
		a.cctx.Set("strokeStyle", colorPaneTileBorder)
		a.cctx.Set("lineWidth", wsOutlinePx)
		a.cctx.Call("strokeRect", wsOutlinePx/2, wsOutlinePx/2,
			a.width-wsOutlinePx, h-wsOutlinePx)
		a.cctx.Set("lineWidth", 1.0)
	}
	// The first-descent capture: the pane tile's face growing into the level
	// outline. Its end rect is that outline, so the handoff is seamless.
	if e := a.overlays.wsExpand; e != nil {
		t := (nowMs() - e.startMs) / totalTransitionMs
		if t > 1 {
			t = 1
		}
		k := anim.EaseOutCubic(t)
		lerp := func(from, to float64) float64 { return from + (to-from)*k }
		h := a.paneAreaH()
		a.cctx.Set("strokeStyle", colorPaneTileBorder)
		a.cctx.Set("lineWidth", lerp(tileBorderPx, wsOutlinePx))
		a.cctx.Call("strokeRect",
			lerp(e.x, wsOutlinePx/2), lerp(e.y, wsOutlinePx/2),
			lerp(e.w, a.width-wsOutlinePx), lerp(e.h, h-wsOutlinePx))
		a.cctx.Set("lineWidth", 1.0)
	}

	// The layout blob is derived from the live tree, so there is no per-gesture
	// persistence call site to forget.
	a.scheduleWorkspaceSave()

	// The same for framing: the writers no-op when nothing moved, so it reaches
	// the server without waiting for an ascent.
	a.scheduleFramingSave()
}

// layoutPanes reserves the notice strip in layout, so a pending error owns
// pixels nothing can paint over. Input hit-testing shares it.
func (a *App) layoutPanes() map[string]pane.Rect {
	return pane.Layout(a.tree, a.rootLayoutRect())
}

// wsOutlinePx is the teal pane-tile outline's width, and the gutter panes inset
// by inside one, so the line and the pane borders never overlap.
const wsOutlinePx = 3.0

// paneAreaH is the height the pane tree occupies, and the bar band's top edge.
// One number, so the panes, the outline and the bar cannot disagree.
func (a *App) paneAreaH() float64 {
	h, _ := wsbar.Band(a.height, errsurface.StripHeight(a.errs.Len()))
	return h
}

// rootLayoutRect is one owner, because the cascading divider resize
// (pane.ResizeThrough) must see the exact rect the layout used.
func (a *App) rootLayoutRect() pane.Rect {
	r := pane.Rect{X: 0, Y: 0, W: a.width, H: a.paneAreaH()}
	if a.ws.Depth() > 0 {
		r.X += wsOutlinePx
		r.Y += wsOutlinePx
		r.W -= 2 * wsOutlinePx
		r.H -= 2 * wsOutlinePx
	}
	return r
}

// drawErrStrip takes its geometry from errsurface, so the click-to-dismiss hit
// test reads the identical layout.
func (a *App) drawErrStrip() {
	notices := a.errs.Notices()
	stripH := errsurface.StripHeight(len(notices))
	if stripH == 0 {
		return
	}
	top := a.height - stripH
	for _, row := range errsurface.Rows(notices, top) {
		bg, fg := colorErrStripBg, colorErrStripText
		if row.Notice.Severity == errsurface.Info {
			bg, fg = colorInfoStripBg, colorInfoStripText
		}
		a.cctx.Set("fillStyle", bg)
		a.cctx.Call("fillRect", 0, row.Y, a.width, errsurface.RowH)
		a.cctx.Set("fillStyle", fg)
		a.cctx.Set("font", "12px system-ui, sans-serif")
		a.cctx.Set("textBaseline", "middle")
		label := errsurface.Label(row.Notice)
		if row.OverflowCount > 0 {
			label += "  (+" + strconv.Itoa(row.OverflowCount) + " more)"
		}
		a.cctx.Call("fillText", label, 12, row.Y+errsurface.RowH/2)
	}
	a.cctx.Set("textBaseline", "alphabetic")
}

// drawPane draws the chrome even when the target grid has not loaded, so the
// user can see the pane is live and recover from a stale descent path.
func (a *App) drawPane(p *pane.Pane, r pane.Rect) {
	gid := a.gridIDForPane(p)
	g, gridOK := a.c.Grid(gid)

	// Clip content inside the border, which is painted on top at the end, so it
	// always frames the content cleanly.
	const inset = paneBorderPx
	withClip(a.cctx, r.X+inset, r.Y+inset, r.W-2*inset, r.H-2*inset, func() {
		pscreen := paneToDragdrop(p, r)

		// Grid lines render whether or not the grid loaded: they communicate
		// the coordinate system. A focused text tile has none, so it gets a
		// plain background.
		if p.ContentID() != "" {
			a.cctx.Set("fillStyle", colorBg)
			a.cctx.Call("fillRect", r.X, r.Y, r.W, r.H)
		} else {
			a.drawGridLines(colorGridLineInterior, pscreen, r)
		}

		if !gridOK && gid != "" && p.ContentID() == "" {
			// Not cached yet, or the last fetch failed. Say which, instead of
			// showing an empty room.
			a.drawGridNotice(r, gid)
		}
		if gridOK {
			cellSize := pscreen.CellPx * pscreen.Zoom
			selected := a.selectedFor(p.ID)
			// In a content descent the pane is inside the tile: render it in
			// the inner box, whose bounds match the textarea exactly, so
			// outside it the grid rules apply.
			if p.ContentID() != "" {
				// descendedTile, not g.Tiles, so an ephemeral url visit,
				// focused off the pane's grid, renders too.
				if file, ok := a.descendedTile(p); ok {
					switch {
					case rpc.TextDocument(file):
						ix, iy, iw, ih := textInnerBox(r)
						a.cctx.Set("fillStyle", colorFileInnerBg)
						a.cctx.Call("fillRect", ix, iy, iw, ih)
						a.drawMarkdownInPane(p, file, ix, iy, iw, ih)
					case rpc.WebContent(file):
						// url and serves_page tiles take the same web-content
						// descent: frozen preview, or live native view.
						ix, iy, iw, ih := paneContentBox(r)
						a.drawURLTileInPane(file, ix, iy, iw, ih)
					case file.Kind == rpc.KindShell:
						ix, iy, iw, ih := paneContentBox(r)
						a.drawShellTileInPane(p, file, ix, iy, iw, ih)
					default:
						ix, iy, iw, ih := textInnerBox(r)
						a.cctx.Set("fillStyle", colorFileInnerBg)
						a.cctx.Call("fillRect", ix, iy, iw, ih)
					}
				}
			} else {
				inHost := g.HostContent()
				for _, n := range g.Tiles {
					if dragdrop.HiddenMatch(a.ghostHiddenTile(), a.ghostHiddenPane(), p.ID, n.Id) {
						continue
					}
					left, top := pscreen.CellToScreen(float64(n.X), float64(n.Y))
					w := float64(n.W) * cellSize
					h := float64(n.H) * cellSize
					if left+w < r.X || top+h < r.Y || left > r.X+r.W || top > r.Y+r.H {
						continue
					}
					nn := n
					outside := tileOutside(nn, inHost)
					dashed := !inHost && isLinkTile(nn)
					a.drawNodeWithPreview(nn, left, top, w, h, cellSize, n.Id == selected, outside, dashed, p.ID)
					a.drawPluginHealthTint(nn, left, top, w, h)
					a.drawDeadLinkFace(nn, left, top, w, h)
				}
				// The fading outline on the tile this pane most recently
				// ascended out of. The alpha decays through the frame loop.
				if tr, ok := a.traces[p.ID]; ok {
					if n, ok := g.Tiles[tr.tileID]; ok {
						left, top := pscreen.CellToScreen(float64(n.X), float64(n.Y))
						drawTraceOutline(a.cctx, left, top,
							float64(n.W)*cellSize, float64(n.H)*cellSize,
							anim.FadeAlpha(nowMs(), tr.startMs, traceDurMs))
					}
				}
				a.drawEdgeIndicators(g.Tiles, pscreen, r)
				if a.ghost != nil && a.ghost.paneID == p.ID {
					gn := a.ghost.tile
					gcs := a.ghost.displayedCellSize
					if gcs <= 0 {
						gcs = cellSize
					}
					w := float64(gn.W) * gcs
					h := float64(gn.H) * gcs
					a.drawGhostTile(gn, a.ghost.screenX, a.ghost.screenY, w, h, gcs, r,
						a.ghost.displayedFragmentation)
				}
			}
		}
	})

	// Border on top, so content can paint to the pane edge without bleeding into
	// the chrome. The hue follows what we descended into, saturated on the
	// focused pane and desaturated on the others.
	focused := p.ID == a.tree.Focus
	urlLive := a.urlViewFor(p.ID) != nil
	border := a.paneBorderColorFor(p, g, gridOK, focused, urlLive)
	strokeTileBorder(a.cctx, r.X, r.Y, r.W, r.H, border, paneBorderPx)

	// The per-mode circle button lives in the bottom bar's right-end slot; see
	// drawBarSlot. Panes carry no corner chrome.
}

// drawCircleButtonChrome is shared by the bar-slot buttons, so their position
// and look match the + button.
func (a *App) drawCircleButtonChrome(cx, cy float64) {
	_, button := a.barTheme()
	a.cctx.Set("fillStyle", button)
	a.cctx.Call("beginPath")
	a.cctx.Call("arc", cx, cy, plusButtonRadius, 0, 2*math.Pi)
	a.cctx.Call("fill")
	a.cctx.Set("strokeStyle", "#dff4f4")
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("stroke")
}

// drawURLBackButton is the bar-slot button on a url descent: a click runs
// history.back() on the descended Chromium tab.
func (a *App) drawURLBackButton() {
	cx, cy := a.plusButtonCenter()
	a.drawCircleButtonChrome(cx, cy)

	// A horizontal stem with a chevron at its left end.
	band, _ := a.barTheme()
	beginSlotGlyph(a.cctx, band)
	a.cctx.Call("beginPath")
	a.cctx.Call("moveTo", cx+6, cy)
	a.cctx.Call("lineTo", cx-6, cy)
	a.cctx.Call("moveTo", cx-2, cy-5)
	a.cctx.Call("lineTo", cx-6, cy)
	a.cctx.Call("lineTo", cx-2, cy+5)
	a.cctx.Call("stroke")
	endGlyph(a.cctx)
}

// drawURLRefreshButton is the bar-slot button on a frozen url descent: a click
// opens the URL stream, the same action as the right-drag-down gesture.
func (a *App) drawURLRefreshButton() {
	cx, cy := a.plusButtonCenter()
	a.drawCircleButtonChrome(cx, cy)

	band, _ := a.barTheme()
	drawRefreshIcon(a.cctx, cx, cy, 7.0, band)
}

// drawURLOpenTabButton replaces the refresh button where the host cannot go
// live (caps.LiveURL false): a click opens the address in a browser tab, the
// next-best descent, and the tile stays frozen.
func (a *App) drawURLOpenTabButton() {
	cx, cy := a.plusButtonCenter()
	a.drawCircleButtonChrome(cx, cy)

	c := a.cctx
	band, _ := a.barTheme()
	c.Set("strokeStyle", band)
	c.Set("lineWidth", 2.0)
	c.Set("lineCap", "round")
	// The tab: a box toward the lower left.
	c.Call("strokeRect", cx-7, cy-1, 8.0, 8.0)
	// The arrow, leaving through the box's upper-right corner.
	c.Call("beginPath")
	c.Call("moveTo", cx+0, cy+0)
	c.Call("lineTo", cx+7, cy-7)
	c.Call("moveTo", cx+2, cy-7)
	c.Call("lineTo", cx+7, cy-7)
	c.Call("lineTo", cx+7, cy-2)
	c.Call("stroke")
	c.Set("lineWidth", 1.0)
	c.Set("lineCap", "butt")
}

// drawGridLines fades to invisible when cells are tiny, so extreme zoom-out
// does not paint a solid wash.
func (a *App) drawGridLines(color string, ps dragdrop.Pane, r pane.Rect) {
	cellSize := ps.CellPx * ps.Zoom
	originX, originY := ps.CellToScreen(0, 0)
	drawGridLinesIn(a.cctx, color, r.X, r.Y, r.W, r.H, cellSize, originX, originY)
}

// drawGridLinesIn spaces lines at cellSize, aligned so cell (0, 0) lands at
// (originX, originY). A well's interior uses it too, so a preview is the same
// kind of grid the user already sees. Under 4px cells it draws nothing.
func drawGridLinesIn(c js.Value, color string, clipX, clipY, clipW, clipH, cellSize, originX, originY float64) {
	if cellSize < 4 {
		return
	}
	alpha := (cellSize - 4) / 20
	if alpha > 0.7 {
		alpha = 0.7
	}
	if alpha < 0.05 {
		return
	}
	c.Set("strokeStyle", color)
	c.Set("lineWidth", 1.0)
	c.Set("globalAlpha", alpha)

	// The integer cell indices whose line falls inside the clip.
	kStartX := int64(math.Ceil((clipX - originX) / cellSize))
	kEndX := int64(math.Floor((clipX + clipW - originX) / cellSize))
	kStartY := int64(math.Ceil((clipY - originY) / cellSize))
	kEndY := int64(math.Floor((clipY + clipH - originY) / cellSize))

	c.Call("beginPath")
	for k := kStartX; k <= kEndX; k++ {
		sx := originX + float64(k)*cellSize
		c.Call("moveTo", math.Floor(sx)+0.5, clipY)
		c.Call("lineTo", math.Floor(sx)+0.5, clipY+clipH)
	}
	for k := kStartY; k <= kEndY; k++ {
		sy := originY + float64(k)*cellSize
		c.Call("moveTo", clipX, math.Floor(sy)+0.5)
		c.Call("lineTo", clipX+clipW, math.Floor(sy)+0.5)
	}
	c.Call("stroke")
	c.Set("globalAlpha", 1.0)
}

// drawNodeWithPreview is the parent-grid renderer: a well gets a one-level
// preview of its child grid at the child's cell scale, so the descent zoom
// crosses no discontinuity. paintPaneID scopes the child-preview hide.
func (a *App) drawNodeWithPreview(n *gridwellv1.Tile, x, y, w, h, parentCellSize float64, selected, outside, dashed bool, paintPaneID string) {
	switch n.Kind {
	case rpc.KindText:
		if !rpc.TextDocument(n) {
			// A page tile is a file whose presentation is web, so its face is
			// its image in the text family's border.
			a.drawPageTile(n, x, y, w, h, selected, outside, dashed)
			a.drawTileBannerLabel(n, x, y, w, h, outside)
			return
		}
		a.drawMarkdownNode(n, x, y, w, h, selected, outside, dashed)
		a.drawTileBannerLabel(n, x, y, w, h, outside)
		return
	case rpc.KindURL:
		a.drawURLTile(n, x, y, w, h, selected, dashed)
		a.drawTileBannerLabel(n, x, y, w, h, outside)
		return
	case rpc.KindShell:
		a.drawShellTile(n, x, y, w, h, selected, dashed)
		a.drawTileBannerLabel(n, x, y, w, h, outside)
		return
	case rpc.KindPane:
		a.drawPaneTilePreview(n, x, y, w, h, selected, outside, dashed)
		return
	}
	if n.Kind != rpc.KindWell {
		drawNode(a.cctx, n, x, y, w, h, selected, outside, tileBorderPx, dashed)
		return
	}
	// Recursion stops at one level, because drawChildPreview paints its children
	// through the flat drawNode.
	child, haveChild := a.c.Grid(n.ChildGridId)
	if !haveChild {
		a.fetchGrid(n.ChildGridId)
	}
	// Matching the pane, so the outline crossing the screen edge has no jump.
	a.cctx.Set("fillStyle", colorBg)
	a.cctx.Call("fillRect", x, y, w, h)

	// previewCell is parentCell times the well's intrinsic ViewZoom. At
	// parent = Overtake_now it matches the just-after-swap live cell, so the
	// path swap is continuous.
	ratio := zoomtrans.EffectiveViewZoom(n.ViewZoom, zoomtrans.DefaultWellViewZoom)
	previewCell := parentCellSize * ratio
	showPreview := haveChild && previewCell >= 0.5

	if isExitWell(n) && !showPreview {
		// The plugin's identity glyph, the same drawing as its swatch and ghost.
		a.drawPluginGlyph(a.pluginGlyph(n.ChildGridId), x, y, w, h)
	} else {
		withClip(a.cctx, x, y, w, h, func() {
			// Aligned so the child point the well's framing centers on lands at
			// the well's center, where the just-after-descent viewport puts it,
			// so the lines glide across the path swap.
			viewCenterX, viewCenterY := zoomtrans.EffectiveCenter(wellOf(n))
			wellCenterX := x + w/2
			wellCenterY := y + h/2
			originX := wellCenterX - viewCenterX*previewCell
			originY := wellCenterY - viewCenterY*previewCell
			drawGridLinesIn(a.cctx, colorGridLineInterior, x, y, w, h, previewCell, originX, originY)

			if showPreview {
				// The hide scopes to the pane being painted (paintPaneID).
				var hide string
				if a.ghost != nil && a.ghost.hiddenPaneID == paintPaneID {
					hide = a.ghost.hiddenTileID
				}
				a.drawChildPreview(child, viewCenterX, viewCenterY,
					wellCenterX, wellCenterY, previewCell, x, y, w, h, hide)
			}
		})
	}

	// Every well is blue; a cross-plugin well differs by the dash, which always
	// means a link, a reference you can unlink.
	strokeTileFrame(a.cctx, x, y, w, h, colorFocusBorder, dashed, selected)
	// A plain well gets no banner: it has no alt text, and tilebanner.Runs
	// returns "" for it.
	a.drawTileBannerLabel(n, x, y, w, h, outside)
}

// tileReadOnly holds for a text tile owned by a plugin, which has no
// write-back, and for an unknown grid: the alternative is a caret over content
// the server would then refuse.
func (a *App) tileReadOnly(n *gridwellv1.Tile) bool {
	writable, _ := a.gridWritable(n.GridId)
	return n.Kind == rpc.KindText && !writable
}

// tileOutside reports the outside-Gridwell treatment: a grid that declares
// host_content, so every row in it is host state; an exit well, wherever it
// sits; or a shell tile.
func tileOutside(n *gridwellv1.Tile, parentHostContent bool) bool {
	if parentHostContent {
		return true
	}
	if isExitWell(n) {
		return true
	}
	if n.Kind == rpc.KindShell {
		return true
	}
	return false
}

// isLinkTile reports a reference rather than owned content: dropping one on the
// trashcan unlinks it, where an owned well deletes for real. Reference is the
// one signal, and a uuid comparison would miss a same-plugin mount.
func isLinkTile(n *gridwellv1.Tile) bool {
	return n.Reference
}

// Short on and off, so a 1 to 2px outline still reads as dashed.
func setTileDash(c js.Value)   { c.Call("setLineDash", jsArray(5, 3)) }
func clearTileDash(c js.Value) { c.Call("setLineDash", jsArray()) }

const bannerFontFamily = `ui-sans-serif, system-ui, -apple-system, sans-serif`

// bannerGeom clamps the banner's font to 9 to 16 screen px, so the label reads
// at a constant size across zoom. The text preview reads the same formula.
func bannerGeom(h, ih float64) (fontPx, bannerH float64, shown bool) {
	const minFontPx = 9.0
	const maxFontPx = 16.0
	fontPx = h * 0.14
	if fontPx < minFontPx {
		fontPx = minFontPx
	}
	if fontPx > maxFontPx {
		fontPx = maxFontPx
	}
	if fontPx*1.4 > ih {
		return fontPx, 0, false
	}
	return fontPx, fontPx + 4, true
}

// drawTileBannerLabel paints the label at the top of the tile in its own kind
// color, clipped to the tile rect.
func (a *App) drawTileBannerLabel(n *gridwellv1.Tile, x, y, w, h float64, outside bool) {
	a.drawTileBannerLabelIn(n, x, y, w, h, bannerTextColor(n, outside))
}

// drawTileBannerLabelIn names the text color rather than deriving it: one
// banner geometry, so a dead link's grey label lands in the same place.
func (a *App) drawTileBannerLabelIn(n *gridwellv1.Tile, x, y, w, h float64, textColor string) {
	label, status := tilebanner.Runs(n)
	if label == "" {
		return
	}
	// Inset by the tile border, so band and outline do not overlap by a pixel.
	ix := x + tileBorderPx
	iy := y + tileBorderPx
	iw := w - 2*tileBorderPx
	ih := h - 2*tileBorderPx
	if iw <= 0 || ih <= 0 {
		return
	}
	fontPx, bannerH, shown := bannerGeom(h, ih)
	if !shown {
		// Too small for a label: the outline alone carries the signal.
		return
	}
	withClip(a.cctx, ix, iy, iw, ih, func() {
		a.cctx.Set("fillStyle", colorSourceLabelBg)
		a.cctx.Call("fillRect", ix, iy, iw, bannerH)
		setFont(a.cctx, fontPx, bannerFontFamily, true)
		a.cctx.Set("fillStyle", textColor)
		a.cctx.Set("textBaseline", "middle")
		a.cctx.Set("textAlign", "start")
		a.cctx.Call("fillText", label, ix+4, iy+bannerH/2)
		if status != "" {
			// The status is the plugin's word, so it is drawn in the one muted
			// color and never in the tile's own: it reads as a note on the
			// name, not as part of it.
			labelW := a.cctx.Call("measureText", label).Get("width").Float()
			setFont(a.cctx, fontPx, bannerFontFamily, false)
			a.cctx.Set("fillStyle", colorMuted)
			a.cctx.Call("fillText", status, ix+4+labelW+fontPx/2, iy+bannerH/2)
		}
		a.cctx.Set("textBaseline", "top")
	})
}

// bannerTextColor echoes the tile's own outline, so label and border read as
// one. A cross-plugin well is blue like every well: it is dashed, not recolored.
func bannerTextColor(n *gridwellv1.Tile, outside bool) string {
	if n.Kind == rpc.KindShell {
		return colorShellBorder
	}
	if isExitWell(n) {
		return colorFocusBorder
	}
	if outside {
		return colorPluginBorder
	}
	switch n.Kind {
	case rpc.KindWell:
		return colorFocusBorder
	case rpc.KindURL:
		return colorURLLine
	case rpc.KindText:
		return colorMarkdownLine
	}
	return colorMuted
}

// fetchTileContent never doubles an in-flight fetch: concurrent fetches for one
// tile are how a stale reply lands after a fresher one and repaints old bytes.
func (a *App) fetchTileContent(tileID string) {
	if tileID == "" {
		return
	}
	if _, ok := a.c.TileContent(tileID); ok {
		return
	}
	// A leaf link resolves through its target id, so a target in an undeclared
	// namespace is not asked for. Same rule as fetchGrid.
	if a.deadNamespace(tileID) {
		return
	}
	ctx, done, ok := a.fetch.contentFetch.Begin(tileID)
	if !ok {
		return
	}
	go func() {
		defer done()
		// Coalesced repaint: body fetches land in bursts, and the failure is
		// already on the strip.
		_ = a.loadTileContent(ctx, tileID, a.scheduleFrame)
	}()
}

// loadTileContent is the one content-fetch body; the lazy render fetch and the
// restore's cursor-placing read differ only in their guards. The error is
// returned as well as surfaced, because a waiting caller has a continuation.
func (a *App) loadTileContent(ctx context.Context, tileID string, then func()) error {
	data, _, version, err := a.cl.ReadContent(ctx, tileID)
	if err != nil {
		// The tile body would otherwise never appear: say why.
		a.surfaceRPCError("ReadContent", err)
		return err
	}
	a.c.PutFetchedContent(tileID, data, version)
	a.refreshFileOverlay()
	then()
	return nil
}

// tileBody fetches lazily through ReadContent, which is routable by tile id
// where a blob id is not, and keys by ContentID, so a leaf link renders the
// one shared copy of its target's bytes.
func (a *App) tileBody(n *gridwellv1.Tile) ([]byte, bool) {
	if b, ok := a.c.TileContent(rpc.ContentID(n)); ok {
		return b, true
	}
	a.fetchTileContent(rpc.ContentID(n))
	return nil, false
}

// drawChildPreview paints the cached child grid at previewCell px, with
// (centerCellX, centerCellY) landing at (centerScreenX, centerScreenY). Child
// wells render flat: the one-level rule. hiddenTileID hides one row by id.
func (a *App) drawChildPreview(child *cache.Grid,
	centerCellX, centerCellY, centerScreenX, centerScreenY, previewCell float64,
	clipX, clipY, clipW, clipH float64,
	hiddenTileID string,
) {
	c := a.cctx
	childInHost := child.HostContent()
	// Scaled, so a distant child grid keeps borders proportionate to its cells.
	borderPx := previewBorderPxFor(previewCell)
	for _, n := range child.Tiles {
		if hiddenTileID != "" && n.Id == hiddenTileID {
			continue
		}
		nodeScreenX := centerScreenX + (float64(n.X)-centerCellX)*previewCell
		nodeScreenY := centerScreenY + (float64(n.Y)-centerCellY)*previewCell
		nodeScreenW := float64(n.W) * previewCell
		nodeScreenH := float64(n.H) * previewCell
		// Cull entries fully outside the clip.
		if nodeScreenX+nodeScreenW < clipX || nodeScreenY+nodeScreenH < clipY ||
			nodeScreenX > clipX+clipW || nodeScreenY > clipY+clipH {
			continue
		}
		nn := n
		// url and shell children do not overlay their JPEGs, so a well's
		// interior reads uniformly.
		drawNode(c, nn, nodeScreenX, nodeScreenY, nodeScreenW, nodeScreenH, false, tileOutside(nn, childInHost), borderPx, false)
	}
}

// drawNode is the flat renderer, used for nested previews and for non-well
// tiles; the parent-grid renderer is drawNodeWithPreview.
func drawNode(c js.Value, n *gridwellv1.Tile, x, y, w, h float64, selected bool, outside bool, borderPx float64, dashed bool) {
	// dashed marks a link, and every kind honors it or lies about ownership.
	if dashed {
		setTileDash(c)
		defer clearTileDash(c)
	}
	// An unknown kind keeps the locked grey body and gets no outline.
	fill, line := colorLocked, ""
	switch n.Kind {
	case rpc.KindWell:
		fill, line = colorBg, colorFocusBorder
	case rpc.KindURL:
		fill, line = colorURLFill, colorURLLine
	case rpc.KindShell:
		fill, line = colorShellFill, colorShellBorder
	case rpc.KindText:
		fill, line = colorMarkdownFill, colorMarkdownLine
		if outside {
			fill, line = colorPluginFill, colorPluginBorder
		}
	case rpc.KindPane:
		// The flat face a pane tile shows one level down, and in a ghost.
		fill, line = colorPaneTileFill, colorPaneTileBorder
	}
	c.Set("fillStyle", fill)
	c.Call("fillRect", x, y, w, h)
	if line != "" {
		strokeTileBorder(c, x, y, w, h, line, borderPx)
	}
	if selected {
		drawSelectedTileOutline(c, x, y, w, h)
	}
}

// drawGhostTile is drawNodeWithPreview at near-zero fragmentation; over a black
// hole it animates toward 1 and cross-fades into a trashcan, reversibly.
func (a *App) drawGhostTile(n *gridwellv1.Tile, x, y, w, h, parentCellSize float64, r pane.Rect, frag float64) {
	// No parent grid is in play, so the ghost's own kind is the outside signal.
	outside := tileOutside(n, false)
	// A dragged link shows dashed, and so does a drop that will create one:
	// dashed always means this is, or becomes, a reference. It is how the user
	// learns mid-drag which right-button mode is armed.
	dashed := isLinkTile(n) || (a.ghost != nil && a.ghost.link)
	if frag < 0.02 {
		a.drawNodeWithPreview(n, x, y, w, h, parentCellSize, false, outside, dashed, "")
		if a.ghost != nil {
			if a.ghost.forbidden {
				drawGhostNoEntryBadge(a.cctx, x+w/2, y+h/2, min(w, h))
			} else if a.ghost.link {
				drawGhostLinkBadge(a.cctx, x+w/2, y+h/2, min(w, h))
			}
		}
		return
	}
	if frag > 1 {
		frag = 1
	}
	// The tile fades out as frag grows; the trashcan fades in.
	if frag < 0.98 {
		a.cctx.Set("globalAlpha", 1.0-frag)
		a.drawNodeWithPreview(n, x, y, w, h, parentCellSize, false, outside, dashed, "")
		a.cctx.Set("globalAlpha", 1.0)
	}
	a.cctx.Set("globalAlpha", frag)
	drawTrashcanIcon(a.cctx, x, y, w, h)
	a.cctx.Set("globalAlpha", 1.0)
}

// paneBorderColorFor picks the border from what the pane is descended into. An
// uncached grid falls back to the generic blue, so the user still sees that
// they descended into something.
func (a *App) paneBorderColorFor(p *pane.Pane, g *cache.Grid, gridOK bool, focused bool, urlLive bool) string {
	return pane.BorderColor(a.borderInputFor(p, g, gridOK, focused, urlLive), paneBorderColors)
}

// borderInputFor resolves the facts pane.FamilyOf classifies on, shared with
// the bottom bar theme, so the frame and the band cannot disagree.
func (a *App) borderInputFor(p *pane.Pane, g *cache.Grid, gridOK bool, focused bool, urlLive bool) pane.BorderInput {
	in := pane.BorderInput{
		HasTextFocus: p.ContentID() != "",
		DescentDepth: len(p.Path()),
		Focused:      focused,
		URLLive:      urlLive,
	}
	if p.ContentID() != "" {
		// descendedTile, not g.Tiles, so an ephemeral descent resolves too.
		// Its border goes gray, because ascent deletes it.
		if tile, ok := a.descendedTile(p); ok {
			in.TileKnown = true
			in.TileKind = tile.Kind
			in.Ephemeral = a.certainlyEphemeral(p, tile)
		}
	}
	if gridOK && g.HostContent() {
		in.InHostGrid = true
	}
	return in
}

// paneBorderColors bundles this renderer's constants for pane.BorderColor.
var paneBorderColors = pane.BorderColors{
	Focused:        colorFocusBorder,
	FocusedFaded:   colorFocusBorderFaded,
	Text:           colorMarkdownLine,
	TextFaded:      colorMarkdownLineFaded,
	URL:            colorURLLine,
	URLFaded:       colorURLLineFaded,
	URLLive:        colorURLLiveLine,
	URLLiveFaded:   colorURLLiveLineFaded,
	Shell:          colorShellBorder,
	ShellFaded:     colorShellBorderFaded,
	Exit:           colorPluginBorder,
	ExitFaded:      colorPluginBorderFaded,
	Ephemeral:      colorEphemeralBorder,
	EphemeralFaded: colorEphemeralBorderFaded,
}

// drawEdgeIndicators marks every tile entirely outside the viewport, where the
// ray from the viewport center to the tile's center crosses the inset rect.
func (a *App) drawEdgeIndicators(nodes map[string]*gridwellv1.Tile, ps dragdrop.Pane, r pane.Rect) {
	cellSize := ps.CellPx * ps.Zoom
	const inset = 12.0
	innerL := r.X + inset
	innerR := r.X + r.W - inset
	innerT := r.Y + inset
	innerB := r.Y + r.H - inset
	cx := r.X + r.W/2
	cy := r.Y + r.H/2

	a.cctx.Set("fillStyle", colorEdgeDot)
	for _, n := range nodes {
		sx, sy := ps.CellToScreen(float64(n.X), float64(n.Y))
		w := float64(n.W) * cellSize
		h := float64(n.H) * cellSize
		// Visible iff the rect intersects the pane.
		if sx+w > r.X && sx < r.X+r.W && sy+h > r.Y && sy < r.Y+r.H {
			continue
		}
		nx := sx + w/2
		ny := sy + h/2
		dx := nx - cx
		dy := ny - cy
		if dx == 0 && dy == 0 {
			continue
		}
		// Find the smallest t > 0 where (cx+t*dx, cy+t*dy) hits an inner edge.
		tMax := math.MaxFloat64
		if dx > 0 {
			tMax = math.Min(tMax, (innerR-cx)/dx)
		} else if dx < 0 {
			tMax = math.Min(tMax, (innerL-cx)/dx)
		}
		if dy > 0 {
			tMax = math.Min(tMax, (innerB-cy)/dy)
		} else if dy < 0 {
			tMax = math.Min(tMax, (innerT-cy)/dy)
		}
		if tMax <= 0 || math.IsInf(tMax, 0) {
			continue
		}
		mx := cx + tMax*dx
		my := cy + tMax*dy
		// Triangle pointing away from center.
		ang := math.Atan2(dy, dx)
		drawTriangle(a.cctx, mx, my, ang, 6)
	}
}

// drawTriangle is every arrowhead in the renderer: the off-screen edge
// indicators and the swap preview's two heads. size is center to tip.
func drawTriangle(c js.Value, cx, cy, angle, size float64) {
	tipX := cx + math.Cos(angle)*size
	tipY := cy + math.Sin(angle)*size
	leftX := cx + math.Cos(angle+2.5)*size
	leftY := cy + math.Sin(angle+2.5)*size
	rightX := cx + math.Cos(angle-2.5)*size
	rightY := cy + math.Sin(angle-2.5)*size
	c.Call("beginPath")
	c.Call("moveTo", tipX, tipY)
	c.Call("lineTo", leftX, leftY)
	c.Call("lineTo", rightX, rightY)
	c.Call("closePath")
	c.Call("fill")
}

// drawGridNotice paints a muted status line in a pane whose grid is not cached;
// pane.GridNotice words it. A mounted node's grid falls back to the id, because
// a mounted id's first segment is the local node; see client/scratch.
func (a *App) drawGridNotice(r pane.Rect, gid string) {
	if r.W < 80 || r.H < 40 {
		return
	}
	name := gid
	if pl, ok := a.pluginByUUID(uuidOf(gid)); ok && pl.Label != "" {
		name = pl.Label
	}
	label := pane.GridNotice(name, a.fetch.gridLoadFailed.Has(gid))
	a.cctx.Call("save")
	a.cctx.Set("fillStyle", colorMuted)
	a.cctx.Set("font", "13px system-ui, sans-serif")
	a.cctx.Set("textAlign", "center")
	a.cctx.Set("textBaseline", "middle")
	a.cctx.Call("fillText", label, r.X+r.W/2, r.Y+r.H/2, r.W-16)
	a.cctx.Call("restore")
}
