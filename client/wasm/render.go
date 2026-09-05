//go:build js && wasm

package main

import (
	"context"
	"math"
	"strconv"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/wsbar"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

const (
	colorBg          = "#0c0d11"
	colorFileInnerBg = "#1c1f26"
	// Notice-strip colors (client/errsurface). Error rows are a dark red
	// band with legible red-tinted text; Info rows (expected reconciliation,
	// e.g. a lost version race that was refetched) use the focus-blue family
	// so they read as "note", not "alarm".
	colorErrStripBg    = "#3a1216"
	colorErrStripText  = "#ff9a9a"
	colorInfoStripBg   = "#16203a"
	colorInfoStripText = "#9ab0ff"
	colorPaneBorder    = "#1f2229"
	colorFocusBorder   = "#4a6fff"
	// colorFocusBorderFaded is the pane outline for descended-but-not-
	// focused panes. Same hue as the focus blue, lower saturation and
	// value so the focused pane still pops, but the others stay
	// visible as "you're also looking inside something here".
	colorFocusBorderFaded = "#2c3d70"
	// colorPluginBorder is the warm brown that marks the plugin / host
	// identity: the launcher home page, plugin (exit) wells and their icons,
	// and read-only host content. Earth-tone "ground", distinct from the grid
	// blue, text green, url purple, and shell orange — it reads as "a boundary
	// into another world", never "a grid you can place tiles in".
	colorPluginBorder      = "#7a6a4a"
	colorPluginBorderFaded = "#4a4233"
	// colorPluginFill is the body color for read-only host content (file
	// metadata, the @info tile in a proc grid) — a dark brown of the same
	// family so the tile reads as "host view", not editable green text.
	colorPluginFill = "#2a2419"
	// Grid lines are uniformly blue: every grid the user navigates is a grid,
	// regardless of which plugin owns it. Quieter than the focus blue border
	// so the lines tint the canvas without overwhelming the content.
	colorGridLineInterior = "#1c2540"
	// Content-tile colors. Each tile kind has its own identity; the
	// user reads tiles by color at a glance and the icon / preview
	// reveals the rest.
	//   - text      → olive green
	//   - url       → purple
	colorMarkdownFill      = "#2c3a1a"
	colorMarkdownLine      = "#8aa05a"
	colorMarkdownLineFaded = "#4a5a3a"
	colorURLFill           = "#2b1a3a"
	colorURLLine           = "#7a5a9a"
	colorURLLineFaded      = "#4a3a5a"
	// colorURLLiveLine is the pane border used when a URL tile is live, with
	// a native view attached. The same purple hue as colorURLLine but
	// brighter and more saturated, so it is clearly distinct from the frozen
	// state. The faded variant dims when the pane loses focus, but stays
	// brighter than the frozen faded purple, so live against frozen still
	// reads at a glance across unfocused panes.
	colorURLLiveLine      = "#a07acc"
	colorURLLiveLineFaded = "#5c4478"
	// colorShellBorder is the orange identity for shell tiles and the pane
	// border on a shell descent — bash runs outside Gridwell's data world, so
	// it gets its own warm hue, distinct from plugin brown.
	colorShellBorder      = "#d4863a"
	colorShellBorderFaded = "#6e4a22"
	// colorShellFill is the dark-orange body shown behind a shell tile's
	// preview / placeholder glyph.
	colorShellFill = "#2e220f"

	// Pane tiles: teal, a hue no other kind uses, so a stored layout reads
	// distinctly in a grid, a palette swatch, and a ghost.
	colorPaneTileFill   = "#10282b"
	colorPaneTileBorder = "#3aa8a8"
	// colorEphemeralBorder is the gray pane border for a descent into an
	// ephemeral (scratch-grid) tile, url or shell alike. Gray overrides the
	// kind color, because ascending deletes the tile — a shell's tmux
	// session and all its processes included — and the border is the
	// warning.
	colorEphemeralBorder      = "#8b8e96"
	colorEphemeralBorderFaded = "#4b4d52"
	// colorSourceLabelBg is the translucent strip behind the name label
	// shown at the top of a tile (plugin / host content). Dark enough that
	// the warm foreground reads clearly over any underlying preview.
	colorSourceLabelBg = "rgba(20, 12, 8, 0.78)"
	// colorNoEntry{Fill,Stroke} drive the international "no entry" badge
	// drawn on the ghost when a drop would be rejected. International
	// convention: red disc, white ring, white diagonal slash.
	colorNoEntryFill   = "#c93030"
	colorNoEntryStroke = "#f6f6f6"
	colorLocked        = "#26262a"
	colorSelected      = "#e3b16f"
	// colorTrace is the ascent-trace ring: the fading "you just came from
	// here" outline. A brighter yellow than the gold selection, so the two
	// read differently while both are visible.
	colorTrace    = "#ffd94a"
	colorEdgeDot  = "#5a6a8a"
	colorPlusBg   = "#23252d"
	colorPlusBgHi = "#2d3140"
	// colorPlusBgDelete fills the + button when it's the live trashcan
	// delete target and the dragged tile is hovering over it — a danger red
	// confirming "release here deletes".
	colorPlusBgDelete = "#6e2b22"
	colorPlusFg       = "#c8c9ce"
	// colorNoLiveFg is the dimmed glyph color for the slashed go-live button
	// on hosts that cannot place a live web view (caps.LiveURL false).
	colorNoLiveFg   = "#787b84"
	colorMenuBg     = "#16181f"
	colorMenuItemHi = "#e8e9ee"
	colorMuted      = "#6c6f78"
	// Launcher tile tints for a non-enterable plugin (client/pluginhealth):
	// broken (Info failed/timed out) gets a red-tinted overlay — the same
	// alarm family as the error notice strip; rootless (healthy, no root
	// configured) gets a plain gray dimming — a configuration gap, not a
	// failure. Drawn as a translucent overlay atop the normal tile so the
	// plugin glyph / preview underneath still reads.
	colorLauncherBrokenTint   = "rgba(180, 40, 40, 0.38)"
	colorLauncherRootlessTint = "rgba(40, 40, 46, 0.55)"
	// A dead link (client/deadref): a link into a namespace this node no
	// longer declares. It is a state, not a failure, so it gets no alarm
	// color — the veil fades the tile most of the way back to the
	// background, and the outline and label are redrawn in the muted grey,
	// keeping the dash that says "link" and the label that says which one.
	// Grey and legible is the whole point: what you can throw away.
	colorDeadLinkVeil = "rgba(12, 13, 17, 0.72)"
	colorDeadLink     = "#5c5f68"
)

const (
	// paneBorderPx is the inset applied on every side of a pane's live
	// content view (URL WebContentsView, shell overlay) and the visible
	// thickness of the kind-colored border frame. The value is
	// load-bearing UX: the 2×paneBorderPx canvas strip between two adjacent
	// live-tile panes is the only surface a user can click to grab the
	// divider (a WebContentsView eats all mouse input over its rect).
	// Single source of truth lives in panebox.LiveViewInsetPx; this is a
	// local alias so render.go can keep its compact constant syntax.
	paneBorderPx = panebox.LiveViewInsetPx
	// tileBorderPx is the visible thickness of a tile outline. Sits
	// entirely INSIDE the tile rect so the banner label (and the cell
	// to the right / below) can't overlap it — every renderer reaches
	// for strokeTileBorder rather than rolling its own strokeRect.
	tileBorderPx = 2.0
)

// withClip runs paint with the canvas clipped to (x, y, w, h), restoring the
// previous state afterwards. Every renderer that has to keep its content
// inside a rect — a pane, a tile face, a bar crumb — brackets through here,
// so the save/beginPath/rect/clip/restore sequence is written once and a
// paint that returns early can never leave an unbalanced save behind.
func withClip(c js.Value, x, y, w, h float64, paint func()) {
	c.Call("save")
	c.Call("beginPath")
	c.Call("rect", x, y, w, h)
	c.Call("clip")
	paint()
	c.Call("restore")
}

// strokeTileBorder draws a borderPx-thick outline at `color` that sits
// entirely inside (x, y, w, h). Canvas centers the stroke on the path,
// so the rect is inset by half the line width on every side. Tiles pass
// tileBorderPx and panes paneBorderPx; drawChildPreview scales the border
// down so it stays proportionate to the cell at a distance.
func strokeTileBorder(c js.Value, x, y, w, h float64, color string, borderPx float64) {
	c.Set("strokeStyle", color)
	c.Set("lineWidth", borderPx)
	half := borderPx / 2
	c.Call("strokeRect", x+half, y+half, w-borderPx, h-borderPx)
}

// previewBorderPxFor scales tileBorderPx down for child-grid previews —
// when you're looking AT a grid from a distance, the borders should
// look proportional, not uniformly 2px regardless of cell scale.
// previewCell is the size in screen pixels of one child cell.
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

// drawSelectedTileOutline paints the gold "this tile is selected" frame
// just outside the cell so it sits independent of the kind-specific
// border. Pure visual chrome; no fill, no clip.
func drawSelectedTileOutline(c js.Value, x, y, w, h float64) {
	c.Set("strokeStyle", colorSelected)
	c.Set("lineWidth", 2.0)
	c.Call("strokeRect", x-1, y-1, w+2, h+2)
	c.Set("lineWidth", 1.0)
}

// strokeTileFrame paints the coda every full-size tile renderer ends with:
// the kind-colored inset border, dashed when the tile is a link, then the
// gold selection ring outside it. One order for all of them — the frame over
// the tile's content, the dash cleared before the selection ring so the gold
// stays solid — so no renderer can drift into a different one.
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

// drawDeadLinkFace paints a dead link over the tile already drawn: a veil
// that fades it back toward the pane background, then the dashed outline and
// the banner label redrawn in the muted grey. It is an overlay for the same
// reason drawPluginHealthTint is — every kind draws itself first and this
// says one thing about the result — and it is the whole visible half of the
// state. Nothing is asked for a dead link and nothing is said about it, so
// the tile has to carry the news on its own.
//
// The selection ring sits outside the footprint and survives the veil: a
// dead link is still a tile you can select and delete, which is the point.
func (a *App) drawDeadLinkFace(n *rpc.Tile, x, y, w, h float64) {
	if !a.deadLink(n) {
		return
	}
	a.cctx.Set("fillStyle", colorDeadLinkVeil)
	a.cctx.Call("fillRect", x, y, w, h)
	strokeTileFrame(a.cctx, x, y, w, h, colorDeadLink, true, false)
	a.drawTileBannerLabelIn(n, x, y, w, h, colorDeadLink)
}

// drawTraceOutline paints the fading yellow ascent-trace ring just outside
// the tile (same geometry as the selection outline, thicker and alpha-faded).
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

// plusButtonRadius mirrors palette.Default().PlusRadius so the wasm
// canvas drawing code (which arcs and fills the + button) can keep
// using a typed numeric literal rather than reaching into the
// palette.Config every time.
const plusButtonRadius = 14

// templateKind identifies one built-in tile primitive in the creation
// palette. Order matters: primitiveKinds determines the layout in the popover
// and the indices hit-testing uses. File and process wells are not
// primitives; they are reached by dragging the fs or proc plugin items.
type templateKind int

const (
	tplWell templateKind = iota
	tplMarkdown
	tplURL
	// tplShell spawns an interactive bash shell tile. Starts frozen with
	// no preview; the user refreshes to spawn the PTY.
	tplShell
	// tplPane creates a pane tile: a stored split-pane layout you descend
	// into. Created never-arranged, so the first descent installs the
	// default single pane.
	tplPane
)

// primitive is everything the palette knows about one built-in tile kind:
// the stable name a test picks the swatch by, the synthetic 1x1 tile the
// swatch and the drag ghost paint, the identity glyph overlaid on it, and the
// create RPC a plain drop fires. One row per kind is the one owner of that
// set, so a kind cannot be half-added — a swatch with no glyph, a glyph with
// no create — and every reader derives from the same order.
type primitive struct {
	kind  templateKind
	name  string
	ghost rpc.Tile
	glyph func(a *App, x, y, w, h float64)
	// create fires this kind's create RPC into gridID — the drop target's
	// grid, which is an open well's child grid when the cursor promoted into
	// one, never re-derived from a pane here.
	create func(a *App, gridID string, cellX, cellY int64)
	// click is what a bare click on this kind's swatch does, with no drag and
	// so no destination cell: the ephemeral visit a url or shell opens
	// without placing a tile. nil is the default and the common case —
	// nothing happens and the menu stays open. It is a column rather than an
	// arm per kind because the fall-through was the bug: a swatch with no
	// hand-written arm let the click reach the canvas behind the popover.
	click func(a *App, p *pane.Pane)
}

// primitives is the palette layout order of the built-in tile primitives,
// left to right. They appear in any writable grid. primitiveKinds is its
// order, the layout the popover and the hit-test indices follow — derived,
// never a second list.
//
// Both are filled in init rather than by a var initializer: the create and
// glyph rows close over App methods, and the package-level reference graph
// from a method back to the table is a cycle the compiler refuses.
var (
	primitives     []primitive
	primitiveKinds []templateKind
)

func init() {
	primitives = []primitive{
		{
			kind: tplWell, name: "well",
			ghost:  rpc.Tile{Kind: rpc.KindWell, W: 1, H: 1},
			glyph:  func(a *App, x, y, w, h float64) { drawWellGlyph(a.cctx, x, y, w, h, colorFocusBorder) },
			create: func(a *App, gid string, cellX, cellY int64) { a.createWellAtCell(gid, cellX, cellY) },
		},
		{
			kind: tplMarkdown, name: "markdown",
			ghost:  rpc.Tile{Kind: rpc.KindText, W: 1, H: 1},
			glyph:  func(a *App, x, y, w, h float64) { drawDocumentGlyph(a.cctx, x, y, w, h, colorMarkdownLine) },
			create: func(a *App, gid string, cellX, cellY int64) { a.createTextAtCell(gid, []byte{}, cellX, cellY) },
		},
		{
			kind: tplURL, name: "url",
			ghost:  rpc.Tile{Kind: rpc.KindURL, W: 1, H: 1},
			glyph:  func(a *App, x, y, w, h float64) { drawGlobeGlyph(a.cctx, x, y, w, h, colorURLLine) },
			create: func(a *App, gid string, cellX, cellY int64) { a.createURLAtCell(gid, cellX, cellY) },
			click:  func(a *App, p *pane.Pane) { a.visitURLFromMenu(p) },
		},
		{
			kind: tplShell, name: "shell",
			ghost:  rpc.Tile{Kind: rpc.KindShell, W: 1, H: 1, AltText: "shell"},
			glyph:  func(a *App, x, y, w, h float64) { drawShellGlyph(a.cctx, x, y, w, h, colorShellBorder) },
			create: func(a *App, gid string, cellX, cellY int64) { a.createShellAtCell(gid, cellX, cellY) },
			click:  func(a *App, p *pane.Pane) { a.visitShellFromMenu(p) },
		},
		{
			kind: tplPane, name: "pane",
			ghost:  rpc.Tile{Kind: rpc.KindPane, W: 1, H: 1, AltText: "workspace"},
			glyph:  func(a *App, x, y, w, h float64) { drawPaneGlyph(a.cctx, x, y, w, h, colorPaneTileBorder) },
			create: func(a *App, gid string, cellX, cellY int64) { a.createPaneAtCell(gid, cellX, cellY) },
		},
	}
	primitiveKinds = make([]templateKind, len(primitives))
	for i, pr := range primitives {
		primitiveKinds[i] = pr.kind
	}
}

// primitiveFor returns the table row for k. Not-ok is an unknown kind: the
// callers each do nothing for it, exactly as their switches fell through.
func primitiveFor(k templateKind) (primitive, bool) {
	for _, pr := range primitives {
		if pr.kind == k {
			return pr, true
		}
	}
	return primitive{}, false
}

// paletteItem is one entry in the creation palette. It is either a
// configured plugin (click to enter, drag to drop an exit-well link)
// or a built-in tile primitive (drag to create). isPlugin selects which
// of plugin / primitive carries meaning.
type paletteItem struct {
	isPlugin  bool
	plugin    rpc.PluginInfo // when isPlugin (also set for a root ENTRY's owner)
	primitive templateKind   // when !isPlugin
	// entry is the plugin-declared root menu entry this pseudo-plugin swatch
	// came from — the home's trashcan, a plugin's second surface. Set only
	// alongside isPlugin; it names the entry so a test can tell a declared
	// root from the plugin's own row.
	entry *rpc.MenuEntry
	// promotePane, when set, marks a promote drag: the item is the ephemeral
	// url visit shown in that pane, dragged off the bar's current crumb, and
	// the drop creates a persistent url tile with its address and relocates
	// the pane onto it. A click without a drag does nothing, since the
	// current crumb is where you are.
	promotePane string
}

// paletteItems returns the palette entries for pane p, in display order:
// every configured plugin first, in server.yaml order, as the palette's top
// row, then — only when the pane's current grid is writable — the tile
// primitives. A read-only grid, such as an fs or proc grid, shows plugins
// only. A shells-disabled node offers no shell primitive: every palette
// consumer — layout, hit-testing, drag ghosts, the test hook — reads this one
// list, so the swatch is gone from all of them at once.
func (a *App) paletteItems(p *pane.Pane) []paletteItem {
	// The menu belongs to the pane's node: a remote pane's top row is the
	// remote node's plugins, exactly what a direct client of that node sees,
	// and the shell primitive obeys that node's policy. "" — local, or a
	// grid not yet cached — is the boot handshake.
	ctx := a.menuCtx(p)
	items := make([]paletteItem, 0, len(ctx.plugins)+len(primitiveKinds))
	for _, pl := range ctx.plugins {
		items = append(items, paletteItem{isPlugin: true, plugin: pl})
		// The plugin's root entries ride its row: extra doorways it declares
		// — the home's trashcan, a plugin's second surface. Each becomes a
		// pseudo-plugin swatch, with the entry's grid as the root and its
		// label and glyph as the face, so every downstream flow — ghost,
		// click-descend, drag-link, health — is the ordinary plugin path
		// with no new arms.
		for i := range pl.MenuEntries {
			e := &pl.MenuEntries[i]
			if e.GridID == "" {
				continue
			}
			// The framing zero-out lives in EntryPlugin: the handshake root
			// view belongs to the main root grid, and an entry grid opens at
			// the default framing. persistFraming's root-arm RootGridID
			// guard keeps its session framing off the main root's fact.
			items = append(items, paletteItem{isPlugin: true, plugin: rpc.EntryPlugin(pl, *e), entry: e})
		}
	}
	if a.gridWritable(a.gridIDForPane(p)) {
		for _, k := range primitiveKinds {
			// The shell swatch obeys the context node's policy, and only
			// that. The PTY rides the web door, so every host can attach,
			// and reading the local caps here would be a second and wrong
			// owner: a remote pane's swatch follows the remote node.
			if k == tplShell && ctx.shellsDisabled {
				continue
			}
			items = append(items, paletteItem{primitive: k})
		}
	}
	return items
}

// paletteTopRow counts the plugin swatches in items: the layout's row split.
// It is derived from the one item list, so it cannot desynchronize from the
// contextual plugin row.
func paletteTopRow(items []paletteItem) int {
	n := 0
	for _, it := range items {
		if it.isPlugin {
			n++
		}
	}
	return n
}

// ghostSizeLerpAlpha is the per-frame fraction by which the ghost's
// displayed cell size approaches its target. At 60 fps this gives a
// time constant of about 120 ms — quick but smooth, matching the
// "grow/shrink as the cursor enters/exits a well" feel.
const ghostSizeLerpAlpha = 0.20

// draw repaints the canvas. Cheap to call repeatedly: each repaint clears
// and redraws every pane fully.
func (a *App) draw() {
	if a.ghost != nil {
		// Lerp displayed size toward target. Snap when close enough so
		// the ghost doesn't jitter at the destination.
		ds := a.ghost.displayedCellSize
		ts := a.ghost.targetCellSize
		if ts > 0 && math.Abs(ts-ds) > 0.5 {
			a.ghost.displayedCellSize = ds + (ts-ds)*ghostSizeLerpAlpha
			a.scheduleFrame()
		} else if ts > 0 {
			a.ghost.displayedCellSize = ts
		}
		// Same lerp for fragmentation — drag onto a black hole and the
		// ghost shatters in; drag back out and it reassembles.
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

	// The creation palette floats above every pane: draw it last, after all
	// panes, so it isn't painted over by a neighbour it overflows into.
	if a.menu.IsOpen() {
		if mp := a.tree.FindPane(a.menu.PaneID()); mp != nil {
			if _, ok := rects[mp.ID]; ok {
				a.drawPalette(mp)
			}
		}
	}

	// In-flight right-button gesture preview (split line, swap arrow,
	// red close-warning border). Drawn on top of all panes but below
	// the textarea overlay (which lives in DOM, not canvas).
	if a.rightDrag != nil {
		a.drawRightDragPreview()
	}
	// In-flight left-button divider resize: the layout crushes live, and
	// this adds the divider hint plus the red release-closes-this-side
	// warning.
	if a.leftResize != nil {
		a.drawLeftResizePreview(a.leftResize)
	}

	// Reposition the textarea overlay (if any) so it tracks the focused
	// pane through resizes and pane-tree edits.
	a.syncTextOverlayPosition()
	// The rendered-HTML overlay tracks the focused pane the same way:
	// position, content key, and visibility per frame.
	a.refreshRenderedOverlay()
	// Same for any live shell overlays — xterm host divs follow their
	// pane's screen rect each frame.
	a.syncShellOverlayPosition()
	// And any live URL tiles — native WebContentsViews follow their pane's
	// content box and park off-screen during canvas-overlay gestures.
	a.syncURLViews()
	// The reserved bottom bands, drawn last so nothing paints over them:
	// the one bottom bar, then the notice strip.
	a.drawBottomBar()
	a.drawErrStrip()
	// Inside a pane tile, a teal line wraps the whole window: a second hint
	// that you are inside one, alongside the named crumb in the bar. It owns
	// a reserved gutter — rootLayoutRect insets the panes by wsOutlinePx —
	// so it never paints over the panes' kind-colored borders. Drawn last so
	// nothing covers it.
	if a.ws.Depth() > 0 {
		// The same height the layout used: the outline wraps the pane area
		// and stays off the bar band and notice strip below it.
		h := a.paneAreaH()
		a.cctx.Set("strokeStyle", colorPaneTileBorder)
		a.cctx.Set("lineWidth", wsOutlinePx)
		a.cctx.Call("strokeRect", wsOutlinePx/2, wsOutlinePx/2,
			a.width-wsOutlinePx, h-wsOutlinePx)
		a.cctx.Set("lineWidth", 1.0)
	}
	// The first-descent capture animation: the pane tile's face growing into
	// the level outline while the content underneath never moves. Its end
	// rect is the outline above, so the install handoff is seamless. Drawn
	// last, like the outline it becomes.
	if e := a.wsExpand; e != nil {
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

	// Inside a pane tile, every repaint arms the debounced layout persister:
	// the blob is derived from the live tree by encode and hash-diff, so
	// there is no per-gesture persistence call site to forget.
	a.scheduleWorkspaceSave()

	// The same shape for grid framing: every repaint arms the settle
	// persister, whose writers no-op when nothing moved, so framing reaches
	// the server without waiting for an ascent.
	a.scheduleFramingSave()
}

// layoutPanes walks the tree and assigns each leaf pane a screen rectangle.
// The notice strip is *reserved layout*: panes fill (height − strip), so a
// pending error owns pixels nothing — including a native WebContentsView,
// which tracks pane rects — can paint over. Input hit-testing shares this
// function, so render and input cannot disagree about where panes are.
func (a *App) layoutPanes() map[string]pane.Rect {
	return pane.Layout(a.tree, a.rootLayoutRect())
}

// wsOutlinePx is the width of the teal pane-tile outline: the reserved gutter
// panes inset by while inside a pane tile, so the line and the pane borders
// never overlap.
const wsOutlinePx = 3.0

// paneAreaH is the height the pane tree occupies: the window minus the
// reserved notice strip minus the one bottom bar's band (wsbar.Band). It is
// also the band's top edge. rootLayoutRect, the pane-tile outline, and the
// descent animation all read this one number, so the panes, the outline, and
// the bar can never disagree about where they meet.
func (a *App) paneAreaH() float64 {
	h, _ := wsbar.Band(a.height, errsurface.StripHeight(a.errs.Len()))
	return h
}

// rootLayoutRect is the rectangle the pane tree lays out into: the canvas
// minus the reserved notice strip and bar band, and, inside a pane tile, minus
// the outline gutter on all four sides. One owner, because the cascading
// divider resize (pane.ResizeThrough) must see the exact rect the layout uses.
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

// drawErrStrip paints the notice strip in the reserved band at the bottom of
// the canvas. Geometry (rows, overflow, labels) comes from errsurface so the
// click-to-dismiss hit test reads the identical layout.
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

// drawPane draws one pane's contents.
//
// The pane chrome (border, grid lines, + button) is always drawn, even when
// the target grid hasn't loaded yet. That way the user can see the pane is
// live and use the + button (or Home key, etc.) to recover from a stale
// descent path.
func (a *App) drawPane(p *pane.Pane, r pane.Rect) {
	gid := a.gridIDForPane(p)
	g, gridOK := a.c.Grid(gid)

	// Clip content to the inside of the border. The border itself is
	// painted on top at the end of this function so it always frames
	// the content cleanly, even if a node or markdown text would
	// otherwise paint over the edge. Every pane has a border now
	// (root included, with its earth-tone hue), so the inset is the
	// same paneBorderPx for all panes.
	const inset = paneBorderPx
	withClip(a.cctx, r.X+inset, r.Y+inset, r.W-2*inset, r.H-2*inset, func() {
		pscreen := paneToDragdrop(p, r)

		// Grid lines render against the background whether or not the grid has
		// loaded: they communicate the coordinate system. A focused text tile
		// has no grid coordinates and no zoom, so it gets a plain background
		// instead of a grid pattern, and the margin around the inner box is a
		// plain ascent zone. URL content fills the pane and covers this
		// anyway.
		if p.ContentID() != "" {
			a.cctx.Set("fillStyle", colorBg)
			a.cctx.Call("fillRect", r.X, r.Y, r.W, r.H)
		} else {
			a.drawGridLines(colorGridLineInterior, pscreen, r)
		}

		if !gridOK && gid != "" && p.ContentID() == "" {
			// The grid is not cached yet — a fetch is in flight, or a plugin is
			// still building its first listing — or its last fetch failed. Say
			// which, instead of showing an empty room.
			a.drawGridNotice(r, gid)
		}
		if gridOK {
			cellSize := pscreen.CellPx * pscreen.Zoom
			selected := a.selectedFor(p.ID)
			// In a content descent the pane is inside the tile: skip the
			// parent-grid walk and render the focused tile in the inner box,
			// inset by the text margin so the surrounding grid pattern is
			// visible. The inner-box bounds match the textarea exactly, so
			// outside the textarea the grid rules apply.
			if p.ContentID() != "" {
				// descendedTile, not g.Tiles[...], so an ephemeral url visit —
				// focused off the pane's grid, in the scratch grid — renders.
				if file, ok := a.descendedTile(p); ok {
					switch {
					case file.Kind == rpc.KindText && !file.ServesPage:
						ix, iy, iw, ih := textInnerBox(r)
						a.cctx.Set("fillStyle", colorFileInnerBg)
						a.cctx.Call("fillRect", ix, iy, iw, ih)
						a.drawMarkdownInPane(p, &file, ix, iy, iw, ih)
					case file.WebContent():
						// url tiles and serves_page tiles take the same web-content
						// descent: a preview when frozen, a native view when live.
						ix, iy, iw, ih := paneContentBox(r)
						a.drawURLTileInPane(&file, ix, iy, iw, ih)
					case file.Kind == rpc.KindShell:
						ix, iy, iw, ih := paneContentBox(r)
						a.drawShellTileInPane(p, &file, ix, iy, iw, ih)
					default:
						ix, iy, iw, ih := textInnerBox(r)
						a.cctx.Set("fillStyle", colorFileInnerBg)
						a.cctx.Call("fillRect", ix, iy, iw, ih)
					}
				}
			} else {
				inHost := g != nil && g.Meta.HostContent
				for _, n := range g.Tiles {
					if dragdrop.HiddenMatch(a.ghostHiddenTile(), a.ghostHiddenPane(), p.ID, n.ID) {
						continue
					}
					left, top := pscreen.CellToScreen(float64(n.X), float64(n.Y))
					w := float64(n.W) * cellSize
					h := float64(n.H) * cellSize
					if left+w < r.X || top+h < r.Y || left > r.X+r.W || top > r.Y+r.H {
						continue
					}
					nn := n
					outside := tileOutside(&nn, inHost)
					dashed := !inHost && isLinkTile(&nn)
					a.drawNodeWithPreview(&nn, left, top, w, h, cellSize, n.ID == selected, outside, dashed, p.ID)
					a.drawPluginHealthTint(&nn, left, top, w, h)
					a.drawDeadLinkFace(&nn, left, top, w, h)
				}
				// Ascent trace: the fading "you just came from here" outline on
				// the tile this pane most recently ascended out of. Drawn after
				// the tiles so it paints on top; the alpha decays through the
				// frame loop, which pruneTraces keeps ticking before dropping
				// the entry.
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
					a.drawGhostTile(&gn, a.ghost.screenX, a.ghost.screenY, w, h, gcs, r,
						a.ghost.displayedFragmentation)
				}
			}
		}
	})

	// Border on top so content can paint up to the pane edge without
	// bleeding visibly into the chrome. The hue follows what we've
	// descended INTO, so the frame echoes the tile that put us here:
	//   - well descent (path > 0, no file focus) → blue
	//   - text descent (file focus on a text tile) → green
	//   - URL descent (file focus on a url tile) → purple
	//   - root (no path, no file focus) → tan
	// Focused panes get the saturated variant, others a desaturated
	// one of the same hue so you can still see at a glance which
	// other panes are also "looking inside" something.
	focused := p.ID == a.tree.Focus
	urlLive := a.urlViewFor(p.ID) != nil
	border := a.paneBorderColorFor(p, g, gridOK, focused, urlLive)
	strokeTileBorder(a.cctx, r.X, r.Y, r.W, r.H, border, paneBorderPx)

	// The per-mode circle button — URL back and refresh, shell refresh, the
	// + menu — lives in the bottom bar's right-end slot, drawn by
	// drawBarSlot. Panes carry no corner chrome.
}

// drawCircleButtonChrome paints the filled, bordered circle shared by the
// bar-slot buttons (URL back, refresh), so the position and look match the +
// button. Like it, the face wears the pane's family hue. The caller then
// draws its glyph on top.
func (a *App) drawCircleButtonChrome(cx, cy float64) {
	_, button := a.barTheme()
	a.cctx.Set("fillStyle", button)
	a.cctx.Call("beginPath")
	a.cctx.Call("arc", cx, cy, float64(plusButtonRadius), 0, 2*math.Pi)
	a.cctx.Call("fill")
	a.cctx.Set("strokeStyle", "#dff4f4")
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("stroke")
}

// drawURLBackButton paints the bar-slot button on a URL-tile descent.
// Click → history.back() on the descended Chromium tab. Same circular
// chrome as the + button so the position is muscle-memory-compatible.
func (a *App) drawURLBackButton() {
	cx, cy := a.plusButtonCenter()
	a.drawCircleButtonChrome(cx, cy)

	// Left-pointing arrow: a horizontal stem with a chevron at its left end.
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

// drawURLRefreshButton paints the bar-slot button on a frozen URL-tile
// descent. Click → open URL stream (same action as the right-drag-down
// refresh gesture). Same circular chrome as drawURLBackButton so the
// position is muscle-memory-compatible.
func (a *App) drawURLRefreshButton() {
	cx, cy := a.plusButtonCenter()
	a.drawCircleButtonChrome(cx, cy)

	// Refresh glyph: reuse drawRefreshIcon at a size that fits the button circle.
	band, _ := a.barTheme()
	drawRefreshIcon(a.cctx, cx, cy, 7.0, band)
}

// drawURLOpenTabButton paints the bar-slot button on a frozen URL-tile
// descent when this host cannot go live (caps.LiveURL false: a plain browser,
// with no Electron bridge). The glyph is an external link, a box with an
// arrow out its corner, and a click opens the tile's address in a new browser
// tab — the browser host's next-best descent. The tile itself stays frozen
// and untouched.
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

// drawGridLines paints faint lines at integer cell boundaries within the
// pane's visible region. The line color is chosen by the grid kind so
// blue/red/brown tint matches the pane border. Lines fade to invisible
// when cells are tiny so extreme zoom-out doesn't paint a solid wash.
func (a *App) drawGridLines(color string, ps dragdrop.Pane, r pane.Rect) {
	cellSize := ps.CellPx * ps.Zoom
	originX, originY := ps.CellToScreen(0, 0)
	drawGridLinesIn(a.cctx, color, r.X, r.Y, r.W, r.H, cellSize, originX, originY)
}

// drawGridLinesIn paints vertical/horizontal grid lines clipped to
// (clipX, clipY, clipW, clipH), spaced at cellSize pixels and aligned so
// integer cell (0, 0) lands at (originX, originY). Used for both the
// parent grid and well interiors so the visual scale of a well's preview
// is the same kind of grid the user is already seeing. The color is the
// kind-specific tint (root tan / interior blue / exit red).
//
// Sub-4px cell sizes draw nothing (the lines would be a solid wash).
// Above that, opacity fades up linearly so zoom-in feels like the grid
// "fades in" rather than appearing abruptly.
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

	// Range of integer cell indices whose grid line falls inside the clip.
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

// drawNodeWithPreview is the parent-grid renderer: wells get a one-level
// preview of their child grid (no recursion), and use the bright blue
// outline that matches the focused-pane color so wells are easy to spot.
//
// The well's interior is filled with the same background color as the
// pane and gets its own grid lines at the child's cell scale. That way
// the descent zoom never crosses a color or grid-line discontinuity:
// at the path-switch moment, the well's preview grid is exactly the
// child grid the user is about to see directly.
// paintPaneID names the pane whose contents are being painted, so the
// child-preview hide scopes to the drag's source pane only. It is "" for
// contexts with no pane, such as ghosts and bar crumbs.
func (a *App) drawNodeWithPreview(n *rpc.Tile, x, y, w, h, parentCellSize float64, selected, outside, dashed bool, paintPaneID string) {
	switch n.Kind {
	case rpc.KindText:
		if n.ServesPage {
			// A page tile's grid face is its content's image — an fs
			// thumbnail of the file — in the text family's border: it is a
			// file, and only its presentation is web content.
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
	// Trigger prefetch if we don't have the child grid yet. Recursion
	// stops naturally at one level because drawChildPreview paints its
	// children via the flat drawNode — no further fetches. Off-screen
	// culling in drawPane bounds how many top-level wells trigger a
	// fetch on first descent.
	child, haveChild := a.c.Grid(n.ChildGridID)
	if !haveChild {
		a.fetchGrid(n.ChildGridID)
	}
	// Background matches the surrounding pane so there's no color jump
	// when the well's outline crosses the screen edges during descent.
	a.cctx.Set("fillStyle", colorBg)
	a.cctx.Call("fillRect", x, y, w, h)

	// Preview cell size: previewCell = parentCell × ratio, where ratio is
	// the well's intrinsic ViewZoom, or DefaultWellViewZoom for an unvisited
	// well, which collapses this to the PreviewFactor calibration. At
	// parent = Overtake_now the previewCell matches the just-after-swap live
	// cell, making the path swap continuous.
	ratio := zoomtrans.EffectiveViewZoom(n.ViewZoom, zoomtrans.DefaultWellViewZoom)
	previewCell := parentCellSize * ratio
	showPreview := haveChild && previewCell >= 0.5

	if isExitWell(n) && !showPreview {
		// A cross-plugin well with no preview loaded yet shows the plugin's
		// identity glyph — the same drawing as its menu swatch and drag
		// ghost, so it reads identically before, during, and after the drop.
		a.drawPluginGlyph(a.pluginGlyph(n.ChildGridID), x, y, w, h)
	} else {
		withClip(a.cctx, x, y, w, h, func() {
			// Child grid lines inside the well, aligned so the child point the
			// well's framing centers on lands at the well's center. This is
			// exactly where the just-after-descent child viewport would put it,
			// so the lines glide continuously across the path swap — including
			// for a never-visited well, whose framing EffectiveCenter resolves
			// to its footprint's own center (the same value Descent uses).
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

	// Outline: every well is blue, and a cross-plugin well differs by the
	// dashed border, not the hue. Dashed always means a link (isLinkTile), a
	// reference you can unlink.
	strokeTileFrame(a.cctx, x, y, w, h, colorFocusBorder, dashed, selected)
	// Banner: "files" or "processes" for root exit wells, the basename for
	// sub-wells. A plain well (KindWell) gets no banner; tileBannerLabel
	// returns "" for it.
	a.drawTileBannerLabel(n, x, y, w, h, outside)
}

// tileReadOnly reports whether the user may edit n's text content. A text
// tile owned by a plugin — a file's metadata, a process's @info — is a
// read-only view of host state, because the plugin has no write-back, so the
// rendered/raw toggle, the textarea overlay, and the ascent's content write
// all consult this and the user cannot type into one and silently re-post a
// stale buffer.
func (a *App) tileReadOnly(n *rpc.Tile) bool {
	return n.Kind == rpc.KindText && !a.gridWritable(n.GridID)
}

// tileOutside reports whether a tile should be rendered with the "outside
// Gridwell" treatment (red outline / banner). True when:
//   - the tile's parent grid DECLARES host_content, so every row in it
//     represents host state, not Gridwell-owned data. The declaration is
//     the plugin's (plugin.v1 Info), carried on the grid: the client never
//     learns which kinds are host-backed
//   - the tile is itself an exit well (its child grid lives in another
//     plugin) anywhere — outside regardless of where the well sits
//   - the tile is a shell tile (bash runs outside Gridwell's data world)
func tileOutside(n *rpc.Tile, parentHostContent bool) bool {
	if parentHostContent {
		return true
	}
	if isExitWell(n) {
		// The child grid lives in another plugin (a host directory, the
		// process table). Red border, regardless of where the well sits.
		return true
	}
	if n.Kind == rpc.KindShell {
		// Bash runs outside Gridwell's data world. Same treatment.
		return true
	}
	return false
}

// isLinkTile reports whether n is a link rather than owned content: a
// reference whose child grid is in another plugin's id space — a host
// directory, the process table, a mounted plugin. These render with a dashed
// border, and dropping one on the trashcan only unlinks it, dropping the tile
// row; an owned interior well deletes for real.
//
// Reference is the one authoritative signal, stamped by the server
// (qualifyTiles) from the child_grid_id shape — the same fact the store's
// delete and clone key on, so render cannot disagree with them. It reads
// Reference alone: a uuid comparison would be a second, weaker derivation
// that misses a same-plugin mount. The one tile built client-side before any
// round trip, the launcher swatch, stamps Reference itself
// (rpc.PluginWellTile, pinned by TestPluginWellTile).
func isLinkTile(n *rpc.Tile) bool {
	return n.Reference
}

// tileBorderDash is the dash pattern for link-tile borders: short on/off so
// a 1–2px outline still reads clearly as "dashed = a link, safe to unlink."
func setTileDash(c js.Value)   { c.Call("setLineDash", jsArray(5, 3)) }
func clearTileDash(c js.Value) { c.Call("setLineDash", jsArray()) }

// tileBannerLabel returns the short label drawn at the top of a tile, or ""
// to suppress the banner. AltText is the single source of truth: the server
// stamps it at insert time from a per-kind derivation — the basename for
// files, the kernel Name for processes, "files" or "processes" for the roots,
// "info" for the synthetic info tile, the first non-empty line for text
// content. The client has no opinion of its own here.
func tileBannerLabel(n *rpc.Tile) string {
	return n.AltText
}

// bannerGeom is the one formula for the alt-text banner's font and strip
// height at tile height h and inner height ih: clamped screen px, 9 to 16, so
// the label reads at a constant size across zoom. The text preview starts its
// content below the banner strip and reads the same formula. shown=false when
// the tile cannot fit the label.
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

// drawTileBannerLabel paints tileBannerLabel(n) inside a small translucent
// banner at the top of the tile. Clipped to the tile rect so an over-long
// label can't bleed past the cell. When outside is true, the text uses
// the red exit color; otherwise the tile-kind color (green for text, blue
// for wells) so the banner echoes the tile's own color grammar.
func (a *App) drawTileBannerLabel(n *rpc.Tile, x, y, w, h float64, outside bool) {
	a.drawTileBannerLabelIn(n, x, y, w, h, bannerTextColor(n, outside))
}

// drawTileBannerLabelIn is drawTileBannerLabel with the text color named
// rather than derived: one banner geometry, so a tile drawn in another state
// — a dead link's grey — cannot end up with the label somewhere else.
func (a *App) drawTileBannerLabelIn(n *rpc.Tile, x, y, w, h float64, textColor string) {
	label := tileBannerLabel(n)
	if label == "" {
		return
	}
	// Inset by the tile border so the banner sits flush against the
	// inside edge of the outline — no 1px overlap of border and label.
	ix := x + tileBorderPx
	iy := y + tileBorderPx
	iw := w - 2*tileBorderPx
	ih := h - 2*tileBorderPx
	if iw <= 0 || ih <= 0 {
		return
	}
	fontPx, bannerH, shown := bannerGeom(h, ih)
	if !shown {
		// Tile too small to bother — outline alone has to carry the
		// signal.
		return
	}
	withClip(a.cctx, ix, iy, iw, ih, func() {
		a.cctx.Set("fillStyle", colorSourceLabelBg)
		a.cctx.Call("fillRect", ix, iy, iw, bannerH)
		setFont(a.cctx, fontPx, `ui-sans-serif, system-ui, -apple-system, sans-serif`, true)
		a.cctx.Set("fillStyle", textColor)
		a.cctx.Set("textBaseline", "middle")
		a.cctx.Set("textAlign", "start")
		a.cctx.Call("fillText", label, ix+4, iy+bannerH/2)
		a.cctx.Set("textBaseline", "top")
	})
}

// bannerTextColor picks a banner-text color that echoes the tile's own
// outline so the label and the border read as one. Shells are orange; a
// cross-plugin (exit) well is blue like every well (it's dashed, not
// recolored); read-only host content is brown; everything else follows its
// kind color.
func bannerTextColor(n *rpc.Tile, outside bool) string {
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

// fetchTileContent issues ReadContent for a plugin tile (file / proc @info)
// and caches the body by tile id. Idempotent: a successful previous fetch
// short-circuits, and an in-flight one is never doubled (contentFetch):
// concurrent fetches for one tile are how a stale reply lands after a fresher
// one and repaints old bytes into the overlay.
func (a *App) fetchTileContent(tileID string) {
	if tileID == "" {
		return
	}
	if _, ok := a.c.TileContent(tileID); ok {
		return
	}
	// A leaf link resolves its bytes through its target id, so a target in a
	// namespace this node does not declare is not asked for. Same rule as
	// fetchGrid: no round trip, no verdict, no notice.
	if a.deadNamespace(tileID) {
		return
	}
	ctx, done, ok := a.contentFetch.Begin(tileID)
	if !ok {
		return
	}
	go func() {
		defer done()
		// Coalesced repaint: body fetches land in bursts (#265).
		a.loadTileContent(ctx, tileID, a.scheduleFrame)
	}()
}

// loadTileContent reads one tile's bytes into the cache and refreshes the
// text overlay from them, then runs then() — the caller's repaint. It is the
// one content-fetch body: the lazy render-path fetch above and the URL boot's
// cursor-placing fetch (fetchBlobAndSetCursor) differ only in their guards
// and in what they do once the bytes have landed. Blocking, so both callers
// run it on their own goroutine.
//
// Content is routable by tile id (ReadContent); blob ids carry no plugin
// namespace and are not routable on their own. The cache is the one text-body
// store every overlay reads from.
func (a *App) loadTileContent(ctx context.Context, tileID string, then func()) {
	data, _, version, err := a.cl.ReadContent(ctx, tileID)
	if err != nil {
		// The tile body would otherwise never appear: say why.
		a.surfaceRPCError("ReadContent", err)
		return
	}
	a.c.PutFetchedContent(tileID, data, version)
	a.refreshFileOverlay()
	then()
}

// tileBody returns a text tile's body bytes, fetching lazily on a miss. Every
// tile is owned by some plugin, so the body always comes through ReadContent,
// which is routable by tile id; blob ids are not routable. Content is keyed by
// ContentID, so a leaf link resolves to its target and renders the one shared
// copy of the bytes.
func (a *App) tileBody(n *rpc.Tile) ([]byte, bool) {
	if b, ok := a.c.TileContent(n.ContentID()); ok {
		return b, true
	}
	a.fetchTileContent(n.ContentID())
	return nil, false
}

// drawChildPreview paints the cached child grid's tiles inside a clipped
// region. Each child cell renders at previewCell pixels. centerCellX/Y is
// the child cell coordinate that should land at (centerScreenX, centerScreenY).
//
// Child wells in the preview render flat (no recursive preview) — that is
// the "one level only" rule.
//
// hiddenTileID, if non-zero, suppresses the child tile with that row
// id — used so that a tile being dragged out of a well preview
// disappears from its original spot while the ghost flies. Matches
// by row id — the only identity a tile has — so cloned siblings stay
// visible during the drag.
func (a *App) drawChildPreview(child *cache.Grid,
	centerCellX, centerCellY, centerScreenX, centerScreenY, previewCell float64,
	clipX, clipY, clipW, clipH float64,
	hiddenTileID string,
) {
	c := a.cctx
	childInHost := child != nil && child.Meta.HostContent
	// Scale the inner-tile border so a child grid viewed from a distance
	// keeps its borders proportionate to the cells. Full-size live tiles
	// use 2px; previews glide down with the cell scale.
	borderPx := previewBorderPxFor(previewCell)
	for _, n := range child.Tiles {
		if hiddenTileID != "" && n.ID == hiddenTileID {
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
		// Every child is the flat kind-colored box: url and shell children
		// do not overlay their frozen JPEGs here, so a well's interior
		// reads uniformly — one visual grammar for looking one level
		// down.
		drawNode(c, &nn, nodeScreenX, nodeScreenY, nodeScreenW, nodeScreenH, false, tileOutside(&nn, childInHost), borderPx, false)
	}
}

// drawNode renders one tile into the canvas at the given screen rectangle.
// `selected` highlights the tile with a dedicated outline color. This is
// the "flat" renderer used for nested previews (no recursion) and for
// non-well tiles; the parent-grid renderer is drawNodeWithPreview.
func drawNode(c js.Value, n *rpc.Tile, x, y, w, h float64, selected bool, outside bool, borderPx float64, dashed bool) {
	// Fill and per-kind outline color in one pass; strokeTileBorder draws
	// the inset border for every kind that has one. borderPx lets the caller
	// scale the outline down for distant previews. dashed marks a link, a
	// tile whose content lives elsewhere, and every kind honors it, or that
	// kind lies about ownership.
	if dashed {
		setTileDash(c)
		defer clearTileDash(c)
	}
	// The kind picks a body color and an outline color; the drawing is the
	// same box for all of them. An unknown kind keeps the locked grey body
	// and gets no outline — the one arm with no line color.
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
		// The flat face a pane tile shows one level down inside another
		// preview — one level deep, flat beyond — and in the drag ghost.
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

// drawGhostTile renders the active drag ghost. When fragmentation is
// near zero this is just drawNodeWithPreview. When the cursor enters
// a black hole, fragmentation animates toward 1 and the ghost cross-
// fades into a trashcan glyph while the size lerp shrinks it toward
// the hole. Drag back out and frag returns to 0 — the trashcan fades
// out and the original tile fades back in at full size.
func (a *App) drawGhostTile(n *rpc.Tile, x, y, w, h, parentCellSize float64, r pane.Rect, frag float64) {
	// The ghost is a free-floating render of one tile; treat its own
	// kind+source_key as the outside signal. No parent grid is in play here
	// (the ghost is flying over the canvas).
	outside := tileOutside(n, false)
	// A dragged link shows dashed too, so you can see what you are carrying,
	// and a drop that will create a link — a ctrl + right-drag, or a
	// cross-namespace left-drag — previews dashed for the same reason:
	// dashed always means this is, or becomes, a reference. It is how the
	// user learns mid-drag which of the two right-button modes is armed.
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
	// Cross-fade: tile fades out as frag grows; trashcan fades in.
	if frag < 0.98 {
		a.cctx.Set("globalAlpha", 1.0-frag)
		a.drawNodeWithPreview(n, x, y, w, h, parentCellSize, false, outside, dashed, "")
		a.cctx.Set("globalAlpha", 1.0)
	}
	a.cctx.Set("globalAlpha", frag)
	drawTrashcanIcon(a.cctx, x, y, w, h)
	a.cctx.Set("globalAlpha", 1.0)
}

// paneBorderColorFor picks the pane border color from the pane's
// current state — what it's descended into (or root if nothing).
//   - descent into a text tile → green
//   - descent into a URL tile (frozen) → purple
//   - descent into a URL tile (live stream) → brighter purple
//   - descent into a well (path > 0, no file focus) → blue
//   - root (nothing descended) → tan
//
// The focused boolean picks the saturated vs faded variant of that
// hue. urlLive is true when a live native view is attached to this
// pane — a live Chromium tab. If the grid containing
// the descended tile isn't cached yet, we fall back to the generic
// blue so the user still sees "descended into something".
func (a *App) paneBorderColorFor(p *pane.Pane, g *cache.Grid, gridOK bool, focused bool, urlLive bool) string {
	return pane.BorderColor(a.borderInputFor(p, g, gridOK, focused, urlLive), paneBorderColors)
}

// borderInputFor resolves the facts pane.FamilyOf classifies on, shared by
// the pane border and the bottom bar theme, so the frame and the band cannot
// disagree about what the pane is showing.
func (a *App) borderInputFor(p *pane.Pane, g *cache.Grid, gridOK bool, focused bool, urlLive bool) pane.BorderInput {
	in := pane.BorderInput{
		HasTextFocus: p.ContentID() != "",
		DescentDepth: len(p.Path()),
		Focused:      focused,
		URLLive:      urlLive,
	}
	if p.ContentID() != "" {
		// descendedTile, not g.Tiles, so an ephemeral descent — focused off
		// the pane's grid, in the scratch grid — resolves too. Its border
		// goes gray, because ascent deletes it.
		if tile, ok := a.descendedTile(p); ok {
			in.TileKnown = true
			in.TileKind = tile.Kind
			in.Ephemeral = a.certainlyEphemeral(p, &tile)
		}
	}
	if gridOK && g != nil && g.Meta.HostContent {
		in.InHostGrid = true
	}
	return in
}

// paneBorderColors bundles the wasm renderer's color constants for the
// pure pane.BorderColor decision function.
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

// drawEdgeIndicators paints small markers along the pane's inner edge for
// every tile whose footprint lies entirely outside the visible viewport.
// Each marker is positioned where the ray from the viewport center to the
// tile's center crosses the pane's inset rectangle, and points outward.
func (a *App) drawEdgeIndicators(nodes map[string]rpc.Tile, ps dragdrop.Pane, r pane.Rect) {
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
		// Node screen rect.
		sx, sy := ps.CellToScreen(float64(n.X), float64(n.Y))
		w := float64(n.W) * cellSize
		h := float64(n.H) * cellSize
		// Visible iff the rect intersects the pane.
		if sx+w > r.X && sx < r.X+r.W && sy+h > r.Y && sy < r.Y+r.H {
			continue
		}
		// Node center in screen space.
		nx := sx + w/2
		ny := sy + h/2
		// Ray from pane center to node center; intersect inset rect.
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

// drawTriangle paints a small filled triangle centered at (cx, cy) pointing
// in the direction of `angle` (radians). `size` is the half-length from
// center to tip. Every arrowhead in the renderer is this one drawing: the
// off-screen edge indicators and the swap preview's two heads.
func drawTriangle(c js.Value, cx, cy, angle, size float64) {
	// Three points: tip, then two base points behind the center perpendicular
	// to the angle.
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

// drawGridNotice paints a centered, muted status line in a pane whose grid
// is not in the cache: pane.GridNotice words it (loading vs. unavailable);
// the plugin's label names the grid when its owner is in the launcher list
// (a mounted node's grid falls back to the id — the label is a wire fact this
// lookup cannot see, since a mounted id's first segment is the LOCAL node,
// which is why no fact about a grid may be derived from one; see
// client/scratch).
func (a *App) drawGridNotice(r pane.Rect, gid string) {
	if r.W < 80 || r.H < 40 {
		return
	}
	name := gid
	if pl, ok := a.pluginByUUID(uuidOf(gid)); ok && pl.Label != "" {
		name = pl.Label
	}
	label := pane.GridNotice(name, a.gridLoadFailed[gid])
	a.cctx.Call("save")
	a.cctx.Set("fillStyle", colorMuted)
	a.cctx.Set("font", "13px system-ui, sans-serif")
	a.cctx.Set("textAlign", "center")
	a.cctx.Set("textBaseline", "middle")
	a.cctx.Call("fillText", label, r.X+r.W/2, r.Y+r.H/2, r.W-16)
	a.cctx.Call("restore")
}
