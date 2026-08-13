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
	"syscall/js"

	"slices"
)

// sendBeacon posts one framing write so it survives the page. Returns
// false when the body couldn't be built or the browser refused (queue
// full) — the caller falls back to the ordinary async post, which MAY
// land; a refused beacon must not silently drop the write two ways.
func (a *App) sendBeacon(path string, body []byte) bool {
	if path == "" || body == nil {
		return false
	}
	u8 := js.Global().Get("Uint8Array").New(len(body))
	js.CopyBytesToJS(u8, body)
	arr := js.Global().Get("Array").New(u8)
	opts := js.Global().Get("Object").New()
	opts.Set("type", "application/json")
	blob := js.Global().Get("Blob").New(arr, opts)
	nav := js.Global().Get("navigator")
	if !nav.Truthy() || !nav.Get("sendBeacon").Truthy() {
		return false
	}
	return nav.Call("sendBeacon", a.origin+path, blob).Bool()
}

// flushOnUnload is the beforeunload framing path: land the in-flight
// transition on its destination, then run the ordinary flush with the
// beacon transport switched in (a.unloading).
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
}
