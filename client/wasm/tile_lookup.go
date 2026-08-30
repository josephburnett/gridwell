//go:build js && wasm

package main

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/pane"
)

// Tile-by-id resolution: an ephemeral url visit focuses a tile off the pane's
// grid, so the renderer, the url stream, and the ascent need a cache-wide
// walk.

// forEachCachedGrid walks the grids this client has cached, in no defined
// order, calling f(gridID, grid); f returns false to stop the walk. It is the
// one cache-wide sweep: by-id lookup, the nav-event url rewrite and the url
// autocomplete all ask "what does this client already know" the same way. A
// grid id whose entry went away between the id list and the read is skipped.
func (a *App) forEachCachedGrid(f func(gid string, g *cache.Grid) bool) {
	for _, gid := range a.c.KnownGridIDs() {
		g, ok := a.c.Grid(gid)
		if !ok {
			continue
		}
		if !f(gid, g) {
			return
		}
	}
}

// cachedTileByID walks the cached grids for the tile row without kicking a
// background fetch on a miss, which is findTileByID's side effect: the flush
// path stays read-only on the cache.
func (a *App) cachedTileByID(id string) *rpc.Tile {
	var found *rpc.Tile
	a.forEachCachedGrid(func(_ string, g *cache.Grid) bool {
		t, ok := g.Tiles[id]
		if !ok {
			return true
		}
		found = &t
		return false
	})
	return found
}

// findTileByID is cachedTileByID with a miss-side kick: on a miss it starts a
// background fetch (fetchTileByID) to pull in the target's grid — the id may
// name a tile whose grid was never visited — so a later frame resolves.
func (a *App) findTileByID(id string) *rpc.Tile {
	if t := a.cachedTileByID(id); t != nil {
		return t
	}
	a.fetchTileByID(id)
	return nil
}

// descendedTile resolves the tile a pane is descended into (p.ContentID()). The
// fast path is the pane's current grid; the fallback is a by-id cache walk for
// a tile that lives OFF the pane's grid — an ephemeral url visit focuses a tile
// in the plugin's scratch grid without re-anchoring the pane onto it, so the
// renderer, the url stream, and the ascent must still find it. Returns
// (_, false) when the pane isn't descended or the tile isn't cached yet.
func (a *App) descendedTile(p *pane.Pane) (rpc.Tile, bool) {
	if p.ContentID() == "" {
		return rpc.Tile{}, false
	}
	if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
		if t, ok := g.Tiles[p.ContentID()]; ok {
			return t, true
		}
	}
	if t := a.findTileByID(p.ContentID()); t != nil {
		return *t, true
	}
	return rpc.Tile{}, false
}
