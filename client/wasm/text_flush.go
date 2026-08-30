//go:build js && wasm

package main

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/textedit"
)

// This file is the ONE way text content reaches the server.
//
// The content-store entry ({bytes, base, dirty}, keyed by tile id) owns a
// text tile's current body: textarea keystrokes mirror into it (the input
// listener). Every flush below reads bytes out of the store BY TILE ID and posts
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
// unsaved edit — the debounced save callback's whole body. Unsaved edits are
// found by TILE ID, not by which pane or overlay holds focus, so an edit can
// never be stranded by focus moving on before the timer fired (the previous
// design's pane-scoped dirty mark was reset by the pane's next descent,
// orphaning the edit as client-only state — charter §7).
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
// read-only tiles (their entries can't normally be dirty; the guard is
// belt-and-braces). The write is id-addressed + version-claimed
// (WriteContent) — there is no descent path on the wire (2026-07-26
// redesign), and content bytes are the ONE thing that still claims a version
// (docs/simplify-plan.md S5).
//
// It is also the outbox's content thunk: every early return below re-records
// the entry, so a drain that could not complete the write leaves it owed
// instead of silently dropping it off the list. And during beforeunload it
// switches to the beacon transport (audit #8, 2026-08-14: the old path
// enqueued async saves on a dying page, so up to a full debounce window of
// typing was reliably lost on every tab close) — one function, so a parked
// edit and a fresh one leave by the same door.
func (a *App) flushTileContent(tileID string) {
	// Resolve to the CONTENT id: a leaf link's edits live (and save) under
	// its target's id — the one shared {bytes, base, dirty} fact — so the
	// write routes to the plugin that owns the bytes and can never land on
	// the link row (which owns none; the store refuses that).
	cid := a.contentKey(tileID)
	data, dirty := a.c.DirtyContent(cid)
	if !dirty {
		a.recordContent(cid)
		return
	}
	// Still owed until a save COMPLETES: an in-flight write is an
	// unacknowledged one, and every path out of here that does not reach the
	// server must leave the entry on the list.
	a.recordContent(cid)
	// t may be nil: the owner row can live in a grid this client never
	// fetched (a leaf link into a foreign plugin). Both arms below handle
	// that, differently — audit #6's not-a-dead-end rule.
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
// and read-only rows (their entries can't normally be dirty; the guard is
// belt-and-braces).
func (a *App) postTileContent(cid string, t *rpc.Tile, data []byte) {
	if t == nil {
		// The owner row is in no cached grid. That is NOT a dead end
		// (2026-08-14 audit #6): a leaf link's target lives in a foreign
		// plugin's grid this client may never have fetched, and the sweep
		// used to report "no destination" every tick, forever, while the
		// edit silently never saved. The edit stays dirty and stays in the
		// outbox; resolve the row in the background (GetTile + its grid) and
		// the next sweep tick flushes through it. Only a DEFINITIVE server
		// answer ("no such tile") reports the orphan — a transport failure
		// retries quietly.
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
// content entry to draw a SaveBasis from. It is only meaningful when t IS the
// owner row; a link row's version tracks its own placement, never the
// target's bytes, so 0 forces the save to claim the basis (always present
// once content was fetched to edit) or conflict visibly.
func (a *App) saveFallbackVersion(cid string, t *rpc.Tile) int64 {
	if t.ID != cid {
		return 0
	}
	return t.Version
}

// beaconTileContent is flushTileContent's beforeunload arm: the bytes leave
// through navigator.sendBeacon, which the browser completes after the page is
// gone (audit #8, 2026-08-14 — the old path enqueued async saves on a dying
// page, so up to a full debounce window of typing was reliably lost on every
// tab close). What may write, and with what claim, is
// textedit.DecideUnloadFlush: an UNCACHED owner row beacons on the SaveBasis
// alone, because only editable text ever becomes dirty and the server issues
// the verdict either way (audit #6).
//
// Returns false when the bytes did NOT leave — nothing to write, or a refused
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
