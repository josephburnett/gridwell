//go:build js && wasm

package main

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"math"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/client/palette"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/pluginhealth"
	"github.com/josephburnett/gridwell/client/tileface"
	"github.com/josephburnett/gridwell/client/wsbar"
)

// The creation palette: the screen-space adapters over client/palette, the
// "+" button, and the popover drawing.

// paletteLayoutFor builds the palette.Layout snapshot for a pane.
// client/palette owns the geometry.
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
		// Both counts come from the one item list, so a folded section leaves
		// the top row empty rather than needing a second flag.
		TopRow: paletteTopRow(items),
		Toggle: show.Toggle,
	}, show
}

// plusButtonCenter is what clicks hit-test against and what the open palette
// anchors to. It falls back to the window corner when the window is too short
// for the band.
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
	rr := plusButtonRadius
	return dx*dx+dy*dy <= rr*rr
}

// drawPlusButton paints the circular + button, which during a tile drag
// becomes the delete target. The round chrome is identical either way, so the
// position stays muscle memory.
func (a *App) drawPlusButton(p *pane.Pane) {
	cx, cy := a.plusButtonCenter()
	deleting := a.tileDragInFlight()
	hot := deleting && a.pointInPlus(a.dragging.curScreenX, a.dragging.curScreenY)
	// The button wears the pane's family hue. The hot trashcan goes
	// danger-red; an open menu gets a brighter ring.
	band, button := a.barTheme()
	bg := button
	if hot {
		bg = a.pal.PlusBgDelete
	}
	a.cctx.Set("fillStyle", bg)
	a.cctx.Call("beginPath")
	a.cctx.Call("arc", cx, cy, plusButtonRadius, 0, 2*math.Pi)
	a.cctx.Call("fill")
	a.cctx.Set("strokeStyle", a.pal.CircleRim)
	if a.menu.OpenOn(p.ID) {
		a.cctx.Set("lineWidth", 2.0)
	} else {
		a.cctx.Set("lineWidth", 1.0)
	}
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)

	if deleting {
		side := plusButtonRadius * 1.4
		a.drawTrashcanIcon(a.cctx, cx-side/2, cy-side/2, side, side)
		return
	}

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

// drawPalette paints the creation popover: the plugin section's disclosure
// strip when there is one, plugins on top, then the primitives.
func (a *App) drawPalette(p *pane.Pane) {
	mx, my, mw, mh := a.paletteRect(p)
	a.cctx.Set("fillStyle", a.pal.MenuBg)
	a.cctx.Call("fillRect", mx, my, mw, mh)
	a.cctx.Set("strokeStyle", a.pal.PaneBorder)
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

// drawPaletteToggle paints the plugin section's disclosure strip, its chevron
// pointing the way the press moves the section. A band and never a swatch, so
// nothing invites the drag a template tile takes.
func (a *App) drawPaletteToggle(r pane.Rect, c palette.Chevron) {
	a.cctx.Set("fillStyle", a.pal.PlusBg)
	a.cctx.Call("fillRect", r.X, r.Y, r.W, r.H)
	cx := r.X + r.W/2
	cy := r.Y + r.H/2
	const halfW = 6.0
	// Canvas y grows downward, so the apex sits above the ends for "up".
	dy := -3.0
	if c == palette.ChevronDown {
		dy = -dy
	}
	a.cctx.Set("strokeStyle", a.pal.PlusFg)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("beginPath")
	a.cctx.Call("moveTo", cx-halfW, cy-dy)
	a.cctx.Call("lineTo", cx, cy+dy)
	a.cctx.Call("lineTo", cx+halfW, cy-dy)
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)
}

// drawPaletteItem shares its body with the live-tile renderer, so a swatch
// reads identical to what the user drops, with a glyph overlaid for a tile
// that has no content yet.
func (a *App) drawPaletteItem(item paletteItem, x, y, w, h float64, hovered bool) {
	n := paletteItemGhostNode(item)
	if item.isPlugin {
		// A plugin swatch is the linked well it drops into a grid, dashed
		// because a cross-plugin link can be unlinked. Drawn identically
		// here, as the drag ghost, and once dropped.
		a.cctx.Set("fillStyle", a.pal.Bg)
		a.cctx.Call("fillRect", x, y, w, h)
		a.strokeTileFrame(a.cctx, x, y, w, h, a.pal.FocusBorder, true /* dashed */, false /* selected */)
		a.drawPluginGlyph(door.RowGlyph(item.plugin), x, y, w, h)
		a.drawTileBannerLabel(n, x, y, w, h, false)
		// A broken or waiting plugin gets the same health tint its link
		// tiles do.
		a.drawPluginHealthTint(n, x, y, w, h)
	} else {
		outside := tileface.Outside(n, false)
		a.drawNode(a.cctx, n, x, y, w, h, false, outside, tileBorderPx, false)
		if pr, ok := primitiveFor(item.primitive); ok {
			pr.glyph(a, x, y, w, h)
		}
	}
	if hovered {
		a.drawSelectedTileOutline(a.cctx, x, y, w, h)
	}
}

// drawPluginGlyph is the name-to-pixels half of a declared identity glyph.
// Which name a row wears is door.RowGlyph's and door.GlyphFor's, so the menu
// swatch and the bar crumb cannot default differently. Never a kind switch:
// the client must not know its plugins.
func (a *App) drawPluginGlyph(glyph string, x, y, w, h float64) {
	switch glyph {
	case rpc.GlyphFolder:
		drawFolderGlyph(a.cctx, x, y, w, h, a.pal.FocusBorder)
	case rpc.GlyphProcess:
		drawProcessGlyph(a.cctx, x, y, w, h, a.pal.FocusBorder)
	case rpc.GlyphWell:
		drawWellGlyph(a.cctx, x, y, w, h, a.pal.FocusBorder)
	case rpc.GlyphTrash:
		drawTrashGlyph(a.cctx, x, y, w, h, a.pal.FocusBorder)
	default:
		// rpc.GlyphGlobe, declared by every connection, and equally any name
		// this client does not know.
		drawGlobeGlyph(a.cctx, x, y, w, h, a.pal.FocusBorder)
	}
}

// drawPluginHealthTint overlays a plugin link tile with its
// pluginhealth-decided tint. Only this node's own plugins have local health,
// so a remote node's tiles surface their state through descent errors.
func (a *App) drawPluginHealthTint(n *gridwellv1.Tile, x, y, w, h float64) {
	// A link with no target is not enterable wherever it lives. The descent
	// guard explains on click.
	if pluginhealth.UnrootedLink(n) {
		// A row this node cannot classify gets the neutral dimming too,
		// because not knowing yet is exactly the waiting face.
		color := a.pal.DoorwayWaitingTint
		// The local plugin list knows more: a failure gets the alarm tint.
		if pl, ok := a.pluginByUUID(rpc.LocalOf(n.Id)); ok {
			if st, classified := pluginhealth.Classify(pl); classified && st == pluginhealth.Broken {
				color = a.pal.DoorwayBrokenTint
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
