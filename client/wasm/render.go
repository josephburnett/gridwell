//go:build js && wasm

package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/dragdrop"
	embedpkg "github.com/josephburnett/gridwell/client/embed"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/palette"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/zoomtrans"
	"github.com/josephburnett/gridwell/internal/rpc"
)

const (
	colorBg          = "#0c0d11"
	colorFileInnerBg = "#1c1f26"
	colorPaneBorder  = "#1f2229"
	colorFocusBorder = "#4a6fff"
	// colorFocusBorderFaded is the pane outline for descended-but-not-
	// focused panes. Same hue as the focus blue, lower saturation and
	// value so the focused pane still pops, but the others stay
	// visible as "you're also looking inside something here".
	colorFocusBorderFaded = "#2c3d70"
	// colorRootBorder marks a pane that's at the user's root grid (no
	// descent path, no file focus). Warm tan — earth-tone "ground",
	// distinct from the descent blue and the file greens / purples so
	// it never gets read as "you're descended into something".
	colorRootBorder = "#7a6a4a"
	// Grid-line colors per grid kind. Each echoes the matching border
	// color (root tan, focus blue, exit red) but at the same low
	// brightness as the legacy neutral grid line, so the lines tint
	// the canvas with the grid's identity without overwhelming the
	// content sitting on top.
	colorGridLineRoot     = "#2a2419"
	colorGridLineInterior = "#1c2540"
	colorGridLineExit     = "#3a2418"
	// Content-tile colors. Each tile kind has its own identity; the
	// user reads tiles by color at a glance and the icon / preview
	// reveals the rest.
	//   - text      → olive green
	//   - url       → purple
	//   - blackhole → red
	colorMarkdownFill      = "#2c3a1a"
	colorMarkdownLine      = "#8aa05a"
	colorMarkdownLineFaded = "#4a5a3a"
	colorURLFill           = "#2b1a3a"
	colorURLLine           = "#7a5a9a"
	colorURLLineFaded      = "#4a3a5a"
	// colorURLLiveLine is the pane border used when a URL tile is live
	// (WebSocket stream open). Same purple hue as colorURLLine but brighter
	// and more saturated so it's clearly distinct from the frozen state.
	colorURLLiveLine   = "#a07acc"
	colorBlackHoleFill = "#3a1c1a"
	colorBlackHoleLine = "#a06a5a"
	// colorBlackHoleSwatchBg fills the palette / live blackhole swatch.
	// Pure black so concentric grey rings read as event horizons.
	colorBlackHoleSwatchBg = "#000000"
	// colorExitBorder is the outline used by file-well / process-well
	// tiles and the pane border when descended into a source-backed
	// grid. Same red family as blackhole but brighter so it reads as a
	// live container (vs blackhole's deletion sink).
	colorExitBorder      = "#c87a5a"
	colorExitBorderFaded = "#6a4032"
	// colorExitFill is the body color for a text-kind tile that lives in
	// or comes from a source-backed grid (the @info tile inside a
	// process-well, file metadata tiles in fs-grids, source-key-bearing
	// text clones). Same dark red family as colorExitBorder so the tile
	// reads as "read-only host view" at every zoom — green fill would
	// invite editing the body the user cannot edit.
	colorExitFill = "#3a241a"
	// colorSourceLabelBg is the translucent strip behind the name label
	// shown at the top of a tile inside an fs/proc grid. Dark enough that
	// the red foreground reads clearly over any underlying preview.
	colorSourceLabelBg = "rgba(20, 12, 8, 0.78)"
	// colorNoEntry{Fill,Stroke} drive the international "no entry" badge
	// drawn on the ghost when a drop would be rejected. International
	// convention: red disc, white ring, white diagonal slash.
	colorNoEntryFill   = "#c93030"
	colorNoEntryStroke = "#f6f6f6"
	colorLocked        = "#26262a"
	colorSelected      = "#e3b16f"
	colorEdgeDot       = "#5a6a8a"
	colorPlusBg        = "#23252d"
	colorPlusBgHi      = "#2d3140"
	colorPlusFg        = "#c8c9ce"
	colorMenuBg        = "#16181f"
	colorMenuItemHi    = "#e8e9ee"
	colorMuted         = "#6c6f78"
	// Inline-markdown body colors used by the rendered-markdown
	// drawing path (drawInlineLines, drawMarkdownRendered).
	colorMarkdownBody    = "#d8d9de"
	colorMarkdownCodeBg2 = "#3a4658"
)

const (
	// paneBorderPx is the visible thickness of the pane outline. Wide
	// enough to be a target for right-drag (resize / close) without
	// dominating the pane's interior. The right-button input layer
	// uses a slightly larger hit-band than this so users don't have to
	// pixel-hunt the divider.
	paneBorderPx = 6.0
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

// plusButtonRadius mirrors palette.Default().PlusRadius so the wasm
// canvas drawing code (which arcs and fills the + button) can keep
// using a typed numeric literal rather than reaching into the
// palette.Config every time.
const plusButtonRadius = 18

// templateKind identifies one entry in the creation palette. Order
// matters: it determines layout in the popover and the indices used
// by hit-testing.
type templateKind int

const (
	tplWell templateKind = iota
	tplMarkdown
	tplURL
	// tplBlackHole spawns a "trashcan" tile. Dropping another tile on
	// top of a black hole deletes the dropped tile — the only delete
	// affordance in the UI.
	tplBlackHole
	// tplFileWell spawns a file-well rooted at "/" (the host
	// filesystem root). Outlined red — its contents come from outside
	// Gridwell.
	tplFileWell
	// tplProcessWell spawns a process-well rooted at PID 1 (init).
	// Also red — host-owned state.
	tplProcessWell
)

// templateKinds is the palette layout order, left to right. The two
// exit-wells go after the interior kinds so the in-Gridwell tiles stay
// grouped on the left.
var templateKinds = []templateKind{tplWell, tplMarkdown, tplURL, tplBlackHole, tplFileWell, tplProcessWell}

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

	// In-flight right-button gesture preview (split line, swap arrow,
	// red close-warning border). Drawn on top of all panes but below
	// the textarea overlay (which lives in DOM, not canvas).
	if a.rightDrag != nil {
		a.drawRightDragPreview()
	}

	// Reposition the textarea overlay (if any) so it tracks the focused
	// pane through resizes and pane-tree edits.
	a.syncFileOverlayPosition()
}

// layoutPanes walks the tree and assigns each leaf pane a screen rectangle.
func (a *App) layoutPanes() map[string]pane.Rect {
	return pane.Layout(a.tree, pane.Rect{X: 0, Y: 0, W: a.width, H: a.height})
}

// drawPane draws one pane's contents.
//
// The pane chrome (border, grid lines, + button) is always drawn, even when
// the target grid hasn't loaded yet. That way the user can see the pane is
// live and use the + button (or Home key, etc.) to recover from a stale
// descent path.
func (a *App) drawPane(p *pane.Pane, r pane.Rect) {
	a.previewPaneID = p.ID
	a.previewPaneRect = r
	defer func() {
		a.previewPaneID = ""
		a.previewPaneRect = pane.Rect{}
	}()
	gid := a.gridIDForPath(p.Path)
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
	// and covers this anyway.)
	if p.TextFocus != 0 {
		a.cctx.Set("fillStyle", colorBg)
		a.cctx.Call("fillRect", r.X, r.Y, r.W, r.H)
	} else {
		a.drawGridLines(paneGridLineColor(p, g, gridOK), pscreen, r)
	}

	if gridOK {
		cellSize := pscreen.CellPx * pscreen.Zoom
		selected := a.selectedTileID[p.ID]
		// In live file mode the pane is "inside" the file: skip the
		// parent-grid node walk and render the focused file in the
		// inner box (inset by the file margin so the surrounding grid
		// pattern is visible). Inner-box bounds match the textarea
		// exactly so the user's "outside textarea = grid rules"
		// mental model is consistent.
		if p.TextFocus != 0 {
			if file, ok := g.Tiles[p.TextFocus]; ok {
				switch file.Kind {
				case rpc.KindText:
					ix, iy, iw, ih := fileInnerBox(p, r)
					a.cctx.Set("fillStyle", colorFileInnerBg)
					a.cctx.Call("fillRect", ix, iy, iw, ih)
					a.drawMarkdownInPane(p, &file, ix, iy, iw, ih)
				case rpc.KindURL:
					ix, iy, iw, ih := paneContentBox(r)
					a.drawURLTileInPane(p, &file, ix, iy, iw, ih)
				default:
					ix, iy, iw, ih := fileInnerBox(p, r)
					a.cctx.Set("fillStyle", colorFileInnerBg)
					a.cctx.Call("fillRect", ix, iy, iw, ih)
				}
			}
		} else {
			inSource := g != nil && (g.Meta.SourceKind == rpc.GridSourceFS || g.Meta.SourceKind == rpc.GridSourceProc)
			for _, n := range g.Tiles {
				if dragdrop.HiddenMatch(a.hiddenTileID, a.hiddenPaneID, p.ID, n.ID) {
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
				a.drawNodeWithPreview(&nn, left, top, w, h, cellSize, r, n.ID == selected, outside)
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
		msg := fmt.Sprintf("loading grid %d…", gid)
		if a.gridLoadFailed[gid] {
			msg = fmt.Sprintf("grid %d unavailable", gid)
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
	urlLive := a.urlStreams[p.ID] != nil
	border := paneBorderColorFor(p, g, gridOK, focused, urlLive)
	a.cctx.Set("strokeStyle", border)
	a.cctx.Set("lineWidth", paneBorderPx)
	half := paneBorderPx / 2
	a.cctx.Call("strokeRect", r.X+half, r.Y+half, r.W-paneBorderPx, r.H-paneBorderPx)

	// URL descent gets a canvas back-arrow button; markdown descent gets
	// the rendered/raw toggle as a DOM overlay button (refreshFileToggle)
	// so the text content can fill the pane.
	if p.TextFocus != 0 {
		if a.isURLDescent(p) {
			if a.urlStreams[p.ID] != nil {
				a.drawURLBackButton(p, r)
			} else {
				a.drawURLRefreshButton(p, r)
			}
		}
	} else {
		// + button is always available; gives the user an entry point
		// even when the grid is unreachable (they can still ascend, etc).
		a.drawPlusButton(p, r)
		if a.menuOpen && a.menuPaneID == p.ID {
			a.drawPalette(p, r)
		}
	}
}

// drawURLBackButton paints the lower-right button on a URL-tile descent.
// Click → history.back() on the descended Chromium tab. Same circular
// chrome as the + button so the position is muscle-memory-compatible.
func (a *App) drawURLBackButton(_ *pane.Pane, r pane.Rect) {
	cx, cy := plusButtonCenter(r)
	a.cctx.Set("fillStyle", colorPlusBg)
	a.cctx.Call("beginPath")
	a.cctx.Call("arc", cx, cy, float64(plusButtonRadius), 0, 2*math.Pi)
	a.cctx.Call("fill")
	a.cctx.Set("strokeStyle", colorPaneBorder)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("stroke")

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
func (a *App) drawURLRefreshButton(_ *pane.Pane, r pane.Rect) {
	cx, cy := plusButtonCenter(r)
	a.cctx.Set("fillStyle", colorPlusBg)
	a.cctx.Call("beginPath")
	a.cctx.Call("arc", cx, cy, float64(plusButtonRadius), 0, 2*math.Pi)
	a.cctx.Call("fill")
	a.cctx.Set("strokeStyle", colorPaneBorder)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("stroke")

	// Refresh glyph: reuse drawRefreshIcon at a size that fits the button circle.
	drawRefreshIcon(a.cctx, cx, cy, 7.0, colorPlusFg)
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
func (a *App) drawNodeWithPreview(n *rpc.Tile, x, y, w, h, parentCellSize float64, r pane.Rect, selected bool, outside bool) {
	switch n.Kind {
	case rpc.KindText:
		a.drawMarkdownNode(n, x, y, w, h, r, selected, outside)
		a.drawTileBannerLabel(n, x, y, w, h, outside)
		return
	case rpc.KindURL:
		a.drawURLTile(n, x, y, w, h, selected)
		a.drawTileBannerLabel(n, x, y, w, h, outside)
		return
	case rpc.KindBlackHole:
		drawBlackHoleSwatch(a.cctx, x, y, w, h)
		strokeTileBorder(a.cctx, x, y, w, h, colorExitBorder, tileBorderPx)
		if selected {
			drawSelectedTileOutline(a.cctx, x, y, w, h)
		}
		a.drawTileBannerLabel(n, x, y, w, h, outside)
		return
	}
	if n.Kind != rpc.KindWell && n.Kind != rpc.KindFileWell && n.Kind != rpc.KindProcessWell {
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
	drawGridLinesIn(a.cctx, wellGridLineColor(n.Kind), x, y, w, h, previewCell, originX, originY)

	if haveChild && previewCell >= 0.5 {
		var hide int64
		if a.hiddenPaneID == a.previewPaneID {
			hide = a.hiddenTileID
		}
		drawChildPreview(a.cctx, child, viewCenterX, viewCenterY,
			wellCenterX, wellCenterY, previewCell, x, y, w, h, hide)
	}
	a.cctx.Call("restore")

	// Outline: blue for interior wells, red for exit-wells (file-well /
	// process-well). The kind drives the color so the color grammar
	// is consistent whether the user sees the tile as a preview or
	// descends into it (where the same kind drives the pane border).
	strokeTileBorder(a.cctx, x, y, w, h, wellOutlineColor(n.Kind), tileBorderPx)
	if selected {
		drawSelectedTileOutline(a.cctx, x, y, w, h)
	}
	// Banner: "files" / "processes" for root exit-wells, basename for
	// non-root sub-wells. Regular Gridwell wells (KindWell) get no banner
	// — tileBannerLabel returns "" for them.
	a.drawTileBannerLabel(n, x, y, w, h, outside)
}

// wellOutlineColor picks the well-tile outline color from its kind.
// Blue for interior wells (Gridwell-owned), red for exit-wells whose
// contents come from outside Gridwell.
func wellOutlineColor(kind string) string {
	if kind == rpc.KindFileWell || kind == rpc.KindProcessWell {
		return colorExitBorder
	}
	return colorFocusBorder
}

// wellGridLineColor picks the grid-line color drawn inside a well's
// preview region from the well's kind. Matches wellOutlineColor's
// grammar one shade quieter — interior wells get a blue grid, exit
// wells (file / process) get a red grid.
func wellGridLineColor(kind string) string {
	if kind == rpc.KindFileWell || kind == rpc.KindProcessWell {
		return colorGridLineExit
	}
	return colorGridLineInterior
}

// paneGridLineColor picks the grid-line color for a pane's leaf grid:
// brown at the root (zero descent depth), red when the leaf is a
// source-backed (fs/proc) grid, blue otherwise. Mirrors the pane
// border grammar one shade quieter. Falls back to interior blue when
// the leaf grid hasn't loaded yet, since "we're descended into
// something Gridwell-owned" is the safe assumption mid-load.
func paneGridLineColor(p *pane.Pane, g *cache.Grid, gridOK bool) string {
	if len(p.Path) == 0 && p.TextFocus == 0 {
		return colorGridLineRoot
	}
	if gridOK && g != nil && (g.Meta.SourceKind == rpc.GridSourceFS || g.Meta.SourceKind == rpc.GridSourceProc) {
		return colorGridLineExit
	}
	return colorGridLineInterior
}

// tileReadOnly reports whether the user is allowed to edit n's text
// content. Text tiles whose `source_key` is set are read-only views of
// host state — file metadata reconciled from /proc, the @info tile in
// a proc-well — produced by the server's source-grid reconciler. The
// rendered/raw toggle, the textarea overlay, and the UpdateText round-
// trip on ascent all consult this so the user can't type into one of
// these, and a stale local buffer can't be silently re-posted.
func tileReadOnly(n *rpc.Tile) bool {
	return n.Kind == rpc.KindText && n.SourceKey != ""
}

// tileOutside reports whether a tile should be rendered with the "outside
// Gridwell" treatment (red outline / banner). True when:
//   - the tile's parent grid is a source-backed grid (fs/proc), so every
//     row in it represents host state, not Gridwell-owned data
//   - the tile is itself an exit-well kind (file-well / process-well)
//     anywhere — its child grid lives outside Gridwell regardless of
//     where the well sits
//   - the tile is a text tile carrying an source_key, i.e. it was cloned
//     out of an fs-grid and still represents an outside reference
func tileOutside(n *rpc.Tile, parentInSource bool) bool {
	if parentInSource {
		return true
	}
	switch n.Kind {
	case rpc.KindFileWell, rpc.KindProcessWell, rpc.KindBlackHole:
		// Black holes route their input to /dev/null — that's "outside"
		// in the same color-grammar sense as a file-well: the dropped
		// tile leaves Gridwell. Red border, "null" banner.
		return true
	}
	if n.Kind == rpc.KindText && n.SourceKey != "" {
		return true
	}
	return false
}

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
// outline so the label and the border read as one. Outside-tiles
// (anything red-bordered) win regardless of kind.
func bannerTextColor(n *rpc.Tile, outside bool) string {
	if outside || n.Kind == rpc.KindFileWell || n.Kind == rpc.KindProcessWell {
		return colorExitBorder
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

// drawMarkdownInPane renders a markdown file as the contents of the
// pane that is currently descended into it. Uses *that* pane's live
// TextMode / TextZoom / TextScroll values, so two split panes both
// looking at the same file can each scroll/zoom independently.
//
// Also responsible for skipping the canvas render in text mode for
// the focused pane (the textarea overlay handles that one). Other
// panes still render the source as canvas text so the user has
// continuity in non-focused split siblings.
func (a *App) drawMarkdownInPane(p *pane.Pane, n *rpc.Tile, x, y, w, h float64) {
	mode := p.TextMode
	if mode == "" {
		mode = rpc.TextModeRendered
	}
	// Fixed scale: the pane is a plain window onto the document. No zoom.
	scale := fileFixedScale
	scrollX := p.TextScrollX
	scrollY := p.TextScrollY

	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	hideForTextarea := mode == rpc.TextModeText && p.ID == a.tree.Focus
	if !hideForTextarea {
		if blob, ok := a.c.Blob(n.BlobID); ok {
			drawMarkdownInRect(a.cctx, string(blob),
				x-scrollX*scale, y-scrollY*scale,
				fileNaturalContentPx*scale, h+scrollY*scale,
				scale, 0, mode, a.makeEmbedDrawer(p.ID))
		} else if n.BlobID != 0 {
			a.fetchBlob(n.BlobID)
		}
	} else if _, ok := a.c.Blob(n.BlobID); !ok && n.BlobID != 0 {
		a.fetchBlob(n.BlobID)
	}

	a.cctx.Call("restore")
}

// drawMarkdownNode renders a markdown file tile at (x, y, w, h).
//
// Two distinct rendering modes:
//
//  1. Preview (no pane is descended into this file). Scale is clamped
//     to <= 1.0 so the rendered text never grows past natural reading
//     size, regardless of how far the user has zoomed into the parent
//     grid. A small file appears as a small but readable preview; a
//     large parent zoom doesn't blow it up.
//  2. Live text mode (a pane is descended into this file). The scale
//     is the pane's TextZoom (independent of parent zoom) and the
//     visible region is taken from TextScrollX/TextScrollY. Wheel
//     events update those fields directly so navigation is buttery.
//
// In both modes the parent grid lines remain visible behind the text
// (no fill), and an outline marks the footprint.
func (a *App) drawMarkdownNode(n *rpc.Tile, x, y, w, h float64, _ pane.Rect, selected bool, outside bool) {
	// Mode comes from the tile (persisted on the server); default to raw
	// text for a never-opened file. A pane descended into this file
	// overrides with its live mode.
	mode := n.TextMode
	if mode == "" {
		mode = rpc.TextModeText
	}
	fp := a.paneFocusedOnFile(n.ID)
	var scale, scrollX, scrollY float64
	if fp != nil {
		if fp.TextMode != "" {
			mode = fp.TextMode
		}
		// While a pane is descended, mirror what the preview will be once
		// the user ascends: cover-crop the LIVE framed window (live scroll
		// + the focused pane's current inner-box size) into this tile.
		// Rendering at raw 1:1 instead made the text look too small in a
		// small tile and rescale as the parent grid zoomed.
		fpRect := a.paneRectByID(fp.ID)
		_, _, iw, ih := fileInnerBox(fp, fpRect)
		scrollX = fp.TextScrollX
		scrollY = fp.TextScrollY
		if iw > 0 && ih > 0 {
			scale = w / iw
			if sy := h / ih; sy > scale {
				scale = sy
			}
		} else {
			scale = fileFixedScale
		}
	} else if n.TextW > 0 && n.TextH > 0 {
		// Preview mirrors the last framed window: cover-crop the saved
		// document-space rectangle (TextX,TextY,TextW,TextH) into the
		// tile footprint (x,y,w,h). The larger axis ratio binds (cover),
		// anchored at the window's top-left so headings stay in view.
		// w/h already include the parent grid zoom, so the label scales
		// naturally as the user zooms the parent grid.
		sxr := w / float64(n.TextW)
		syr := h / float64(n.TextH)
		scale = sxr
		if syr > scale {
			scale = syr
		}
		scrollX = float64(n.TextX)
		scrollY = float64(n.TextY)
	} else {
		// Never framed: fit the document width to the tile, top-aligned.
		scale = w / fileNaturalContentPx
		if scale < 0.02 {
			scale = 0.02
		}
	}

	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	// Paint the full cell with the same light-grey background that
	// live mode uses for its inner-box. The user sees the cell as the
	// preview unit; the grey = cell.
	a.cctx.Set("fillStyle", colorFileInnerBg)
	a.cctx.Call("fillRect", x, y, w, h)

	// When the focused pane is descended into THIS file in text mode,
	// the textarea overlay renders the editable source. Drawing the
	// markdown to the canvas behind it would just produce a doubled,
	// misaligned render, so skip it.
	hideForTextarea := fp != nil && fp.TextMode == rpc.TextModeText && fp.ID == a.tree.Focus
	if !hideForTextarea {
		if blob, ok := a.c.Blob(n.BlobID); ok {
			// Layout width is fixed at the natural content width so
			// every preview wraps lines the same way live mode does —
			// principle of continuity across the path swap. Text that
			// would extend past the cell to the right (because live
			// inner-box is wider than tall) gets clipped at the cell
			// edge — that's the "start from the left" behavior the
			// user asked for.
			// Preview embeds render the same as live embeds (kind-tinted
			// thumbnail) but skip hit registration — clicking a preview
			// embed descends into the parent text tile, not the embed's
			// target. Live mode wires the hit list via makeEmbedDrawer.
			drawMarkdownInRect(a.cctx, string(blob),
				x-scrollX*scale, y-scrollY*scale,
				fileNaturalContentPx*scale, h+scrollY*scale,
				scale, 0, mode, a.makePreviewEmbedDrawer())
		} else if n.BlobID != 0 {
			a.fetchBlob(n.BlobID)
		}
	} else if _, ok := a.c.Blob(n.BlobID); !ok && n.BlobID != 0 {
		a.fetchBlob(n.BlobID)
	}

	a.cctx.Call("restore")

	// Outline color follows the color grammar: green for in-Gridwell text
	// tiles, red when this tile represents something outside (clone of a
	// source-grid file, or rendered inside a source grid).
	outlineColor := colorMarkdownLine
	if outside {
		outlineColor = colorExitBorder
	}
	strokeTileBorder(a.cctx, x, y, w, h, outlineColor, tileBorderPx)
	if selected {
		drawSelectedTileOutline(a.cctx, x, y, w, h)
	}
}

// drawMarkdownInRect lays out and paints `src` (markdown source) into the
// rectangle (x, y, w, h) at the given `scale` (1.0 = base sizes), starting
// at vertical scroll offset `scrollY` measured in logical pixels (i.e.,
// the units the layout would use at scale 1.0). The mode picks between
// rendered (block layout with headings/bold/etc.) and text (raw source as
// monospace).
//
// The rect's clip is the caller's responsibility.
func drawMarkdownInRect(c js.Value, src string, x, y, w, h, scale, scrollY float64, mode string, drawEmbed embedDrawer) {
	if mode == rpc.TextModeText {
		drawMarkdownText(c, src, x, y, w, h, scale, scrollY)
		return
	}
	drawMarkdownRendered(c, src, x, y, w, h, scale, scrollY, drawEmbed)
}

// markdownStyle holds the per-block-kind font/spacing parameters in
// logical pixels (i.e., before applying scale).
type markdownStyle struct {
	bodyPx     float64
	h1Px       float64
	h2Px       float64
	h3Px       float64
	codePx     float64
	pad        float64 // logical px gutter at top/left of layout
	gapAfter   float64 // logical px below each block
	monospace  string
	sansSerif  string
	textColor  string
	mutedColor string
	codeBg     string
	quoteBar   string
}

func defaultMarkdownStyle() markdownStyle {
	return markdownStyle{
		bodyPx:     14,
		h1Px:       24,
		h2Px:       19,
		h3Px:       16,
		codePx:     13,
		pad:        6,
		gapAfter:   4,
		monospace:  `ui-monospace, "SF Mono", Menlo, Consolas, monospace`,
		sansSerif:  `ui-sans-serif, system-ui, -apple-system, sans-serif`,
		textColor:  "#d8d9de",
		mutedColor: "#9ca0ad",
		codeBg:     "#1c1d24",
		quoteBar:   "#3a4b5a",
	}
}

// blockFontSize returns the font size in logical pixels for a block kind.
func (s markdownStyle) blockFontSize(k markdown.BlockKind) float64 {
	switch k {
	case markdown.BlockHeading1:
		return s.h1Px
	case markdown.BlockHeading2:
		return s.h2Px
	case markdown.BlockHeading3:
		return s.h3Px
	case markdown.BlockCode:
		return s.codePx
	}
	return s.bodyPx
}

// blockFamily returns the font family for a block kind.
func (s markdownStyle) blockFamily(k markdown.BlockKind) string {
	if k == markdown.BlockCode {
		return s.monospace
	}
	return s.sansSerif
}

// drawMarkdownRendered does block-level layout of `src` and paints it
// scaled by `scale` into (x, y, w, h), scrolled vertically by scrollY
// logical pixels (so scrollY=0 shows from the top).
func drawMarkdownRendered(c js.Value, src string, x, y, w, h, scale, scrollY float64, drawEmbed embedDrawer) {
	st := defaultMarkdownStyle()
	blocks := markdown.Parse(src)
	contentWidthLogical := (w / scale) - 2*st.pad
	if contentWidthLogical < 8 {
		contentWidthLogical = 8
	}

	c.Set("textBaseline", "top")
	c.Set("fillStyle", st.textColor)

	cursorY := st.pad - scrollY
	for _, b := range blocks {
		fontPx := st.blockFontSize(b.Kind)
		family := st.blockFamily(b.Kind)
		lineHeight := fontPx * 1.35

		switch b.Kind {
		case markdown.BlockBlank:
			cursorY += st.bodyPx * 0.6
			continue
		case markdown.BlockCode:
			// Code block: monospace, optional background tint.
			text := ""
			if len(b.Spans) > 0 {
				text = b.Spans[0].Text
			}
			lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
			blockHeight := lineHeight * float64(len(lines))
			top := cursorY * scale
			if top+blockHeight*scale > 0 && top < h {
				c.Set("fillStyle", st.codeBg)
				c.Call("fillRect", x+st.pad*scale, y+top, contentWidthLogical*scale, blockHeight*scale)
			}
			setFont(c, fontPx*scale, family, false, false)
			c.Set("fillStyle", st.textColor)
			for _, ln := range lines {
				yPx := cursorY * scale
				if yPx+lineHeight*scale > 0 && yPx < h {
					c.Call("fillText", ln, x+st.pad*scale+4, y+yPx)
				}
				cursorY += lineHeight
			}
			cursorY += st.gapAfter
			continue
		case markdown.BlockBlockquote:
			// Vertical bar in the gutter; indent text.
			topLogical := cursorY
			indent := 12.0
			lines := wrapInline(c, b.Spans, contentWidthLogical-indent, fontPx, family, scale)
			blockHeight := lineHeight * float64(len(lines))
			topPx := topLogical * scale
			if topPx+blockHeight*scale > 0 && topPx < h {
				c.Set("fillStyle", st.quoteBar)
				c.Call("fillRect", x+st.pad*scale, y+topPx, 3*scale, blockHeight*scale)
			}
			c.Set("fillStyle", st.mutedColor)
			drawInlineLines(c, lines, x+(st.pad+indent)*scale, y, cursorY, lineHeight, fontPx, family, scale, h, drawEmbed)
			cursorY += blockHeight + st.gapAfter
			continue
		case markdown.BlockListItem:
			// Bullet, then wrapped inline text.
			indent := 14.0
			lines := wrapInline(c, b.Spans, contentWidthLogical-indent, fontPx, family, scale)
			// Bullet sits on the first line.
			yPx := cursorY * scale
			if yPx+lineHeight*scale > 0 && yPx < h {
				setFont(c, fontPx*scale, family, false, false)
				c.Set("fillStyle", st.textColor)
				c.Call("fillText", "•", x+(st.pad+4)*scale, y+yPx)
			}
			drawInlineLines(c, lines, x+(st.pad+indent)*scale, y, cursorY, lineHeight, fontPx, family, scale, h, drawEmbed)
			cursorY += lineHeight*float64(len(lines)) + st.gapAfter
			continue
		}
		// Headings and paragraphs.
		lines := wrapInline(c, b.Spans, contentWidthLogical, fontPx, family, scale)
		drawInlineLines(c, lines, x+st.pad*scale, y, cursorY, lineHeight, fontPx, family, scale, h, drawEmbed)
		cursorY += lineHeight*float64(len(lines)) + st.gapAfter
	}
}

// wrapInline measures the spans and wraps them into lines that fit
// contentWidthLogical at the given font size. The font is set per-call so
// measureText reflects the right metrics; scale is applied uniformly so
// the wrap matches what the caller will paint.
func wrapInline(c js.Value, spans []markdown.Span, contentWidthLogical, fontPx float64, family string, scale float64) [][]markdown.Span {
	measure := func(sp markdown.Span) float64 {
		if embedpkg.SpanIsEmbed(sp) {
			ew, _ := embedLogicalSize(sp)
			return ew
		}
		setFont(c, fontPx*scale, family, sp.Style&markdown.StyleBold != 0, sp.Style&markdown.StyleItalic != 0)
		mt := c.Call("measureText", sp.Text)
		// Convert measured pixels back to logical units.
		return mt.Get("width").Float() / scale
	}
	return markdown.Wrap(spans, contentWidthLogical, measure)
}

// embedLogicalSize is the wasm-side adapter that binds the renderer's
// defaults to the pure embed.SpanEmbedSize.
func embedLogicalSize(sp markdown.Span) (float64, float64) {
	return embedpkg.SpanEmbedSize(sp, defaultEmbedW, defaultEmbedH)
}

// drawInlineLines paints wrapped inline lines starting at logical
// (xPx, baseTopLogical) with the given lineHeight; clips drawing to (y, h)
// in screen pixels. Embeds are dispatched to drawEmbed (when non-nil) and
// fall back to their alt text when no drawer is supplied.
func drawInlineLines(c js.Value, lines [][]markdown.Span, xPx, yBase, baseTopLogical, lineHeight, fontPx float64, family string, scale, h float64, drawEmbed embedDrawer) {
	for li, line := range lines {
		yLogical := baseTopLogical + float64(li)*lineHeight
		yPx := yLogical * scale
		if yPx+lineHeight*scale < 0 || yPx > h {
			continue
		}
		curX := xPx
		for _, sp := range line {
			if sp.Style&markdown.StyleEmbed != 0 {
				ewLogical, ehLogical := embedLogicalSize(sp)
				ew := ewLogical * scale
				eh := ehLogical * scale
				if drawEmbed != nil {
					drawEmbed(curX, yBase+yPx, ew, eh, sp.Href, sp.Alt)
				} else {
					// Preview / no-embed context: render the alt text inline.
					setFont(c, fontPx*scale, family, false, true)
					c.Set("fillStyle", colorMuted)
					label := sp.Alt
					if label == "" {
						label = "[embed]"
					}
					c.Call("fillText", label, curX, yBase+yPx)
				}
				curX += ew
				continue
			}
			if sp.Style&markdown.StyleLink != 0 {
				// If the href looks like a tile descent path, paint a
				// preview embed rather than a plain text link — that's
				// how the plain `[alt](href)` form becomes an embed
				// inside Gridwell while staying a normal link outside.
				if drawEmbed != nil && embedpkg.LeafTileIDFromHref(sp.Href) != 0 {
					ewLogical, ehLogical := embedLogicalSize(sp)
					ew := ewLogical * scale
					eh := ehLogical * scale
					drawEmbed(curX, yBase+yPx, ew, eh, sp.Href, sp.Text)
					curX += ew
					continue
				}
				setFont(c, fontPx*scale, family,
					sp.Style&markdown.StyleBold != 0,
					sp.Style&markdown.StyleItalic != 0)
				c.Set("fillStyle", colorURLLine)
				c.Call("fillText", sp.Text, curX, yBase+yPx)
				w := c.Call("measureText", sp.Text).Get("width").Float()
				// Underline.
				c.Set("fillStyle", colorURLLine)
				c.Call("fillRect", curX, yBase+yPx+fontPx*scale*1.05, w, 1)
				curX += w
				continue
			}
			bold := sp.Style&markdown.StyleBold != 0
			italic := sp.Style&markdown.StyleItalic != 0
			code := sp.Style&markdown.StyleCode != 0
			family2 := family
			if code {
				family2 = `ui-monospace, "SF Mono", Menlo, Consolas, monospace`
			}
			setFont(c, fontPx*scale, family2, bold, italic)
			if code {
				c.Set("fillStyle", colorMarkdownCodeBg2)
				w := c.Call("measureText", sp.Text).Get("width").Float()
				c.Call("fillRect", curX-2, yBase+yPx, w+4, lineHeight*scale)
			}
			c.Set("fillStyle", colorMarkdownBody)
			c.Call("fillText", sp.Text, curX, yBase+yPx)
			w := c.Call("measureText", sp.Text).Get("width").Float()
			curX += w
		}
	}
}

// setFont assembles a CSS font shorthand string and assigns it. Bold/italic
// are optional; size is in pixels.
func setFont(c js.Value, sizePx float64, family string, bold, italic bool) {
	style := "normal"
	if italic {
		style = "italic"
	}
	weight := "normal"
	if bold {
		weight = "bold"
	}
	// Browsers refuse fonts at 0px; clamp to a tiny minimum so the call doesn't error.
	if sizePx < 1 {
		sizePx = 1
	}
	c.Set("font", fmt.Sprintf("%s %s %.2fpx %s", style, weight, sizePx, family))
}

// drawMarkdownText paints src as raw monospace text at the same scale the
// rendered view would use. Used for the source-mode preview and as a faint
// backdrop while the textarea overlay is painted on top. The w parameter is
// unused: text mode does not soft-wrap; long lines are clipped by the
// caller's clip rect.
func drawMarkdownText(c js.Value, src string, x, y, _ /* w */, h, scale, scrollY float64) {
	st := defaultMarkdownStyle()
	fontPx := st.codePx
	lineHeight := fontPx * 1.35
	setFont(c, fontPx*scale, st.monospace, false, false)
	c.Set("textBaseline", "top")
	c.Set("fillStyle", st.textColor)
	lines := strings.Split(src, "\n")
	cursorY := st.pad - scrollY
	for _, ln := range lines {
		yPx := cursorY * scale
		if yPx+lineHeight*scale > 0 && yPx < h {
			c.Call("fillText", ln, x+st.pad*scale, y+yPx)
		}
		cursorY += lineHeight
	}
}

// paneFocusedOnFile returns any pane currently descended into the given
// file tile id, or nil if none. Used by the renderer to pull the live
// scroll/mode state instead of the saved view_y.
func (a *App) paneFocusedOnFile(fileTileID int64) *pane.Pane {
	var found *pane.Pane
	a.tree.Walk(func(p *pane.Pane) {
		if p.TextFocus == fileTileID {
			found = p
		}
	})
	return found
}

// fetchBlob issues GetBlob for the given blob id and stores the bytes in
// the cache. Idempotent: a successful previous fetch short-circuits.
func (a *App) fetchBlob(blobID int64) {
	if blobID == 0 {
		return
	}
	if _, ok := a.c.Blob(blobID); ok {
		return
	}
	go func() {
		data, err := a.cl.GetBlob(context.Background(), blobID)
		if err != nil {
			return
		}
		a.c.PutBlob(blobID, data)
		// If the focused pane is waiting on this blob to populate its
		// text-mode editor, seed the textarea now.
		a.refreshFileOverlay()
		a.draw()
	}()
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
func drawChildPreview(c js.Value, child *cache.Grid,
	centerCellX, centerCellY, centerScreenX, centerScreenY, previewCell float64,
	clipX, clipY, clipW, clipH float64,
	hiddenTileID int64,
) {
	childInSource := child != nil && (child.Meta.SourceKind == rpc.GridSourceFS || child.Meta.SourceKind == rpc.GridSourceProc)
	// Scale the inner-tile border so a child grid viewed from a distance
	// keeps its borders proportionate to the cells. Full-size live tiles
	// use 2px; previews glide down with the cell scale.
	borderPx := previewBorderPxFor(previewCell)
	for _, n := range child.Tiles {
		if hiddenTileID != 0 && n.ID == hiddenTileID {
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
	}
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
	case rpc.KindWell, rpc.KindFileWell, rpc.KindProcessWell:
		c.Set("fillStyle", colorBg)
		c.Call("fillRect", x, y, w, h)
		strokeTileBorder(c, x, y, w, h, wellOutlineColor(n.Kind), borderPx)
	case rpc.KindBlackHole:
		drawBlackHoleSwatch(c, x, y, w, h)
		strokeTileBorder(c, x, y, w, h, colorExitBorder, borderPx)
	case rpc.KindURL:
		c.Set("fillStyle", colorURLFill)
		c.Call("fillRect", x, y, w, h)
		strokeTileBorder(c, x, y, w, h, colorURLLine, borderPx)
	case rpc.KindText:
		fill := colorMarkdownFill
		line := colorMarkdownLine
		if outside {
			fill = colorExitFill
			line = colorExitBorder
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
	if frag < 0.02 {
		a.drawNodeWithPreview(n, x, y, w, h, parentCellSize, r, false, outside)
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
		a.drawNodeWithPreview(n, x, y, w, h, parentCellSize, r, false, outside)
		a.cctx.Set("globalAlpha", 1.0)
	}
	a.cctx.Set("globalAlpha", frag)
	drawTrashcanIcon(a.cctx, x, y, w, h)
	a.cctx.Set("globalAlpha", 1.0)
}

// drawTrashcanIcon paints a generic "trash" glyph inside (x, y, w, h).
// Pure strokes in a single colour so it stays legible at every size
// the ghost shrinks down to.
func drawTrashcanIcon(c js.Value, x, y, w, h float64) {
	// Side margin so the can doesn't touch the bounding rect.
	mx := w * 0.18
	my := h * 0.12
	left := x + mx
	right := x + w - mx
	top := y + my
	bottom := y + h - my
	bodyTop := top + (bottom-top)*0.22
	lidHeight := (bodyTop - top) * 0.55
	c.Set("strokeStyle", colorMenuItemHi)
	c.Set("fillStyle", colorMenuItemHi)
	lw := math.Max(1.5, math.Min(w, h)/22)
	c.Set("lineWidth", lw)

	// Handle on top of the lid.
	handleW := (right - left) * 0.32
	handleX := (left+right)/2 - handleW/2
	handleY := top
	c.Call("beginPath")
	c.Call("rect", handleX, handleY, handleW, lidHeight*0.7)
	c.Call("stroke")

	// Lid: a horizontal slab spanning the full width.
	c.Call("beginPath")
	c.Call("rect", left, top+lidHeight, right-left, lidHeight)
	c.Call("stroke")

	// Body: slightly tapered inward at the bottom for the classic can
	// silhouette. Stroked, not filled, so the underlying ghost (if
	// any) shows through faintly during the cross-fade.
	bodyW := right - left
	taper := bodyW * 0.06
	c.Call("beginPath")
	c.Call("moveTo", left+taper, bottom)
	c.Call("lineTo", left, bodyTop)
	c.Call("lineTo", right, bodyTop)
	c.Call("lineTo", right-taper, bottom)
	c.Call("closePath")
	c.Call("stroke")

	// Three vertical ribs inside the body — recognisable trashcan
	// detail even at small sizes.
	ribSpacing := (bodyW - 2*taper) / 4
	for i := 1; i <= 3; i++ {
		rx := left + taper + ribSpacing*float64(i)
		c.Call("beginPath")
		c.Call("moveTo", rx, bodyTop+(bottom-bodyTop)*0.18)
		c.Call("lineTo", rx-taper*0.7, bottom-(bottom-bodyTop)*0.08)
		c.Call("stroke")
	}
	c.Set("lineWidth", 1.0)
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
func paneBorderColorFor(p *pane.Pane, g *cache.Grid, gridOK bool, focused bool, urlLive bool) string {
	in := pane.BorderInput{
		HasTextFocus: p.TextFocus != 0,
		DescentDepth: len(p.Path),
		Focused:      focused,
		URLLive:      urlLive,
	}
	if p.TextFocus != 0 && gridOK {
		if tile, ok := g.Tiles[p.TextFocus]; ok {
			in.TileKnown = true
			in.TileKind = tile.Kind
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
	Focused:      colorFocusBorder,
	FocusedFaded: colorFocusBorderFaded,
	Root:         colorRootBorder,
	Text:         colorMarkdownLine,
	TextFaded:    colorMarkdownLineFaded,
	URL:          colorURLLine,
	URLFaded:     colorURLLineFaded,
	URLLive:      colorURLLiveLine,
	Exit:         colorExitBorder,
	ExitFaded:    colorExitBorderFaded,
}

// drawDocumentGlyph paints a "page with text lines" icon centered in
// (x, y, w, h): a vertical rectangle (the page) with three horizontal
// lines inside suggesting body text — the last line slightly shorter
// so it reads as a paragraph end. Used for the markdown palette tile
// and anywhere else the markdown identity needs a quick visual cue.
func drawDocumentGlyph(c js.Value, x, y, w, h float64, color string) {
	c.Set("strokeStyle", color)
	lw := math.Max(1.0, math.Min(w, h)/28)
	c.Set("lineWidth", lw)
	// Page rect: slightly taller than wide, centered.
	pw := w * 0.46
	ph := h * 0.62
	px := x + (w-pw)/2
	py := y + (h-ph)/2
	c.Call("strokeRect", px+0.5, py+0.5, pw-1, ph-1)
	// Three text lines.
	for i, frac := range []float64{0.28, 0.50, 0.72} {
		ly := py + ph*frac
		endFrac := 0.78
		if i == 2 {
			endFrac = 0.55 // last line shorter — paragraph end
		}
		c.Call("beginPath")
		c.Call("moveTo", px+pw*0.15, ly)
		c.Call("lineTo", px+pw*0.15+pw*endFrac, ly)
		c.Call("stroke")
	}
	c.Set("lineWidth", 1.0)
}

// drawGlobeGlyph paints a stylized globe centered in (x, y, w, h):
// an outer circle, a horizontal equator, and a vertical meridian
// drawn as a narrow ellipse. Used for the URL palette tile.
func drawGlobeGlyph(c js.Value, x, y, w, h float64, color string) {
	c.Set("strokeStyle", color)
	lw := math.Max(1.0, math.Min(w, h)/24)
	c.Set("lineWidth", lw)
	cx := x + w/2
	cy := y + h/2
	r := math.Min(w, h) * 0.30
	c.Call("beginPath")
	c.Call("arc", cx, cy, r, 0.0, 2*math.Pi)
	c.Call("stroke")
	// Equator.
	c.Call("beginPath")
	c.Call("moveTo", cx-r, cy)
	c.Call("lineTo", cx+r, cy)
	c.Call("stroke")
	// Vertical meridian — a narrow ellipse passing through the poles
	// so it reads as a curved longitude line rather than a straight
	// bar.
	c.Call("beginPath")
	c.Call("ellipse", cx, cy, r*0.45, r, 0.0, 0.0, 2*math.Pi)
	c.Call("stroke")
	c.Set("lineWidth", 1.0)
}

// drawFolderGlyph paints a simple folder icon centered in (x, y, w, h):
// a rectangle body with a slanted tab on its upper-left edge. The whole
// outline is one closed path so the tab's right edge is part of the
// stroke (previously a separate body strokeRect left the tab open on
// the right).
func drawFolderGlyph(c js.Value, x, y, w, h float64, color string) {
	c.Set("strokeStyle", color)
	lw := math.Max(1.0, math.Min(w, h)/24)
	c.Set("lineWidth", lw)
	bw := w * 0.55
	bh := h * 0.40
	bx := x + (w-bw)/2
	by := y + h*0.42
	tabW := bw * 0.35
	tabH := bh * 0.20
	// Outer outline, clockwise from body bottom-left.
	c.Call("beginPath")
	c.Call("moveTo", bx, by+bh)             // body bottom-left
	c.Call("lineTo", bx, by)                // up the body left side
	c.Call("lineTo", bx+tabW, by)           // right along body top to tab base
	c.Call("lineTo", bx+tabW+tabH, by-tabH) // slanted tab edge up-right
	c.Call("lineTo", bx+bw, by-tabH)        // across tab top
	c.Call("lineTo", bx+bw, by+bh)          // down body right side
	c.Call("closePath")                     // back along body bottom
	c.Call("stroke")
	c.Set("lineWidth", 1.0)
}

// drawProcessGlyph paints a small process-tree icon centered in (x, y,
// w, h): a parent node and two child nodes connected by lines. Used for
// the process-well palette tile.
func drawProcessGlyph(c js.Value, x, y, w, h float64, color string) {
	c.Set("strokeStyle", color)
	c.Set("fillStyle", color)
	lw := math.Max(1.0, math.Min(w, h)/24)
	c.Set("lineWidth", lw)
	cx := x + w/2
	parentY := y + h*0.32
	childY := y + h*0.66
	childOffset := w * 0.18
	nodeR := math.Min(w, h) * 0.08
	// Parent node.
	c.Call("beginPath")
	c.Call("arc", cx, parentY, nodeR, 0.0, 2*math.Pi)
	c.Call("fill")
	// Two child nodes.
	for _, dx := range []float64{-childOffset, childOffset} {
		c.Call("beginPath")
		c.Call("arc", cx+dx, childY, nodeR, 0.0, 2*math.Pi)
		c.Call("fill")
		// Connector.
		c.Call("beginPath")
		c.Call("moveTo", cx, parentY+nodeR)
		c.Call("lineTo", cx+dx, childY-nodeR)
		c.Call("stroke")
	}
	c.Set("lineWidth", 1.0)
}

// drawBlackHoleSwatch paints the canonical black-hole tile fill: a
// pure-black rectangle. The orange border drawn by the caller carries
// the "this is an exit" signal; the "null" banner label spells out the
// destination. No interior glyph — the dark void is the metaphor.
func drawBlackHoleSwatch(c js.Value, x, y, w, h float64) {
	c.Set("fillStyle", colorBlackHoleSwatchBg)
	c.Call("fillRect", x, y, w, h)
}

// drawEdgeIndicators paints small markers along the pane's inner edge for
// every tile whose footprint lies entirely outside the visible viewport.
// Each marker is positioned where the ray from the viewport center to the
// tile's center crosses the pane's inset rectangle, and points outward.
func (a *App) drawEdgeIndicators(nodes map[int64]rpc.Tile, ps dragdrop.Pane, r pane.Rect) {
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
func paletteLayoutFor(p *pane.Pane, r pane.Rect) palette.Layout {
	return palette.Layout{
		Cfg:      palette.Default(),
		Pane:     palette.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H},
		PaneZoom: p.Zoom,
		NumTiles: len(templateKinds),
	}
}

// plusButtonCenter returns the screen-space center of the + button for a
// given pane. The pane's zoom does not influence the + button, but
// using paletteLayoutFor keeps every screen-space layout computation
// going through one helper.
func plusButtonCenter(r pane.Rect) (float64, float64) {
	l := palette.Layout{
		Cfg:  palette.Default(),
		Pane: palette.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H},
	}
	return l.PlusCenter()
}

// pointInPlus reports whether (x, y) lies within the + button for the given
// pane rect.
func pointInPlus(r pane.Rect, x, y float64) bool {
	l := palette.Layout{
		Cfg:  palette.Default(),
		Pane: palette.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H},
	}
	return l.PointInPlus(x, y)
}

// drawPlusButton paints the floating circular + button in the pane's lower
// right.
func (a *App) drawPlusButton(p *pane.Pane, r pane.Rect) {
	cx, cy := plusButtonCenter(r)
	bg := colorPlusBg
	if a.menuOpen && a.menuPaneID == p.ID {
		bg = colorPlusBgHi
	}
	a.cctx.Set("fillStyle", bg)
	a.cctx.Call("beginPath")
	a.cctx.Call("arc", cx, cy, float64(plusButtonRadius), 0, 2*math.Pi)
	a.cctx.Call("fill")
	a.cctx.Set("strokeStyle", colorPaneBorder)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("stroke")

	// Plus glyph: two strokes through center.
	a.cctx.Set("strokeStyle", colorPlusFg)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("beginPath")
	a.cctx.Call("moveTo", cx-8, cy)
	a.cctx.Call("lineTo", cx+8, cy)
	a.cctx.Call("moveTo", cx, cy-8)
	a.cctx.Call("lineTo", cx, cy+8)
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)
}

// paletteRect is the wasm-side adapter for palette.Layout.PopoverRect.
func paletteRect(p *pane.Pane, r pane.Rect) (x, y, w, h float64) {
	pop := paletteLayoutFor(p, r).PopoverRect()
	return pop.X, pop.Y, pop.W, pop.H
}

// paletteTileRect is the wasm-side adapter for palette.Layout.TileRect.
func paletteTileRect(p *pane.Pane, r pane.Rect, i int) (x, y, w, h float64) {
	tr := paletteLayoutFor(p, r).TileRect(i)
	return tr.X, tr.Y, tr.W, tr.H
}

// drawPalette paints the creation popover: a background container and
// a horizontal row of preview tiles, one per templateKind.
func (a *App) drawPalette(p *pane.Pane, r pane.Rect) {
	mx, my, mw, mh := paletteRect(p, r)
	a.cctx.Set("fillStyle", colorMenuBg)
	a.cctx.Call("fillRect", mx, my, mw, mh)
	a.cctx.Set("strokeStyle", colorPaneBorder)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("strokeRect", mx+0.5, my+0.5, mw-1, mh-1)
	for i, kind := range templateKinds {
		tx, ty, tw, th := paletteTileRect(p, r, i)
		hovered := a.menuHover == i
		a.drawPaletteTile(kind, tx, ty, tw, th, hovered)
	}
}

// drawPaletteTile renders one preview tile inside the palette. The body
// (fill + border + banner) is shared with the live-tile renderer so a
// palette swatch reads identical to what the user drops — same color
// grammar, same "null"/"files"/"processes" banner. A kind-specific
// glyph is overlaid on tile kinds where the live tile lacks a static
// identity cue (markdown / url / file-well / process-well), so the
// palette still reads "what is this?" before the tile has content.
func (a *App) drawPaletteTile(kind templateKind, x, y, w, h float64, hovered bool) {
	n := templateGhostNode(kind)
	outside := tileOutside(&n, false)
	drawNode(a.cctx, &n, x, y, w, h, false, outside, tileBorderPx)
	// Well swatches get a mini-grid in their kind-tinted color so the
	// palette preview reads the same as a live well: blue grid inside
	// a Gridwell well, red grid inside a file / process well. Spacing
	// matches the live drawNodeWithPreview default-view-zoom path —
	// the swatch shows roughly DefaultWellViewZoom-scaled cells.
	if n.Kind == rpc.KindWell || n.Kind == rpc.KindFileWell || n.Kind == rpc.KindProcessWell {
		previewCell := w * zoomtrans.DefaultWellViewZoom
		a.cctx.Call("save")
		a.cctx.Call("beginPath")
		a.cctx.Call("rect", x, y, w, h)
		a.cctx.Call("clip")
		drawGridLinesIn(a.cctx, wellGridLineColor(n.Kind), x, y, w, h, previewCell, x+w/2, y+h/2)
		a.cctx.Call("restore")
	}
	a.drawTileBannerLabel(&n, x, y, w, h, outside)
	switch kind {
	case tplMarkdown:
		drawDocumentGlyph(a.cctx, x, y, w, h, colorMenuItemHi)
	case tplURL:
		drawGlobeGlyph(a.cctx, x, y, w, h, colorMenuItemHi)
	case tplFileWell:
		drawFolderGlyph(a.cctx, x, y, w, h, colorExitBorder)
	case tplProcessWell:
		drawProcessGlyph(a.cctx, x, y, w, h, colorExitBorder)
	}
	if hovered {
		drawSelectedTileOutline(a.cctx, x, y, w, h)
	}
}

// paletteTileIndexAt is the wasm-side adapter for palette.Layout.TileIndexAt.
func paletteTileIndexAt(p *pane.Pane, r pane.Rect, x, y float64) int {
	return paletteLayoutFor(p, r).TileIndexAt(x, y)
}

// pointInPalette is the wasm-side adapter for palette.Layout.PointInPopover.
func pointInPalette(p *pane.Pane, r pane.Rect, x, y float64) bool {
	return paletteLayoutFor(p, r).PointInPopover(x, y)
}
