//go:build js && wasm

package main

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
)

// The create RPCs, one per primitive, each placing a 1x1 tile. Nothing
// decides anything here: a create takes the grid and cell the drop verdict
// already allowed. openConfigureURL rides along because a bare url tile's
// address is asked for on its first descent, not at create.

// createTile is the one create call. Every primitive, link and ephemeral
// visit lands its row through here, so the label the outbox keys on and the
// continuation the row feeds are one shape rather than a copy per caller.
func (a *App) createTile(label, gid string, req *gridwellv1.CreateTileRequest,
	onSuccess func(*gridwellv1.Tile)) {
	a.postTileMutate(label, gid, func(ctx context.Context) (*gridwellv1.Tile, error) {
		return a.cl.CreateTile(ctx, req)
	}, onSuccess)
}

// createWellAtCell creates an unnamed well; naming happens from inside,
// through the bar title.
func (a *App) createWellAtCell(gid string, cellX, cellY int64) {
	a.createTile("CreateWell", gid, &gridwellv1.CreateTileRequest{GridId: gid,
		Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: cellX, Y: cellY, W: 1, H: 1}}, nil)
}

func (a *App) createTextAtCell(gid string, data []byte, cellX, cellY int64) {
	req := &gridwellv1.CreateTileRequest{GridId: gid,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: cellX, Y: cellY, W: 1, H: 1}}
	a.postTileMutate("CreateText", gid, func(ctx context.Context) (*gridwellv1.Tile, error) {
		return a.cl.CreateWithContent(ctx, req, data)
	}, nil)
}

// createURLAtCell lands an address-less url tile. The first descent prompts
// for the address and writes it as the tile's content.
func (a *App) createURLAtCell(gid string, cellX, cellY int64) {
	a.createTile("CreateURL", gid, &gridwellv1.CreateTileRequest{GridId: gid,
		Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: cellX, Y: cellY, W: 1, H: 1}}, nil)
}

// openConfigureURL prompts for a bare url tile's address on its first
// descent. Submitting writes the address as content, versioned and bumping,
// and then descends, so the fill-in flows straight into the page.
func (a *App) openConfigureURL(p *pane.Pane, t *gridwellv1.Tile) {
	gid := a.gridIDForPane(p)
	paneID, id := p.ID, t.Id
	// The address is content, so the write claims the row's version as the
	// descent saw it.
	version := t.Version
	candidates := a.urlSuggestCandidates(uuidOf(gid))
	a.openURLModal(candidates, func(url string) {
		go func() {
			// The plain dispatcher, not postWriteContent: the typed url has
			// no cache entry behind it, so the content path's rule that the
			// dirty entry is the record cannot cover it. The dispatcher
			// parks this closure instead.
			var tile *gridwellv1.Tile
			err := a.do(write{
				label: "ConfigureURL", gid: gid, id: id,
				source: "url", failText: "url save failed",
				call: func(ctx context.Context) error {
					t, werr := a.cl.WriteContent(ctx, id, version, []byte(url))
					if werr == nil {
						tile = t
					}
					return werr
				},
			})
			if err != nil {
				return
			}
			a.c.UpdateTile(tile.GridId, tile)
			fp := a.tree.FindPane(paneID)
			if fp == nil || fp.ContentID() != "" {
				return
			}
			a.descend(fp, tile)
			a.draw()
		}()
	}, func() {
		a.draw()
	})
}

// createShellAtCell lands a shell tile. The first descent creates its private
// tmux session, and re-descending reattaches to the same one.
func (a *App) createShellAtCell(gid string, cellX, cellY int64) {
	// No auto-descent, like every other primitive. DecideAutoLive's
	// fresh-shell arm creates the session on the first descent.
	a.createTile("CreateShell", gid, &gridwellv1.CreateTileRequest{GridId: gid,
		Tile: &gridwellv1.Tile{Kind: rpc.KindShell, X: cellX, Y: cellY, W: 1, H: 1}}, nil)
}
