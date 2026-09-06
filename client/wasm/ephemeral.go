//go:build js && wasm

package main

import (
	"context"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/scratch"
)

// This file is the scratch grid: reading which one a pane's grid names,
// the visits that land a url or a shell in it, and the two reads that
// decide what may be done about a tile that lives there. certainlyEphemeral
// gates the acts (delete on ascent, promote, the gray border) and
// possiblyEphemeral gates the durable writes — the asymmetry is deliberate
// and each is documented at its own function. client/scratch owns the
// rules; this file supplies the reads and runs the verbs.

// scratchGridOf reads the pane's own grid as the scratch rule sees it. The
// stamp rides on the grid (Grid.ScratchGridID, written by the serving node
// and chained through mounts), which is the one owner: nothing here derives
// it from an id, because a mounted remote grid's first segment is the LOCAL
// node and any roster keyed on it answers for the wrong machine.
//
// A pure read, and deliberately: the border asks per frame, and a read that
// kicked its own fetch would hammer a grid nobody is drawing — a dead link's
// namespace above all, which is never to be fetched for. Fetching a pane's
// grid belongs to the paths that show it; client/nav's landOnFrame does it
// for the one place that lands in a content frame without ever having drawn
// the grid behind it.
func (a *App) scratchGridOf(p *pane.Pane) scratch.Grid {
	return a.scratchGridIn(a.gridIDForPane(p))
}

// scratchGridIn is that read against an already-resolved grid id, for the
// callers that have just walked the place and must not walk it twice.
func (a *App) scratchGridIn(gridID string) scratch.Grid {
	g, ok := a.c.Grid(gridID)
	if !ok {
		return scratch.Grid{}
	}
	return scratch.Grid{Cached: true, ScratchGridID: g.Meta.ScratchGridID}
}

// scratchFor is scratch.For over that read: the scratch grid a visit from
// this pane lands in, and whether that is known yet.
func (a *App) scratchFor(p *pane.Pane) (string, bool) {
	return scratch.For(a.scratchGridOf(p))
}

// scratchOrReport is scratchFor with the failure surfaced: a visit with
// nowhere to land, or nowhere known yet, must say so, or the click looks like
// it just did nothing. Every ephemeral-visit entry point asks here, so no
// path can fail silently.
func (a *App) scratchOrReport(p *pane.Pane) string {
	s, known := a.scratchFor(p)
	switch {
	case !known:
		// The visit is a gesture on a pane that is showing its grid, so this
		// is the rare mid-load click; the draw that follows fetches, and the
		// next attempt has an answer.
		a.reportErr(errsurface.Info, "ephemeral",
			"this grid is still loading — try the visit again in a moment")
	case s == "":
		a.reportErr(errsurface.Error, "ephemeral",
			"nowhere to open an ephemeral visit: this grid carries no scratch grid")
	}
	return s
}

// visitEphemeralURL creates an ephemeral url tile in the current plugin's
// scratch grid (off any visible grid) and descends into it, going live —
// "descend into a url" from the menu's url swatch (clicked, not dragged). The
// tile lives only in the scratch grid (visited-url history that feeds
// autocomplete; a resolvable deep-link), so ascent returns to where you were
// and leaves nothing on the grid. Mirrors createURLAtCell's auto-go-live, but
// descends WITHOUT re-anchoring — the pane keeps its grid and just focuses the
// off-grid tile, which render / url stream / ascent resolve by id (descendedTile).
func (a *App) visitEphemeralURL(p *pane.Pane, url string) {
	scratch := a.scratchOrReport(p)
	if scratch == "" {
		return
	}
	paneID := p.ID
	req := &rpc.CreateURLRequest{
		GridID: scratch, X: 0, Y: 0, W: 1, H: 1, URL: url,
	}
	a.postTileMutate("CreateURL", scratch, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateURL(ctx, req)
	}, func(tile rpc.Tile) {
		if fp := a.tree.FindPane(paneID); fp != nil {
			a.descend(fp, &tile)
		}
	})
}

// certainlyEphemeral: t is an ephemeral (scratch-grid) tile of the pane's
// grid, AND that is known. It is what the ACTS read — delete it on ascent,
// promote it off the crumb, draw the gray dies-on-ascent border — because
// each of those is irreversible or a promise, and none may be made on a
// guess. An unloaded grid answers no, and the read kicked the fetch that
// makes the next answer real.
func (a *App) certainlyEphemeral(p *pane.Pane, t *rpc.Tile) bool {
	eph, known := scratch.Ephemeral(a.scratchGridOf(p), t.GridID)
	return known && eph
}

// possiblyEphemeral: t is ephemeral, or it is not known yet. It is what the
// DURABLE WRITES read — persisting a visit's scroll, its url trail, a freeze
// intent, or re-anchoring its pane onto its own grid — because a write made
// about a visit that is about to die leaves a mark the user never asked for,
// while a write skipped costs only that it is made a moment later.
func (a *App) possiblyEphemeral(p *pane.Pane, t *rpc.Tile) bool {
	eph, known := scratch.Ephemeral(a.scratchGridOf(p), t.GridID)
	return eph || !known
}

// deleteEphemeralTile removes an ascended-from ephemeral tile — gray means
// gone. The row is deleted, and for a shell the plugin kills its tmux
// session, and all its processes, as part of the delete.
//
// It goes through the one dispatcher, like every other mutation, and it PARKS
// (id set): a transport failure retries it on reconnect and beacons it at
// unload. The trashcan delete deliberately does not park, because a row that
// did not die comes back on screen and the user can ask again — but an
// ephemeral row is off-grid, and its shell's tmux session with it, so a lost
// cleanup is invisible and leaks until the startup sweep. A bare goroutine
// had neither: no retry, no beacon, and during beforeunload it was never even
// scheduled.
func (a *App) deleteEphemeralTile(gridID, tileID string) {
	// No claim: a delete is the user's explicit action, and the stream close
	// that precedes it triggers the plugin's detach-time title capture.
	// Captures do not bump the row and a delete carries no claim, so the two
	// cannot race.
	req := &rpc.DeleteTileRequest{TileID: tileID}
	// Drop any cached liveness probe: the row is going, and so is the tmux
	// session behind it.
	delete(a.shellAlive, tileID)
	delete(a.shellAliveProbing, tileID)
	// No refetch: the scratch grid is off any pane, so nothing renders it and
	// the event stream carries the removal into the cache.
	a.post(write{
		label: "DeleteTile", gid: gridID, id: tileID,
		source: "ephemeral", failText: "ephemeral tile cleanup failed",
		call: func(ctx context.Context) error { return a.cl.DeleteTile(ctx, req) },
		beacon: func() (string, []byte, string) {
			path, body := rpc.DeleteTileBeacon(req)
			return path, body, rpc.BeaconJSONType
		},
	})
}

// visitEphemeralShell creates an ephemeral shell tile in the current plugin's
// scratch grid and descends into it, spawning the PTY: "open a shell" from
// the menu's shell swatch, clicked rather than dragged. The shell twin of
// visitEphemeralURL, with the opposite exit contract — ascent deletes the
// tile and its tmux session, which the gray border warns about.
func (a *App) visitEphemeralShell(p *pane.Pane) {
	scratch := a.scratchOrReport(p)
	if scratch == "" {
		return
	}
	paneID := p.ID
	req := &rpc.CreateShellRequest{GridID: scratch, X: 0, Y: 0, W: 1, H: 1}
	a.postTileMutate("CreateShell", scratch, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateShell(ctx, req)
	}, func(tile rpc.Tile) {
		if fp := a.tree.FindPane(paneID); fp != nil {
			a.descend(fp, &tile)
		}
	})
}

// openLinkBelow handles a link opened out of a live tile: a new-window intent
// from a live url view (target=_blank, window.open, ctrl or cmd-click) and a
// url activated in a live shell. It splits the pane horizontally and opens the
// url as an ephemeral visit in the new lower half, so the link renders next to
// the page or terminal it came from, on the same session, and dies on ascent
// like every ephemeral visit. If the split fails, on a degenerate pane, the
// visit opens in place instead: the link is never silently dropped.
func (a *App) openLinkBelow(paneID, url string) {
	p := a.tree.FindPane(paneID)
	if p == nil {
		return
	}
	// The visit needs somewhere to land before anything splits: without a
	// scratch grid the split would only birth a pane with nothing to show.
	if a.scratchOrReport(p) == "" {
		return
	}
	// SplitOnSideAt splits the focused pane, and the link's pane may not be
	// it, since a background page can call window.open. Focus it first,
	// which is also where the user's attention is about to go.
	a.focusToPane(p)
	newP := a.splitBelowForOpen(p)
	if newP == p {
		a.visitEphemeralURL(p, url)
		return
	}
	a.draw()
	a.scheduleURLUpdate()
	a.visitEphemeralURL(newP, url)
}

// shellURLActivate handles a click on an http(s) url in a live shell, the
// xterm link provider's activate: open it below, exactly like a link a live
// url view pops, so links out of live tiles have one behavior. A stacked
// visit is just another frame on the pane's place. A no-op if the shell is no
// longer the pane's active descent.
func (a *App) shellURLActivate(paneID, url string) {
	if p := a.tree.FindPane(paneID); p != nil && p.ContentID() != "" {
		a.openLinkBelow(paneID, url)
	}
}

// promoteEphemeralURL turns the ephemeral url visit shown in pane
// originPaneID into a persistent url tile at (cellX, cellY) of grid gid — the
// drop target's grid — with destPaneID the pane the visit relocates into: the
// bar crumb dragged onto a grid. The tile is created with the visit's current
// address, since the page may have navigated, and the promote verb then moves
// the visit onto it: the view's final frame, title and trail freeze onto the
// new tile, the ephemeral row dies unless a split sibling still shows it, the
// pane relocates, and the page goes live again on the new tile.
func (a *App) promoteEphemeralURL(originPaneID, destPaneID, gid string, cellX, cellY int64) {
	op := a.tree.FindPane(originPaneID)
	if op == nil {
		return
	}
	t, ok := a.descendedTile(op)
	if !ok || t.Kind != rpc.KindURL || !a.certainlyEphemeral(op, &t) {
		return
	}
	url := t.URLString
	if v := a.urlViewFor(op.ID); v != nil && v.lastURL != "" {
		url = v.lastURL
	}
	destID := destPaneID
	oldID := t.ID
	req := &rpc.CreateURLRequest{GridID: gid, X: cellX, Y: cellY, W: 1, H: 1, URL: url}
	a.postTileMutate("CreateURL", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateURL(ctx, req)
	}, func(created rpc.Tile) {
		// The create was the await: what happens now — the freeze onto the new
		// row, the ephemeral delete, the relocation, going live again — is the
		// promote verb, planned against the world as it is when the row lands.
		a.runGesture(nav.Gesture{Kind: nav.GesturePromote, PaneID: originPaneID,
			DestPaneID: destID, OldID: oldID, Created: created})
	})
}
