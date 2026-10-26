//go:build js && wasm

package main

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"math"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/client/menu"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/pluginhealth"
	"github.com/josephburnett/gridwell/client/tilepreview"
	"github.com/josephburnett/gridwell/client/wsbar"
)

// The + menu: the screen-space adapters over client/menu, the
// "+" button, and the popover drawing.

// menuLayoutFor builds the menu.Layout snapshot for a pane.
// client/menu owns the geometry.
func (a *App) menuLayoutFor(p *pane.Pane) menu.Layout {
	l, _ := a.menuLayoutAndShow(p)
	return l
}

// menuLayoutAndShow builds the layout together with the section decision
// behind it, for the two callers that draw or hit-test the chevron strip.
func (a *App) menuLayoutAndShow(p *pane.Pane) (menu.Layout, menu.Shown) {
	cx, cy := a.plusButtonCenter()
	items, show := a.menuView(p)
	return menu.Layout{
		Cfg:      menu.Default(),
		PlusX:    cx,
		PlusY:    cy,
		NumTiles: len(items),
		// Both counts come from the one item list, so a folded section leaves
		// the top row empty rather than needing a second flag.
		TopRow: menuTopRow(items),
		Toggle: show.Toggle,
	}, show
}

// plusButtonCenter is what clicks hit-test against and what the open menu
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

// menuRect is the wasm-side adapter for menu.Layout.PopoverRect.
func (a *App) menuRect(p *pane.Pane) (x, y, w, h float64) {
	pop := a.menuLayoutFor(p).PopoverRect()
	return pop.X, pop.Y, pop.W, pop.H
}

// menuTileRect is the wasm-side adapter for menu.Layout.TileRect.
func (a *App) menuTileRect(p *pane.Pane, i int) (x, y, w, h float64) {
	tr := a.menuLayoutFor(p).TileRect(i)
	return tr.X, tr.Y, tr.W, tr.H
}

// drawMenu paints the creation popover: the plugin section's disclosure
// strip when there is one, plugins on top, then the primitives.
func (a *App) drawMenu(p *pane.Pane) {
	mx, my, mw, mh := a.menuRect(p)
	fillRectC(a.cctx, mx, my, mw, mh, a.pal.MenuBg)
	a.cctx.Set("strokeStyle", a.pal.PaneBorder)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("strokeRect", mx+0.5, my+0.5, mw-1, mh-1)
	items, show := a.menuView(p)
	for i, item := range items {
		tx, ty, tw, th := a.menuTileRect(p, i)
		hovered := a.menu.Hover() == i
		a.drawMenuItem(item, tx, ty, tw, th, hovered)
	}
	if show.Toggle {
		a.drawMenuToggle(a.menuLayoutFor(p).ToggleRect(), show.Chevron)
	}
}

// drawMenuToggle paints the plugin section's chevron strip, its chevron
// pointing the way the press moves the section. A band and never a swatch, so
// nothing invites the drag a template tile takes.
func (a *App) drawMenuToggle(r pane.Rect, c menu.Chevron) {
	fillRectC(a.cctx, r.X, r.Y, r.W, r.H, a.pal.PlusBg)
	cx := r.X + r.W/2
	cy := r.Y + r.H/2
	const halfW = 6.0
	// Canvas y grows downward, so the apex sits above the ends for "up".
	dy := -3.0
	if c == menu.ChevronDown {
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

// drawMenuItem shares its body with the live-tile renderer, so a swatch
// reads identical to what the user drops, with a glyph overlaid for a tile
// that has no content yet.
func (a *App) drawMenuItem(item menuItem, x, y, w, h float64, hovered bool) {
	n := menuItemGhostNode(item)
	if item.isPlugin {
		// A plugin swatch is the linked door it drops into a grid, dashed
		// because a cross-plugin link can be unlinked. Drawn identically
		// here, as the drag ghost, and once dropped.
		fillRectC(a.cctx, x, y, w, h, a.pal.Bg)
		a.strokeTileFrame(a.cctx, x, y, w, h, a.pal.FocusBorder, true /* dashed */, false /* selected */)
		a.drawPluginGlyph(door.RowGlyph(item.plugin), x, y, w, h)
		a.drawTileBannerLabel(n, x, y, w, h, false)
		// A broken or waiting plugin gets the same health tint its link
		// tiles do.
		a.drawPluginHealthTint(n, x, y, w, h)
	} else {
		outside := tilepreview.Outside(n, false)
		a.drawNode(a.cctx, n, x, y, w, h, false, outside, tileBorderPx, false)
		if pr, ok := primitiveFor(item.primitive); ok {
			pr.glyph(a, x, y, w, h)
		}
	}
	if hovered {
		a.drawFocusedTileOutline(a.cctx, x, y, w, h)
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
	case rpc.GlyphDoor:
		drawDoorGlyph(a.cctx, x, y, w, h, a.pal.FocusBorder)
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
// so a remote node's tiles surface their state through descent errors. A url
// tile the node says is broken (Tile.broken_reason) wears a broken door's
// tint, a far one's by its own node's verdict.
func (a *App) drawPluginHealthTint(n *gridwellv1.Tile, x, y, w, h float64) {
	if n.BrokenReason != "" {
		fillRectC(a.cctx, x, y, w, h, a.pal.DoorBrokenTint)
		return
	}
	// A link with no target is not enterable wherever it lives. The descent
	// guard explains on click.
	if pluginhealth.UnrootedLink(n) {
		// A row this node cannot classify gets the neutral dimming too,
		// because not knowing yet is exactly the waiting look.
		color := a.pal.DoorWaitingTint
		// The local plugin list knows more: a failure gets the alarm tint.
		if pl, ok := a.pluginByUUID(rpc.LocalOf(n.Id)); ok {
			if st, classified := pluginhealth.Classify(pl); classified && st == pluginhealth.Broken {
				color = a.pal.DoorBrokenTint
			}
		}
		fillRectC(a.cctx, x, y, w, h, color)
	}
}

// menuTileIndexAt is the wasm-side adapter for menu.Layout.TileIndexAt.
func (a *App) menuTileIndexAt(p *pane.Pane, x, y float64) int {
	return a.menuLayoutFor(p).TileIndexAt(x, y)
}

// pointInMenu is the wasm-side adapter for menu.Layout.PointInPopover.
func (a *App) pointInMenu(p *pane.Pane, x, y float64) bool {
	return a.menuLayoutFor(p).PointInPopover(x, y)
}

// pointInMenuToggle is the wasm-side adapter for
// menu.Layout.PointInToggle: false whenever the popover has no strip, so
// the press path needs no second guard.
func (a *App) pointInMenuToggle(p *pane.Pane, x, y float64) bool {
	return a.menuLayoutFor(p).PointInToggle(x, y)
}
