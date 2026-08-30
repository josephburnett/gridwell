//go:build js && wasm

package main

// The unload flush (framing-audit decisions 2026-08-13): quitting or
// reloading inside the settle window must not lose the last pan/scroll.
// Three mechanisms:
//
//   - beacons: during beforeunload a write posts through
//     navigator.sendBeacon (Chromium completes beacons after the page
//     dies) instead of a goroutine RPC that dies with the page. The
//     bodies are the exact wire form the ordinary calls send
//     (api/rpc's *Beacon helpers — one request builder, two transports).
//     WHICH transport a write takes is the dispatcher's decision now
//     (write.beacon, client/wasm/mutate.go), not each call site's.
//   - the OUTBOX drains here too (docs/simplify-plan.md S5): everything an
//     earlier outage parked — a settled viewport, a frozen face, a
//     workspace arrangement, unsaved bytes — leaves through the beacon
//     transport instead of dying with the page. Before, the unload flush
//     knew only about fresh framing and dirty text; anything parked was
//     silently lost at quit.
//   - a transition in flight persists its DESTINATION: the viewport the
//     user chose is the transition's end state, and the old flush simply
//     skipped it (the mid-animation values are presentation; the
//     destination is user state).

import (
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
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

// flushOnUnload is the beforeunload durable-state path: land the in-flight
// transition on its destination, switch the beacon transport in
// (a.unloading), run the settle-persister flush for framing the user just
// changed, DRAIN THE OUTBOX so everything already owed leaves too, and
// finally beacon the one thing neither covers — a live page's navigation
// state, which lives in the bridge, not in any ledger.
func (a *App) flushOnUnload() {
	if tr := a.transition; tr != nil && len(tr.segments) > 0 {
		if p := a.tree.FindPane(tr.paneID); p != nil {
			seg := tr.segments[len(tr.segments)-1]
			if seg.place != nil {
				p.Stack = seg.place.Clone()
			}
			p.Cx, p.Cy, p.Zoom = seg.toCx, seg.toCy, seg.toZoom
		}
		a.completeTransition()
	}
	a.unloading = true
	a.flushFramingSave()
	a.syncContentOutbox()
	for _, retry := range a.out.Drain() {
		retry()
	}
	a.flushURLStateOnUnload()
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
