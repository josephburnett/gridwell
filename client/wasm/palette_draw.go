//go:build js && wasm

package main

import (
	"fmt"
	"math"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/palette"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/pluginhealth"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// This file holds the creation-palette: the screen-space layout adapters
// over the pure client/palette package, the floating "+" button, and the
// popover drawing (one swatch per templateKind, with its identity glyph).

func (a *App) paletteLayoutFor(p *pane.Pane, r pane.Rect) palette.Layout {
	return palette.Layout{
		Cfg:      palette.Default(),
		Pane:     palette.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H},
		PaneZoom: p.Zoom,
		NumTiles: len(a.paletteItems(p)),
	}
}

// plusLayout builds the minimal Layout needed to place the + button for pane
// p — just the rect plus the launcher-centering flag. The pane's zoom does
// not influence the + button.
// plusLayout builds the layout for a pane's lower-right + button. The launcher
// has no + button at all (it shows its plugin tiles directly), so the button
// is always in the same corner home — no special-casing.
func plusLayout(_ *pane.Pane, r pane.Rect) palette.Layout {
	return palette.Layout{
		Cfg:  palette.Default(),
		Pane: palette.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H},
	}
}

// plusButtonCenter returns the screen-space center of the + button for pane p.
func plusButtonCenter(p *pane.Pane, r pane.Rect) (float64, float64) {
	return plusLayout(p, r).PlusCenter()
}

// pointInPlus reports whether (x, y) lies within the + button for pane p.
func pointInPlus(p *pane.Pane, r pane.Rect, x, y float64) bool {
	return plusLayout(p, r).PointInPlus(x, y)
}

// drawPlusButton paints the floating circular + button in the pane's lower
// right. During a tile drag whose source is this pane, the same round button
// becomes the delete target: it shows a trashcan instead of a +, and its
// circle goes danger-red while the dragged ghost hovers over it ("release
// here deletes"). The round chrome is identical either way so the position is
// muscle-memory-stable.
func (a *App) drawPlusButton(p *pane.Pane, r pane.Rect) {
	cx, cy := plusButtonCenter(p, r)
	deleting := a.tileDragInFlight() && a.dragging.originPaneID == p.ID
	hot := deleting && pointInPlus(p, r, a.dragging.curScreenX, a.dragging.curScreenY)
	bg := colorPlusBg
	switch {
	case hot:
		bg = colorPlusBgDelete
	case a.menu.OpenOn(p.ID):
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

// installPaletteNameField wires the palette's HTML name input once at boot.
// Keystrokes must not leak to the canvas's window-level keydown handler;
// Escape closes the palette (the input hides and clears via the draw sync).
// The js.Func lives for the app's lifetime — installed once, never released.
func (a *App) installPaletteNameField() {
	a.paletteName = a.doc.Call("getElementById", "gw-palette-name")
	if !a.paletteName.Truthy() {
		return
	}
	a.paletteName.Call("addEventListener", "keydown", js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		ev.Call("stopPropagation")
		if ev.Get("key").String() == "Escape" {
			ev.Call("preventDefault")
			a.menu.Close()
			a.canvas.Call("focus")
			a.draw()
		}
		return nil
	}))
}

// syncPaletteNameField makes the DOM input a pure view of the menu state:
// while the palette is open on a visible pane the input sits exactly over the
// popover's name row; otherwise it is hidden and its draft value cleared (each
// open starts blank). Called every draw, the same pattern as
// syncFileOverlayPosition — reading never mutates, so a no-op sync is safe.
func (a *App) syncPaletteNameField(rects map[string]pane.Rect) {
	in := a.paletteName
	if !in.Truthy() {
		return
	}
	st := in.Get("style")
	if a.menu.IsOpen() {
		if mp := a.tree.FindPane(a.menu.PaneID()); mp != nil {
			if mr, ok := rects[mp.ID]; ok && len(a.paletteItems(mp)) > 0 {
				nf := a.paletteLayoutFor(mp, mr).NameFieldRect()
				st.Set("left", fmt.Sprintf("%.0fpx", nf.X))
				st.Set("top", fmt.Sprintf("%.0fpx", nf.Y))
				st.Set("width", fmt.Sprintf("%.0fpx", nf.W))
				st.Set("height", fmt.Sprintf("%.0fpx", nf.H))
				st.Set("display", "block")
				return
			}
		}
	}
	if st.Get("display").String() != "none" {
		st.Set("display", "none")
		in.Set("value", "")
	}
}

// paletteNameValue returns the trimmed draft in the palette's name field —
// the label for the tile about to be created ("" = unnamed).
func (a *App) paletteNameValue() string {
	if !a.paletteName.Truthy() {
		return ""
	}
	return strings.TrimSpace(a.paletteName.Get("value").String())
}

// paletteRect is the wasm-side adapter for palette.Layout.PopoverRect.
func (a *App) paletteRect(p *pane.Pane, r pane.Rect) (x, y, w, h float64) {
	pop := a.paletteLayoutFor(p, r).PopoverRect()
	return pop.X, pop.Y, pop.W, pop.H
}

// paletteTileRect is the wasm-side adapter for palette.Layout.TileRect.
func (a *App) paletteTileRect(p *pane.Pane, r pane.Rect, i int) (x, y, w, h float64) {
	tr := a.paletteLayoutFor(p, r).TileRect(i)
	return tr.X, tr.Y, tr.W, tr.H
}

// drawPalette paints the creation popover: a background container and
// a horizontal row of preview tiles, one per palette item (primitives
// only; plugins are on the launcher, not the palette).
func (a *App) drawPalette(p *pane.Pane, r pane.Rect) {
	mx, my, mw, mh := a.paletteRect(p, r)
	a.cctx.Set("fillStyle", colorMenuBg)
	a.cctx.Call("fillRect", mx, my, mw, mh)
	a.cctx.Set("strokeStyle", colorPaneBorder)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("strokeRect", mx+0.5, my+0.5, mw-1, mh-1)
	for i, item := range a.paletteItems(p) {
		tx, ty, tw, th := a.paletteTileRect(p, r, i)
		hovered := a.menu.Hover() == i
		a.drawPaletteItem(item, tx, ty, tw, th, hovered)
	}
}

// drawPaletteItem renders one preview tile inside the palette. The body
// (fill + border) is shared with the live-tile renderer so a palette swatch
// reads identical to what the user drops — same color grammar. A
// kind-specific glyph is overlaid so the swatch reads "what is this?" before
// the tile has content. Only primitive items appear in the palette; plugin
// items are exclusive to the launcher.
func (a *App) drawPaletteItem(item paletteItem, x, y, w, h float64, hovered bool) {
	n := paletteItemGhostNode(item)
	outside := tileOutside(&n, false)
	drawNode(a.cctx, &n, x, y, w, h, false, outside, tileBorderPx)
	switch item.primitive {
	case tplWell:
		drawWellGlyph(a.cctx, x, y, w, h, colorFocusBorder)
	case tplMarkdown:
		drawDocumentGlyph(a.cctx, x, y, w, h, colorMarkdownLine)
	case tplURL:
		drawGlobeGlyph(a.cctx, x, y, w, h, colorURLLine)
	case tplShell:
		drawShellGlyph(a.cctx, x, y, w, h, colorShellBorder)
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
	if n.GridID != a.nodeGrid || a.nodeGrid == "" {
		return
	}
	pl, ok := a.pluginByUUID(lastSegment(n.ID))
	if !ok {
		return
	}
	var color string
	switch pluginhealth.Classify(pl) {
	case pluginhealth.Broken:
		color = colorLauncherBrokenTint
	case pluginhealth.Rootless:
		color = colorLauncherRootlessTint
	default:
		return
	}
	a.cctx.Set("fillStyle", color)
	a.cctx.Call("fillRect", x, y, w, h)
}

// paletteTileIndexAt is the wasm-side adapter for palette.Layout.TileIndexAt.
func (a *App) paletteTileIndexAt(p *pane.Pane, r pane.Rect, x, y float64) int {
	return a.paletteLayoutFor(p, r).TileIndexAt(x, y)
}

// pointInPalette is the wasm-side adapter for palette.Layout.PointInPopover.
func (a *App) pointInPalette(p *pane.Pane, r pane.Rect, x, y float64) bool {
	return a.paletteLayoutFor(p, r).PointInPopover(x, y)
}
