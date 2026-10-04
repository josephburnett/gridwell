//go:build js && wasm

package main

import (
	"cmp"
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"math"
	"strconv"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/cadence"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/palette"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/tileface"
	"github.com/josephburnett/gridwell/client/traceevent"
	"github.com/josephburnett/gridwell/client/wsbar"
)

const (
	// paneBorderPx is load-bearing: the strip between two live-tile panes is the
	// only surface that can grab the divider, since a WebContentsView eats input.
	paneBorderPx = panebox.LiveViewInsetPx
	// tileBorderPx sits inside the tile rect, clear of the banner and neighbour.
	tileBorderPx = 2.0
)

// withClip balances save/restore even for a paint that returns early.
func withClip(c js.Value, x, y, w, h float64, paint func()) {
	c.Call("save")
	c.Call("beginPath")
	c.Call("rect", x, y, w, h)
	c.Call("clip")
	paint()
	c.Call("restore")
}

func fillRectC(c js.Value, x, y, w, h float64, color string) {
	c.Set("fillStyle", color)
	c.Call("fillRect", x, y, w, h)
}

// labelOpts is the face drawLabel wears; zero values are canvas defaults.
type labelOpts struct {
	font     string
	fill     string
	align    string
	baseline string
	maxW     float64
}

// drawLabel paints one label and leaves the canvas as it found it. Every
// fillText goes through here except drawMarkdownText.
func drawLabel(c js.Value, text string, x, y float64, o labelOpts) {
	c.Call("save")
	c.Set("font", o.font)
	c.Set("fillStyle", o.fill)
	c.Set("textAlign", cmp.Or(o.align, "start"))
	c.Set("textBaseline", cmp.Or(o.baseline, "alphabetic"))
	if o.maxW > 0 {
		c.Call("fillText", text, x, y, o.maxW)
	} else {
		c.Call("fillText", text, x, y)
	}
	c.Call("restore")
}

// strokeTileBorder keeps the outline inside (x, y, w, h).
func strokeTileBorder(c js.Value, x, y, w, h float64, color string, borderPx float64) {
	c.Set("strokeStyle", color)
	c.Set("lineWidth", borderPx)
	half := borderPx / 2
	c.Call("strokeRect", x+half, y+half, w-borderPx, h-borderPx)
}

// previewBorderPxFor keeps a child-grid preview's borders proportional.
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

func (a *App) drawSelectedTileOutline(c js.Value, x, y, w, h float64) {
	c.Set("strokeStyle", a.pal.Selected)
	c.Set("lineWidth", 2.0)
	c.Call("strokeRect", x-1, y-1, w+2, h+2)
	c.Set("lineWidth", 1.0)
}

// strokeTileFrame is the coda every full-size tile renderer ends with.
func (a *App) strokeTileFrame(c js.Value, x, y, w, h float64, color string, dashed, selected bool) {
	if dashed {
		setTileDash(c)
	}
	strokeTileBorder(c, x, y, w, h, color, tileBorderPx)
	if dashed {
		clearTileDash(c)
	}
	if selected {
		a.drawSelectedTileOutline(c, x, y, w, h)
	}
}

// drawDeadLinkFace paints over the tile already drawn: a dead link carries the
// news alone.
func (a *App) drawDeadLinkFace(n *gridwellv1.Tile, x, y, w, h float64) {
	if !a.deadLink(n) {
		return
	}
	fillRectC(a.cctx, x, y, w, h, a.pal.DeadLinkVeil)
	a.strokeTileFrame(a.cctx, x, y, w, h, a.pal.DeadLink, true, false)
	a.drawTileBannerLabelIn(n, x, y, w, h, a.pal.DeadLink)
}

func (a *App) drawTraceOutline(c js.Value, x, y, w, h, alpha float64) {
	if alpha <= 0 {
		return
	}
	c.Call("save")
	c.Set("globalAlpha", alpha)
	c.Set("strokeStyle", a.pal.Trace)
	c.Set("lineWidth", 3.0)
	c.Call("strokeRect", x-2, y-2, w+4, h+4)
	c.Call("restore")
}

// plusButtonRadius comes from palette.Default(), so canvas and DOM agree.
var plusButtonRadius = palette.Default().PlusRadius

// templateKind identifies one built-in tile primitive. Order matters: it is
// the popover layout and the hit-test index.
type templateKind int

const (
	tplWell templateKind = iota
	tplMarkdown
	tplURL
	tplShell
	// tplPane is a stored split-pane layout, created never-arranged.
	tplPane
)

// primitive is one row per built-in kind, so a kind cannot be half-added.
type primitive struct {
	kind  templateKind
	name  string
	ghost *gridwellv1.Tile
	glyph func(a *App, x, y, w, h float64)
	// create fires into gridID, the drop target's grid, never re-derived here.
	create func(a *App, gridID string, cellX, cellY int64)
	// click is what a bare click on the swatch does. A column, not an arm per
	// kind, so no kind falls through to the canvas behind the popover.
	click func(a *App, p *pane.Pane)
}

// primitives is the palette layout order and primitiveKinds its derived
// order. Both fill in init, because the rows close over App methods.
var (
	primitives     []primitive
	primitiveKinds []templateKind
)

func init() {
	primitives = []primitive{
		{
			kind: tplWell, name: "well",
			ghost:  &gridwellv1.Tile{Kind: rpc.KindWell, W: 1, H: 1},
			glyph:  func(a *App, x, y, w, h float64) { drawWellGlyph(a.cctx, x, y, w, h, a.pal.FocusBorder) },
			create: func(a *App, gid string, cellX, cellY int64) { a.createWellAtCell(gid, cellX, cellY) },
		},
		{
			kind: tplMarkdown, name: "markdown",
			ghost:  &gridwellv1.Tile{Kind: rpc.KindText, W: 1, H: 1},
			glyph:  func(a *App, x, y, w, h float64) { drawDocumentGlyph(a.cctx, x, y, w, h, a.pal.MarkdownLine) },
			create: func(a *App, gid string, cellX, cellY int64) { a.createTextAtCell(gid, []byte{}, cellX, cellY) },
		},
		{
			kind: tplURL, name: "url",
			ghost:  &gridwellv1.Tile{Kind: rpc.KindURL, W: 1, H: 1},
			glyph:  func(a *App, x, y, w, h float64) { drawGlobeGlyph(a.cctx, x, y, w, h, a.pal.URLLine) },
			create: func(a *App, gid string, cellX, cellY int64) { a.createURLAtCell(gid, cellX, cellY) },
			click:  func(a *App, p *pane.Pane) { a.visitURLFromMenu(p) },
		},
		{
			kind: tplShell, name: "shell",
			ghost:  &gridwellv1.Tile{Kind: rpc.KindShell, W: 1, H: 1, AltText: "shell"},
			glyph:  func(a *App, x, y, w, h float64) { drawShellGlyph(a.cctx, x, y, w, h, a.pal.ShellBorder) },
			create: func(a *App, gid string, cellX, cellY int64) { a.createShellAtCell(gid, cellX, cellY) },
			click:  func(a *App, p *pane.Pane) { a.visitShellFromMenu(p) },
		},
		{
			kind: tplPane, name: "pane",
			ghost:  &gridwellv1.Tile{Kind: rpc.KindPane, W: 1, H: 1, AltText: "workspace"},
			glyph:  func(a *App, x, y, w, h float64) { drawPaneGlyph(a.cctx, x, y, w, h, a.pal.PaneTileBorder) },
			create: func(a *App, gid string, cellX, cellY int64) { a.createPaneAtCell(gid, cellX, cellY) },
		},
	}
	primitiveKinds = make([]templateKind, len(primitives))
	for i, pr := range primitives {
		primitiveKinds[i] = pr.kind
	}
}

func primitiveFor(k templateKind) (primitive, bool) {
	for _, pr := range primitives {
		if pr.kind == k {
			return pr, true
		}
	}
	return primitive{}, false
}

// paletteItem is a configured plugin or a built-in primitive.
type paletteItem struct {
	isPlugin  bool
	plugin    *gridwellv1.PluginInfo // when isPlugin (also set for a root ENTRY's owner)
	primitive templateKind           // when !isPlugin
	entry     *gridwellv1.MenuEntry
	// promotePane marks a promote drag of the ephemeral url crumb in that pane.
	promotePane string
}

// paletteItems returns pane p's palette entries in display order. Every
// consumer reads this one list, so a swatch not shown cannot be clicked.
func (a *App) paletteItems(p *pane.Pane) []paletteItem {
	items, _ := a.paletteView(p)
	return items
}

// paletteView returns the items and the section decision; see palette.Show.
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

// paletteGroups is unfiltered by the fold, so palette.Show counts what the
// node declares.
func (a *App) paletteGroups(p *pane.Pane) (plugins, primitives []paletteItem) {
	// The menu belongs to the pane's node; "", local or uncached, is the handshake.
	ctx := a.menuCtx(p)
	// palette.Doorways owns the composition. Every swatch is a pseudo-plugin, so
	// every downstream flow is one path.
	sw := palette.Doorways(ctx.plugins)
	items := make([]paletteItem, 0, len(sw))
	for _, s := range sw {
		items = append(items, paletteItem{isPlugin: true, plugin: s.Plugin, entry: s.Entry})
	}
	prims := make([]paletteItem, 0, len(primitiveKinds))
	writable, _ := a.gridWritable(a.gridIDForPane(p))
	offer := palette.Offer{Writable: writable, ShellsDisabled: ctx.shellsDisabled}
	if offer.Primitives() {
		for _, k := range primitiveKinds {
			if k == tplShell && !offer.Shell() {
				continue
			}
			prims = append(prims, paletteItem{primitive: k})
		}
	}
	return items, prims
}

func paletteTopRow(items []paletteItem) int {
	n := 0
	for _, it := range items {
		if it.isPlugin {
			n++
		}
	}
	return n
}

const ghostSizeLerpAlpha = 0.20

func (a *App) draw() {
	if a.ghost != nil {
		ds := a.ghost.displayedCellSize
		ts := a.ghost.targetCellSize
		if ts > 0 && math.Abs(ts-ds) > 0.5 {
			a.ghost.displayedCellSize = ds + (ts-ds)*ghostSizeLerpAlpha
			a.scheduleFrame(traceevent.WhyGhost)
		} else if ts > 0 {
			a.ghost.displayedCellSize = ts
		}
		df := a.ghost.displayedFragmentation
		tf := a.ghost.targetFragmentation
		if math.Abs(tf-df) > 0.01 {
			a.ghost.displayedFragmentation = df + (tf-df)*ghostSizeLerpAlpha
			a.scheduleFrame(traceevent.WhyGhost)
		} else {
			a.ghost.displayedFragmentation = tf
		}
	}

	fillRectC(a.cctx, 0, 0, a.width, a.height, a.pal.Bg)

	a.adoptPendingViews()
	rects := a.layoutPanes()
	for paneID, r := range rects {
		p := a.tree.FindPane(paneID)
		if p == nil {
			continue
		}
		a.drawPane(p, r)
	}

	if a.menu.IsOpen() {
		if mp := a.tree.FindPane(a.menu.PaneID()); mp != nil {
			if _, ok := rects[mp.ID]; ok {
				a.drawPalette(mp)
			}
		}
	}

	if a.rightDrag != nil {
		a.drawRightDragPreview()
	}
	if a.leftResize != nil {
		a.drawLeftResizePreview(a.leftResize)
	}

	// Native views park off-screen during a canvas gesture.
	a.syncTextOverlayPosition()
	a.refreshRenderedOverlay()
	a.syncShellOverlayPosition()
	a.syncURLViews()
	a.syncMirrors(rects)
	a.syncInterest(rects)
	a.drawBottomBar()
	a.drawErrStrip()
	// Inside a pane tile, a teal line wraps the window in a reserved gutter.
	if a.ws.Depth() > 0 {
		h := a.paneAreaH()
		a.cctx.Set("strokeStyle", a.pal.PaneTileBorder)
		a.cctx.Set("lineWidth", wsOutlinePx)
		a.cctx.Call("strokeRect", wsOutlinePx/2, wsOutlinePx/2,
			a.width-wsOutlinePx, h-wsOutlinePx)
		a.cctx.Set("lineWidth", 1.0)
	}
	// The first-descent capture: the pane tile's face growing into the outline.
	if e := a.overlays.wsExpand; e != nil {
		t := (nowMs() - e.startMs) / totalTransitionMs
		if t > 1 {
			t = 1
		}
		k := anim.EaseOutCubic(t)
		lerp := func(from, to float64) float64 { return from + (to-from)*k }
		h := a.paneAreaH()
		a.cctx.Set("strokeStyle", a.pal.PaneTileBorder)
		a.cctx.Set("lineWidth", lerp(tileBorderPx, wsOutlinePx))
		a.cctx.Call("strokeRect",
			lerp(e.x, wsOutlinePx/2), lerp(e.y, wsOutlinePx/2),
			lerp(e.w, a.width-wsOutlinePx), lerp(e.h, h-wsOutlinePx))
		a.cctx.Set("lineWidth", 1.0)
	}

	// Both persisters derive from the live tree, armed by a fingerprint of what
	// each would write: a frame is not a change, and a live tile repaints on the
	// mirror's cadence.
	a.scheduleWorkspaceSave(pane.LayoutFingerprint(a.tree))
	a.scheduleFramingSave(pane.FramingFingerprint(a.tree))
}

// layoutPanes reserves the notice strip, shared with input hit-testing.
func (a *App) layoutPanes() map[string]pane.Rect {
	return pane.Layout(a.tree, a.rootLayoutRect())
}

// wsOutlinePx is the pane-tile outline's width and the gutter panes inset by.
const wsOutlinePx = 3.0

// paneAreaH is the pane tree's height and the bar band's top edge.
func (a *App) paneAreaH() float64 {
	h, _ := wsbar.Band(a.height, errsurface.StripHeight(a.errs.Len()))
	return h
}

// rootLayoutRect is one owner: pane.ResizeThrough must see the layout's rect.
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

// drawErrStrip takes its geometry from errsurface, shared with the hit test.
func (a *App) drawErrStrip() {
	notices := a.errs.Notices()
	stripH := errsurface.StripHeight(len(notices))
	if stripH == 0 {
		return
	}
	top := a.height - stripH
	for _, row := range errsurface.Rows(notices, top) {
		bg, fg := a.pal.ErrStripBg, a.pal.ErrStripText
		if row.Notice.Severity == errsurface.Info {
			bg, fg = a.pal.InfoStripBg, a.pal.InfoStripText
		}
		fillRectC(a.cctx, 0, row.Y, a.width, errsurface.RowH, bg)
		label := errsurface.Label(row.Notice)
		if row.OverflowCount > 0 {
			label += "  (+" + strconv.Itoa(row.OverflowCount) + " more)"
		}
		drawLabel(a.cctx, label, 12, row.Y+errsurface.RowH/2, labelOpts{
			font: "12px system-ui, sans-serif", fill: fg, baseline: "middle",
		})
	}
}

// drawPane draws the chrome even when the grid has not loaded, so a stale
// descent path is visible and recoverable.
func (a *App) drawPane(p *pane.Pane, r pane.Rect) {
	gid := a.gridIDForPane(p)
	g, gridOK := a.c.Grid(gid)

	const inset = paneBorderPx
	pscreen, shown := p.Screen(r)
	withClip(a.cctx, r.X+inset, r.Y+inset, r.W-2*inset, r.H-2*inset, func() {
		// A pane with no view has no cells to draw.
		if !shown {
			fillRectC(a.cctx, r.X, r.Y, r.W, r.H, a.pal.Bg)
			return
		}

		// Grid lines render whether or not the grid loaded; a focused text tile has
		// none.
		if p.ContentID() != "" {
			fillRectC(a.cctx, r.X, r.Y, r.W, r.H, a.pal.Bg)
		} else {
			a.drawGridLines(a.pal.GridLineInterior, pscreen, r)
		}

		if !gridOK && gid != "" && p.ContentID() == "" {
			a.drawGridNotice(r, gid)
		}
		if gridOK {
			cellSize := pscreen.Cell()
			selected := a.selectedFor(p.ID)
			// In a content descent, render in the inner box that matches the textarea.
			if p.ContentID() != "" {
				// descendedTile, not g.Tiles, so an ephemeral url visit renders too.
				if file, ok := a.descendedTile(p); ok {
					switch {
					case rpc.TextDocument(file):
						ix, iy, iw, ih := textInnerBox(r)
						fillRectC(a.cctx, ix, iy, iw, ih, a.pal.FileInnerBg)
						a.drawMarkdownInPane(p, file, ix, iy, iw, ih)
					case rpc.WebContent(file):
						ix, iy, iw, ih := paneContentBox(r)
						a.drawURLTileInPane(file, ix, iy, iw, ih)
					case file.Kind == rpc.KindShell:
						ix, iy, iw, ih := paneContentBox(r)
						a.drawShellTileInPane(p, file, ix, iy, iw, ih)
					default:
						ix, iy, iw, ih := textInnerBox(r)
						fillRectC(a.cctx, ix, iy, iw, ih, a.pal.FileInnerBg)
					}
				}
			} else {
				inHost := g.HostContent()
				for _, n := range g.Tiles {
					if dragdrop.HiddenMatch(a.ghostHiddenTile(), a.ghostHiddenPane(), p.ID, n.Id) {
						continue
					}
					if !pscreen.Shows(float64(n.X), float64(n.Y), float64(n.W), float64(n.H)) {
						continue
					}
					left, top := pscreen.CellToScreen(float64(n.X), float64(n.Y))
					w := float64(n.W) * cellSize
					h := float64(n.H) * cellSize
					nn := n
					outside := tileface.Outside(nn, inHost)
					dashed := isLinkTile(nn)
					a.drawNodeWithPreview(nn, left, top, w, h, cellSize, n.Id == selected, outside, dashed, p.ID)
					a.drawPluginHealthTint(nn, left, top, w, h)
					a.drawDeadLinkFace(nn, left, top, w, h)
				}
				// The fading outline on the tile this pane most recently ascended out of.
				if tr, ok := a.traces[p.ID]; ok {
					if n, ok := g.Tiles[tr.tileID]; ok {
						left, top := pscreen.CellToScreen(float64(n.X), float64(n.Y))
						a.drawTraceOutline(a.cctx, left, top,
							float64(n.W)*cellSize, float64(n.H)*cellSize,
							anim.FadeAlpha(nowMs(), tr.startMs, cadence.TraceFadeMs))
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
					a.drawGhostTile(gn, a.ghost.X, a.ghost.Y, w, h, gcs, r,
						a.ghost.displayedFragmentation)
				}
			}
		}
	})

	// Border on top, hued by what the pane descended into, saturated on focus.
	focused := p.ID == a.tree.Focus
	urlLive := a.urlViewFor(p.ID) != nil
	border := a.paneBorderColorFor(p, g, gridOK, focused, urlLive)
	strokeTileBorder(a.cctx, r.X, r.Y, r.W, r.H, border, paneBorderPx)

	// The per-mode circle button lives in the bar; see drawBarSlot.
}

func (a *App) drawCircleButtonChrome(cx, cy float64) {
	_, button := a.barTheme()
	a.cctx.Set("fillStyle", button)
	a.cctx.Call("beginPath")
	a.cctx.Call("arc", cx, cy, plusButtonRadius, 0, 2*math.Pi)
	a.cctx.Call("fill")
	a.cctx.Set("strokeStyle", a.pal.CircleRim)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("stroke")
}

// drawURLBackButton runs history.back() on the descended Chromium tab.
func (a *App) drawURLBackButton() {
	cx, cy := a.plusButtonCenter()
	a.drawCircleButtonChrome(cx, cy)

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

// drawURLRefreshButton opens the URL stream on a frozen url descent.
func (a *App) drawURLRefreshButton() {
	cx, cy := a.plusButtonCenter()
	a.drawCircleButtonChrome(cx, cy)

	band, _ := a.barTheme()
	drawRefreshIcon(a.cctx, cx, cy, 7.0, band)
}

// drawFreezeButton freezes a live shell descent; once frozen the same circle
// carries the reconnect arrow.
func (a *App) drawFreezeButton() {
	cx, cy := a.plusButtonCenter()
	a.drawCircleButtonChrome(cx, cy)

	band, _ := a.barTheme()
	beginSlotGlyph(a.cctx, band)
	a.cctx.Call("beginPath")
	const r = 7.0
	for _, d := range [][2]float64{{0, r}, {r * 0.87, r * 0.5}, {r * 0.87, -r * 0.5}} {
		a.cctx.Call("moveTo", cx-d[0], cy-d[1])
		a.cctx.Call("lineTo", cx+d[0], cy+d[1])
	}
	a.cctx.Call("stroke")
	endGlyph(a.cctx)
}

// drawURLOpenTabButton replaces refresh where caps.LiveURL is false: it opens
// the address in a browser tab and the tile stays frozen.
func (a *App) drawURLOpenTabButton() {
	cx, cy := a.plusButtonCenter()
	a.drawCircleButtonChrome(cx, cy)

	c := a.cctx
	band, _ := a.barTheme()
	c.Set("strokeStyle", band)
	c.Set("lineWidth", 2.0)
	c.Set("lineCap", "round")
	c.Call("strokeRect", cx-7, cy-1, 8.0, 8.0)
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

// drawGridLines fades out when cells are tiny, so zoom-out paints no wash.
func (a *App) drawGridLines(color string, ps dragdrop.Pane, r pane.Rect) {
	cellSize := ps.Cell()
	originX, originY := ps.CellToScreen(0, 0)
	drawGridLinesIn(a.cctx, color, r.X, r.Y, r.W, r.H, cellSize, originX, originY)
}

// drawGridLinesIn spaces lines at cellSize with cell (0, 0) at (originX,
// originY). Under 4px cells it draws nothing.
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

// drawNodeWithPreview previews a well's child grid at the child's cell scale,
// so the descent zoom crosses no discontinuity.
func (a *App) drawNodeWithPreview(n *gridwellv1.Tile, x, y, w, h, parentCellSize float64, selected, outside, dashed bool, paintPaneID string) {
	switch n.Kind {
	case rpc.KindText:
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
		a.drawNode(a.cctx, n, x, y, w, h, selected, outside, tileBorderPx, dashed)
		return
	}
	// One level: drawChildPreview paints its children through the flat drawNode.
	child, haveChild := a.c.Grid(n.ChildGridId)
	if !haveChild {
		a.fetchGrid(n.ChildGridId)
	}
	fillRectC(a.cctx, x, y, w, h, a.pal.Bg)

	// previewCell is parentCell times the well's intrinsic ratio, so the path
	// swap at Overtake_now is continuous.
	wl := wellOf(n)
	previewCell := parentCellSize * wl.Ratio()
	showPreview := haveChild && previewCell >= 0.5

	if isExitWell(n) && !showPreview {
		a.drawPluginGlyph(a.pluginGlyph(n.ChildGridId), x, y, w, h)
	} else {
		withClip(a.cctx, x, y, w, h, func() {
			// Aligned so the child point the well's framing centers on lands at the
			// well's center, where the descent viewport puts it.
			viewCenterX, viewCenterY := wl.Center()
			wellCenterX := x + w/2
			wellCenterY := y + h/2
			originX := wellCenterX - viewCenterX*previewCell
			originY := wellCenterY - viewCenterY*previewCell
			drawGridLinesIn(a.cctx, a.pal.GridLineInterior, x, y, w, h, previewCell, originX, originY)

			if showPreview {
				var hide string
				if a.ghost != nil && a.ghost.hiddenPaneID == paintPaneID {
					hide = a.ghost.hiddenTileID
				}
				a.drawChildPreview(child, viewCenterX, viewCenterY,
					wellCenterX, wellCenterY, previewCell, x, y, w, h, hide)
			}
		})
	}

	// Every well is blue; the dash means a link.
	a.strokeTileFrame(a.cctx, x, y, w, h, a.pal.FocusBorder, dashed, selected)
	a.drawTileBannerLabel(n, x, y, w, h, outside)
}

// tileReadOnly holds for a plugin's text tile, which has no write-back, and
// for an unknown grid.
func (a *App) tileReadOnly(n *gridwellv1.Tile) bool {
	writable, _ := a.gridWritable(n.GridId)
	return n.Kind == rpc.KindText && !writable
}

// isLinkTile reports a reference: trashing one unlinks it. Reference is the
// one signal; a uuid comparison would miss a same-plugin mount.
func isLinkTile(n *gridwellv1.Tile) bool {
	return n.Reference
}

func setTileDash(c js.Value)   { c.Call("setLineDash", jsArray(5, 3)) }
func clearTileDash(c js.Value) { c.Call("setLineDash", jsArray()) }

const bannerFontFamily = `ui-sans-serif, system-ui, -apple-system, sans-serif`

// bannerGeom clamps the banner font to 9 to 16 screen px; the text preview
// reads the same formula.
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

func (a *App) drawTileBannerLabel(n *gridwellv1.Tile, x, y, w, h float64, outside bool) {
	a.drawTileBannerLabelIn(n, x, y, w, h, a.bannerTextColor(n, outside))
}

// drawTileBannerLabelIn takes the text color, so a dead link's grey label
// shares the one banner geometry.
func (a *App) drawTileBannerLabelIn(n *gridwellv1.Tile, x, y, w, h float64, textColor string) {
	text := tileface.BannerText(n)
	if text == "" {
		return
	}
	ix := x + tileBorderPx
	iy := y + tileBorderPx
	iw := w - 2*tileBorderPx
	ih := h - 2*tileBorderPx
	if iw <= 0 || ih <= 0 {
		return
	}
	fontPx, bannerH, shown := bannerGeom(h, ih)
	if !shown {
		return
	}
	a.bannerTexts[n.Id] = text
	withClip(a.cctx, ix, iy, iw, ih, func() {
		fillRectC(a.cctx, ix, iy, iw, bannerH, a.pal.SourceLabelBg)
		drawLabel(a.cctx, text, ix+4, iy+bannerH/2, labelOpts{
			font: fontSpec(fontPx, bannerFontFamily, true), fill: textColor, baseline: "middle",
		})
	})
}

func (a *App) bannerTextColor(n *gridwellv1.Tile, outside bool) string {
	switch tileface.BannerHue(n, outside) {
	case tileface.HueShell:
		return a.pal.ShellBorder
	case tileface.HueWell:
		return a.pal.FocusBorder
	case tileface.HueHost:
		return a.pal.PluginBorder
	case tileface.HueURL:
		return a.pal.URLLine
	case tileface.HueText:
		return a.pal.MarkdownLine
	}
	return a.pal.Muted
}

// fetchTileContent never doubles an in-flight fetch, or a stale reply could
// land after a fresher one.
func (a *App) fetchTileContent(tileID string) {
	if tileID == "" {
		return
	}
	if _, ok := a.c.TileContent(tileID); ok {
		return
	}
	// A leaf link in an undeclared namespace is not asked; see fetchGrid.
	if a.deadNamespace(tileID) {
		return
	}
	ctx, done, ok := a.fetch.contents.Ask(tileID)
	if !ok {
		return
	}
	go func() {
		defer done()
		// Coalesced: body fetches land in bursts.
		_ = a.loadTileContent(ctx, tileID, func() { a.scheduleFrame(traceevent.WhyContent) })
	}()
}

// loadTileContent is the one content-fetch body. It returns the error as well
// as surfacing it, because a waiting caller has a continuation.
func (a *App) loadTileContent(ctx context.Context, tileID string, then func()) error {
	asked := a.c.AskContent(tileID)
	data, _, version, err := a.cl.ReadContent(ctx, tileID)
	// clientsync.ReactRead is the one table; this runs its arms.
	o := clientsync.Of(err)
	a.fetch.contents.Settle(tileID, clientsync.ReactRead(o))
	if err != nil {
		// Say why, unless the dead face already does.
		if clientsync.ReadSurfaces(o) {
			a.surfaceRPCError("ReadContent", err)
		}
		return err
	}
	a.c.PutFetchedContent(tileID, data, version, asked)
	a.refreshFileOverlay()
	then()
	return nil
}

// tileBody reads through ReadContent, keyed by ContentID, so a leaf link
// renders the one shared copy of its target's bytes.
func (a *App) tileBody(n *gridwellv1.Tile) ([]byte, bool) {
	if b, ok := a.c.TileContent(rpc.ContentID(n)); ok {
		return b, true
	}
	a.fetchTileContent(rpc.ContentID(n))
	return nil, false
}

// drawChildPreview paints the cached child grid at previewCell px. Child
// wells render flat: the one-level rule.
func (a *App) drawChildPreview(child *cache.Grid,
	centerCellX, centerCellY, centerScreenX, centerScreenY, previewCell float64,
	clipX, clipY, clipW, clipH float64,
	hiddenTileID string,
) {
	c := a.cctx
	childInHost := child.HostContent()
	borderPx := previewBorderPxFor(previewCell)
	for _, n := range child.Tiles {
		if hiddenTileID != "" && n.Id == hiddenTileID {
			continue
		}
		nodeScreenX := centerScreenX + (float64(n.X)-centerCellX)*previewCell
		nodeScreenY := centerScreenY + (float64(n.Y)-centerCellY)*previewCell
		nodeScreenW := float64(n.W) * previewCell
		nodeScreenH := float64(n.H) * previewCell
		if nodeScreenX+nodeScreenW < clipX || nodeScreenY+nodeScreenH < clipY ||
			nodeScreenX > clipX+clipW || nodeScreenY > clipY+clipH {
			continue
		}
		nn := n
		// url and shell children skip their JPEGs, so a well's interior reads uniformly.
		a.drawNode(c, nn, nodeScreenX, nodeScreenY, nodeScreenW, nodeScreenH, false, tileface.Outside(nn, childInHost), borderPx, false)
	}
}

// drawNode is the flat renderer, for nested previews and non-well tiles.
func (a *App) drawNode(c js.Value, n *gridwellv1.Tile, x, y, w, h float64, selected bool, outside bool, borderPx float64, dashed bool) {
	if dashed {
		setTileDash(c)
		defer clearTileDash(c)
	}
	fill, line := a.pal.Locked, ""
	switch n.Kind {
	case rpc.KindWell:
		fill, line = a.pal.Bg, a.pal.FocusBorder
	case rpc.KindURL:
		fill, line = a.pal.URLFill, a.pal.URLLine
	case rpc.KindShell:
		fill, line = a.pal.ShellFill, a.pal.ShellBorder
	case rpc.KindText:
		fill, line = a.pal.MarkdownFill, a.pal.MarkdownLine
		if outside {
			fill, line = a.pal.PluginFill, a.pal.PluginBorder
		}
	case rpc.KindPane:
		fill, line = a.pal.PaneTileFill, a.pal.PaneTileBorder
	}
	fillRectC(c, x, y, w, h, fill)
	if line != "" {
		strokeTileBorder(c, x, y, w, h, line, borderPx)
	}
	if selected {
		a.drawSelectedTileOutline(c, x, y, w, h)
	}
}

// drawGhostTile is drawNodeWithPreview at near-zero fragmentation; over a
// black hole it cross-fades into a trashcan.
func (a *App) drawGhostTile(n *gridwellv1.Tile, x, y, w, h, parentCellSize float64, r pane.Rect, frag float64) {
	outside := tileface.Outside(n, false)
	// Dashed means this is, or becomes, a reference: it shows mid-drag which
	// right-button mode is armed.
	dashed := isLinkTile(n) || (a.ghost != nil && a.ghost.link)
	if frag < 0.02 {
		a.drawNodeWithPreview(n, x, y, w, h, parentCellSize, false, outside, dashed, "")
		if a.ghost != nil {
			if a.ghost.forbidden {
				a.drawGhostNoEntryBadge(a.cctx, x+w/2, y+h/2, min(w, h))
			} else if a.ghost.link {
				a.drawGhostLinkBadge(a.cctx, x+w/2, y+h/2, min(w, h))
			}
		}
		return
	}
	if frag > 1 {
		frag = 1
	}
	if frag < 0.98 {
		a.cctx.Set("globalAlpha", 1.0-frag)
		a.drawNodeWithPreview(n, x, y, w, h, parentCellSize, false, outside, dashed, "")
		a.cctx.Set("globalAlpha", 1.0)
	}
	a.cctx.Set("globalAlpha", frag)
	a.drawTrashcanIcon(a.cctx, x, y, w, h)
	a.cctx.Set("globalAlpha", 1.0)
}

// paneBorderColorFor picks the border from what the pane descended into.
func (a *App) paneBorderColorFor(p *pane.Pane, g *cache.Grid, gridOK bool, focused bool, urlLive bool) string {
	return pane.BorderColor(a.borderInputFor(p, g, gridOK, focused, urlLive), a.paneBorderColors())
}

// borderInputFor is shared with the bottom bar, so frame and band agree.
func (a *App) borderInputFor(p *pane.Pane, g *cache.Grid, gridOK bool, focused bool, urlLive bool) pane.BorderInput {
	in := pane.BorderInput{
		HasTextFocus: p.ContentID() != "",
		DescentDepth: len(p.Path()),
		Focused:      focused,
		URLLive:      urlLive,
	}
	if p.ContentID() != "" {
		// descendedTile, so an ephemeral descent resolves too, gray because ascent
		// deletes it.
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

func (a *App) paneBorderColors() pane.BorderColors {
	return pane.BorderColors{
		Focused:        a.pal.FocusBorder,
		FocusedFaded:   a.pal.FocusBorderFaded,
		Text:           a.pal.MarkdownLine,
		TextFaded:      a.pal.MarkdownLineFaded,
		URL:            a.pal.URLLine,
		URLFaded:       a.pal.URLLineFaded,
		URLLive:        a.pal.URLLiveLine,
		URLLiveFaded:   a.pal.URLLiveLineFaded,
		Shell:          a.pal.ShellBorder,
		ShellFaded:     a.pal.ShellBorderFaded,
		Exit:           a.pal.PluginBorder,
		ExitFaded:      a.pal.PluginBorderFaded,
		Ephemeral:      a.pal.EphemeralBorder,
		EphemeralFaded: a.pal.EphemeralBorderFaded,
	}
}

// drawEdgeIndicators marks every tile outside the viewport on the ray from
// the viewport center.
func (a *App) drawEdgeIndicators(nodes map[string]*gridwellv1.Tile, ps dragdrop.Pane, r pane.Rect) {
	cellSize := ps.Cell()
	const inset = 12.0
	innerL := r.X + inset
	innerR := r.X + r.W - inset
	innerT := r.Y + inset
	innerB := r.Y + r.H - inset
	cx := r.X + r.W/2
	cy := r.Y + r.H/2

	a.cctx.Set("fillStyle", a.pal.EdgeDot)
	for _, n := range nodes {
		sx, sy := ps.CellToScreen(float64(n.X), float64(n.Y))
		w := float64(n.W) * cellSize
		h := float64(n.H) * cellSize
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
		ang := math.Atan2(dy, dx)
		drawTriangle(a.cctx, mx, my, ang, 6)
	}
}

// drawTriangle is every arrowhead in the renderer; size is center to tip.
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

// drawGridNotice paints a status line in a pane whose grid is not cached;
// pane.GridNotice words it. A mounted grid falls back to the id; see
// client/scratch.
func (a *App) drawGridNotice(r pane.Rect, gid string) {
	if r.W < 80 || r.H < 40 {
		return
	}
	name := gid
	if pl, ok := a.pluginByUUID(uuidOf(gid)); ok && pl.Label != "" {
		name = pl.Label
	}
	label := pane.GridNotice(name, a.fetch.grids.Failed(gid))
	drawLabel(a.cctx, label, r.X+r.W/2, r.Y+r.H/2, labelOpts{
		font: "13px system-ui, sans-serif", fill: a.pal.Muted,
		align: "center", baseline: "middle", maxW: r.W - 16,
	})
}
