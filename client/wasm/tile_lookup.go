//go:build js && wasm

package main

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/textedit"
)

// Tile-by-id resolution: an ephemeral url visit focuses a tile off the pane's
// grid, so the renderer, the url stream and the ascent need a cache-wide
// walk.

// forEachCachedGrid is the one cache-wide sweep, in no defined order; f
// returns false to stop it. By-id lookup, the nav-event url rewrite and the
// url autocomplete all ask what this client knows the same way.
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

// cachedTileByID is findTileByID without the miss-side fetch, so the flush
// path stays read-only on the cache.
func (a *App) cachedTileByID(id string) *gridwellv1.Tile {
	var found *gridwellv1.Tile
	a.forEachCachedGrid(func(_ string, g *cache.Grid) bool {
		t, ok := g.Tiles[id]
		if !ok {
			return true
		}
		found = t
		return false
	})
	return found
}

// findTileByID kicks a background fetch on a miss, since the id may name a
// tile whose grid was never visited, so a later frame resolves.
func (a *App) findTileByID(id string) *gridwellv1.Tile {
	if t := a.cachedTileByID(id); t != nil {
		return t
	}
	a.fetchTileByID(id)
	return nil
}

// tileForPane resolves a tile a pane names. The fallback by-id walk is for a
// tile off the pane's grid: an ephemeral url visit focuses one in the scratch
// grid, and a delete moves a descended row to the trash, neither of which
// re-anchors the pane. False when the tile is not cached yet.
func (a *App) tileForPane(p *pane.Pane, tileID string) (*gridwellv1.Tile, bool) {
	if tileID == "" {
		return nil, false
	}
	if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
		if t, ok := g.Tiles[tileID]; ok {
			return t, true
		}
	}
	if t := a.findTileByID(tileID); t != nil {
		return t, true
	}
	return nil, false
}

// descendedTile resolves the tile a pane is descended into, false when it is
// not descended.
func (a *App) descendedTile(p *pane.Pane) (*gridwellv1.Tile, bool) {
	return a.tileForPane(p, p.ContentID())
}

// focusedTextDescent is the one read behind every text overlay: the focused
// pane, its descended row (nil until the row lands), the box the pane
// occupies, and textedit.DecideDescent's verdict on what shows.
func (a *App) focusedTextDescent() (*pane.Pane, *gridwellv1.Tile, pane.Rect, textedit.Descent) {
	p := a.tree.FocusedPane()
	if p == nil || p.ContentID() == "" {
		return nil, nil, pane.Rect{}, textedit.Descent{}
	}
	t, _ := a.descendedTile(p)
	readOnly := false
	if t != nil {
		readOnly = a.tileReadOnly(t)
	}
	return p, t, paneRectFor(a, p), textedit.DecideDescent(t, readOnly, p.TextMode)
}

// descentKind classifies what the pane is descended into, off the one
// resolver, so the url arm and the shell arm cannot disagree about an
// ephemeral visit. rpc.DescentOf owns the classification.
func (a *App) descentKind(p *pane.Pane) rpc.Descent {
	if p == nil {
		return rpc.DescentNone
	}
	t, ok := a.descendedTile(p)
	if !ok {
		return rpc.DescentNone
	}
	return rpc.DescentOf(t)
}
