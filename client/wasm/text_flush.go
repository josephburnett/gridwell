//go:build js && wasm

package main

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/textedit"
)

// This file is the one way text content reaches the server.
//
// The cache entry ({bytes, base, dirty}, keyed by tile id) owns a text tile's
// current body: textarea keystrokes mirror into it through the input
// listener. Every flush below reads bytes out of the cache by tile id and
// posts them only when the entry is dirty. No flush reads the DOM.
//
// That is a structural property, not a rule to remember: bytes can only be
// posted under the tile id they were edited under, so "tile A saved with tile
// B's buffer" has no code path. Reading the singleton <textarea> at flush
// time instead would pair it with whichever tile the flushed pane pointed at,
// and every bulk flush over a pane the singleton was not bound to would swap
// one document's content for another's under the victim's own valid basis.

// flushDirtyText posts every text tile whose cache entry carries an unsaved
// edit — the debounced save callback's whole body. Unsaved edits are found by
// tile id, not by which pane or overlay holds focus, so an edit cannot be
// stranded by focus moving on before the timer fired. A pane-scoped dirty
// mark would be reset by the pane's next descent, orphaning the edit as
// client-only state.
//
// This is the DEBOUNCE, not the retry: a write the server never answered is
// held by the outbox and re-posted by the retry kick, through this same
// flushTileContent.
func (a *App) flushDirtyText() {
	for _, tileID := range a.c.DirtyTileIDs() {
		a.flushTileContent(tileID)
	}
}

// flushTileContent posts one tile's unsaved bytes, resolving the tile row
// from the cache by id. A no-op for clean entries, non-text tiles, and
// read-only tiles; their entries cannot normally be dirty, and the guard is
// belt and braces. The write is id-addressed and version-claimed
// (WriteContent): no descent path rides the wire, and content bytes are the
// one thing that claims a version.
//
// It is also the outbox's content thunk: every early return below re-records
// the entry, so a drain that could not complete the write leaves it owed
// instead of dropping it off the list. During beforeunload it switches to the
// beacon transport, because an async save enqueued on a dying page loses up
// to a full debounce window of typing. One function, so a parked edit and a
// fresh one leave by the same door.
func (a *App) flushTileContent(tileID string) {
	// Resolve to the content id: a leaf link's edits live, and save, under
	// its target's id — the one shared {bytes, base, dirty} fact — so the
	// write routes to the plugin that owns the bytes and cannot land on the
	// link row, which owns none and which the store refuses.
	cid := a.contentKey(tileID)
	data, dirty := a.c.DirtyContent(cid)
	if !dirty {
		a.recordContent(cid)
		return
	}
	// Still owed until a save completes: an in-flight write is an
	// unacknowledged one, and every path out of here that does not reach the
	// server leaves the entry on the list.
	a.recordContent(cid)
	// t may be nil: the owner row can live in a grid this client never
	// fetched (a leaf link into a foreign plugin). Both arms below handle
	// that, differently — it is not a dead end.
	t := a.cachedTileByID(tileID)
	if t == nil && cid != tileID {
		t = a.cachedTileByID(cid)
	}
	if a.unloading && a.beaconTileContent(cid, t, data) {
		return
	}
	a.postTileContent(cid, t, data)
}

// postTileContent is flushTileContent's ordinary arm: the page is alive, so
// the bytes go through the per-tile serial save queue. A no-op for non-text
// and read-only rows, whose entries cannot normally be dirty; the guard is
// belt and braces.
func (a *App) postTileContent(cid string, t *rpc.Tile, data []byte) {
	if t == nil {
		// The owner row is in no cached grid. That is not a dead end: a leaf
		// link's target lives in a foreign plugin's grid this client may
		// never have fetched. Reporting "no destination" here would repeat
		// every tick, forever, while the edit never saved. The edit stays
		// dirty and stays in the outbox; resolve the row in the background
		// (GetTile plus its grid) and the next sweep tick flushes through
		// it. Only a definitive server answer ("no such tile") reports the
		// orphan; a transport failure retries quietly.
		if a.tileLoadFailed[cid] {
			a.reportErr(errsurface.Error, "textedit",
				"unsaved text edit has no destination — its tile is no longer known")
			return
		}
		a.fetchTileByID(cid)
		return
	}
	if t.Kind != rpc.KindText || a.tileReadOnly(t) {
		return
	}
	a.enqueueTextSave(t.GridID, cid, a.saveFallbackVersion(cid, t), data)
}

// saveFallbackVersion is the claim a save falls back on when the cache has no
// content entry to draw a SaveBasis from. It is only meaningful when t is the
// owner row: a link row's version tracks its own placement, never the
// target's bytes, so 0 forces the save to claim the basis — always present
// once content was fetched to edit — or to conflict visibly.
func (a *App) saveFallbackVersion(cid string, t *rpc.Tile) int64 {
	if t.ID != cid {
		return 0
	}
	return t.Version
}

// beaconTileContent is flushTileContent's beforeunload arm: the bytes leave
// through navigator.sendBeacon, which the browser completes after the page is
// gone. An async save enqueued on a dying page loses up to a full debounce
// window of typing on every tab close. What may write, and with what claim,
// is textedit.DecideUnloadFlush: an uncached owner row beacons on the
// SaveBasis alone, because only editable text ever becomes dirty and the
// server issues the verdict either way.
//
// Returns false when the bytes did not leave — nothing to write, or a refused
// or oversized beacon — and the caller falls back to the ordinary async post,
// which beats guaranteeing the loss.
func (a *App) beaconTileContent(cid string, t *rpc.Tile, data []byte) bool {
	basis, haveBasis := a.c.SaveBasis(cid)
	var rowVersion int64
	editable := false
	if t != nil {
		rowVersion = t.Version
		editable = t.Kind == rpc.KindText && !a.tileReadOnly(t)
	}
	version, do := textedit.DecideUnloadFlush(t != nil, editable, rowVersion, basis, haveBasis)
	switch do {
	case textedit.UnloadSkip:
		return true // nothing may write; not a fallback case
	case textedit.UnloadAsync:
		return false
	}
	path, body := rpc.WriteContentBeacon(cid, version, data)
	return body != nil && a.sendBeacon(path, body, rpc.BeaconStreamType)
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

// cachedTileByID walks the cached grids for the tile row without kicking a
// background fetch on a miss, which is findTileByID's side effect: the flush
// path stays read-only on the cache.
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
