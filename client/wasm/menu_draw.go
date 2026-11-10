//go:build js && wasm

package main

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"math"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/client/menu"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/sourcehealth"
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
		// The counts come from the one item list, so a folded strip leaves
		// no strip row rather than needing a second flag.
		Sections: menuSections(items),
		Toggle:   show.Toggle,
		Scroll:   a.menu.StripScroll(),
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

// drawMenu paints the creation popover: the source section's disclosure
// strip when there is one, sources on top, then the primitives.
func (a *App) drawMenu(p *pane.Pane) {
	mx, my, mw, mh := a.menuRect(p)
	fillRectC(a.cctx, mx, my, mw, mh, a.pal.MenuBg)
	a.cctx.Set("strokeStyle", a.pal.PaneBorder)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("strokeRect", mx+0.5, my+0.5, mw-1, mh-1)
	items, show := a.menuView(p)
	l := a.menuLayoutFor(p)
	strip := l.StripRect()
	for i, item := range items {
		tx, ty, tw, th := a.menuTileRect(p, i)
		hovered := a.menu.Hover() == i
		// A strip item scrolled part way out is cut at the strip's edge.
		clip := item.inStrip()
		if clip {
			a.cctx.Call("save")
			a.cctx.Call("beginPath")
			a.cctx.Call("rect", strip.X, strip.Y, strip.W, strip.H)
			a.cctx.Call("clip")
		}
		a.drawMenuItem(item, tx, ty, tw, th, hovered)
		if clip {
			a.cctx.Call("restore")
		}
	}
	if show.Toggle {
		a.drawMenuToggle(l.ToggleRect(), show.Chevron)
	}
}

// drawMenuToggle paints the source section's chevron strip, its chevron
// pointing the way the press moves the section. A band and never a swatch, so
// nothing invites the drag a swatch takes.
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
	fam := pane.TileFamily(n)
	line := a.pal.Family(fam).Line
	switch {
	case item.copySwatch:
		a.drawCopySwatch(x, y, w, h)
	case item.template != nil:
		// A template moving out leaves its slot empty, as a tile moving
		// on a grid does.
		if a.ghost == nil || !a.ghost.hidden.Has(item.template.Id) {
			a.drawTemplate(item.template, x, y, w, h)
		}
	case item.isSwatch:
		// A source swatch is the linked door it drops into a grid, dashed
		// because a cross-source link can be unlinked. Drawn identically
		// here, as the drag ghost, and once dropped.
		fillRectC(a.cctx, x, y, w, h, a.pal.Bg)
		a.strokeTileFrame(a.cctx, x, y, w, h, line, true /* dashed */, false /* selected */)
		a.drawSourceGlyph(door.RowGlyph(item.source), x, y, w, h, line)
		a.drawTileBannerLabel(n, x, y, w, h, fam)
		// A broken or waiting source gets the same health tint its link
		// tiles do.
		a.drawSourceHealthTint(n, x, y, w, h)
	default:
		a.drawNode(a.cctx, n, x, y, w, h, false, fam, tileBorderPx, false)
		if pr, ok := primitiveFor(item.primitive); ok {
			pr.glyph(a.cctx, x, y, w, h, line)
		}
	}
	if hovered {
		a.drawFocusedTileOutline(a.cctx, x, y, w, h)
	}
}

// drawTemplate draws a template as its crumb is drawn in the bar: its
// preview filling the square slot, its kind's border on it.
func (a *App) drawTemplate(n *gridwellv1.Tile, x, y, w, h float64) {
	a.drawNodeWithPreview(n, x, y, w, h, pane.SquareCell(min(w, h), n), false, pane.TileFamily(n), isLinkTile(n), "")
}

// drawCopySwatch is the strip's first item: dashed, grey on the menu's
// ground, with a plus, so it reads as no tile of any kind.
func (a *App) drawCopySwatch(x, y, w, h float64) {
	fillRectC(a.cctx, x, y, w, h, a.pal.Bg)
	a.strokeTileFrame(a.cctx, x, y, w, h, a.pal.PlusFg, true /* dashed */, false /* selected */)
	a.cctx.Set("strokeStyle", a.pal.PlusFg)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("beginPath")
	cx, cy, arm := x+w/2, y+h/2, min(w, h)/5
	a.cctx.Call("moveTo", cx-arm, cy)
	a.cctx.Call("lineTo", cx+arm, cy)
	a.cctx.Call("moveTo", cx, cy-arm)
	a.cctx.Call("lineTo", cx, cy+arm)
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)
}

// drawSourceGlyph is the name-to-pixels half of a declared identity glyph.
// Which name a row wears is door.RowGlyph's and door.GlyphFor's, so the menu
// swatch and the bar crumb cannot default differently. Never a kind switch:
// the client must not know its sources. color is the door's family's line.
func (a *App) drawSourceGlyph(glyph string, x, y, w, h float64, color string) {
	switch glyph {
	case rpc.GlyphFolder:
		drawFolderGlyph(a.cctx, x, y, w, h, color)
	case rpc.GlyphDoor:
		drawDoorGlyph(a.cctx, x, y, w, h, color)
	case rpc.GlyphTrash:
		drawTrashGlyph(a.cctx, x, y, w, h, color)
	default:
		// rpc.GlyphGlobe, declared by every connection, and equally any name
		// this client does not know.
		drawGlobeGlyph(a.cctx, x, y, w, h, color)
	}
}

// drawSourceHealthTint overlays a source link tile with its
// sourcehealth-decided tint. Only this node's own sources have local health,
// so a remote node's tiles surface their state through descent errors.
func (a *App) drawSourceHealthTint(n *gridwellv1.Tile, x, y, w, h float64) {
	// A link with no target is not enterable wherever it lives. The descent
	// guard explains on click.
	if sourcehealth.UnrootedLink(n) {
		// A row this node cannot classify gets the neutral dimming too,
		// because not knowing yet is exactly the waiting look.
		color := a.pal.DoorWaitingTint
		// The local source list knows more: a failure gets the alarm tint.
		if pl, ok := a.sourceByUUID(rpc.LocalOf(n.Id)); ok {
			if st, classified := sourcehealth.Classify(pl); classified && st == sourcehealth.Broken {
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

// menuTemplateAt is the template under the pointer and its item index, nil
// off one.
func (a *App) menuTemplateAt(p *pane.Pane, x, y float64) (*gridwellv1.Tile, int) {
	i := a.menuTileIndexAt(p, x, y)
	if items := a.menuItems(p); i >= 0 && i < len(items) && items[i].template != nil {
		return items[i].template, i
	}
	return nil, -1
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
