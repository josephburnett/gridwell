//go:build js && wasm

package main

import (
	"math"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/client/palette"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/pluginhealth"
	"github.com/josephburnett/gridwell/client/wsbar"
)

// This file holds the creation-palette: the screen-space layout adapters
// over the pure client/palette package, the floating "+" button, and the
// popover drawing (one swatch per templateKind, with its identity glyph).

// paletteLayoutFor builds the pure-go palette.Layout snapshot for a
// given pane. The palette package owns the geometry; wasm only has to
// pour the inputs in.
func (a *App) paletteLayoutFor(p *pane.Pane) palette.Layout {
	l, _ := a.paletteLayoutAndShow(p)
	return l
}

// paletteLayoutAndShow builds the layout together with the section decision
// behind it, for the two callers that draw or hit-test the disclosure strip.
func (a *App) paletteLayoutAndShow(p *pane.Pane) (palette.Layout, palette.Shown) {
	cx, cy := a.plusButtonCenter()
	items, show := a.paletteView(p)
	return palette.Layout{
		Cfg:      palette.Default(),
		PlusX:    cx,
		PlusY:    cy,
		NumTiles: len(items),
		// Plugins fill the top row; the primitives, if any, drop to a second
		// row below. Both counts come from the one item list, so a folded
		// section leaves the top row empty rather than needing a second flag.
		TopRow: paletteTopRow(items),
		Toggle: show.Toggle,
	}, show
}

// plusButtonCenter returns the screen-space center of the circle button in
// the one bar's right-end slot — the position clicks hit-test against and the
// open palette anchors to. There is one slot, wearing the focused pane's
// mode. It falls back to the window corner only when the window is too short
// to hold the band.
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
	// The button wears the pane's family hue, saturated on the subtle dark
	// band so it stands out while still matching the scheme. The hot
	// trashcan goes danger-red; an open menu gets a brighter ring.
	band, button := a.barTheme()
	bg := button
	if hot {
		bg = colorPlusBgDelete
	}
	a.cctx.Set("fillStyle", bg)
	a.cctx.Call("beginPath")
	a.cctx.Call("arc", cx, cy, float64(plusButtonRadius), 0, 2*math.Pi)
	a.cctx.Call("fill")
	a.cctx.Set("strokeStyle", "#dff4f4")
	if a.menu.OpenOn(p.ID) {
		a.cctx.Set("lineWidth", 2.0)
	} else {
		a.cctx.Set("lineWidth", 1.0)
	}
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)

	if deleting {
		// Trashcan glyph centered in the circle — drop a tile here to delete.
		side := float64(plusButtonRadius) * 1.4
		drawTrashcanIcon(a.cctx, cx-side/2, cy-side/2, side, side)
		return
	}

	// Plus glyph: two strokes through center, dark on the family face.
	a.cctx.Set("strokeStyle", band)
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

// drawPalette paints the creation popover: a background container, the
// disclosure strip for the plugin section when there is one, and a row of
// preview tiles per palette item group — plugins on top, then the tile
// primitives in writable grids.
func (a *App) drawPalette(p *pane.Pane) {
	mx, my, mw, mh := a.paletteRect(p)
	a.cctx.Set("fillStyle", colorMenuBg)
	a.cctx.Call("fillRect", mx, my, mw, mh)
	a.cctx.Set("strokeStyle", colorPaneBorder)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("strokeRect", mx+0.5, my+0.5, mw-1, mh-1)
	items, show := a.paletteView(p)
	for i, item := range items {
		tx, ty, tw, th := a.paletteTileRect(p, i)
		hovered := a.menu.Hover() == i
		a.drawPaletteItem(item, tx, ty, tw, th, hovered)
	}
	if show.Toggle {
		a.drawPaletteToggle(a.paletteLayoutFor(p).ToggleRect(), show.Chevron)
	}
}

// drawPaletteToggle paints the plugin section's disclosure strip: a flat band
// with a centered chevron pointing the way the press moves the section — up
// to open it above the primitives, down to fold it back. It is drawn as a
// band, never as a swatch, so nothing about it invites the drag a template
// tile takes.
func (a *App) drawPaletteToggle(r pane.Rect, c palette.Chevron) {
	a.cctx.Set("fillStyle", colorPlusBg)
	a.cctx.Call("fillRect", r.X, r.Y, r.W, r.H)
	// Chevron: two strokes meeting at a point, half the strip's height.
	cx := r.X + r.W/2
	cy := r.Y + r.H/2
	const halfW = 6.0
	// The apex sits above the ends for "up" (canvas y grows downward) and
	// below them for "down".
	dy := -3.0
	if c == palette.ChevronDown {
		dy = -dy
	}
	a.cctx.Set("strokeStyle", colorPlusFg)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("beginPath")
	a.cctx.Call("moveTo", cx-halfW, cy-dy)
	a.cctx.Call("lineTo", cx, cy+dy)
	a.cctx.Call("lineTo", cx+halfW, cy-dy)
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)
}

// drawPaletteItem renders one preview tile inside the palette. The body
// (fill + border) is shared with the live-tile renderer so a palette swatch
// reads identical to what the user drops — same color grammar. A
// kind-specific glyph is overlaid so the swatch reads "what is this?" before
// the tile has content.
func (a *App) drawPaletteItem(item paletteItem, x, y, w, h float64, hovered bool) {
	n := paletteItemGhostNode(item)
	if item.isPlugin {
		// A plugin swatch is the linked well it drops into a grid: a blue,
		// dashed-bordered well (dashed means a cross-plugin link you can
		// unlink), its kind glyph, and its name banner (AltText is the
		// server.yaml label). Drawn identically here, as the drag ghost, and
		// once dropped.
		a.cctx.Set("fillStyle", colorBg)
		a.cctx.Call("fillRect", x, y, w, h)
		strokeTileFrame(a.cctx, x, y, w, h, colorFocusBorder, true /* dashed */, false /* selected */)
		a.drawPluginGlyph(door.RowGlyph(item.plugin), x, y, w, h)
		a.drawTileBannerLabel(&n, x, y, w, h, false)
		// A broken or waiting plugin gets the same health tint its link
		// tiles do; the click guard explains on click.
		a.drawPluginHealthTint(&n, x, y, w, h)
	} else {
		outside := tileOutside(&n, false)
		drawNode(a.cctx, &n, x, y, w, h, false, outside, tileBorderPx, false)
		if pr, ok := primitiveFor(item.primitive); ok {
			pr.glyph(a, x, y, w, h)
		}
	}
	if hovered {
		drawSelectedTileOutline(a.cctx, x, y, w, h)
	}
}

// drawPluginGlyph overlays a declared identity glyph, all in the grid blue.
// This is the name-to-pixels half only: which name a row or a grid wears is
// door.RowGlyph and door.GlyphFor, so the menu swatch and the bar crumb
// cannot default differently. Never a kind switch: the client must not know
// its plugins. Full size (glyphBox). One drawing shared by the menu swatch,
// the drag ghost, and a cross-plugin well with no preview yet, so all three
// read identically.
func (a *App) drawPluginGlyph(glyph string, x, y, w, h float64) {
	switch glyph {
	case rpc.GlyphFolder:
		drawFolderGlyph(a.cctx, x, y, w, h, colorFocusBorder)
	case rpc.GlyphProcess:
		drawProcessGlyph(a.cctx, x, y, w, h, colorFocusBorder)
	case rpc.GlyphWell:
		drawWellGlyph(a.cctx, x, y, w, h, colorFocusBorder)
	case rpc.GlyphTrash:
		drawTrashGlyph(a.cctx, x, y, w, h, colorFocusBorder)
	default:
		// rpc.GlyphGlobe — declared by every connection — and equally any
		// name this client does not know.
		drawGlobeGlyph(a.cctx, x, y, w, h, colorFocusBorder)
	}
}

// drawPluginHealthTint overlays a plugin link tile with its
// pluginhealth-decided tint, drawn atop the normal tile so the glyph or
// preview underneath still reads; an enterable plugin gets no overlay. Which
// status, if any, is pluginhealth.Classify's decision, and this function only
// maps it to pixels. Only this node's own plugins have local health; a remote
// node's plugin tiles surface their state through descent errors instead.
func (a *App) drawPluginHealthTint(n *rpc.Tile, x, y, w, h float64) {
	// A link with no target is not enterable wherever it lives: a broken or
	// waiting plugin link, one to a remote plugin included, whose health
	// the local plugin list cannot know. Dim it; the descent guard explains
	// on click.
	if pluginhealth.UnrootedLink(n) {
		// The neutral dimming is also what a row this node cannot classify
		// gets — a remote plugin's launcher — because not knowing yet is
		// exactly the waiting face.
		color := colorLauncherWaitingTint
		// The local plugin list knows more: a failure of any kind gets the
		// alarm tint, waiting the neutral one.
		if pl, ok := a.pluginByUUID(rpc.LocalOf(n.ID)); ok {
			if pluginhealth.Classify(pl) == pluginhealth.Broken {
				color = colorLauncherBrokenTint
			}
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

// pointInPaletteToggle is the wasm-side adapter for
// palette.Layout.PointInToggle: false whenever the popover has no strip, so
// the press path needs no second guard.
func (a *App) pointInPaletteToggle(p *pane.Pane, x, y float64) bool {
	return a.paletteLayoutFor(p).PointInToggle(x, y)
}
