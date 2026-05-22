//go:build js && wasm

package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/zoomtrans"
	"github.com/josephburnett/gridwell/internal/rpc"
)

const (
	colorBg          = "#0c0d11"
	colorFileInnerBg = "#1c1f26"
	colorPaneBorder  = "#1f2229"
	colorFocusBorder = "#4a6fff"
	colorGridLine    = "#15171d"
	// File colors are keyed off the first half of the MIME type, not the
	// specific subtype: text/markdown and text/uri-list share one palette,
	// image/* shares another. The user identifies stones by color; the
	// preview (when zoomed in enough) reveals the rest.
	colorTextFill    = "#2b1a3a"
	colorTextLine    = "#7a5a9a"
	colorImageFill   = "#1a3a2b"
	colorImageLine   = "#5a8a6a"
	colorLocked      = "#26262a"
	colorSelected    = "#e3b16f"
	colorEdgeDot     = "#5a6a8a"
	colorPlusBg      = "#23252d"
	colorPlusBgHi    = "#2d3140"
	colorPlusFg      = "#c8c9ce"
	colorMenuBg      = "#16181f"
	colorMenuItemHi  = "#e8e9ee"
	colorMuted       = "#6c6f78"
)

const (
	plusButtonRadius = 18
	plusButtonInset  = 24

	// Palette tile size clamps. The tile size in the palette tracks the
	// pane's zoom (so the user sees roughly what they'll get), but is
	// bounded so the palette stays usable at extreme zoom.
	paletteMinTilePx = 48.0
	paletteMaxTilePx = 128.0
	paletteGapPx     = 8.0

	// paneBorderPx is the visible thickness of the pane outline. Wide
	// enough to be a target for right-drag (resize / close) without
	// dominating the pane's interior. The right-button input layer
	// uses a slightly larger hit-band than this so users don't have to
	// pixel-hunt the divider.
	paneBorderPx = 6.0
)

// templateKind identifies one entry in the creation palette. Order
// matters: it determines layout in the popover and the indices used
// by hit-testing.
type templateKind int

const (
	tplWell templateKind = iota
	tplMarkdown
	tplURL
	tplUpload
	// tplBlackHole spawns a "trashcan" tile. Dropping another tile on
	// top of a black hole deletes the dropped tile — the only delete
	// affordance in the UI.
	tplBlackHole
)

// templateKinds is the palette layout order, left to right.
var templateKinds = []templateKind{tplWell, tplMarkdown, tplURL, tplUpload, tplBlackHole}

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

// paneRect is a rectangle in screen coordinates. It mirrors pane.Rect
// so the rest of the wasm code keeps using a local type while the
// underlying layout is computed by the (testable) pane.Layout helper.
type paneRect struct {
	X, Y, W, H float64
}

// layoutPanes walks the tree and assigns each leaf pane a screen rectangle.
func (a *App) layoutPanes() map[string]paneRect {
	src := pane.Layout(a.tree, pane.Rect{X: 0, Y: 0, W: a.width, H: a.height})
	out := make(map[string]paneRect, len(src))
	for id, r := range src {
		out[id] = paneRect{X: r.X, Y: r.Y, W: r.W, H: r.H}
	}
	return out
}

// drawPane draws one pane's contents.
//
// The pane chrome (border, grid lines, + button) is always drawn, even when
// the target grid hasn't loaded yet. That way the user can see the pane is
// live and use the + button (or Home key, etc.) to recover from a stale
// descent path.
func (a *App) drawPane(p *pane.Pane, r paneRect) {
	a.previewPaneID = p.ID
	a.previewPaneRect = r
	defer func() {
		a.previewPaneID = ""
		a.previewPaneRect = paneRect{}
	}()
	gid := a.gridIDForPath(p.Path)
	g, gridOK := a.c.Grid(gid)

	// Clip content to the inside of the border. The border itself is
	// painted on top at the end of this function so it always frames
	// the content cleanly, even if a node or markdown text would
	// otherwise paint over the edge. At root (no border) the inset is
	// 0 so content fills the full pane.
	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	inset := 0.0
	if len(p.Path) > 0 || p.FileFocus != 0 {
		inset = paneBorderPx
	}
	a.cctx.Call("rect", r.X+inset, r.Y+inset, r.W-2*inset, r.H-2*inset)
	a.cctx.Call("clip")

	pscreen := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}

	// Grid lines render against background regardless of whether the grid
	// has loaded — they communicate the coordinate system to the user.
	// In live file mode the grid lines reflect FileZoom centered on the
	// pane (the outer ring is the visible "grid" cue), not the parent's
	// grid coordinates.
	if p.FileFocus != 0 {
		fz := p.FileZoom
		if fz <= 0 {
			fz = 1.0
		}
		fcell := cellPx * fz
		fox := r.X + r.W/2
		foy := r.Y + r.H/2
		drawGridLinesIn(a.cctx, r.X, r.Y, r.W, r.H, fcell, fox, foy)
	} else {
		a.drawGridLines(pscreen, r)
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
		if p.FileFocus != 0 {
			if file, ok := g.Tiles[p.FileFocus]; ok {
				switch {
				case file.Type == "file" && file.MimeType == "text/markdown":
					ix, iy, iw, ih := fileInnerBox(p, r)
					a.cctx.Set("fillStyle", colorFileInnerBg)
					a.cctx.Call("fillRect", ix, iy, iw, ih)
					a.drawMarkdownInPane(p, &file, ix, iy, iw, ih)
				case file.IsURL():
					ix, iy, iw, ih := paneContentBox(r)
					a.drawURLTileInPane(p, &file, ix, iy, iw, ih)
				default:
					ix, iy, iw, ih := fileInnerBox(p, r)
					a.cctx.Set("fillStyle", colorFileInnerBg)
					a.cctx.Call("fillRect", ix, iy, iw, ih)
				}
			}
		} else {
			for _, n := range g.Tiles {
				if a.hiddenObjectID != "" && a.hiddenPaneID == p.ID && n.ObjectID == a.hiddenObjectID {
					continue
				}
				left, top := pscreen.CellToScreen(float64(n.X), float64(n.Y))
				w := float64(n.W) * cellSize
				h := float64(n.H) * cellSize
				if left+w < r.X || top+h < r.Y || left > r.X+r.W || top > r.Y+r.H {
					continue
				}
				nn := n
				a.drawNodeWithPreview(&nn, left, top, w, h, cellSize, r, n.ID == selected)
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
	// bleeding visibly into the chrome. Suppressed at the user's root
	// (no path, no file focus) — the absence is the cue that there's
	// nothing to ascend to.
	if len(p.Path) > 0 || p.FileFocus != 0 {
		border := colorPaneBorder
		if p.ID == a.tree.Focus {
			border = colorFocusBorder
		}
		a.cctx.Set("strokeStyle", border)
		a.cctx.Set("lineWidth", paneBorderPx)
		half := paneBorderPx / 2
		a.cctx.Call("strokeRect", r.X+half, r.Y+half, r.W-paneBorderPx, r.H-paneBorderPx)
	}

	// In file-focus mode replace the + with a text/rendered toggle; the
	// menu never opens here. URL tiles have no text/rendered modes, so
	// no toggle button — they're driven by right-click instead.
	if p.FileFocus != 0 {
		if !a.isURLDescent(p) {
			a.drawFileToggleButton(p, r)
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

// drawFileToggleButton paints the lower-right button that switches a
// file-focused pane between "text" (raw editable source) and "rendered"
// (canvas markdown layout). Visually mimics the + button so the position
// and feel are familiar; the glyph hints at the *target* mode using
// font shape: a typeset serif "A" means clicking renders, a fixed-width
// monospace "A" means clicking edits as source.
func (a *App) drawFileToggleButton(p *pane.Pane, r paneRect) {
	cx, cy := plusButtonCenter(r)
	a.cctx.Set("fillStyle", colorPlusBg)
	a.cctx.Call("beginPath")
	a.cctx.Call("arc", cx, cy, float64(plusButtonRadius), 0, 2*math.Pi)
	a.cctx.Call("fill")
	a.cctx.Set("strokeStyle", colorPaneBorder)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("stroke")

	// In text mode, the click goes to rendered → show a serif glyph.
	// In rendered mode, the click goes to text → show a monospace glyph.
	font := `italic 18px ui-serif, "Times New Roman", Georgia, serif`
	if p.FileMode == "rendered" {
		font = `18px ui-monospace, "SF Mono", Menlo, Consolas, monospace`
	}
	a.cctx.Set("fillStyle", colorPlusFg)
	a.cctx.Set("font", font)
	a.cctx.Set("textBaseline", "middle")
	a.cctx.Set("textAlign", "center")
	a.cctx.Call("fillText", "a", cx, cy+1)
	a.cctx.Set("textAlign", "start")
	a.cctx.Set("textBaseline", "alphabetic")
}

// drawGridLines paints faint lines at integer cell boundaries within the
// pane's visible region. Lines fade to invisible when cells are tiny so
// extreme zoom-out doesn't paint a solid grey wash.
func (a *App) drawGridLines(ps dragdrop.Pane, r paneRect) {
	cellSize := ps.CellPx * ps.Zoom
	originX, originY := ps.CellToScreen(0, 0)
	drawGridLinesIn(a.cctx, r.X, r.Y, r.W, r.H, cellSize, originX, originY)
}

// drawGridLinesIn paints faint vertical/horizontal grid lines clipped to
// (clipX, clipY, clipW, clipH), spaced at cellSize pixels and aligned so
// integer cell (0, 0) lands at (originX, originY). Used for both the
// parent grid and well interiors so the visual scale of a well's preview
// is the same kind of grid the user is already seeing.
//
// Sub-4px cell sizes draw nothing (the lines would be a solid wash).
// Above that, opacity fades up linearly so zoom-in feels like the grid
// "fades in" rather than appearing abruptly.
func drawGridLinesIn(c js.Value, clipX, clipY, clipW, clipH, cellSize, originX, originY float64) {
	if cellSize < 4 {
		return
	}
	alpha := (cellSize - 4) / 20
	if alpha > 0.6 {
		alpha = 0.6
	}
	if alpha < 0.05 {
		return
	}
	c.Set("strokeStyle", colorGridLine)
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
func (a *App) drawNodeWithPreview(n *rpc.Tile, x, y, w, h, parentCellSize float64, r paneRect, selected bool) {
	if n.Type == "file" && n.MimeType == "text/markdown" {
		a.drawMarkdownNode(n, x, y, w, h, parentCellSize, r, selected)
		return
	}
	if n.IsURL() {
		a.drawURLTile(n, x, y, w, h, selected)
		return
	}
	if n.IsBlackHole() {
		drawBlackHoleSwatch(a.cctx, x, y, w, h)
		if selected {
			a.cctx.Set("strokeStyle", colorSelected)
			a.cctx.Set("lineWidth", 2.0)
			a.cctx.Call("strokeRect", x-1, y-1, w+2, h+2)
			a.cctx.Set("lineWidth", 1.0)
		}
		return
	}
	if n.Type != "well" {
		drawNode(a.cctx, n, x, y, w, h, selected)
		return
	}
	// Trigger prefetch if we don't have the child grid yet.
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
	drawGridLinesIn(a.cctx, x, y, w, h, previewCell, originX, originY)

	if haveChild && previewCell >= 0.5 {
		hide := ""
		if a.hiddenObjectID != "" && a.hiddenPaneID == a.previewPaneID {
			hide = a.hiddenObjectID
		}
		drawChildPreview(a.cctx, child, viewCenterX, viewCenterY,
			wellCenterX, wellCenterY, previewCell, x, y, w, h, hide)
	}
	a.cctx.Call("restore")

	// Outline: bright blue, matching the focused-pane color.
	a.cctx.Set("strokeStyle", colorFocusBorder)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("strokeRect", x, y, w, h)
	if selected {
		a.cctx.Set("strokeStyle", colorSelected)
		a.cctx.Set("lineWidth", 2.0)
		a.cctx.Call("strokeRect", x-1, y-1, w+2, h+2)
		a.cctx.Set("lineWidth", 1.0)
	}
}

// drawMarkdownInPane renders a markdown file as the contents of the
// pane that is currently descended into it. Uses *that* pane's live
// FileMode / FileZoom / FileScroll values, so two split panes both
// looking at the same file can each scroll/zoom independently.
//
// Also responsible for skipping the canvas render in text mode for
// the focused pane (the textarea overlay handles that one). Other
// panes still render the source as canvas text so the user has
// continuity in non-focused split siblings.
func (a *App) drawMarkdownInPane(p *pane.Pane, n *rpc.Tile, x, y, w, h float64) {
	mode := p.FileMode
	if mode == "" {
		mode = "rendered"
	}
	scale := p.FileZoom
	if scale <= 0 {
		scale = 1.0
	}
	scrollX := p.FileScrollX
	scrollY := p.FileScrollY

	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	hideForTextarea := mode == "text" && p.ID == a.tree.Focus
	if !hideForTextarea {
		if blob, ok := a.c.Blob(n.BlobID); ok {
			drawMarkdownInRect(a.cctx, string(blob.Data),
				x-scrollX*scale, y-scrollY*scale,
				fileNaturalContentPx*scale, h+scrollY*scale,
				scale, 0, mode)
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
//  2. Live file mode (a pane is descended into this file). The scale
//     is the pane's FileZoom (independent of parent zoom) and the
//     visible region is taken from FileScrollX/FileScrollY. Wheel
//     events update those fields directly so navigation is buttery.
//
// In both modes the parent grid lines remain visible behind the text
// (no fill), and an outline marks the footprint.
func (a *App) drawMarkdownNode(n *rpc.Tile, x, y, w, h, parentCellSize float64, r paneRect, selected bool) {
	mode := "text"
	if last, ok := a.fileLastMode[n.ID]; ok && last != "" {
		mode = last
	}
	fp := a.paneFocusedOnFile(n.ID)
	var scale, scrollX, scrollY float64
	if fp != nil {
		if fp.FileMode != "" {
			mode = fp.FileMode
		}
		scale = fp.FileZoom
		if scale <= 0 {
			scale = 1.0
		}
		scrollX = fp.FileScrollX
		scrollY = fp.FileScrollY
	} else {
		// Preview scale = parentZoom × effectiveRatio, where the
		// effective ratio is the file's stored ViewZoom (or the
		// unvisited-file default substituted by fileEffectiveRatio).
		// At parent = fileOvertake_now this equals the live FileZoom
		// computed by fileLiveZoom, so the path swap is visually
		// continuous for visited AND unvisited files.
		parentZoom := parentCellSize / cellPx
		ratio := fileEffectiveRatio(r, n.W, n.H, n.ViewZoom)
		previewScale := parentZoom * ratio
		if previewScale < 0.05 {
			previewScale = 0.05
		}
		scale = previewScale
		scrollY = float64(n.ViewY)
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
	hideForTextarea := fp != nil && fp.FileMode == "text" && fp.ID == a.tree.Focus
	if !hideForTextarea {
		if blob, ok := a.c.Blob(n.BlobID); ok {
			// Layout width is fixed at the natural content width so
			// every preview wraps lines the same way live mode does —
			// principle of continuity across the path swap. Text that
			// would extend past the cell to the right (because live
			// inner-box is wider than tall) gets clipped at the cell
			// edge — that's the "start from the left" behavior the
			// user asked for.
			drawMarkdownInRect(a.cctx, string(blob.Data),
				x-scrollX*scale, y-scrollY*scale,
				fileNaturalContentPx*scale, h+scrollY*scale,
				scale, 0, mode)
		} else if n.BlobID != 0 {
			a.fetchBlob(n.BlobID)
		}
	} else if _, ok := a.c.Blob(n.BlobID); !ok && n.BlobID != 0 {
		a.fetchBlob(n.BlobID)
	}

	a.cctx.Call("restore")
	_ = parentCellSize

	// Outline: same palette as flat text-file fill so identity-by-color is
	// preserved. Selected nodes get the gold outline on top.
	a.cctx.Set("strokeStyle", colorTextLine)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("strokeRect", x, y, w, h)
	if selected {
		a.cctx.Set("strokeStyle", colorSelected)
		a.cctx.Set("lineWidth", 2.0)
		a.cctx.Call("strokeRect", x-1, y-1, w+2, h+2)
		a.cctx.Set("lineWidth", 1.0)
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
func drawMarkdownInRect(c js.Value, src string, x, y, w, h, scale, scrollY float64, mode string) {
	if mode == "text" {
		drawMarkdownText(c, src, x, y, w, h, scale, scrollY)
		return
	}
	drawMarkdownRendered(c, src, x, y, w, h, scale, scrollY)
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
func drawMarkdownRendered(c js.Value, src string, x, y, w, h, scale, scrollY float64) {
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
			drawInlineLines(c, lines, x+(st.pad+indent)*scale, y, cursorY, lineHeight, fontPx, family, scale, h)
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
			drawInlineLines(c, lines, x+(st.pad+indent)*scale, y, cursorY, lineHeight, fontPx, family, scale, h)
			cursorY += lineHeight*float64(len(lines)) + st.gapAfter
			continue
		}
		// Headings and paragraphs.
		bold := b.Kind == markdown.BlockHeading1 || b.Kind == markdown.BlockHeading2 || b.Kind == markdown.BlockHeading3
		_ = bold
		lines := wrapInline(c, b.Spans, contentWidthLogical, fontPx, family, scale)
		drawInlineLines(c, lines, x+st.pad*scale, y, cursorY, lineHeight, fontPx, family, scale, h)
		cursorY += lineHeight*float64(len(lines)) + st.gapAfter
	}
}

// wrapInline measures the spans and wraps them into lines that fit
// contentWidthLogical at the given font size. The font is set per-call so
// measureText reflects the right metrics; scale is applied uniformly so
// the wrap matches what the caller will paint.
func wrapInline(c js.Value, spans []markdown.Span, contentWidthLogical, fontPx float64, family string, scale float64) [][]markdown.Span {
	measure := func(text string, style markdown.SpanStyle) float64 {
		setFont(c, fontPx*scale, family, style&markdown.StyleBold != 0, style&markdown.StyleItalic != 0)
		mt := c.Call("measureText", text)
		// Convert measured pixels back to logical units.
		return mt.Get("width").Float() / scale
	}
	return markdown.Wrap(spans, contentWidthLogical, measure)
}

// drawInlineLines paints wrapped inline lines starting at logical
// (xPx, baseTopLogical) with the given lineHeight; clips drawing to (y, h)
// in screen pixels.
func drawInlineLines(c js.Value, lines [][]markdown.Span, xPx, yBase, baseTopLogical, lineHeight, fontPx float64, family string, scale, h float64) {
	for li, line := range lines {
		yLogical := baseTopLogical + float64(li)*lineHeight
		yPx := yLogical * scale
		if yPx+lineHeight*scale < 0 || yPx > h {
			continue
		}
		curX := xPx
		for _, sp := range line {
			bold := sp.Style&markdown.StyleBold != 0
			italic := sp.Style&markdown.StyleItalic != 0
			code := sp.Style&markdown.StyleCode != 0
			family2 := family
			if code {
				family2 = `ui-monospace, "SF Mono", Menlo, Consolas, monospace`
			}
			setFont(c, fontPx*scale, family2, bold, italic)
			if code {
				c.Set("fillStyle", "#3a4658")
				w := c.Call("measureText", sp.Text).Get("width").Float()
				c.Call("fillRect", curX-2, yBase+yPx, w+4, lineHeight*scale)
			}
			c.Set("fillStyle", "#d8d9de")
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

// drawMarkdownText paints `src` as raw monospace text at the same scale
// the rendered view would use. Used both for the source-mode preview and
// as a faint backdrop while the textarea overlay is painted on top.
//
// `w` is unused for now: text mode does not soft-wrap; long lines are
// clipped at the right edge by the caller's clip rect.
func drawMarkdownText(c js.Value, src string, x, y, w, h, scale, scrollY float64) {
	_ = w
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
		if p.FileFocus == fileTileID {
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
		var resp rpc.GetBlobResponse
		status, err := postJSON("/rpc/GetBlob", rpc.GetBlobRequest{BlobID: blobID}, &resp)
		if err != nil || status != 200 {
			return
		}
		a.c.PutBlob(blobID, resp.Data, resp.MimeType)
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
// hiddenObjectID, if non-empty, suppresses any child tile with that
// ObjectID — used so that a tile being dragged out of a well preview
// disappears from its original spot while the ghost flies.
func drawChildPreview(c js.Value, child *cache.Grid,
	centerCellX, centerCellY, centerScreenX, centerScreenY, previewCell float64,
	clipX, clipY, clipW, clipH float64,
	hiddenObjectID string,
) {
	for _, n := range child.Tiles {
		if hiddenObjectID != "" && n.ObjectID == hiddenObjectID {
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
		drawNode(c, &nn, nodeScreenX, nodeScreenY, nodeScreenW, nodeScreenH, false)
	}
}

// drawNode renders one tile into the canvas at the given screen rectangle.
// `selected` highlights the tile with a dedicated outline color. This is
// the "flat" renderer used for nested previews (no recursion) and for
// non-well tiles; the parent-grid renderer is drawNodeWithPreview.
func drawNode(c js.Value, n *rpc.Tile, x, y, w, h float64, selected bool) {
	switch {
	case n.Type == "well":
		c.Set("fillStyle", colorBg)
		c.Call("fillRect", x, y, w, h)
		c.Set("strokeStyle", colorFocusBorder)
		c.Set("lineWidth", 1.0)
		c.Call("strokeRect", x, y, w, h)
	case n.IsBlackHole():
		drawBlackHoleSwatch(c, x, y, w, h)
	case n.Type == "file" && strings.HasPrefix(n.MimeType, "text/"):
		c.Set("fillStyle", colorTextFill)
		c.Call("fillRect", x, y, w, h)
		c.Set("strokeStyle", colorTextLine)
		c.Call("strokeRect", x, y, w, h)
	case n.Type == "file" && strings.HasPrefix(n.MimeType, "image/"):
		c.Set("fillStyle", colorImageFill)
		c.Call("fillRect", x, y, w, h)
		c.Set("strokeStyle", colorImageLine)
		c.Call("strokeRect", x, y, w, h)
	default:
		c.Set("fillStyle", colorLocked)
		c.Call("fillRect", x, y, w, h)
	}
	if selected {
		c.Set("strokeStyle", colorSelected)
		c.Set("lineWidth", 2.0)
		c.Call("strokeRect", x-1, y-1, w+2, h+2)
		c.Set("lineWidth", 1.0)
	}
}

// drawGhostTile renders the active drag ghost. When fragmentation is
// near zero this is just drawNodeWithPreview. When the cursor enters a
// black hole, fragmentation animates toward 1 and the ghost splits into
// four corner shards that drift outward, with reduced alpha, while
// continuing to shrink via the size lerp. Drag back out and frag
// animates back to 0 — shards return to their seats, alpha recovers.
func (a *App) drawGhostTile(n *rpc.Tile, x, y, w, h, parentCellSize float64, r paneRect, frag float64) {
	if frag < 0.02 {
		a.drawNodeWithPreview(n, x, y, w, h, parentCellSize, r, false)
		return
	}
	if frag > 1 {
		frag = 1
	}
	// Alpha fades to ~0.35 at full fragmentation so the user sees the
	// tile dissolving without it vanishing before release.
	alpha := 1.0 - 0.65*frag
	a.cctx.Set("globalAlpha", alpha)
	// Drift each corner outward by up to ~35% of the (already-shrunk)
	// tile size. The shards' own size shrinks slightly with frag too so
	// they look like they're being pulled apart, not just floating.
	drift := 0.35 * frag * math.Max(w, h)
	shardScale := 0.5 - 0.1*frag
	sw := w * shardScale
	sh := h * shardScale
	type quad struct{ dx, dy float64 }
	quads := []quad{
		{-1, -1}, {1, -1}, {-1, 1}, {1, 1},
	}
	cx := x + w/2
	cy := y + h/2
	for _, q := range quads {
		qx := cx + q.dx*drift - sw/2
		qy := cy + q.dy*drift - sh/2
		a.cctx.Call("save")
		a.cctx.Call("beginPath")
		a.cctx.Call("rect", qx, qy, sw, sh)
		a.cctx.Call("clip")
		// Draw the whole shrunk tile so the visible quadrant lines up
		// with that corner of the source — gives the "broken apart"
		// look without per-shard content carving.
		shardCellSize := parentCellSize * shardScale
		fullW := float64(n.W) * shardCellSize
		fullH := float64(n.H) * shardCellSize
		shardTileX := qx - (q.dx+1)/2*fullW + sw/2 + q.dx*sw/2
		shardTileY := qy - (q.dy+1)/2*fullH + sh/2 + q.dy*sh/2
		_ = shardTileX
		_ = shardTileY
		// Simple version: render a tinted square per shard. The
		// alpha + drift sells the effect without needing the source
		// tile content per-quadrant (which would require recursive
		// clipping the well preview, etc.).
		a.cctx.Set("fillStyle", colorFileInnerBg)
		a.cctx.Call("fillRect", qx, qy, sw, sh)
		a.cctx.Set("strokeStyle", colorMuted)
		a.cctx.Set("lineWidth", 1.0)
		a.cctx.Call("strokeRect", qx+0.5, qy+0.5, sw-1, sh-1)
		a.cctx.Call("restore")
	}
	a.cctx.Set("globalAlpha", 1.0)
}

// drawBlackHoleSwatch paints the canonical black-hole tile visual into
// (x, y, w, h): a pure-black fill with a few concentric grey rings to
// suggest event horizons. Used both for palette icon and for live
// tiles in a grid. No outline — the rings imply the boundary.
func drawBlackHoleSwatch(c js.Value, x, y, w, h float64) {
	c.Set("fillStyle", "#000000")
	c.Call("fillRect", x, y, w, h)
	cx := x + w/2
	cy := y + h/2
	rMax := math.Min(w, h) / 2
	if rMax <= 0 {
		return
	}
	c.Set("lineWidth", 1.0)
	for i, frac := range []float64{0.85, 0.6, 0.35} {
		r := rMax * frac
		shade := []string{"#1f2229", "#2a2f3a", "#3a4b5a"}[i]
		c.Set("strokeStyle", shade)
		c.Call("beginPath")
		c.Call("arc", cx, cy, r, 0.0, 2*math.Pi)
		c.Call("stroke")
	}
}

// drawEdgeIndicators paints small markers along the pane's inner edge for
// every tile whose footprint lies entirely outside the visible viewport.
// Each marker is positioned where the ray from the viewport center to the
// tile's center crosses the pane's inset rectangle, and points outward.
func (a *App) drawEdgeIndicators(nodes map[int64]rpc.Tile, ps dragdrop.Pane, r paneRect) {
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

// plusButtonCenter returns the screen-space center of the + button for a
// given pane.
func plusButtonCenter(r paneRect) (float64, float64) {
	return r.X + r.W - plusButtonInset, r.Y + r.H - plusButtonInset
}

// pointInPlus reports whether (x, y) lies within the + button for the given
// pane rect.
func pointInPlus(r paneRect, x, y float64) bool {
	cx, cy := plusButtonCenter(r)
	dx := x - cx
	dy := y - cy
	return dx*dx+dy*dy <= plusButtonRadius*plusButtonRadius
}

// drawPlusButton paints the floating circular + button in the pane's lower
// right.
func (a *App) drawPlusButton(p *pane.Pane, r paneRect) {
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

// paletteTilePx returns the per-tile size in screen pixels for the
// palette over pane p, clamped to [paletteMinTilePx, paletteMaxTilePx].
// Tracks the pane's current zoom so the palette tile previews roughly
// the size of the placed tile, while staying usable at extreme zoom.
func paletteTilePx(p *pane.Pane) float64 {
	z := p.Zoom
	if z <= 0 {
		z = 1.0
	}
	t := cellPx * z
	if t < paletteMinTilePx {
		t = paletteMinTilePx
	}
	if t > paletteMaxTilePx {
		t = paletteMaxTilePx
	}
	return t
}

// paletteRect returns the screen-space rectangle the creation palette
// occupies for a given pane. The popover sits just above the + button,
// anchored bottom-right (matching the pre-palette menu).
func paletteRect(p *pane.Pane, r paneRect) (x, y, w, h float64) {
	tile := paletteTilePx(p)
	w = float64(len(templateKinds))*tile + float64(len(templateKinds)+1)*paletteGapPx
	h = tile + 2*paletteGapPx
	cx, cy := plusButtonCenter(r)
	x = cx + plusButtonRadius - w
	y = cy - plusButtonRadius - h - 8
	return
}

// paletteTileRect returns the screen rect for the i'th template tile
// inside the palette popover.
func paletteTileRect(p *pane.Pane, r paneRect, i int) (x, y, w, h float64) {
	px, py, _, _ := paletteRect(p, r)
	tile := paletteTilePx(p)
	x = px + paletteGapPx + float64(i)*(tile+paletteGapPx)
	y = py + paletteGapPx
	w = tile
	h = tile
	return
}

// drawPalette paints the creation popover: a background container and
// a horizontal row of preview tiles, one per templateKind.
func (a *App) drawPalette(p *pane.Pane, r paneRect) {
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

// drawPaletteTile renders one preview tile inside the palette. Each
// kind paints to roughly match what the user will get when they drop:
// well = empty well outline, markdown = monospaced text, url = "url"
// label, upload = arrow glyph.
func (a *App) drawPaletteTile(kind templateKind, x, y, w, h float64, hovered bool) {
	switch kind {
	case tplWell:
		a.cctx.Set("fillStyle", colorBg)
		a.cctx.Call("fillRect", x, y, w, h)
		a.cctx.Set("strokeStyle", colorFocusBorder)
		a.cctx.Set("lineWidth", 1.0)
		a.cctx.Call("strokeRect", x, y, w, h)
	case tplMarkdown:
		a.cctx.Set("fillStyle", colorTextFill)
		a.cctx.Call("fillRect", x, y, w, h)
		a.cctx.Set("strokeStyle", colorTextLine)
		a.cctx.Call("strokeRect", x, y, w, h)
		a.cctx.Set("fillStyle", colorMenuItemHi)
		fontPx := w * 0.18
		if fontPx < 9 {
			fontPx = 9
		}
		if fontPx > 16 {
			fontPx = 16
		}
		a.cctx.Set("font", strconv.FormatFloat(fontPx, 'f', 1, 64)+"px ui-monospace")
		// A few short lines mimicking text content.
		a.cctx.Call("fillText", "aa", x+w*0.18, y+h*0.35)
		a.cctx.Call("fillText", "bb", x+w*0.18, y+h*0.55)
		a.cctx.Call("fillText", "cc", x+w*0.18, y+h*0.75)
	case tplURL:
		a.cctx.Set("fillStyle", colorTextFill)
		a.cctx.Call("fillRect", x, y, w, h)
		a.cctx.Set("strokeStyle", colorTextLine)
		a.cctx.Call("strokeRect", x, y, w, h)
		a.cctx.Set("fillStyle", colorMenuItemHi)
		fontPx := w * 0.22
		if fontPx < 11 {
			fontPx = 11
		}
		if fontPx > 22 {
			fontPx = 22
		}
		a.cctx.Set("font", strconv.FormatFloat(fontPx, 'f', 1, 64)+"px ui-sans-serif")
		a.cctx.Call("fillText", "url", x+w*0.18, y+h*0.6)
	case tplUpload:
		a.cctx.Set("fillStyle", colorImageFill)
		a.cctx.Call("fillRect", x, y, w, h)
		a.cctx.Set("strokeStyle", colorImageLine)
		a.cctx.Call("strokeRect", x, y, w, h)
		// Up arrow centered in the tile.
		a.cctx.Set("strokeStyle", colorMenuItemHi)
		a.cctx.Set("lineWidth", 2.0)
		cx := x + w/2
		cy := y + h/2
		armLen := w * 0.22
		a.cctx.Call("beginPath")
		a.cctx.Call("moveTo", cx, cy+armLen)
		a.cctx.Call("lineTo", cx, cy-armLen)
		a.cctx.Call("moveTo", cx-armLen*0.6, cy-armLen*0.4)
		a.cctx.Call("lineTo", cx, cy-armLen)
		a.cctx.Call("lineTo", cx+armLen*0.6, cy-armLen*0.4)
		a.cctx.Call("stroke")
		a.cctx.Set("lineWidth", 1.0)
	case tplBlackHole:
		drawBlackHoleSwatch(a.cctx, x, y, w, h)
	}
	if hovered {
		a.cctx.Set("strokeStyle", colorSelected)
		a.cctx.Set("lineWidth", 2.0)
		a.cctx.Call("strokeRect", x-1, y-1, w+2, h+2)
		a.cctx.Set("lineWidth", 1.0)
	}
}

// paletteTileIndexAt returns the index of the palette tile at (x, y),
// or -1 if outside any tile (still inside palette gutter, or outside
// the popover entirely).
func paletteTileIndexAt(p *pane.Pane, r paneRect, x, y float64) int {
	for i := range templateKinds {
		tx, ty, tw, th := paletteTileRect(p, r, i)
		if x >= tx && x <= tx+tw && y >= ty && y <= ty+th {
			return i
		}
	}
	return -1
}

// pointInPalette reports whether (x, y) lies anywhere inside the
// palette popover (including gutters between tiles). Used to decide
// whether a click outside the tiles should still be "swallowed" by
// the palette (keep it open) vs. dismissing it.
func pointInPalette(p *pane.Pane, r paneRect, x, y float64) bool {
	mx, my, mw, mh := paletteRect(p, r)
	return x >= mx && x <= mx+mw && y >= my && y <= my+mh
}

