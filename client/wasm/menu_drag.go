//go:build js && wasm

package main

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"math"

	"google.golang.org/protobuf/proto"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/menu"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/sourcehealth"
)

// The + menu swatch as a gesture: arming a menu drag, what a bare click
// does, and what a release over a grid creates. What a swatch shows is
// client/menu's decision, what a click means is menu.ClickOn's, and
// what a release does is menu.DropOn's. The create RPCs are in
// create_tile.go.

// startMenuDrag arms a drag from the i'th menu item, as a regular tile
// drag but with fromMenu=true so the release branches to creation. The
// menu stays open until commit.
func (a *App) startMenuDrag(p *pane.Pane, r pane.Rect, idx int, sx, sy float64) {
	items := a.menuItems(p)
	if idx < 0 || idx >= len(items) {
		return
	}
	item := items[idx]
	if item.template != nil {
		a.startTemplateDrag(p, idx, item.template, sx, sy, dragdrop.IntentMove)
		return
	}
	tx, ty, tw, _ := a.menuTileRect(p, idx)
	if item.copySwatch {
		a.dragging = &dragState{originPaneID: p.ID, menuPress: true, copySwatch: true, item: item,
			touch: a.fingerPress, startScreenX: sx, startScreenY: sy, curScreenX: sx, curScreenY: sy,
			cellOffsetX: 0.5, cellOffsetY: 0.5, snapshotTile: menuItemGhostNode(item),
			originScreenX: tx, originScreenY: ty, srcCellSize: tw}
		return
	}
	a.dragging = &dragState{
		originPaneID: p.ID,
		menuPress:    true,
		fromMenu:     true,
		item:         item,
		// The menu belongs to the pane's node, and the drop rules compare
		// this against the destination's.
		menuNS:        a.paneNodeNS(p),
		startScreenX:  sx,
		startScreenY:  sy,
		curScreenX:    sx,
		curScreenY:    sy,
		cellOffsetX:   0.5,
		cellOffsetY:   0.5,
		snapshotTile:  menuItemGhostNode(item),
		originScreenX: tx,
		originScreenY: ty,
		// The ghost starts at the fixed swatch size and the drop-target
		// machinery lerps it to the destination grid's cell size, the same
		// as dragging a tile across doors.
		srcCellSize: tw,
	}
}

// startTemplateDrag arms the drag of template n, the menu's i'th item, as
// any tile's drag is armed: it carries the template's row from the templates
// grid, so a left-drag moves it out, a right-drag copies and ctrl links, and
// dragdrop.DecideDrop decides as it does for every tile. The strip is only
// where the ghost starts.
func (a *App) startTemplateDrag(p *pane.Pane, i int, n *gridwellv1.Tile, sx, sy float64, intent dragdrop.Intent) {
	r := a.menuLayoutFor(p).TileRect(i)
	a.dragging = &dragState{
		originPaneID: p.ID,
		menuPress:    true,
		item:         menuItem{template: n},
		intent:       intent,
		touch:        a.fingerPress,
		startScreenX: sx,
		startScreenY: sy,
		curScreenX:   sx,
		curScreenY:   sy,
		srcGridID:    a.templates,
		srcCellSize:  pane.SquareCell(min(r.W, r.H), n),
	}
	a.dragging.grabTile(n, float64(n.X)+(sx-r.X)/r.W*float64(max(n.W, 1)), float64(n.Y)+(sy-r.Y)/r.H*float64(max(n.H, 1)), r.X, r.Y)
	a.dragging.carried = a.carryRows(a.templates, n)
}

// commitTemplateCopy copies the tile the copy swatch was released over into
// the templates grid, at menu.TemplateCell over the grid as cached, and the
// ghost flies home to the swatch; the new template comes into the strip with
// the event that tells of it.
func (a *App) commitTemplateCopy(d *dragState, t *dropTarget) {
	a.cancelDragSnapBack(d)
	g, ok := a.c.Grid(a.templates)
	if !ok {
		a.reportErr(errsurface.Info, "templates", "the templates are still loading — try again in a moment")
		return
	}
	x, y := menu.TemplateCell(g.Tiles, t.tile.H)
	req := &gridwellv1.CopyTileRequest{TileId: t.tile.Id, DestGridId: a.templates, X: x, Y: y}
	a.postTileMutate("CopyTile", a.templates, func(ctx context.Context) (*gridwellv1.Tile, error) {
		return a.cl.CopyTile(ctx, req)
	}, nil)
}

// menuItemGhostNode synthesizes the 1x1 tile the ghost renderer paints, so
// an in-flight item takes the same draw path as a real tile. A source item
// carries the source's uuid as its id, so the health tint and the
// not-enterable descent guard can name it. Every answer is caller-owned:
// primitives share one ghost apiece, and a caller placing the ghost at a
// cell would otherwise leave those coordinates on the next gesture's swatch.
func menuItemGhostNode(item menuItem) *gridwellv1.Tile {
	switch {
	case item.template != nil:
		return proto.CloneOf(item.template)
	case item.copySwatch:
		return &gridwellv1.Tile{W: 1, H: 1}
	}
	if item.isSwatch {
		t := rpc.SourceDoorTile(item.source)
		t.Id = item.source.Uuid
		return t
	}
	if pr, ok := primitiveFor(item.primitive); ok {
		return proto.CloneOf(pr.ghost)
	}
	return &gridwellv1.Tile{}
}

// clickMenuItem runs the bare-click behavior of the menu item a menu
// drag was armed from. Which behavior is menu.ClickOn's, over the swatch
// alone since a click has no destination. Every swatch has an answer.
func (a *App) clickMenuItem(d *dragState) {
	fp := a.tree.FindPane(d.originPaneID)
	if fp == nil {
		return
	}
	pr, _ := primitiveFor(d.item.primitive)
	switch menu.ClickOn(menu.Swatch{
		IsSwatch:   d.item.isSwatch,
		Promote:    d.item.promotePane != "",
		Visits:     pr.click != nil,
		Template:   d.item.template != nil,
		CopySwatch: d.item.copySwatch,
	}) {
	case menu.ClickCopy:
		a.menu.Close()
		a.visitCopy(fp, d.item.template)
	case menu.ClickEnter:
		// The same descent a link tile takes, through a synthetic link tile
		// at the pane's view center, so ascent lands back exactly here.
		door := menuItemGhostNode(d.item)
		if v, ok := fp.Live(); ok {
			door.X, door.Y = int64(math.Floor(v.Cx()-0.5)), int64(math.Floor(v.Cy()-0.5))
		}
		a.descend(fp, door)
	case menu.ClickVisit:
		pr.click(a, fp)
	case menu.ClickNothing:
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
	a.openURLModal(a.urlSuggestCandidates(rpc.UUIDOf(a.gridIDForPane(p))),
		func(url string) {
			if vp := a.tree.FindPane(paneID); vp != nil {
				a.visitEphemeralURL(vp, url)
			}
		}, nil)
}

// visitShellFromMenu is the shell swatch's click. Ascent deletes the tile and
// its tmux, which the gray border warns about; a drag places a
// persistent tile.
func (a *App) visitShellFromMenu(p *pane.Pane) {
	a.menu.Close()
	a.visitEphemeralShell(p) // reports if there is nowhere to open
}

// commitMenuDrop resolves the menu drag at release. menu.DropOn
// says what the release does; this gathers the facts and runs it. The
// destination is the (t, dropX, dropY) the one gather produced for the verdict
// that routed this call, so the create lands exactly where DecideDrop said it
// may, and dropTargetAt owns what is legal.
func (a *App) commitMenuDrop(d *dragState, t *dropTarget, dropX, dropY int64) {
	pr, isPrimitive := primitiveFor(d.item.primitive)
	r := menu.Release{Target: t != nil, Door: d.item.isSwatch,
		Promote: d.item.primitive == primURL && d.item.promotePane != "",
		Disk:    !d.item.isSwatch && isPrimitive && pr.disk}
	if r.Target {
		// The target's grid is the open door's child when the cursor promoted
		// into one, not the pane's own leaf grid.
		r.Occupied = a.occupiedForDrop(t.gridID, dragdrop.Carry{{
			W: max(d.snapshotTile.W, 1), H: max(d.snapshotTile.H, 1)}}, dropX, dropY, false)
		r.SameNode = a.gridNodeNS(t.gridID) == d.menuNS
		r.AcceptsTiles, _ = a.gridAcceptsTiles(t.gridID)
		r.DiskWritable = a.offerOn(t.gridID).Disk()
		if r.Door {
			st, classified := sourcehealth.Classify(d.item.source)
			r.Enterable = classified && st == sourcehealth.Enterable
		}
	}
	switch menu.DropOn(r) {
	case menu.DropSnapBack:
		a.cancelDragSnapBack(d)
		return
	case menu.DropRefuse:
		a.reportErr(errsurface.Info, "menu",
			"this menu belongs to another node — drop into a grid on that node, or open the menu here")
		a.cancelDragSnapBack(d)
		return
	case menu.DropLink:
		// A door becomes a door to another namespace link to its root grid, and a
		// connection row drops the same way with its chained root already
		// qualified.
		a.landGhostAtCell(t, dropX, dropY)
		a.createSourceLinkAtCell(t.gridID, d.item.source, dropX, dropY)
	case menu.DropPromote:
		// The ephemeral url off the bar's crumb becomes a persistent tile and
		// the pane relocates onto it.
		a.landGhostAtCell(t, dropX, dropY)
		a.promoteEphemeralURL(d.item.promotePane, t.pane.ID, t.gridID, dropX, dropY)
	case menu.DropCreate:
		// A Gridwell tile's drop never prompts: whatever a kind needs is
		// asked for on the first descent. A file or a directory is named
		// before it exists, since the name is its address on disk.
		a.landGhostAtCell(t, dropX, dropY)
		if isPrimitive {
			pr.create(a, t.gridID, dropX, dropY)
		}
	}
	a.menu.Close()
}

// createSourceLinkAtCell creates a door to another namespace link to the source's qualified
// root grid, seeding its framing from the source's persisted root view so the
// preview shows what descent will show.
func (a *App) createSourceLinkAtCell(gid string, pl *gridwellv1.SourceInfo, cellX, cellY int64) {
	a.createTile("CreateDoor", gid, &gridwellv1.CreateTileRequest{GridId: gid,
		Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: cellX, Y: cellY, W: 1, H: 1,
			ChildGridId: pl.RootGridId, AltText: pl.Label,
			ViewCx: pl.RootViewCx, ViewCy: pl.RootViewCy, ViewZoom: pl.RootViewZoom}}, nil)
}
