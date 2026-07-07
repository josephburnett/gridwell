//go:build js && wasm

package main

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/zoomtrans"
	"github.com/josephburnett/gridwell/internal/rpc"
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
	// colorURLLiveLine is the pane border used when a URL tile is live
	// (WebSocket stream open). Same purple hue as colorURLLine but brighter
	// and more saturated so it's clearly distinct from the frozen state.
	colorURLLiveLine = "#a07acc"
	// colorShellBorder is the orange identity for shell tiles and the pane
	// border on a shell descent — bash runs outside Gridwell's data world, so
	// it gets its own warm hue, distinct from plugin brown.
	colorShellBorder      = "#d4863a"
	colorShellBorderFaded = "#6e4a22"
	// colorShellFill is the dark-orange body shown behind a shell tile's
	// preview / placeholder glyph.
	colorShellFill = "#2e220f"
	// colorEphemeralBorder is the GRAY pane border for a descent into an
	// ephemeral (scratch-grid) tile — url or shell alike. Gray overrides the
	// kind color: ascending DELETES the tile (a shell's tmux session and all
	// its processes included), and the border is the warning (issue #85).
	colorEphemeralBorder      = "#8b8e96"
	colorEphemeralBorderFaded = "#4b4d52"
	// colorExitBorder / colorExitFill are the error red used for a broken
	// embed reference (a link whose target no longer resolves) — a genuine
	// "this is wrong" signal, distinct from the plugin/shell identities.
	colorExitBorder = "#c87a5a"
	colorExitFill   = "#3a241a"
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
	// colorTrace is the ascent-trace ring — the fading "you just came from
	// HERE" outline (issue #83). Brighter yellow than the gold selection so
	// the two read differently while both are visible.
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
	// Inline-markdown body colors used by the rendered-markdown
	// drawing path (drawInlineLines, drawMarkdownRendered).
	colorMarkdownBody    = "#d8d9de"
	colorMarkdownCodeBg2 = "#3a4658"
	// Launcher tile tints for a non-enterable plugin (client/pluginhealth):
	// broken (Info failed/timed out) gets a red-tinted overlay — the same
	// alarm family as the error notice strip; rootless (healthy, no root
	// configured) gets a plain gray dimming — a configuration gap, not a
	// failure. Drawn as a translucent overlay atop the normal tile so the
	// plugin glyph / preview underneath still reads.
	colorLauncherBrokenTint   = "rgba(180, 40, 40, 0.38)"
	colorLauncherRootlessTint = "rgba(40, 40, 46, 0.55)"
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

// strokeTileBorder draws a borderPx-thick outline at `color` that sits
// entirely inside (x, y, w, h). Canvas centers the stroke on the path,
// so the rect is inset by half the line width on every side. Most
// callers pass the tileBorderPx constant; drawChildPreview scales the
// border down so it stays proportionate to the cell at a distance.
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
const plusButtonRadius = 18

// templateKind identifies one built-in tile primitive in the creation
// palette. Order matters: primitiveKinds determines layout in the popover
// and the indices used by hit-testing. File and process wells are no longer
// primitives — they are reached by dragging the fs / proc *plugin* items.
type templateKind int

const (
	tplWell templateKind = iota
	tplMarkdown
	tplURL
	// tplShell spawns an interactive bash shell tile. Starts frozen with
	// no preview; the user refreshes to spawn the PTY.
	tplShell
)

// primitiveKinds is the palette layout order of the built-in tile
// primitives, left to right. They appear in any writable grid.
var primitiveKinds = []templateKind{tplWell, tplMarkdown, tplURL, tplShell}

// paletteItem is one entry in the creation palette: a built-in tile
// primitive that the user drags onto the canvas to create a tile.
// Plugins are not in this palette; they are link tiles on the node grid
// (the landing page), created and linked like any other tile.
type paletteItem struct {
	primitive templateKind // the primitive this swatch creates
}

// paletteItems returns the palette entries for pane p: the tile
// primitives, but only when the pane's current grid is writable
// (Grid.Writable — the node grid and fs/proc grids get an empty palette).
func (a *App) paletteItems(p *pane.Pane) []paletteItem {
	if !a.gridWritable(a.gridIDForPane(p)) {
		return nil
	}
	items := make([]paletteItem, 0, len(primitiveKinds))
	for _, k := range primitiveKinds {
		items = append(items, paletteItem{primitive: k})
	}
	return items
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

	a.embedHits = a.embedHits[:0]

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
			if mr, ok := rects[mp.ID]; ok {
				a.drawPalette(mp, mr)
			}
		}
	}
	// The palette's HTML name input tracks the popover (or hides with it).
	a.syncPaletteNameField(rects)

	// In-flight right-button gesture preview (split line, swap arrow,
	// red close-warning border). Drawn on top of all panes but below
	// the textarea overlay (which lives in DOM, not canvas).
	if a.rightDrag != nil {
		a.drawRightDragPreview()
	}

	// Reposition the textarea overlay (if any) so it tracks the focused
	// pane through resizes and pane-tree edits.
	a.syncTextOverlayPosition()
	// Same for any live shell overlays — xterm host divs follow their
	// pane's screen rect each frame.
	a.syncShellOverlayPosition()
	// And any live URL tiles — native WebContentsViews follow their pane's
	// content box and park off-screen during canvas-overlay gestures.
	a.syncURLViews()
	// The rename pill tracks the focused pane's top-center (issue #61).
	a.syncRenamePill()

	// The notice strip lives in the band layoutPanes reserved below every
	// pane; drawn last so a notice is never painted over.
	a.drawErrStrip()
}

// layoutPanes walks the tree and assigns each leaf pane a screen rectangle.
// The notice strip is *reserved layout*: panes fill (height − strip), so a
// pending error owns pixels nothing — including a native WebContentsView,
// which tracks pane rects — can paint over. Input hit-testing shares this
// function, so render and input cannot disagree about where panes are.
func (a *App) layoutPanes() map[string]pane.Rect {
	return pane.Layout(a.tree, a.rootLayoutRect())
}

// rootLayoutRect is the rectangle the pane tree lays out into: the canvas
// minus the reserved notice strip. One owner — the cascading divider resize
// (pane.ResizeThrough) must see the exact rect the layout uses.
func (a *App) rootLayoutRect() pane.Rect {
	return pane.Rect{X: 0, Y: 0, W: a.width, H: a.height - errsurface.StripHeight(a.errs.Len())}
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
	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	const inset = paneBorderPx
	a.cctx.Call("rect", r.X+inset, r.Y+inset, r.W-2*inset, r.H-2*inset)
	a.cctx.Call("clip")

	pscreen := paneToDragdrop(p, r)

	// Grid lines render against background regardless of whether the grid
	// has loaded — they communicate the coordinate system to the user.
	// A focused file has no grid coordinates and (now) no zoom, so it
	// gets a plain background instead of a grid pattern: the old pattern
	// was the zoom cue, which no longer applies. The margin around the
	// inner box is just a plain ascent zone. (URL content fills the pane
	// and covers this anyway.) The node grid (the landing page) is a real
	// grid and renders like one.
	if p.TextFocus != "" {
		a.cctx.Set("fillStyle", colorBg)
		a.cctx.Call("fillRect", r.X, r.Y, r.W, r.H)
	} else {
		a.drawGridLines(paneGridLineColor(p, g, gridOK), pscreen, r)
	}

	if gridOK {
		cellSize := pscreen.CellPx * pscreen.Zoom
		selected := a.selectedFor(p.ID)
		// In live file mode the pane is "inside" the file: skip the
		// parent-grid node walk and render the focused file in the
		// inner box (inset by the file margin so the surrounding grid
		// pattern is visible). Inner-box bounds match the textarea
		// exactly so the user's "outside textarea = grid rules"
		// mental model is consistent.
		if p.TextFocus != "" {
			// descendedTile (not g.Tiles[...]) so an ephemeral url visit — focused
			// off the pane's grid, in the scratch grid — still renders.
			if file, ok := a.descendedTile(p); ok {
				switch file.Kind {
				case rpc.KindText:
					ix, iy, iw, ih := textInnerBox(p, r)
					a.cctx.Set("fillStyle", colorFileInnerBg)
					a.cctx.Call("fillRect", ix, iy, iw, ih)
					a.drawMarkdownInPane(p, &file, ix, iy, iw, ih)
				case rpc.KindURL:
					ix, iy, iw, ih := paneContentBox(r)
					a.drawURLTileInPane(p, &file, ix, iy, iw, ih)
				case rpc.KindShell:
					ix, iy, iw, ih := paneContentBox(r)
					a.drawShellTileInPane(p, &file, ix, iy, iw, ih)
				default:
					ix, iy, iw, ih := textInnerBox(p, r)
					a.cctx.Set("fillStyle", colorFileInnerBg)
					a.cctx.Call("fillRect", ix, iy, iw, ih)
				}
			}
		} else {
			inSource := g != nil && (g.Meta.SourceKind == rpc.GridSourceFS || g.Meta.SourceKind == rpc.GridSourceProc)
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
				outside := tileOutside(&nn, inSource)
				dashed := !inSource && isLinkTile(&nn)
				a.drawNodeWithPreview(&nn, left, top, w, h, cellSize, r, n.ID == selected, outside, dashed, p.ID)
				a.drawPluginHealthTint(&nn, left, top, w, h)
			}
			// Ascent trace: the fading "you just came from HERE" outline on the
			// tile this pane most recently ascended out of (issue #83). Drawn
			// after the tiles so it paints on top; alpha decays via the frame
			// loop (pruneTraces keeps it ticking, then drops the entry).
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
	} else {
		// Status line in the upper-left so the user knows what state
		// we're in and which grid id we're trying to load.
		msg := fmt.Sprintf("loading grid %s…", gid)
		if a.gridLoadFailed[gid] {
			msg = fmt.Sprintf("grid %s unavailable", gid)
		}
		a.cctx.Set("fillStyle", colorMuted)
		a.cctx.Set("font", "12px ui-monospace")
		a.cctx.Call("fillText", msg, r.X+12, r.Y+24)
	}

	a.cctx.Call("restore")

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
	a.cctx.Set("strokeStyle", border)
	a.cctx.Set("lineWidth", paneBorderPx)
	half := paneBorderPx / 2
	a.cctx.Call("strokeRect", r.X+half, r.Y+half, r.W-paneBorderPx, r.H-paneBorderPx)

	// Per-pane controls belong to the active pane only: the focused pane
	// shows its corner button (URL back/refresh, shell refresh) or, on a
	// grid, the + creation menu; an unfocused pane is chrome-free so you
	// see only the content you placed there. This matches the rendered/raw
	// toggle, which is already a focused-only DOM overlay
	// (refreshFileToggle). The matching click hit-tests in onMouseDown are
	// gated on the pre-click focus, so clicking an unfocused pane's (hidden)
	// corner just focuses it rather than silently firing the button.
	if p.TextFocus != "" {
		if focused && a.isURLDescent(p) {
			if a.urlViewFor(p.ID) != nil {
				a.drawURLBackButton(p, r)
			} else if a.caps.LiveURL {
				a.drawURLRefreshButton(p, r)
			} else {
				a.drawURLNoLiveButton(p, r)
			}
		} else if focused && a.isShellDescent(p) && !a.hasShellStream(p.ID) {
			// Frozen shell descent: lower-right refresh button either
			// creates a fresh tmux session (no snapshot yet) or
			// attaches to the existing one. Hidden when the tile has
			// a snapshot but its tmux session is gone — the JPEG is
			// all that remains. shellRefreshButtonVisible decides and
			// kicks off the ShellSessionAlive probe if the answer
			// isn't cached yet.
			if file, ok := g.Tiles[p.TextFocus]; ok && a.shellRefreshButtonVisible(&file) {
				a.drawURLRefreshButton(p, r)
			}
		}
	} else if !a.isNodeGridPane(p) && (focused || (a.tileDragInFlight() && a.dragging.originPaneID == p.ID)) {
		// + button: the focused grid's entry point for creating tiles (and a
		// visible handle even when the grid is unreachable — you can still
		// ascend). It also appears on a tile-drag's SOURCE pane even when that
		// pane isn't focused, because during a drag it becomes the trashcan
		// delete target ("drag a tile back to the menu it came from").
		// drawPlusButton paints a trashcan instead of a + in that state. The
		// palette popover itself is drawn after every pane (see draw) so it
		// floats above neighbouring panes it overflows into. The node grid
		// (the landing page) has no + button — it is read-only, and hiding
		// the button also removes the drag-delete target so a plugin tile
		// can never be dropped on a trashcan.
		a.drawPlusButton(p, r)
	}
}

// drawCircleButtonChrome paints the filled, bordered circle shared by the
// lower-right corner buttons (URL back / refresh), so the position and look
// stay muscle-memory-compatible with the + button. The caller then draws its
// glyph on top at (cx, cy).
func (a *App) drawCircleButtonChrome(cx, cy float64) {
	a.cctx.Set("fillStyle", colorPlusBg)
	a.cctx.Call("beginPath")
	a.cctx.Call("arc", cx, cy, float64(plusButtonRadius), 0, 2*math.Pi)
	a.cctx.Call("fill")
	a.cctx.Set("strokeStyle", colorPaneBorder)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("stroke")
}

// drawURLBackButton paints the lower-right button on a URL-tile descent.
// Click → history.back() on the descended Chromium tab. Same circular
// chrome as the + button so the position is muscle-memory-compatible.
func (a *App) drawURLBackButton(p *pane.Pane, r pane.Rect) {
	cx, cy := plusButtonCenter(p, r)
	a.drawCircleButtonChrome(cx, cy)

	// Left-pointing arrow: a horizontal stem with a chevron at its left end.
	a.cctx.Set("strokeStyle", colorPlusFg)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Set("lineCap", "round")
	a.cctx.Set("lineJoin", "round")
	a.cctx.Call("beginPath")
	a.cctx.Call("moveTo", cx+6, cy)
	a.cctx.Call("lineTo", cx-6, cy)
	a.cctx.Call("moveTo", cx-2, cy-5)
	a.cctx.Call("lineTo", cx-6, cy)
	a.cctx.Call("lineTo", cx-2, cy+5)
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Set("lineCap", "butt")
	a.cctx.Set("lineJoin", "miter")
}

// drawURLRefreshButton paints the lower-right button on a frozen URL-tile
// descent. Click → open URL stream (same action as the right-drag-down
// refresh gesture). Same circular chrome as drawURLBackButton so the
// position is muscle-memory-compatible.
func (a *App) drawURLRefreshButton(p *pane.Pane, r pane.Rect) {
	cx, cy := plusButtonCenter(p, r)
	a.drawCircleButtonChrome(cx, cy)

	// Refresh glyph: reuse drawRefreshIcon at a size that fits the button circle.
	drawRefreshIcon(a.cctx, cx, cy, 7.0, colorPlusFg)
}

// drawURLNoLiveButton paints the corner button on a frozen URL-tile descent
// when this host cannot go live (caps.LiveURL false — a plain browser, no
// Electron bridge): the refresh glyph, dimmed, with a slash through it.
// Click → an Info notice explaining why (caps.GoLiveNotice), never a silent
// dead tap.
func (a *App) drawURLNoLiveButton(p *pane.Pane, r pane.Rect) {
	cx, cy := plusButtonCenter(p, r)
	a.drawCircleButtonChrome(cx, cy)

	drawRefreshIcon(a.cctx, cx, cy, 7.0, colorNoLiveFg)
	// The slash: corner-to-corner over the glyph, same dim color, so the
	// shape reads as "refresh — crossed out" at a glance.
	a.cctx.Set("strokeStyle", colorNoLiveFg)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Set("lineCap", "round")
	a.cctx.Call("beginPath")
	a.cctx.Call("moveTo", cx-8, cy+8)
	a.cctx.Call("lineTo", cx+8, cy-8)
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Set("lineCap", "butt")
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
// child-preview hide scopes to the drag's SOURCE pane only ("" for contexts
// with no pane — embeds in previews, ghosts). Formerly the previewPaneID
// App scratch field, now passed down (issue #25).
func (a *App) drawNodeWithPreview(n *rpc.Tile, x, y, w, h, parentCellSize float64, r pane.Rect, selected, outside, dashed bool, paintPaneID string) {
	switch n.Kind {
	case rpc.KindText:
		a.drawMarkdownNode(n, x, y, w, h, r, selected, outside, dashed)
		a.drawTileBannerLabel(n, x, y, w, h, outside)
		return
	case rpc.KindURL:
		a.drawURLTile(n, x, y, w, h, selected)
		a.drawTileBannerLabel(n, x, y, w, h, outside)
		return
	case rpc.KindShell:
		a.drawShellTile(n, x, y, w, h, selected)
		a.drawTileBannerLabel(n, x, y, w, h, outside)
		return
	}
	if n.Kind != rpc.KindWell {
		drawNode(a.cctx, n, x, y, w, h, selected, outside, tileBorderPx)
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

	// Preview cell size: previewCell = parentCell × ratio, where ratio
	// is the well's intrinsic ViewZoom (or DefaultWellViewZoom for an
	// unvisited well, which collapses this to the legacy PreviewFactor
	// calibration). At parent = Overtake_now the previewCell matches
	// the just-after-swap live cell, making the path swap continuous.
	ratio := zoomtrans.EffectiveViewZoom(n.ViewZoom, zoomtrans.DefaultWellViewZoom)
	previewCell := parentCellSize * ratio
	showPreview := haveChild && previewCell >= 0.5

	if isExitWell(n) && !showPreview {
		// A cross-plugin well with no preview loaded yet shows the plugin's
		// identity glyph — the same drawing as its menu swatch and drag
		// ghost, so it reads identically before, during, and after the drop.
		a.drawPluginGlyph(a.pluginKind(n.ChildGridID), x, y, w, h)
	} else {
		a.cctx.Call("save")
		a.cctx.Call("beginPath")
		a.cctx.Call("rect", x, y, w, h)
		a.cctx.Call("clip")

		// Child grid lines inside the well, aligned so child cell (0, 0)
		// lands at the well's center offset by the well's view region. This
		// is exactly where the just-after-descent child viewport would put
		// it, so the lines glide continuously across the path swap.
		viewCenterX := float64(n.ViewX) + float64(n.W)/2
		viewCenterY := float64(n.ViewY) + float64(n.H)/2
		wellCenterX := x + w/2
		wellCenterY := y + h/2
		originX := wellCenterX - viewCenterX*previewCell
		originY := wellCenterY - viewCenterY*previewCell
		drawGridLinesIn(a.cctx, wellGridLineColor(n), x, y, w, h, previewCell, originX, originY)

		if showPreview {
			// The hide scopes to the pane being painted: paneR is the pane
			// rect drawNodeWithPreview received from drawPane's tile loop.
			var hide string
			if a.ghost != nil && a.ghost.hiddenPaneID == paintPaneID {
				hide = a.ghost.hiddenTileID
			}
			a.drawChildPreview(child, viewCenterX, viewCenterY,
				wellCenterX, wellCenterY, previewCell, x, y, w, h, hide)
		}
		a.cctx.Call("restore")
	}

	// Outline: blue for interior wells, red for exit-wells (whose child grid
	// lives in another plugin). The child-grid uuid drives the color so the
	// grammar is consistent whether the user sees the tile as a preview or
	// descends into it (where the same distinction drives the pane border).
	// Dashed when this well is a link in a regular grid (see isLinkTile).
	if dashed {
		setTileDash(a.cctx)
	}
	strokeTileBorder(a.cctx, x, y, w, h, wellOutlineColor(n), tileBorderPx)
	if dashed {
		clearTileDash(a.cctx)
	}
	if selected {
		drawSelectedTileOutline(a.cctx, x, y, w, h)
	}
	// Banner: "files" / "processes" for root exit-wells, basename for
	// non-root sub-wells. Regular Gridwell wells (KindWell) get no banner
	// — tileBannerLabel returns "" for them.
	a.drawTileBannerLabel(n, x, y, w, h, outside)
}

// wellOutlineColor picks the well-tile outline color: blue for every well,
// interior or cross-plugin. A cross-plugin (exit) well is distinguished not by
// hue but by a DASHED border (see isLinkTile / drawNodeWithPreview) — same
// blue, dashed, signalling "a link you can unlink".
func wellOutlineColor(*rpc.Tile) string {
	return colorFocusBorder
}

// wellGridLineColor / paneGridLineColor pick the grid-line color drawn inside
// a well's preview region and a pane's leaf grid. Every grid the user
// navigates is uniformly blue, whichever plugin owns it (the launcher home,
// which is gridless, never draws grid lines).
func wellGridLineColor(*rpc.Tile) string {
	return colorGridLineInterior
}

func paneGridLineColor(*pane.Pane, *cache.Grid, bool) string {
	return colorGridLineInterior
}

// tileReadOnly reports whether the user is allowed to edit n's text
// content. A text tile owned by a plugin (a file's metadata, a process's
// @info) is a read-only view of host state — the plugin has no write-back —
// so the rendered/raw toggle, the textarea overlay, and the UpdateText round-
// trip on ascent all consult this to keep the user from typing into one and
// silently re-posting a stale buffer.
func (a *App) tileReadOnly(n *rpc.Tile) bool {
	return n.Kind == rpc.KindText && !a.gridWritable(n.GridID)
}

// tileOutside reports whether a tile should be rendered with the "outside
// Gridwell" treatment (red outline / banner). True when:
//   - the tile's parent grid is a source-backed grid (fs/proc), so every
//     row in it represents host state, not Gridwell-owned data
//   - the tile is itself an exit well (its child grid lives in another
//     plugin) anywhere — outside regardless of where the well sits
//   - the tile is a shell tile (bash runs outside Gridwell's data world)
func tileOutside(n *rpc.Tile, parentInSource bool) bool {
	if parentInSource {
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

// isLinkTile reports whether n is a LINK, not owned content: a reference whose
// child grid is in another plugin's id space (a host directory, the process
// table, a mounted plugin — including the localdb mounted into its own grid).
// These render with a dashed border, and dropping one on /dev/null only unlinks
// it (drops the tile row); an owned interior well deletes for real.
//
// Reference is the authoritative signal the server stamps (qualifyTiles) from
// the child_grid_id shape — the same fact the store's delete/clone key on, so
// render can't disagree with them. isExitWell still covers the synthetic
// empty-GridID launcher tile (built client-side before any server round-trip),
// which also sets Reference, so either alone would do; both keeps it robust.
func isLinkTile(n *rpc.Tile) bool {
	return n.Reference || isExitWell(n)
}

// tileBorderDash is the dash pattern for link-tile borders: short on/off so
// a 1–2px outline still reads clearly as "dashed = a link, safe to unlink."
func setTileDash(c js.Value)   { c.Call("setLineDash", jsArray(5, 3)) }
func clearTileDash(c js.Value) { c.Call("setLineDash", jsArray()) }

// tileBannerLabel returns the short label drawn at the top of a tile,
// or "" to suppress the banner. AltText is the single source of truth —
// the server stamps it at insert time from a per-kind derivation
// (basename for files, kernel Name for processes, "files"/"processes"
// for the roots, "info" for the synthetic info tile, first non-empty
// line for text content). The client has no opinion of its own here.
func tileBannerLabel(n *rpc.Tile) string {
	return n.AltText
}

// drawTileBannerLabel paints tileBannerLabel(n) inside a small translucent
// banner at the top of the tile. Clipped to the tile rect so an over-long
// label can't bleed past the cell. When outside is true, the text uses
// the red exit color; otherwise the tile-kind color (green for text, blue
// for wells) so the banner echoes the tile's own color grammar.
func (a *App) drawTileBannerLabel(n *rpc.Tile, x, y, w, h float64, outside bool) {
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
	// Scale font with cell size so the label stays legible across zooms
	// without overwhelming small tiles.
	const minFontPx = 9.0
	const maxFontPx = 16.0
	fontPx := h * 0.14
	if fontPx < minFontPx {
		fontPx = minFontPx
	}
	if fontPx > maxFontPx {
		fontPx = maxFontPx
	}
	if fontPx*1.4 > ih {
		// Tile too small to bother — outline alone has to carry the
		// signal.
		return
	}
	bannerH := fontPx + 4
	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", ix, iy, iw, ih)
	a.cctx.Call("clip")
	a.cctx.Set("fillStyle", colorSourceLabelBg)
	a.cctx.Call("fillRect", ix, iy, iw, bannerH)
	setFont(a.cctx, fontPx, `ui-sans-serif, system-ui, -apple-system, sans-serif`, true, false)
	a.cctx.Set("fillStyle", bannerTextColor(n, outside))
	a.cctx.Set("textBaseline", "middle")
	a.cctx.Set("textAlign", "start")
	a.cctx.Call("fillText", label, ix+4, iy+bannerH/2)
	a.cctx.Set("textBaseline", "top")
	a.cctx.Call("restore")
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

// paneFocusedOnFile was removed in fix #35: it returned any pane descended into
// a given file tile, so drawMarkdownNode could preview at the live pane's width.
// That cross-pane reach-through was the root cause of two bugs: wrong-size
// preview (A, layout width used sibling pane's inner width) and blank preview
// (B, hideForTextarea suppressed canvas in every pane showing the tile). The
// fix always passes focused=false to PreviewScaleScroll, using stored framing.

// fetchTileContent issues GetTileContent for a plugin tile (file / proc @info)
// and caches the body by tile id. Idempotent: a successful previous fetch
// short-circuits.
func (a *App) fetchTileContent(tileID string) {
	if tileID == "" {
		return
	}
	if _, ok := a.c.TileContent(tileID); ok {
		return
	}
	go func() {
		data, err := a.cl.GetTileContent(context.Background(), tileID)
		if err != nil {
			// The tile body will simply never appear — say why (charter §6).
			a.surfaceRPCError("GetTileContent", err)
			return
		}
		a.c.PutTileContent(tileID, data)
		a.refreshFileOverlay()
		a.draw()
	}()
}

// tileBody returns a text tile's body bytes, fetching lazily on a miss. In the
// rootless model every tile is owned by some plugin, so the body always comes
// via GetTileContent (routable by tile id) — blob ids aren't routable.
func (a *App) tileBody(n *rpc.Tile) ([]byte, bool) {
	if b, ok := a.c.TileContent(n.ID); ok {
		return b, true
	}
	a.fetchTileContent(n.ID)
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
// by row id (not ObjectID) so cloned siblings stay visible during
// the drag.
func (a *App) drawChildPreview(child *cache.Grid,
	centerCellX, centerCellY, centerScreenX, centerScreenY, previewCell float64,
	clipX, clipY, clipW, clipH float64,
	hiddenTileID string,
) {
	c := a.cctx
	childInSource := child != nil && (child.Meta.SourceKind == rpc.GridSourceFS || child.Meta.SourceKind == rpc.GridSourceProc)
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
		drawNode(c, &nn, nodeScreenX, nodeScreenY, nodeScreenW, nodeScreenH, false, tileOutside(&nn, childInSource), borderPx)
		// A url/shell tile shown inside a well preview must carry the same
		// cached frame the parent-grid renderer paints — otherwise a tile that
		// is live in another pane stays a flat colored box here while its live
		// twin updates (breaks "the preview is what you were looking at",
		// mirror edition). drawNode is the flat fill; overlay the JPEG on top.
		a.overlayChildPreview(&nn, nodeScreenX, nodeScreenY, nodeScreenW, nodeScreenH, borderPx)
	}
}

// overlayChildPreview paints the cached preview JPEG for a url/shell tile over
// the flat fill drawNode already laid down, clipped to the tile rect, then
// re-strokes the border so the image doesn't bleed across it. Non-url/shell
// tiles (and tiles with no cached frame yet) are left as drawNode drew them; a
// missing-but-expected preview kicks off the same fetch the parent-grid
// renderer uses, so the cache fills in and the next frame shows it.
func (a *App) overlayChildPreview(n *rpc.Tile, x, y, w, h, borderPx float64) {
	if n.Kind != rpc.KindURL && n.Kind != rpc.KindShell {
		return
	}
	cached, ok := a.urlPreview.Get(n.ID, n.PreviewBlobID)
	if !ok {
		if n.PreviewBlobID != 0 {
			a.fetchURLPreview(n.ID, n.PreviewBlobID)
		}
		return
	}
	img, ok := previewImage(cached)
	if !ok {
		return
	}
	c := a.cctx
	c.Call("save")
	c.Call("beginPath")
	c.Call("rect", x, y, w, h)
	c.Call("clip")
	drawImageContain(c, img, x, y, w, h)
	c.Call("restore")
	line := colorURLLine
	if n.Kind == rpc.KindShell {
		line = colorShellBorder
	}
	strokeTileBorder(c, x, y, w, h, line, borderPx)
}

// drawNode renders one tile into the canvas at the given screen rectangle.
// `selected` highlights the tile with a dedicated outline color. This is
// the "flat" renderer used for nested previews (no recursion) and for
// non-well tiles; the parent-grid renderer is drawNodeWithPreview.
func drawNode(c js.Value, n *rpc.Tile, x, y, w, h float64, selected bool, outside bool, borderPx float64) {
	// Fill + per-kind outline color in one pass; strokeTileBorder draws
	// the inset border for all kinds that have one. borderPx lets the
	// caller scale the outline down for distant previews.
	switch n.Kind {
	case rpc.KindWell:
		c.Set("fillStyle", colorBg)
		c.Call("fillRect", x, y, w, h)
		strokeTileBorder(c, x, y, w, h, wellOutlineColor(n), borderPx)
	case rpc.KindURL:
		c.Set("fillStyle", colorURLFill)
		c.Call("fillRect", x, y, w, h)
		strokeTileBorder(c, x, y, w, h, colorURLLine, borderPx)
	case rpc.KindShell:
		c.Set("fillStyle", colorShellFill)
		c.Call("fillRect", x, y, w, h)
		strokeTileBorder(c, x, y, w, h, colorShellBorder, borderPx)
	case rpc.KindText:
		fill := colorMarkdownFill
		line := colorMarkdownLine
		if outside {
			fill = colorPluginFill
			line = colorPluginBorder
		}
		c.Set("fillStyle", fill)
		c.Call("fillRect", x, y, w, h)
		strokeTileBorder(c, x, y, w, h, line, borderPx)
	default:
		c.Set("fillStyle", colorLocked)
		c.Call("fillRect", x, y, w, h)
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
	// A dragged link (file-well / process-well / file) shows dashed too, so
	// you can see what you're carrying. The ghost is always over a regular
	// surface (no source grid in play), so dashed == isLinkTile.
	dashed := isLinkTile(n)
	if frag < 0.02 {
		a.drawNodeWithPreview(n, x, y, w, h, parentCellSize, r, false, outside, dashed, "")
		if a.ghost != nil {
			if a.ghost.forbidden {
				drawGhostNoEntryBadge(a.cctx, x+w/2, y+h/2, min(w, h))
			} else if a.ghost.overDoc {
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
		a.drawNodeWithPreview(n, x, y, w, h, parentCellSize, r, false, outside, dashed, "")
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
// hue. urlLive is true when an active WebSocket stream is open for
// this pane, indicating a live Chromium tab. If the grid containing
// the descended tile isn't cached yet, we fall back to the generic
// blue so the user still sees "descended into something".
func (a *App) paneBorderColorFor(p *pane.Pane, g *cache.Grid, gridOK bool, focused bool, urlLive bool) string {
	in := pane.BorderInput{
		HasTextFocus: p.TextFocus != "",
		DescentDepth: len(p.Path),
		Focused:      focused,
		URLLive:      urlLive,
		IsLauncher:   a.isNodeGridPane(p),
	}
	if p.TextFocus != "" {
		// descendedTile (not g.Tiles) so an EPHEMERAL descent — focused off
		// the pane's grid, in the scratch grid — resolves too; its border
		// goes gray because ascent deletes it (issue #85).
		if tile, ok := a.descendedTile(p); ok {
			in.TileKnown = true
			in.TileKind = tile.Kind
			in.Ephemeral = a.isEphemeralTile(p, &tile)
		}
	}
	if gridOK && g != nil && (g.Meta.SourceKind == rpc.GridSourceFS || g.Meta.SourceKind == rpc.GridSourceProc) {
		in.InSourceGrid = true
	}
	return pane.BorderColor(in, paneBorderColors)
}

// paneBorderColors bundles the wasm renderer's color constants for the
// pure pane.BorderColor decision function.
var paneBorderColors = pane.BorderColors{
	Focused:        colorFocusBorder,
	FocusedFaded:   colorFocusBorderFaded,
	Root:           colorPluginBorder,
	Text:           colorMarkdownLine,
	TextFaded:      colorMarkdownLineFaded,
	URL:            colorURLLine,
	URLFaded:       colorURLLineFaded,
	URLLive:        colorURLLiveLine,
	Shell:          colorShellBorder,
	ShellFaded:     colorShellBorderFaded,
	Exit:           colorPluginBorder,
	ExitFaded:      colorPluginBorderFaded,
	Ephemeral:      colorEphemeralBorder,
	EphemeralFaded: colorEphemeralBorderFaded,
}

// The palette/identity glyphs all share one visual spec so the creation
// menu reads as a consistent set rather than seven unrelated drawings:
// the same stroke weight (glyphLineWidth), the same centered footprint
// (glyphBox, ~52% of the smaller side), round line ends, and a color that
// matches the tile's own kind border. beginGlyph/endGlyph bracket each one.

// glyphLineWidth is the shared stroke weight, tied to tile size so icons
// scale with their swatch but always read at the same relative weight.
// Deliberately thin — fat strokes read as cartoonish.
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
		a.drawTriangle(mx, my, ang, 6)
	}
}

// drawTriangle paints a small filled triangle centered at (cx, cy) pointing
// in the direction of `angle` (radians). `size` is the half-length from
// center to tip.
func (a *App) drawTriangle(cx, cy, angle, size float64) {
	// Three points: tip, then two base points behind the center perpendicular
	// to the angle.
	tipX := cx + math.Cos(angle)*size
	tipY := cy + math.Sin(angle)*size
	leftX := cx + math.Cos(angle+2.5)*size
	leftY := cy + math.Sin(angle+2.5)*size
	rightX := cx + math.Cos(angle-2.5)*size
	rightY := cy + math.Sin(angle-2.5)*size
	a.cctx.Call("beginPath")
	a.cctx.Call("moveTo", tipX, tipY)
	a.cctx.Call("lineTo", leftX, leftY)
	a.cctx.Call("lineTo", rightX, rightY)
	a.cctx.Call("closePath")
	a.cctx.Call("fill")
}

// paletteLayoutFor builds the pure-go palette.Layout snapshot for a
// given pane. The palette package owns the geometry; wasm only has to
// pour the inputs in.
