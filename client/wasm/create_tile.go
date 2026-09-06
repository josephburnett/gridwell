//go:build js && wasm

package main

import (
	"context"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
)

// This file holds the create RPCs, one per primitive, each placing a 1x1
// tile at a cell of a named grid. They are the tail of a + menu swatch
// drop (palette_drag.go) and nothing decides anything here: a create takes
// the grid and cell the drop verdict already allowed. openConfigureURL
// rides along because a bare url tile's address is asked for on its first
// descent, not at create.

// createWellAtCell fires CreateWell at the given cell of grid gid. The
// footprint is 1×1 and the well is created unnamed; naming happens from
// inside, through the bar title.
func (a *App) createWellAtCell(gid string, cellX, cellY int64) {
	req := &rpc.CreateWellRequest{
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
	}
	a.postTileMutate("CreateWell", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateWell(ctx, req)
	}, nil)
}

// createTextAtCell fires CreateText at the given cell of grid gid with the
// given initial bytes. Footprint is 1×1.
func (a *App) createTextAtCell(gid string, data []byte, cellX, cellY int64) {
	req := &rpc.CreateTextRequest{
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
		Data: data,
	}
	a.postTileMutate("CreateText", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateText(ctx, req)
	}, nil)
}

// createURLAtCell fires CreateURL at the given cell of grid gid,
// address-less: the tile lands inert, and the first descent prompts for the
// address (openConfigureURL) and writes it as the tile's content.
func (a *App) createURLAtCell(gid string, cellX, cellY int64) {
	req := &rpc.CreateURLRequest{
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
	}
	a.postTileMutate("CreateURL", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateURL(ctx, req)
	}, nil)
}

// openConfigureURL prompts for a bare url tile's address on its first
// descent, reusing the url modal with its visited-url suggestions. Submitting
// writes the address as the tile's content — the store's url arm: versioned,
// validated, bumping — and then descends, so the fill-in flows straight into
// the page. Every descent goes live, so there is no special go-live
// handling.
func (a *App) openConfigureURL(p *pane.Pane, t *rpc.Tile) {
	gid := a.gridIDForPane(p)
	paneID, id := p.ID, t.ID
	// The address is content, so this write claims a version like every
	// content write: the row as the descent saw it, which is exactly the
	// value the user is filling in.
	version := t.Version
	candidates := a.urlSuggestCandidates(uuidOf(gid))
	a.openURLModal(candidates, func(url string) {
		go func() {
			// Through the plain dispatcher, not postWriteContent: the typed
			// url has no cache entry backing it, since the modal is the only
			// holder, so the content path's "the dirty entry is the record"
			// rule cannot cover it. The dispatcher parks the closure itself
			// on a transport failure and the address lands on the retry
			// kick; only the descent is skipped.
			var tile rpc.Tile
			err := a.do(write{
				label: "ConfigureURL", gid: gid, id: id,
				source: "url", failText: "url save failed",
				call: func(ctx context.Context) error {
					t, werr := a.cl.WriteContent(ctx, id, version, []byte(url))
					if werr == nil {
						tile = *t
					}
					return werr
				},
			})
			if err != nil {
				return
			}
			a.c.UpdateTile(tile.GridID, tile)
			fp := a.tree.FindPane(paneID)
			if fp == nil || fp.ContentID() != "" {
				return
			}
			a.descend(fp, &tile)
			a.draw()
		}()
	}, func() {
		a.draw()
	})
}

// createShellAtCell fires CreateShell at the given cell. The first descent
// creates the tile's private tmux session; a later ascent shows the frozen
// JPEG, and re-descending reattaches to the same session with its state
// preserved.
func (a *App) createShellAtCell(gid string, cellX, cellY int64) {
	req := &rpc.CreateShellRequest{
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
	}
	// The drop just lands the tile, with no auto-descent, like every other
	// primitive. The first descent creates the session, through
	// DecideAutoLive's fresh-shell arm, which fires when there is no preview
	// blob.
	a.postTileMutate("CreateShell", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateShell(ctx, req)
	}, nil)
}
