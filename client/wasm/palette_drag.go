//go:build js && wasm

package main

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"math"

	"google.golang.org/protobuf/proto"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/palette"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/pluginhealth"
)

// The + menu swatch as a gesture: arming a template drag, what a bare click
// does, and what a release over a grid creates. What a swatch shows is
// client/palette's decision, what a click means is palette.ClickOn's, and
// what a release does is palette.DropOn's. The create RPCs are in
// create_tile.go.

// startPaletteDrag arms a drag from the i'th palette item, as a regular tile
// drag but with isTemplate=true so the release branches to creation. The
// palette stays open until commit.
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
		// The menu belongs to the pane's node, and the drop rules compare
		// this against the destination's.
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
		// The ghost starts at the fixed swatch size and the drop-target
		// machinery lerps it to the destination grid's cell size, the same
		// as dragging a tile across wells.
		srcCellSize: tw,
	}
}

// paletteItemGhostNode synthesizes the 1x1 tile the ghost renderer paints, so
// an in-flight item takes the same draw path as a real tile. A plugin item
// carries the plugin's uuid as its id, so the health tint and the
// not-enterable descent guard can name it. Every answer is caller-owned:
// primitives share one template apiece, and a caller placing the ghost at a
// cell would otherwise leave those coordinates on the next gesture's swatch.
func paletteItemGhostNode(item paletteItem) *gridwellv1.Tile {
	if item.isPlugin {
		t := rpc.PluginWellTile(item.plugin)
		t.Id = item.plugin.Uuid
		return t
	}
	if pr, ok := primitiveFor(item.primitive); ok {
		return proto.CloneOf(pr.ghost)
	}
	return &gridwellv1.Tile{}
}

// clickTemplate runs the bare-click behavior of the palette item a template
// drag was armed from. Which behavior is palette.ClickOn's, over the swatch
// alone since a click has no destination. Every swatch has an answer.
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
		// The same descent a link tile takes, through a synthetic link tile
		// at the pane's view center, so ascent lands back exactly here.
		well := paletteItemGhostNode(d.item)
		well.X, well.Y = int64(math.Floor(fp.Cx-0.5)), int64(math.Floor(fp.Cy-0.5))
		a.descend(fp, well)
	case palette.ClickVisit:
		pr.click(a, fp)
	case palette.ClickNothing:
	}
}

// visitURLFromMenu is the url swatch's click: the url modal, then a descent
// into a live url tile in the scratch grid. A drag places a real url tile.
func (a *App) visitURLFromMenu(p *pane.Pane) {
	// On a host with no live view the modal would only produce a blank
	// frozen tile, so say why up front.
	if !a.caps.LiveURL {
		a.menu.Close()
		a.reportErr(caps.GoLiveNotice())
		return
	}
	paneID := p.ID
	a.menu.Close()
	// Typing a url into a visit that cannot land would fail only on
	// submit.
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

// visitShellFromMenu is the shell swatch's click. Ascent deletes the tile and
// its tmux session, which the gray border warns about; a drag places a
// persistent tile.
func (a *App) visitShellFromMenu(p *pane.Pane) {
	a.menu.Close()
	a.visitEphemeralShell(p) // reports if there is nowhere to open
}

// commitTemplateDrop resolves the template drag at release. palette.DropOn
// says what the release does; this gathers the facts and runs it. The
// destination is the (t, dropX, dropY) the one gather produced for the verdict
// that routed this call, so the create lands exactly where DecideDrop said it
// may, and dropTargetAt owns what is legal.
func (a *App) commitTemplateDrop(d *dragState, t *dropTarget, dropX, dropY int64) {
	r := palette.Release{Target: t != nil, Doorway: d.item.isPlugin,
		Promote: d.item.primitive == tplURL && d.item.promotePane != ""}
	if r.Target {
		// The target's grid is the open well's child when the cursor promoted
		// into one, not the pane's own leaf grid.
		r.Occupied = a.occupiedForDrop(t.gridID, dropX, dropY,
			max(d.snapshotTile.W, 1), max(d.snapshotTile.H, 1), "")
		r.SameNode = a.gridNodeNS(t.gridID) == d.menuNS
		r.Writable, _ = a.gridWritable(t.gridID)
		if r.Doorway {
			st, classified := pluginhealth.Classify(d.item.plugin)
			r.Enterable = classified && st == pluginhealth.Enterable
		}
	}
	switch palette.DropOn(r) {
	case palette.DropSnapBack:
		a.cancelDragSnapBack(d)
		return
	case palette.DropRefuse:
		a.reportErr(errsurface.Info, "menu",
			"this menu belongs to another node — drop into a grid on that node, or open the menu here")
		a.cancelDragSnapBack(d)
		return
	case palette.DropLink:
		// A doorway becomes an exit-well link to its root grid, and a
		// connection row drops the same way with its chained root already
		// qualified.
		a.landGhostAtCell(t, dropX, dropY)
		a.createPluginLinkAtCell(t.gridID, d.item.plugin, dropX, dropY)
	case palette.DropPromote:
		// The ephemeral url off the bar's crumb becomes a persistent tile and
		// the pane relocates onto it.
		a.landGhostAtCell(t, dropX, dropY)
		a.promoteEphemeralURL(d.item.promotePane, t.pane.ID, t.gridID, dropX, dropY)
	case palette.DropCreate:
		// The drop never prompts: whatever a kind needs is asked for on the
		// first descent, so create is one experience everywhere.
		a.landGhostAtCell(t, dropX, dropY)
		if pr, ok := primitiveFor(d.item.primitive); ok {
			pr.create(a, t.gridID, dropX, dropY)
		}
	}
	a.menu.Close()
}

// createPluginLinkAtCell creates an exit-well link to the plugin's qualified
// root grid, seeding its framing from the plugin's persisted root view so the
// preview shows what descent will show.
func (a *App) createPluginLinkAtCell(gid string, pl *gridwellv1.PluginInfo, cellX, cellY int64) {
	req := &gridwellv1.CreateTileRequest{GridId: gid,
		Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: cellX, Y: cellY, W: 1, H: 1,
			ChildGridId: pl.RootGridId, AltText: pl.Label,
			ViewCx: pl.RootViewCx, ViewCy: pl.RootViewCy, ViewZoom: pl.RootViewZoom}}
	a.postTileMutate("CreateWell", gid, func(ctx context.Context) (*gridwellv1.Tile, error) {
		return a.cl.CreateTile(ctx, req)
	}, nil)
}
