//go:build js && wasm

package main

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/scratch"
)

// The scratch grid: which one a pane's grid names, the visits that land in
// it, and the two reads that decide what may be done about a tile living
// there. client/scratch owns the rules.

// scratchGridOf reads the pane's own grid as the scratch rule sees it. The
// stamp rides on the grid, because deriving it from an id would answer for
// the wrong machine: a mounted remote grid's first segment is the local node.
// A pure read, since the border asks per frame and a read that kicked its own
// fetch would hammer a grid nobody is drawing.
func (a *App) scratchGridOf(p *pane.Pane) scratch.Grid {
	return a.scratchGridIn(a.gridIDForPane(p))
}

// scratchGridIn is that read by grid id, for callers that have just walked
// the place and must not walk it twice.
func (a *App) scratchGridIn(gridID string) scratch.Grid {
	g, ok := a.c.Grid(gridID)
	if !ok {
		return scratch.Grid{}
	}
	return scratch.Grid{Cached: true, ScratchGridID: g.Meta.ScratchGridId}
}

// scratchFor is the scratch grid a visit from this pane lands in, and
// whether that is known yet.
func (a *App) scratchFor(p *pane.Pane) (string, bool) {
	return scratch.For(a.scratchGridOf(p))
}

// scratchOrReport is scratchFor with the failure surfaced, or the click looks
// like it did nothing.
func (a *App) scratchOrReport(p *pane.Pane) string {
	s, known := a.scratchFor(p)
	switch {
	case !known:
		// A mid-load click: the draw that follows fetches, and the next
		// attempt has an answer.
		a.reportErr(errsurface.Info, "ephemeral",
			"this grid is still loading — try the visit again in a moment")
	case s == "":
		a.reportErr(errsurface.Error, "ephemeral",
			"nowhere to open an ephemeral visit: this grid carries no scratch grid")
	}
	return s
}

// visitEphemeral creates tile in the pane's scratch grid and descends into
// it, going live. The descent does not re-anchor the pane, which keeps its
// grid and focuses the off-grid tile descendedTile resolves. label is the
// kind's own, because it keys the parked write.
func (a *App) visitEphemeral(p *pane.Pane, label string, tile *gridwellv1.Tile) {
	scratch := a.scratchOrReport(p)
	if scratch == "" {
		return
	}
	paneID := p.ID
	a.createTile(label, scratch, &gridwellv1.CreateTileRequest{GridId: scratch, Tile: tile},
		func(created *gridwellv1.Tile) {
			if fp := a.tree.FindPane(paneID); fp != nil {
				a.descend(fp, created)
			}
		})
}

func (a *App) visitEphemeralURL(p *pane.Pane, url string) {
	a.visitEphemeral(p, "CreateURL",
		&gridwellv1.Tile{Kind: rpc.KindURL, X: 0, Y: 0, W: 1, H: 1, UrlString: url})
}

// certainlyEphemeral: t is a scratch-grid tile of the pane's grid, and that
// is known. The acts read it, because each is irreversible or a promise and
// none may be made on a guess.
func (a *App) certainlyEphemeral(p *pane.Pane, t *gridwellv1.Tile) bool {
	eph, known := scratch.Ephemeral(a.scratchGridOf(p), t.GridId)
	return known && eph
}

// possiblyEphemeral: t is ephemeral, or it is not known yet. The durable
// writes read it, because a write about a visit that is about to die leaves a
// mark the user never asked for, while a write skipped is only late.
func (a *App) possiblyEphemeral(p *pane.Pane, t *gridwellv1.Tile) bool {
	eph, known := scratch.Ephemeral(a.scratchGridOf(p), t.GridId)
	return eph || !known
}

// deleteEphemeralTile removes an ascended-from ephemeral tile, and for a
// shell the plugin kills its tmux session too. Unlike the trashcan delete it
// parks, because an ephemeral row is off-grid and a lost cleanup is invisible
// until the startup sweep.
func (a *App) deleteEphemeralTile(gridID, tileID string) {
	// No claim: the stream close that precedes this triggers the plugin's
	// detach-time title capture, and captures do not bump the row, so the two
	// cannot race.
	req := &gridwellv1.DeleteTileRequest{TileId: tileID}
	// The row is going, and so is the tmux session behind it.
	delete(a.shellAlive, tileID)
	delete(a.shellAliveProbing, tileID)
	// No refetch: nothing renders the scratch grid, and the event stream
	// carries the removal into the cache.
	a.post(write{
		label: "DeleteTile", gid: gridID, id: tileID,
		source: "ephemeral", failText: "ephemeral tile cleanup failed",
		call:   func(ctx context.Context) error { return a.cl.DeleteTile(ctx, req) },
		beacon: jsonBeacon(func() (string, []byte) { return rpc.DeleteTileBeacon(req) }),
	})
}

// visitEphemeralShell has the opposite exit contract to the url visit: ascent
// deletes the tile and its tmux session, which the gray border warns about.
func (a *App) visitEphemeralShell(p *pane.Pane) {
	a.visitEphemeral(p, "CreateShell",
		&gridwellv1.Tile{Kind: rpc.KindShell, X: 0, Y: 0, W: 1, H: 1})
}

// openLinkBelow handles a link opened out of a live tile: an ephemeral visit
// in the pane pane.SplitBelowForOpen names, below the page it came from or in
// its place, so the link is never silently dropped.
func (a *App) openLinkBelow(paneID, url string) {
	p := a.tree.FindPane(paneID)
	if p == nil {
		return
	}
	// Without a scratch grid the split would only birth a pane with nothing
	// to show.
	if a.scratchOrReport(p) == "" {
		return
	}
	// The split is of the focused pane, and a background page can call
	// window.open, so focus the link's pane first.
	a.focusToPane(p)
	target, split := a.splitBelowForOpen(p)
	if split {
		a.draw()
		a.scheduleURLUpdate()
	}
	a.visitEphemeralURL(target, url)
}

// shellURLActivate opens a url clicked in a live shell below, exactly like a
// link a live url view pops, so links out of live tiles have one behavior.
func (a *App) shellURLActivate(paneID, url string) {
	if p := a.tree.FindPane(paneID); p != nil && p.ContentID() != "" {
		a.openLinkBelow(paneID, url)
	}
}

// promoteEphemeralURL turns the ephemeral url visit in originPaneID into a
// persistent tile at (cellX, cellY) of gid, relocating the visit into
// destPaneID. The tile carries the visit's current address, since the page may
// have navigated, and the promote verb then moves the visit onto it.
func (a *App) promoteEphemeralURL(originPaneID, destPaneID, gid string, cellX, cellY int64) {
	op := a.tree.FindPane(originPaneID)
	if op == nil {
		return
	}
	t, ok := a.descendedTile(op)
	if !ok || t.Kind != rpc.KindURL || !a.certainlyEphemeral(op, t) {
		return
	}
	url := t.UrlString
	if v := a.urlViewFor(op.ID); v != nil && v.lastURL != "" {
		url = v.lastURL
	}
	destID := destPaneID
	oldID := t.Id
	a.createTile("CreateURL", gid, &gridwellv1.CreateTileRequest{GridId: gid,
		Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: cellX, Y: cellY, W: 1, H: 1, UrlString: url}},
		func(created *gridwellv1.Tile) {
			// The create was the await. The rest is the promote verb, planned
			// against the world as it is when the row lands.
			a.runGesture(nav.Gesture{Kind: nav.GesturePromote, PaneID: originPaneID,
				DestPaneID: destID, OldID: oldID, Created: created})
		})
}
