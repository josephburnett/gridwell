//go:build js && wasm

package main

// The unload flush (framing-audit decisions 2026-08-13): quitting or
// reloading inside the settle window must not lose the last pan/scroll.
// Two mechanisms:
//
//   - beacons: during beforeunload every framing write posts through
//     navigator.sendBeacon (Chromium completes beacons after the page
//     dies) instead of a goroutine RPC that dies with the page. The
//     bodies are the exact wire form the ordinary calls send
//     (internal/rpc's *Beacon helpers — one request builder, two
//     transports).
//   - a transition in flight persists its DESTINATION: the viewport the
//     user chose is the transition's end state, and the old flush simply
//     skipped it (the mid-animation values are presentation; the
//     destination is user state).

import (
	"slices"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/textedit"
)

// sendBeacon posts one write so it survives the page (contentType picks
// the wire form — unary proto-JSON or the WriteContent streaming
// envelope). Returns false when the body couldn't be built or the
// browser refused (queue full) — the caller falls back to the ordinary
// async post, which MAY land; a refused beacon must not silently drop
// the write two ways.
func (a *App) sendBeacon(path string, body []byte, contentType string) bool {
	if path == "" || body == nil {
		return false
	}
	u8 := js.Global().Get("Uint8Array").New(len(body))
	js.CopyBytesToJS(u8, body)
	arr := js.Global().Get("Array").New(u8)
	opts := js.Global().Get("Object").New()
	opts.Set("type", contentType)
	blob := js.Global().Get("Blob").New(arr, opts)
	nav := js.Global().Get("navigator")
	if !nav.Truthy() || !nav.Get("sendBeacon").Truthy() {
		return false
	}
	return nav.Call("sendBeacon", a.origin+path, blob).Bool()
}

// flushOnUnload is the beforeunload durable-state path: land the
// in-flight transition on its destination, then run the framing flush
// with the beacon transport switched in (a.unloading), then beacon the
// two content-shaped leftovers the framing flush doesn't carry — dirty
// text and live pages' navigation state.
func (a *App) flushOnUnload() {
	if tr := a.transition; tr != nil && len(tr.segments) > 0 {
		if p := a.tree.FindPane(tr.paneID); p != nil {
			seg := tr.segments[len(tr.segments)-1]
			p.Path = slices.Clone(seg.path)
			p.Cx, p.Cy, p.Zoom = seg.toCx, seg.toCy, seg.toZoom
		}
		a.completeTransition()
	}
	a.unloading = true
	a.flushFramingSave()
	a.flushContentOnUnload()
	a.flushURLStateOnUnload()
}

// flushContentOnUnload beacons every dirty text body (audit #8,
// 2026-08-14): the old path enqueued async saves on a dying page, so up
// to a full debounce window of typing was reliably lost on every tab
// close — while framing had beacons all along. What may write, and with
// what claim, is textedit.DecideUnloadFlush (an UNCACHED owner row
// beacons on the SaveBasis alone — audit #6's not-a-dead-end rule); a
// refused or oversized beacon falls back to the async enqueue, which
// beats guaranteeing the loss.
func (a *App) flushContentOnUnload() {
	for _, cid := range a.c.DirtyTileIDs() {
		data, dirty := a.c.DirtyContent(cid)
		if !dirty {
			continue
		}
		t := a.cachedTileByID(cid)
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
			continue
		case textedit.UnloadAsync:
			a.flushTileContent(cid)
			continue
		}
		if path, body := rpc.WriteContentBeacon(cid, version, data); body != nil &&
			a.sendBeacon(path, body, rpc.BeaconStreamType) {
			continue
		}
		a.flushTileContent(cid)
	}
}

// flushURLStateOnUnload beacons the address (+title) a live durable page
// navigated to (audit #2, 2026-08-14): navigation state used to persist
// exactly once — at a teardown whose bridge IPC reply never arrives
// during unload — so closing the tab reverted every live url tile to its
// descent-time address. No jpeg and no history ride the beacon (the
// bridge holds both, unreachable now; the store skips empty fields, so
// the previous face and trail survive rather than being blanked).
func (a *App) flushURLStateOnUnload() {
	for _, pl := range a.locals {
		v := pl.urlView
		if v == nil || v.page || !v.durable || !v.navDirty {
			continue
		}
		url := v.lastURL
		if url == "" {
			if ct := a.cachedTileByID(v.tileID); ct != nil {
				url = ct.URLString
			}
		}
		if url == "" {
			continue
		}
		if path, body := rpc.SetURLStateBeacon(&rpc.SetURLStateRequest{
			TileID: v.tileID, URL: url, Title: v.lastTitle,
		}); body != nil {
			a.sendBeacon(path, body, rpc.BeaconJSONType)
		}
	}
}

// sendBeaconJSON adapts the two-value (path, body) unary beacon builders
// to sendBeacon's typed signature.
func (a *App) sendBeaconJSON(path string, body []byte) bool {
	return a.sendBeacon(path, body, rpc.BeaconJSONType)
}
