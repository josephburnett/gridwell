//go:build js && wasm

package main

import (
	"context"
	"math"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/palette"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/pluginhealth"
)

// This file is the + menu swatch as a gesture: arming a template drag from
// a swatch, what a bare click on one does, and what a release over a grid
// creates. What a swatch shows is client/palette's decision and
// palette_draw.go's drawing; what a click means is palette.ClickOn's. The
// create RPCs a drop ends in are in create_tile.go.

// startPaletteDrag arms a drag from the i'th palette item. The dragState is
// set up so the existing ghost machinery treats it like a regular tile drag
// (snapshot tile + cell offset), but with isTemplate=true so onMouseUp
// branches to creation/mount instead of move. The palette stays open during
// the drag — it'll close on commit. For a plugin item the release path also
// distinguishes a click (enter the plugin) from a drag (mount a link).
func (a *App) startPaletteDrag(p *pane.Pane, r pane.Rect, idx int, sx, sy float64) {
	items := a.paletteItems(p)
	if idx < 0 || idx >= len(items) {
		return
	}
	item := items[idx]
	tx, ty, tw, _ := a.paletteTileRect(p, idx)
	a.dragging = &dragState{
		originPaneID:  p.ID,
		originFocused: true, // the palette only opens on the focused pane
		isTemplate:    true,
		item:          item,
		// The menu belongs to the pane's node: the drop rules compare this
		// against the destination's node.
		menuNS:        a.paneNodeNS(p),
		startScreenX:  sx,
		startScreenY:  sy,
		curScreenX:    sx,
		curScreenY:    sy,
		cellOffsetX:   0.5,
		cellOffsetY:   0.5,
		snapshotTile:  paletteItemGhostNode(item),
		originScreenX: tx,
		originScreenY: ty,
		// Start the ghost at the (fixed, zoom-independent) swatch size; the
		// drop-target machinery grows/shrinks it to the destination grid's
		// cell size as the cursor moves over a pane, and back to the swatch
		// size when off-grid — same lerp as dragging a tile across wells.
		srcCellSize: tw,
	}
}

// paletteItemGhostNode synthesizes a 1×1 rpc.Tile matching the palette item,
// so the ghost renderer can paint the in-flight tile using the same drawNode
// path that a real tile would use. A plugin item is the shared synthetic
// exit well (rpc.PluginWellTile) with the plugin's uuid as its id, so the
// health tint and the not-enterable descent guard can name the plugin.
func paletteItemGhostNode(item paletteItem) rpc.Tile {
	if item.isPlugin {
		t := rpc.PluginWellTile(item.plugin)
		t.ID = item.plugin.UUID
		return t
	}
	if pr, ok := primitiveFor(item.primitive); ok {
		return pr.ghost
	}
	return rpc.Tile{}
}

// clickTemplate runs the bare-click behavior of the palette item a template
// drag was armed from. WHICH behavior is palette.ClickOn's decision, over the
// swatch alone — a click has no destination, so the coordinates play no part —
// and what a primitive does is its own row's `click`, nil meaning nothing.
// Every swatch therefore has an answer, including "nothing", and the menu
// stays open for it.
func (a *App) clickTemplate(d *dragState) {
	fp := a.tree.FindPane(d.originPaneID)
	if fp == nil {
		return
	}
	pr, _ := primitiveFor(d.item.primitive)
	switch palette.ClickOn(palette.Swatch{
		IsPlugin: d.item.isPlugin,
		Promote:  d.item.promotePane != "",
		Visits:   pr.click != nil,
	}) {
	case palette.ClickEnter:
		// Enter the plugin, connection, or declared root the swatch names.
		// The descent is the same one a link tile takes — one verb, one
		// pushed frame — through a synthetic link tile placed at the pane's
		// view center, so ascent lands back exactly here. A drag instead
		// drops the exit-well link (commitTemplateDrop).
		well := paletteItemGhostNode(d.item)
		well.X, well.Y = int64(math.Floor(fp.Cx-0.5)), int64(math.Floor(fp.Cy-0.5))
		a.descend(fp, &well)
	case palette.ClickVisit:
		pr.click(a, fp)
	case palette.ClickHere, palette.ClickNothing:
		// The crumb you are standing on, or a swatch that only creates by
		// being dragged: nothing happens, and the menu stays open.
	}
}

// visitURLFromMenu is the url swatch's click: open the url modal and, on
// submit, descend into a live url tile created in the off-grid scratch grid —
// a page visited without placing a tile. A drag instead places a real url
// tile (the table's create).
func (a *App) visitURLFromMenu(p *pane.Pane) {
	// An ephemeral visit is a live view. On a host without one, a plain
	// browser, the modal would only produce a blank frozen tile, so say why
	// up front. Drag-create still places a real url tile.
	if !a.caps.LiveURL {
		a.menu.Close()
		a.reportErr(caps.GoLiveNotice())
		return
	}
	paneID := p.ID
	a.menu.Close()
	// Check before the modal opens: typing a url into a visit that cannot
	// land would fail only on submit.
	if a.scratchOrReport(p) == "" {
		return
	}
	a.openURLModal(a.urlSuggestCandidates(uuidOf(a.gridIDForPane(p))),
		func(url string) {
			if vp := a.tree.FindPane(paneID); vp != nil {
				a.visitEphemeralURL(vp, url)
			}
		}, nil)
}

// visitShellFromMenu is the shell swatch's click: an ephemeral shell created
// in the off-grid scratch grid, descended into, with the PTY spawned. Ascent
// deletes it — the tile row and the tmux session with all its processes,
// which the gray border warns about. A drag instead places a real, persistent
// shell tile.
func (a *App) visitShellFromMenu(p *pane.Pane) {
	a.menu.Close()
	a.visitEphemeralShell(p) // reports if there is nowhere to open
}

// commitTemplateDrop resolves the template drag at release: the create RPC at
// the snapped cell, or a snap-back with the palette left open. Overlap snaps
// back; on any successful commit the palette closes.
//
// The destination is not resolved here. It is the (t, dropX, dropY) the one
// gather (dropInputAt) produced for the verdict that routed this call, so the
// create lands exactly where DecideDrop said it may. A second hit-test would
// be a second opinion about a legal destination — and dropTargetAt is the one
// owner of that: it refuses a content descent and off-canvas, which a bare
// paneAtScreen does not, so a swatch released over a descended pane used to
// create a tile in the hidden grid behind it, at a cell computed under that
// grid's zoom rather than the one on screen.
func (a *App) commitTemplateDrop(d *dragState, t *dropTarget, dropX, dropY int64) {
	if t == nil {
		a.cancelDragSnapBack(d)
		return
	}
	destPane := t.pane

	// Bail early if the drop cell would overlap an existing tile in the
	// destination grid — the target's grid, which is the open well's child
	// when the cursor promoted into one, not the pane's own leaf grid.
	if a.occupiedForDrop(t.gridID, dropX, dropY,
		max(d.snapshotTile.W, 1), max(d.snapshotTile.H, 1), "") {
		a.cancelDragSnapBack(d)
		return
	}

	// A plugin item dropped into the destination grid becomes an exit-well
	// link to its root grid. A connection row drops the same way, its
	// chained root already qualified; links are the cross-boundary
	// vocabulary. Only writable grids accept it, and anything else snaps
	// back.
	if d.item.isPlugin {
		droppable := pluginhealth.Classify(d.item.plugin) == pluginhealth.Enterable
		// Unknown is not writable here: this drop mints a link right away,
		// and minting into a grid that may refuse it would show a tile the
		// next read takes back.
		writable, _ := a.gridWritable(t.gridID)
		if !droppable || !writable {
			a.cancelDragSnapBack(d)
			return
		}
		a.landGhostAtCell(t, dropX, dropY)
		a.createPluginLinkAtCell(t.gridID, d.item.plugin, dropX, dropY)
		a.menu.Close()
		return
	}

	// A primitive belongs to the node whose menu offered it: the swatch was
	// gated by that node's grids and policy, and creating a remote node's
	// text tile inside a local grid is a category error. Same-node drops
	// only; a cross-node drop refuses visibly and snaps back.
	if a.gridNodeNS(t.gridID) != d.menuNS {
		a.reportErr(errsurface.Info, "menu",
			"this menu belongs to another node — drop into a grid on that node, or open the menu here")
		a.cancelDragSnapBack(d)
		return
	}

	// Every template commits immediately with the snap-and-create gesture.
	// The drop never prompts: whatever a kind needs to be useful is asked
	// for on the first descent, so create is one experience everywhere —
	// drop, descend, fill in.
	a.landGhostAtCell(t, dropX, dropY)

	// A promote drag is the one arm that is not a plain create: the ephemeral
	// url dragged off the bar's crumb becomes a persistent tile carrying its
	// address, and the pane relocates onto it. Every other drop is the
	// primitives table's create.
	if d.item.primitive == tplURL && d.item.promotePane != "" {
		a.promoteEphemeralURL(d.item.promotePane, destPane.ID, t.gridID, dropX, dropY)
	} else if pr, ok := primitiveFor(d.item.primitive); ok {
		pr.create(a, t.gridID, dropX, dropY)
	}
	a.menu.Close()
}

// createPluginLinkAtCell fires CreateWell with the plugin's qualified root
// grid as the child: an exit-well link, through CreateTile, the one create.
// The link's framing seeds from the plugin's persisted root view, so its
// preview shows what descent will show.
func (a *App) createPluginLinkAtCell(gid string, pl rpc.PluginInfo, cellX, cellY int64) {
	req := &rpc.CreateWellRequest{
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
		ChildGridID: pl.RootGridID,
		Label:       pl.Label,
		Framing:     rpc.Framing{Cx: pl.RootViewCx, Cy: pl.RootViewCy, Zoom: pl.RootViewZoom},
	}
	a.postTileMutate("CreateWell", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateWell(ctx, req)
	}, nil)
}
