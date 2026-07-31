//go:build js && wasm

package main

import (
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// Tile-by-id resolution — general helpers that survived the embed
// deletion (issue #218): an ephemeral url visit focuses a tile off the
// pane's grid, so the renderer, the url stream, and the ascent still
// need the cache-wide walk.

// findTileByID walks the client tile cache for any cached row with the
// given id (ephemeral url visits and urlsync read it). On a miss it kicks a
// background fetch (fetchTileByID) to pull in the target's grid — the id
// may name a tile whose grid was never visited — so a later frame resolves.
func (a *App) findTileByID(id string) *rpc.Tile {
	for _, gid := range a.c.KnownGridIDs() {
		g, ok := a.c.Grid(gid)
		if !ok {
			continue
		}
		if t, ok := g.Tiles[id]; ok {
			return &t
		}
	}
	a.fetchTileByID(id)
	return nil
}

// descendedTile resolves the tile a pane is descended into (p.TextFocus). The
// fast path is the pane's current grid; the fallback is a by-id cache walk for
// a tile that lives OFF the pane's grid — an ephemeral url visit focuses a tile
// in the plugin's scratch grid without re-anchoring the pane onto it, so the
// renderer, the url stream, and the ascent must still find it. Returns
// (_, false) when the pane isn't descended or the tile isn't cached yet.
func (a *App) descendedTile(p *pane.Pane) (rpc.Tile, bool) {
	if p.TextFocus == "" {
		return rpc.Tile{}, false
	}
	if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
		if t, ok := g.Tiles[p.TextFocus]; ok {
			return t, true
		}
	}
	if t := a.findTileByID(p.TextFocus); t != nil {
		return *t, true
	}
	return rpc.Tile{}, false
}
