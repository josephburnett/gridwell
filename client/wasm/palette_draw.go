//go:build js && wasm

package main

import (
	"math"

	"github.com/josephburnett/gridwell/client/palette"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/pluginhealth"
	"github.com/josephburnett/gridwell/client/wsbar"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// This file holds the creation-palette: the screen-space layout adapters
// over the pure client/palette package, the floating "+" button, and the
// popover drawing (one swatch per templateKind, with its identity glyph).

// paletteLayoutFor builds the pure-go palette.Layout snapshot for a
// given pane. The palette package owns the geometry; wasm only has to
// pour the inputs in.
func (a *App) paletteLayoutFor(p *pane.Pane) palette.Layout {
	cx, cy := a.plusButtonCenter()
	return palette.Layout{
		Cfg:      palette.Default(),
		PlusX:    cx,
		PlusY:    cy,
		NumTiles: len(a.paletteItems(p)),
		// Plugins fill the top row; the primitives (if any) drop to a second
		// row below. paletteItems always lists the plugins first.
		TopRow: len(a.plugins),
	}
}

// plusButtonCenter returns the screen-space center of the circle button:
// the right end of the ACTIVE pane's bar band (issues #214/#220). Falls
// back to the window corner only when no pane is focused (boot frame).
func (a *App) plusButtonCenter() (float64, float64) {
	bx, top, bw, ok := a.bottomBarRect()
	if !ok {
		return a.width - wsbar.SlotW/2, a.height - wsbar.RowH/2
	}
	return bx + bw - wsbar.SlotW/2, top + wsbar.RowH/2
}

// pointInPlus reports whether (x, y) lies within the circle button.
func (a *App) pointInPlus(x, y float64) bool {
	cx, cy := a.plusButtonCenter()
	dx, dy := x-cx, y-cy
	rr := palette.Default().PlusRadius
	return dx*dx+dy*dy <= rr*rr
}

// drawPlusButton paints the circular + button in the bar's slot. During a
// tile drag, the same round button becomes the delete target: it shows a
// trashcan instead of a +, and its circle goes danger-red while the dragged
// ghost hovers over it ("release here deletes"). The round chrome is
// identical either way so the position is muscle-memory-stable.
func (a *App) drawPlusButton(p *pane.Pane) {
	cx, cy := a.plusButtonCenter()
	deleting := a.tileDragInFlight()
	hot := deleting && a.pointInPlus(a.dragging.curScreenX, a.dragging.curScreenY)
	// The pane-tile teal (2026-07-30 tweak): the slot reads as part of the
	// bar's family, standing out from the dark band instead of graying into
	// its corner. Open menu brightens it; the hot trashcan goes danger-red.
	bg := colorPaneTileBorder
	switch {
	case hot:
		bg = colorPlusBgDelete
	case a.menu.OpenOn(p.ID):
		bg = "#5ecfcf"
	}
	a.cctx.Set("fillStyle", bg)
	a.cctx.Call("beginPath")
	a.cctx.Call("arc", cx, cy, float64(plusButtonRadius), 0, 2*math.Pi)
	a.cctx.Call("fill")
	a.cctx.Set("strokeStyle", "#dff4f4")
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("stroke")

	if deleting {
		// Trashcan glyph centered in the circle — drop a tile here to delete.
		side := float64(plusButtonRadius) * 1.4
		drawTrashcanIcon(a.cctx, cx-side/2, cy-side/2, side, side)
		return
	}

	// Plus glyph: two strokes through center, dark on the teal face.
	a.cctx.Set("strokeStyle", colorPaneTileFill)
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
func (a *App) paletteRect(p *pane.Pane) (x, y, w, h float64) {
	pop := a.paletteLayoutFor(p).PopoverRect()
	return pop.X, pop.Y, pop.W, pop.H
}

// paletteTileRect is the wasm-side adapter for palette.Layout.TileRect.
func (a *App) paletteTileRect(p *pane.Pane, i int) (x, y, w, h float64) {
	tr := a.paletteLayoutFor(p).TileRect(i)
	return tr.X, tr.Y, tr.W, tr.H
}

// drawPalette paints the creation popover: a background container and
// a row of preview tiles per palette item group — plugins on top, then
// the tile primitives in writable grids.
func (a *App) drawPalette(p *pane.Pane) {
	mx, my, mw, mh := a.paletteRect(p)
	a.cctx.Set("fillStyle", colorMenuBg)
	a.cctx.Call("fillRect", mx, my, mw, mh)
	a.cctx.Set("strokeStyle", colorPaneBorder)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("strokeRect", mx+0.5, my+0.5, mw-1, mh-1)
	for i, item := range a.paletteItems(p) {
		tx, ty, tw, th := a.paletteTileRect(p, i)
		hovered := a.menu.Hover() == i
		a.drawPaletteItem(item, tx, ty, tw, th, hovered)
	}
}

// drawPaletteItem renders one preview tile inside the palette. The body
// (fill + border) is shared with the live-tile renderer so a palette swatch
// reads identical to what the user drops — same color grammar. A
// kind-specific glyph is overlaid so the swatch reads "what is this?" before
// the tile has content.
func (a *App) drawPaletteItem(item paletteItem, x, y, w, h float64, hovered bool) {
	n := paletteItemGhostNode(item)
	if item.isPlugin {
		// Plugin swatch == the linked well it drops into a grid: a blue,
		// DASHED-bordered well (dashed = a cross-plugin link you can unlink),
		// its kind glyph, and its name banner (AltText = the server.yaml
		// label). Drawn identically here, as the drag ghost, and once dropped.
		a.cctx.Set("fillStyle", colorBg)
		a.cctx.Call("fillRect", x, y, w, h)
		setTileDash(a.cctx)
		strokeTileBorder(a.cctx, x, y, w, h, colorFocusBorder, tileBorderPx)
		clearTileDash(a.cctx)
		a.drawPluginGlyph(item.plugin.Kind, x, y, w, h)
		a.drawTileBannerLabel(&n, x, y, w, h, false)
		// Broken/rootless plugins get the same health tint their node-grid
		// tiles do; the click guard explains on click.
		a.drawPluginHealthTint(&n, x, y, w, h)
	} else {
		outside := tileOutside(&n, false)
		drawNode(a.cctx, &n, x, y, w, h, false, outside, tileBorderPx, false)
		switch item.primitive {
		case tplWell:
			drawWellGlyph(a.cctx, x, y, w, h, colorFocusBorder)
		case tplMarkdown:
			drawDocumentGlyph(a.cctx, x, y, w, h, colorMarkdownLine)
		case tplURL:
			drawGlobeGlyph(a.cctx, x, y, w, h, colorURLLine)
		case tplShell:
			drawShellGlyph(a.cctx, x, y, w, h, colorShellBorder)
		case tplPane:
			drawPaneGlyph(a.cctx, x, y, w, h, colorPaneTileBorder)
		}
	}
	if hovered {
		drawSelectedTileOutline(a.cctx, x, y, w, h)
	}
}

// drawPluginGlyph overlays a plugin's identity glyph — all in the grid blue —
// chosen by kind: a folder for fs, a process tree for proc, a well for a
// localdb plugin, and a globe as the generic fallback (e.g. ssh). Full size
// (glyphBox). One drawing shared by the menu swatch, the drag ghost, and a
// cross-plugin well that has no preview yet, so all three read identically.
func (a *App) drawPluginGlyph(kind string, x, y, w, h float64) {
	switch kind {
	case "fs":
		drawFolderGlyph(a.cctx, x, y, w, h, colorFocusBorder)
	case "proc":
		drawProcessGlyph(a.cctx, x, y, w, h, colorFocusBorder)
	case "localdb":
		drawWellGlyph(a.cctx, x, y, w, h, colorFocusBorder)
	default:
		drawGlobeGlyph(a.cctx, x, y, w, h, colorFocusBorder)
	}
}

// drawPluginHealthTint overlays a node-grid plugin tile with its
// pluginhealth-decided tint (drawn atop the normal tile so the glyph/preview
// underneath still reads); an enterable plugin gets no overlay. The decision
// (which status, if any) is pluginhealth.Classify — this function only maps
// that decision to pixels, per charter §5. Only tiles of THIS node's node
// grid have local health; a remote node's plugin tiles surface their state
// through descent errors instead.
func (a *App) drawPluginHealthTint(n *rpc.Tile, x, y, w, h float64) {
	// A LINK with no target is not enterable wherever it lives — a broken or
	// rootless plugin tile on ANY node grid (including a remote one, whose
	// health the local plugin list cannot know). Dim it; the descent guard
	// explains on click.
	if n.Reference && rpc.IsWellKind(n.Kind) && n.ChildGridID == "" {
		color := colorLauncherRootlessTint
		// The LOCAL node grid knows more: broken (Info failed) gets the
		// alarm tint, rootless the softer one.
		if pl, ok := a.pluginByUUID(rpc.LocalOf(n.ID)); ok && pluginhealth.Classify(pl) == pluginhealth.Broken {
			color = colorLauncherBrokenTint
		}
		a.cctx.Set("fillStyle", color)
		a.cctx.Call("fillRect", x, y, w, h)
	}
}

// paletteTileIndexAt is the wasm-side adapter for palette.Layout.TileIndexAt.
func (a *App) paletteTileIndexAt(p *pane.Pane, x, y float64) int {
	return a.paletteLayoutFor(p).TileIndexAt(x, y)
}

// pointInPalette is the wasm-side adapter for palette.Layout.PointInPopover.
func (a *App) pointInPalette(p *pane.Pane, x, y float64) bool {
	return a.paletteLayoutFor(p).PointInPopover(x, y)
}
