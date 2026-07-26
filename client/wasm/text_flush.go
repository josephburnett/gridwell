//go:build js && wasm

package main

import (
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// This file is the ONE way text content reaches the server.
//
// The content-store entry ({bytes, base, dirty}, keyed by tile id) owns a text
// tile's current body: raw-mode keystrokes mirror into it (the textarea input
// listener), rendered-mode keystrokes write it directly, embed drops splice
// into it. Every flush below reads bytes out of the store BY TILE ID and posts
// them only when the entry is dirty. No flush ever reads the DOM.
//
// That is the fix for the 2026-07-18 cross-tile stomp, stated as a structural
// property: bytes can only be posted under the tile id they were edited
// under, so "tile A saved with tile B's buffer" has no code path. The old
// design read the singleton <textarea> at flush time and paired it with
// whichever tile the flushed pane pointed at; every bulk flush (pane
// collapse, workspace enter/leave) over a pane the singleton wasn't bound to
// silently swapped one document's content for another's — and the save
// claimed the victim's own valid basis, so the server accepted it.

// flushDirtyText posts every text tile whose content-store entry carries an
// unsaved edit. The debounced save callback's whole body: pending edits are
// found by TILE ID, not by which pane or overlay holds focus, so an edit can
// never be stranded by focus moving on before the timer fired (the previous
// design's pane-scoped dirty mark was reset by the pane's next descent,
// orphaning the edit as client-only state — charter §7).
func (a *App) flushDirtyText() {
	for _, tileID := range a.c.DirtyTileIDs() {
		a.flushTileContent(tileID)
	}
}

// flushTileContent posts one tile's dirty bytes, resolving the tile row from
// the cache by id. A no-op for clean entries, non-text tiles, and read-only
// tiles (their entries can't normally be dirty; the guard is belt-and-braces).
// The write is id-addressed + version-claimed (WriteContent) — there is no
// descent path on the wire (2026-07-26 redesign).
func (a *App) flushTileContent(tileID string) {
	// Resolve to the CONTENT id: a leaf link's edits live (and save) under
	// its target's id — the one shared {bytes, base, dirty} fact — so the
	// write routes to the plugin that owns the bytes and can never land on
	// the link row (which owns none; the store refuses that).
	cid := a.contentKey(tileID)
	data, dirty := a.c.DirtyContent(cid)
	if !dirty {
		return
	}
	t := a.cachedTileByID(tileID)
	if t == nil {
		// The row vanished from every cached grid while its edit was still
		// pending (the grid was evicted, or the tile deleted elsewhere).
		// Nothing routes the save; say so rather than dropping it silently.
		a.reportErr(errsurface.Error, "textedit",
			"unsaved text edit has no destination — its tile is no longer known")
		return
	}
	if t.Kind != rpc.KindText || a.tileReadOnly(t) {
		return
	}
	// The version fallback is only meaningful when t IS the owner row; a
	// link row's version tracks its own placement, never the target's
	// bytes. 0 forces the save to claim the cache's SaveBasis (always
	// present once content was fetched to edit) or conflict visibly.
	fallback := t.Version
	if t.ID != cid {
		fallback = 0
	}
	a.enqueueTextSave(t.GridID, cid, fallback, data)
}

// contentKey resolves a tile id to the id that OWNS its content bytes — the
// wasm-side twin of rpc.Tile.ContentID for call sites that hold only an id
// (the flush sweep, the textarea binding). Falls back to the id itself when
// the row isn't cached: content entries are keyed by ContentID at write time,
// so an uncached id IS already a content id.
func (a *App) contentKey(tileID string) string {
	if t := a.cachedTileByID(tileID); t != nil {
		return t.ContentID()
	}
	return tileID
}

// cachedTileByID walks the cached grids for the tile row, WITHOUT kicking a
// background fetch on a miss (findTileByID's side effect — wrong here: the
// flush path must stay read-only on the cache).
func (a *App) cachedTileByID(id string) *rpc.Tile {
	for _, gid := range a.c.KnownGridIDs() {
		g, ok := a.c.Grid(gid)
		if !ok {
			continue
		}
		if t, ok := g.Tiles[id]; ok {
			return &t
		}
	}
	return nil
}
