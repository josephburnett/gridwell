//go:build js && wasm

package main

import (
	"math"

	"github.com/josephburnett/gridwell/client/palette"
	"github.com/josephburnett/gridwell/client/pane"
)

// This file holds the creation-palette: the screen-space layout adapters
// over the pure client/palette package, the floating "+" button, and the
// popover drawing (one swatch per templateKind, with its identity glyph).

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
// right. During a tile drag whose source is this pane, the same round button
// becomes the delete target: it shows a trashcan instead of a +, and its
// circle goes danger-red while the dragged ghost hovers over it ("release
// here deletes"). The round chrome is identical either way so the position is
// muscle-memory-stable.
func (a *App) drawPlusButton(p *pane.Pane, r pane.Rect) {
	cx, cy := plusButtonCenter(r)
	deleting := a.tileDragInFlight() && a.dragging.originPaneID == p.ID
	hot := deleting && pointInPlus(r, a.dragging.curScreenX, a.dragging.curScreenY)
	bg := colorPlusBg
	switch {
	case hot:
		bg = colorPlusBgDelete
	case a.menuOpen && a.menuPaneID == p.ID:
		bg = colorPlusBgHi
	}
	a.cctx.Set("fillStyle", bg)
	a.cctx.Call("beginPath")
	a.cctx.Call("arc", cx, cy, float64(plusButtonRadius), 0, 2*math.Pi)
	a.cctx.Call("fill")
	a.cctx.Set("strokeStyle", colorPaneBorder)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("stroke")

	if deleting {
		// Trashcan glyph centered in the circle — drop a tile here to delete.
		side := float64(plusButtonRadius) * 1.4
		drawTrashcanIcon(a.cctx, cx-side/2, cy-side/2, side, side)
		return
	}

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
// grammar, same "/dev/null"/"files"/"processes" banner. A kind-specific
// glyph is overlaid on tile kinds where the live tile lacks a static
// identity cue (markdown / url / file-well / process-well), so the
// palette still reads "what is this?" before the tile has content.
func (a *App) drawPaletteTile(kind templateKind, x, y, w, h float64, hovered bool) {
	n := templateGhostNode(kind)
	outside := tileOutside(&n, false)
	drawNode(a.cctx, &n, x, y, w, h, false, outside, tileBorderPx)
	// No banner in the palette — the glyph alone identifies the kind. (Placed
	// exit tiles still carry their "/dev/null" / "files" / … label.) One
	// consistent line glyph per kind, in that kind's own color, sized to
	// mostly fill the swatch.
	switch kind {
	case tplWell:
		drawWellGlyph(a.cctx, x, y, w, h, colorFocusBorder)
	case tplMarkdown:
		drawDocumentGlyph(a.cctx, x, y, w, h, colorMarkdownLine)
	case tplURL:
		drawGlobeGlyph(a.cctx, x, y, w, h, colorURLLine)
	case tplFileWell:
		drawFolderGlyph(a.cctx, x, y, w, h, colorExitBorder)
	case tplProcessWell:
		drawProcessGlyph(a.cctx, x, y, w, h, colorExitBorder)
	case tplShell:
		drawShellGlyph(a.cctx, x, y, w, h, colorExitBorder)
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
