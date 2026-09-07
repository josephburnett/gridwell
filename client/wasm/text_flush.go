//go:build js && wasm

package main

import (
	"context"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/textedit"
)

// This file is the one way text content reaches the server: the debounced
// sweep, the outbox drain, the beforeunload beacon, and the ascent flush
// (saveTextBeforeAscent), which posts the body and the framed window
// together when a pane leaves a text descent.
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
// the bytes go through the per-tile serial save queue. A no-op for rows that
// own no document body — a non-text kind, a page row (rpc.Tile.TextDocument)
// — and for read-only ones, whose entries cannot normally be dirty; the guard
// is belt and braces.
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
		if a.fetch.tileLoadFailed[cid] {
			a.reportErr(errsurface.Error, "textedit",
				"unsaved text edit has no destination — its tile is no longer known")
			return
		}
		a.fetchTileByID(cid)
		return
	}
	if !t.TextDocument() || a.tileReadOnly(t) {
		return
	}
	a.enqueueTextSave(t.GridID, t.ID, cid, t.Version, data)
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
	editable, owner := false, false
	if t != nil {
		rowVersion = t.Version
		editable = t.TextDocument() && !a.tileReadOnly(t)
		owner = t.ID == cid
	}
	version, do := textedit.DecideUnloadFlush(t != nil, editable, owner, rowVersion, basis, haveBasis)
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

// saveTextBeforeAscent posts the editor buffer (if text mode is active)
// and the framed window back to the server, through the dispatcher: a
// failure reacts via clientsync (transport parks in the outbox, a verdict
// refetches and surfaces) like every other mutation.
func (a *App) saveTextBeforeAscent(p *pane.Pane, file rpc.Tile) {
	// SetTextView, and the framed-window cache patch, are text-tile
	// concerns: url and shell tiles carry no text framing, and the server's
	// SetTextView rejects non-text kinds with InvalidArgument, which would
	// surface as an error the user has to read. A serves_page descent is web
	// content and carries no text framing either.
	if !file.TextDocument() {
		return
	}
	gid := a.gridIDForPane(p)
	r := paneRectFor(a, p)
	scrollX := int64(p.TextScrollX + 0.5)
	scrollY := int64(p.TextScrollY + 0.5)

	// Content: read the tile's own cache entry, and only when it carries an
	// unsaved edit. Never the DOM. Reading the singleton textarea here would
	// attribute its bytes to whatever tile this pane points at, so a bulk
	// flush — a pane collapse, a level boundary — over a pane the singleton
	// was not bound to would save one document's bytes as another's content.
	// Posting unconditionally would also make a merely-opened tile rewrite
	// its blob and bump its version on every visit; dirty-gating keeps a
	// pure read write-free.
	// Read-only host tiles have no write-back at all: no content, since the
	// body is derived, and no framing store either — the fs plugin's SetTile
	// refuses text framing, so posting SetTextView from here would only
	// manufacture an error strip. Their mode and scroll stay session
	// facts.
	if a.tileReadOnly(&file) {
		return
	}
	buf, hasBuf := a.c.DirtyContent(file.ContentID())

	// The framed window in doc px: scroll position + the inner box size
	// (= screen px, since scale is fixed at 1.0). The parent-grid preview
	// crops this rectangle out of the re-rendered doc.
	_, _, iw, ih := textInnerBox(r)
	viewW := int64(iw + 0.5)
	viewH := int64(ih + 0.5)

	// Patch the cache immediately so the ascent transition (and any other
	// pane previewing this tile) reflects the framed window + mode before
	// the server round-trip lands.
	patched := file
	patched.TextX = scrollX
	patched.TextY = scrollY
	patched.TextW = viewW
	patched.TextH = viewH
	patched.TextMode = p.TextMode
	a.c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: patched}})

	mode := p.TextMode
	// Through the document's save queue: a debounced keystroke save may still
	// be in flight, and this flush claims a version too, so the queue
	// serializes them and the claim is read at send time. The chain is named
	// by textedit.SaveQueueKey, the same rule the debounce sweep reads —
	// keyed on the viewed row here, a leaf link's ascent flush would ride a
	// chain of its own and race the sweep for the one basis they share.
	a.persist.textSaves.Enqueue(textedit.SaveQueueKey(file.ID, file.ContentID()), func() {
		// Update the content first if the user was editing, through the one
		// claim-and-post door every text write uses. The write addresses the
		// content owner, so a link's doc saves under its target's id, as
		// flushTileContent does, and the fallback row is this snapshot — the
		// row as the flush read it, above, with the bytes. Re-reading the
		// row here would claim a version a foreign writer may have advanced
		// since, vouching for bytes this client never saw.
		if hasBuf {
			cid := file.ContentID()
			if _, ok := a.saveClaimedContent(gid, cid, file.ID == cid, file.Version, buf); !ok {
				return
			}
		}
		// Persist the framed window and mode so re-descent and the preview
		// show it however the user left it across reloads, and only when
		// something changed (textedit.FramingChanged, the one rule): a pure
		// descend-and-ascent must not write.
		next := textedit.Framing{X: scrollX, Y: scrollY, W: viewW, H: viewH, Mode: mode}
		if !textedit.FramingChanged(textedit.FramingOf(file), next) {
			return
		}
		req := &rpc.SetTextViewRequest{
			TileID:   file.ID,
			TextX:    scrollX,
			TextY:    scrollY,
			TextW:    viewW,
			TextH:    viewH,
			TextMode: mode,
		}
		a.do(write{
			label: "SetTextView", gid: gid, id: file.ID, refetchOnOK: true,
			call: func(ctx context.Context) error {
				_, err := a.cl.SetTextView(ctx, req)
				return err
			},
			beacon: func() (string, []byte, string) {
				path, body := rpc.SetTextViewBeacon(req)
				return path, body, rpc.BeaconJSONType
			},
		})
	})
}
