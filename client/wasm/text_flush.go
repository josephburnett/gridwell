//go:build js && wasm

package main

import (
	"google.golang.org/protobuf/proto"

	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/textedit"
)

// The one way text content reaches the server: the debounced sweep, the
// outbox drain, the beforeunload beacon, and the ascent flush. Every flush
// reads bytes out of the cache entry by tile id and never from the DOM, so
// bytes can only be posted under the id they were edited under. Reading the
// singleton <textarea> would save one document's content as another's.

// flushDirtyText posts every text tile whose cache entry carries an unsaved
// edit. Edits are found by tile id, not by which pane holds focus, so focus
// moving on cannot strand one. This is the debounce, not the retry: the
// outbox re-posts an unanswered write through the same flushTileContent.
func (a *App) flushDirtyText() {
	for _, tileID := range a.c.DirtyTileIDs() {
		a.flushTileContent(tileID)
	}
}

// flushTileContent posts one tile's unsaved bytes, and is the outbox's
// content thunk: every early return re-records the entry, so a drain that
// could not complete leaves it owed. During beforeunload it beacons, because
// an async save on a dying page loses a debounce window of typing.
func (a *App) flushTileContent(tileID string) {
	// A leaf link's edits save under its target's id, so the write routes to
	// the plugin that owns the bytes rather than the link row, which the
	// store refuses.
	cid := a.contentKey(tileID)
	data, dirty := a.c.DirtyContent(cid)
	if !dirty {
		a.recordContent(cid)
		return
	}
	// Still owed until a save completes: an in-flight write is an
	// unacknowledged one.
	a.recordContent(cid)
	// t may be nil: the owner row can live in a grid this client never
	// fetched. Both arms handle that.
	t := a.cachedTileByID(tileID)
	if t == nil && cid != tileID {
		t = a.cachedTileByID(cid)
	}
	if a.unloading && a.beaconTileContent(cid, t, data) {
		return
	}
	a.postTileContent(cid, t, data)
}

// postTileContent is flushTileContent's ordinary arm: the bytes go through
// the per-tile serial save queue.
func (a *App) postTileContent(cid string, t *gridwellv1.Tile, data []byte) {
	if t == nil {
		// The owner row is in no cached grid, which is not a dead end: a
		// leaf link's target may live in a grid this client never fetched.
		// Only a definitive server answer reports the orphan; a transport
		// failure retries quietly.
		if a.fetch.tileLoadFailed.Has(cid) {
			a.reportErr(errsurface.Error, "textedit",
				"unsaved text edit has no destination — its tile is no longer known")
			return
		}
		a.fetchTileByID(cid)
		return
	}
	if !rpc.TextDocument(t) || a.tileReadOnly(t) {
		return
	}
	a.enqueueTextSave(t.GridId, t.Id, cid, t.Version, data)
}

// beaconTileContent is flushTileContent's beforeunload arm. What may write,
// and with what claim, is textedit.DecideUnloadFlush's. False means the bytes
// did not leave and the caller falls back to the async post.
func (a *App) beaconTileContent(cid string, t *gridwellv1.Tile, data []byte) bool {
	basis, haveBasis := a.c.SaveBasis(cid)
	var rowVersion int64
	editable, owner := false, false
	if t != nil {
		rowVersion = t.Version
		editable = rpc.TextDocument(t) && !a.tileReadOnly(t)
		owner = t.Id == cid
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

// contentKey is rpc.ContentID for call sites that hold only an id. An
// uncached row falls back to the id itself, which is already a content id
// because entries are keyed by ContentID at write time.
func (a *App) contentKey(tileID string) string {
	if t := a.cachedTileByID(tileID); t != nil {
		return rpc.ContentID(t)
	}
	return tileID
}

// saveTextBeforeAscent posts the editor buffer and the framed window when a
// pane leaves a text descent, through the dispatcher like every other
// mutation.
func (a *App) saveTextBeforeAscent(p *pane.Pane, file *gridwellv1.Tile) {
	// url, shell and serves_page rows carry no text framing, and the server
	// rejects a non-text kind with InvalidArgument.
	if !rpc.TextDocument(file) {
		return
	}
	gid := a.gridIDForPane(p)
	r := paneRectFor(a, p)
	scrollX := int64(p.TextScrollX + 0.5)
	scrollY := int64(p.TextScrollY + 0.5)

	// Dirty-gating keeps a pure read write-free: posting unconditionally
	// would bump a merely-opened tile's version on every visit. A read-only
	// row posts no content but still posts framing, which is a node fact for
	// every text tile.
	buf, hasBuf := a.c.DirtyContent(rpc.ContentID(file))
	if a.tileReadOnly(file) {
		hasBuf = false
	}

	// Doc px, which equals screen px since scale is fixed at 1.0. The
	// parent-grid preview crops this rectangle out of the re-rendered doc.
	_, _, iw, ih := textInnerBox(r)
	viewW := int64(iw + 0.5)
	viewH := int64(ih + 0.5)

	// Patch the cache first, so the ascent transition reflects the framed
	// window before the round trip lands.
	patched := proto.CloneOf(file)
	patched.TextX = scrollX
	patched.TextY = scrollY
	patched.TextW = viewW
	patched.TextH = viewH
	patched.TextMode = p.TextMode
	a.c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
		TileChanged: &gridwellv1.TileChanged{Tile: patched}}})

	mode := p.TextMode
	// Through the document's save queue, because a debounced keystroke save
	// may still be in flight and this claims a version too. The chain is
	// textedit.SaveQueueKey's, so a leaf link's ascent flush cannot race the
	// sweep for the one basis they share.
	a.persist.textSaves.Enqueue(textedit.SaveQueueKey(file.Id, rpc.ContentID(file)), func() {
		// The fallback row is this snapshot, read above with the bytes.
		// Re-reading it here would claim a version a foreign writer may have
		// advanced since.
		if hasBuf {
			cid := rpc.ContentID(file)
			if _, ok := a.saveClaimedContent(gid, cid, file.Id == cid, file.Version, buf); !ok {
				return
			}
		}
		// Only when something changed, per textedit.FramingChanged: a pure
		// descend-and-ascent must not write.
		next := textedit.Framing{X: scrollX, Y: scrollY, W: viewW, H: viewH, Mode: mode}
		if !textedit.FramingChanged(textedit.FramingOf(file), next) {
			return
		}
		req := &gridwellv1.SetTileRequest{TileId: file.Id,
			Tile: &gridwellv1.Tile{Kind: rpc.KindText,
				TextX: scrollX, TextY: scrollY, TextW: viewW, TextH: viewH, TextMode: mode}}
		a.do(write{
			label: "SetTextView", gid: gid, id: file.Id, refetchOnOK: true,
			call: func(ctx context.Context) error {
				_, err := a.cl.SetTile(ctx, req)
				return err
			},
			beacon: func() (string, []byte, string) {
				path, body := rpc.SetTileBeacon(req)
				return path, body, rpc.BeaconJSONType
			},
		})
	})
}
