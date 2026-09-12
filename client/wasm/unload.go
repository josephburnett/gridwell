//go:build js && wasm

package main

// The unload flush: quitting or reloading inside the settle window must not
// lose the last pan or scroll. A write posts through navigator.sendBeacon,
// which Chromium completes after the page dies; which transport a write takes
// is the dispatcher's decision, not each call site's. The outbox drains here
// too, and a transition in flight persists its destination.

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/urlview"
)

// sendBeacon posts one write so it survives the page; contentType picks the
// wire form. Returns false when the body could not be built or the browser
// refused, and the caller falls back to the ordinary async post, which may
// land.
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

// flushOnUnload is the beforeunload durable-state path. It ends with a live
// page's navigation state, the one thing the settle flush and the outbox do
// not cover, because it lives in the bridge and not in any ledger.
func (a *App) flushOnUnload() {
	a.trans.CancelAll()
	a.unloading = true
	a.flushFramingSave()
	a.syncContentOutbox()
	for _, retry := range a.persist.out.Drain() {
		retry()
	}
	a.flushURLStateOnUnload()
}

// flushURLStateOnUnload beacons the address and title a live page navigated
// to, for every view urlview.DecideUnloadURLState says owns one. Persisting it
// only at teardown would lose it, since the bridge's IPC reply never arrives
// during unload. No jpeg and no history rides the beacon, because the bridge
// holds both and is unreachable now; the store skips empty fields, so the
// previous face and trail survive.
func (a *App) flushURLStateOnUnload() {
	for _, pl := range a.locals {
		v := pl.urlView
		if v == nil {
			continue
		}
		cached := ""
		if ct := a.cachedTileByID(v.tileID); ct != nil {
			cached = ct.UrlString
		}
		url, write := urlview.DecideUnloadURLState(v.page, v.durable, v.navDirty, v.lastURL, cached)
		if !write {
			continue
		}
		if path, body := rpc.SetTileBeacon(&gridwellv1.SetTileRequest{TileId: v.tileID,
			Tile: &gridwellv1.Tile{Kind: rpc.KindURL, UrlString: url, AltText: v.lastTitle},
		}); body != nil {
			a.sendBeacon(path, body, rpc.BeaconJSONType)
		}
	}
}
