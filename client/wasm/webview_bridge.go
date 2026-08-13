//go:build js && wasm

package main

import (
	"encoding/base64"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/errsurface"
)

// webview_bridge.go is the wasm-side door to the Electron main process's
// native URL-tile machinery, exposed by the preload script as
// window.gridwell. When Gridwell runs inside the Electron shell this object
// is present and live URL tiles render as native WebContentsViews. When the
// app is loaded in a plain browser (e.g. `make serve` for a quick look) the
// object is absent and the live path is simply unavailable — URL tiles show
// their frozen preview and nothing panics.

// bridge returns the window.gridwell object, or a zero js.Value if it isn't
// present (non-Electron host).
func bridge() js.Value {
	g := js.Global().Get("gridwell")
	if !g.Truthy() {
		return js.Value{}
	}
	return g
}

// bridgeAvailable reports whether the native webview bridge is present.
func bridgeAvailable() bool {
	return bridge().Truthy()
}

// bridgeCaps reads the host's OWN capability declaration
// (window.gridwell.caps — 2026-08-13): which bridge halves it implements.
// A bridge without the field is the legacy Electron preload, the only
// shape that ever shipped without one — it has both halves. caps.Derive
// is the one reader.
func bridgeCaps() caps.Bridge {
	g := bridge()
	if !g.Truthy() {
		return caps.NoBridge()
	}
	c := g.Get("caps")
	if !c.Truthy() {
		return caps.LegacyBridge()
	}
	return caps.Bridge{
		Present:   true,
		LiveURL:   c.Get("liveUrl").Truthy(),
		LiveShell: c.Get("liveShell").Truthy(),
	}
}

// viewBounds is a content-box rectangle in CSS px relative to the window's
// content area — exactly what panebox.ContentBox produces and what
// WebContentsView.setBounds consumes (1:1, DIP == CSS px here).
type viewBounds struct {
	X, Y, W, H float64
}

func (b viewBounds) toJS() js.Value {
	o := js.Global().Get("Object").New()
	o.Set("x", b.X)
	o.Set("y", b.Y)
	o.Set("width", b.W)
	o.Set("height", b.H)
	return o
}

// bridgePlace asks main to create/attach a WebContentsView for paneID showing
// url at bounds. Every live view shares the ONE host-local session (owner
// decision 2026-07-26 — there is no per-plugin partition or session key).
func bridgePlace(paneID string, tileID string, objectID, url string, b viewBounds, contentZoom float64, history string, durable bool) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	args.Set("tileId", tileID)
	args.Set("objectId", objectID)
	args.Set("url", url)
	args.Set("bounds", b.toJS())
	args.Set("contentZoom", contentZoom)
	args.Set("history", history)
	// durable gates the context menu's Freeze Page (issue #240): an
	// ephemeral visit has nothing to re-descend into.
	args.Set("durable", durable)
	g.Call("placeWebview", args)
}

// bridgeSetBounds repositions/resizes the view for paneID.
func bridgeSetBounds(paneID string, b viewBounds) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	args.Set("bounds", b.toJS())
	g.Call("setBounds", args)
}

// bridgeSetHidden parks/unparks the view so canvas overlays (palette, drag
// ghosts, modals) can paint where the native view would otherwise occlude.
// focused is bookkeeping for main's focus-steal guard (the per-pane corner
// control it used to show/hide is gone — issue #214).
func bridgeSetHidden(paneID string, hidden, focused bool) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	args.Set("hidden", hidden)
	args.Set("focused", focused)
	g.Call("setHidden", args)
}

// bridgeSetZoom sets the USER content zoom for the live view on paneID (the
// tile's content_zoom, issue #82). The main process composes it with the
// min-width layout zoom — the two multiply; neither overwrites the other.
func bridgeSetZoom(paneID string, zoom float64) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	args.Set("zoom", zoom)
	g.Call("setZoom", args)
}

// bridgeRemove tears the view down and invokes onFreeze with the final
// frame (decoded JPEG bytes), url, and title so the caller can persist the
// frozen preview. onFreeze runs asynchronously when the JS promise settles;
// a missing bridge or failed capture yields empty values.
func bridgeRemove(paneID string, onFreeze func(jpeg []byte, url, title, history string)) {
	g := bridge()
	if !g.Truthy() {
		onFreeze(nil, "", "", "")
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	promise := g.Call("removeWebview", args)

	var then js.Func
	then = js.FuncOf(func(_ js.Value, p []js.Value) any {
		defer then.Release()
		res := p[0]
		jpeg := decodeBase64(res.Get("jpegBase64"))
		url := jsString(res.Get("url"))
		title := jsString(res.Get("title"))
		history := jsString(res.Get("history"))
		onFreeze(jpeg, url, title, history)
		return nil
	})
	var catch js.Func
	catch = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		defer catch.Release()
		onFreeze(nil, "", "", "")
		return nil
	})
	promise.Call("then", then).Call("catch", catch)
}

// ── shell transport (2026-07-26: PTY bytes ride main's gRPC OpenShell
// stream over IPC; the WS bridge is gone) ───────────────────────────────────

// bridgeShellOpen asks main to open the OpenShell stream for a pane.
// tileID is the qualified id of the tile that OWNS the PTY session (links
// resolved by the caller). onOpen runs when main has accepted the open —
// writes invoked after this call are ordered behind it by the IPC pipe, so
// no client-side queue is needed.
func bridgeShellOpen(paneID, tileID string, cols, rows int, onOpen func()) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	o := js.Global().Get("Object").New()
	o.Set("paneId", paneID)
	o.Set("tileId", tileID)
	o.Set("cols", cols)
	o.Set("rows", rows)
	promiseThen(g.Call("shellOpen", o), onOpen)
}

// bridgeShellWrite forwards keystroke bytes (a Uint8Array) to the pane's PTY.
func bridgeShellWrite(paneID string, u8 js.Value) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	o := js.Global().Get("Object").New()
	o.Set("paneId", paneID)
	o.Set("data", u8)
	g.Call("shellWrite", o)
}

// bridgeShellResize forwards a winsize change to the pane's PTY.
func bridgeShellResize(paneID string, cols, rows int) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	o := js.Global().Get("Object").New()
	o.Set("paneId", paneID)
	o.Set("cols", cols)
	o.Set("rows", rows)
	g.Call("shellResize", o)
}

// bridgeShellClose ends the pane's stream from this side. Main suppresses
// the exit event for a local close (this side asked; the caller does its own
// teardown), so no shellExit follows.
func bridgeShellClose(paneID string) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	o := js.Global().Get("Object").New()
	o.Set("paneId", paneID)
	g.Call("shellClose", o)
}

// promiseThen runs fn when a JS promise resolves (rejections are surfaced by
// the invoke pipeline as EV.error; nothing to do here).
func promiseThen(p js.Value, fn func()) {
	if fn == nil || !p.Truthy() {
		return
	}
	var cb js.Func
	cb = js.FuncOf(func(js.Value, []js.Value) any {
		fn()
		cb.Release()
		return nil
	})
	p.Call("then", cb)
}

// bridgeGoBack navigates the view for paneID back in its history — the bar
// slot's back button (issue #214; the native corner control is gone).
func bridgeGoBack(paneID string) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	g.Call("goBack", args)
}

// bridgeShowMenu pops the live view's context menu (Freeze Page included)
// with no in-page context — the bar circle's right-click. A page that
// hijacks contextmenu makes the in-page menu unreachable; the circle sits
// on the canvas outside the view's rect, so this door always opens.
func bridgeShowMenu(paneID string) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	g.Call("showMenu", args)
}

// installWebviewListeners registers the main→renderer push handlers (frame
// mirroring and navigation). Frames update the per-tile preview cache so
// every other pane showing the tile reflects live navigation; nav events
// keep the cached tile URL current. No-op without the bridge.
func (a *App) installWebviewListeners() {
	g := bridge()
	if !g.Truthy() {
		return
	}
	onFrame := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		tileID := jsString(ev.Get("tileId"))
		jpeg := decodeBase64(ev.Get("jpegBase64"))
		if len(jpeg) > 0 {
			a.urlPreview.PutWildcard(tileID, jpeg, func() { a.draw() })
		}
		return nil
	})
	onNav := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		tileID := jsString(ev.Get("tileId"))
		url := jsString(ev.Get("url"))
		if url != "" {
			a.updateCachedTileURL(tileID, url)
			a.draw()
		}
		return nil
	})
	// A right-button press over a LIVE URL view can't reach the canvas (the
	// native WebContentsView owns it), so the view's preload forwards it here
	// in canvas coords. We begin the same pane gesture the canvas would, then
	// park the view so the rest of the drag lands on the canvas.
	onRightForward := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		a.onForwardedRightDown(ev.Get("x").Float(), ev.Get("y").Float())
		return nil
	})
	// A middle-button press over a LIVE URL view is the ascend gesture; the
	// native view swallows it, so main forwards it here in canvas coords.
	onMiddleForward := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		a.onForwardedMiddleDown(ev.Get("x").Float(), ev.Get("y").Float())
		return nil
	})
	// A left-button press over a LIVE URL view is a focus-transfer intent; the
	// native view swallows the canvas's own mousedown, so main forwards it here in
	// canvas coords. The click was NOT prevented in the preload — in-page
	// interaction stays with the page — so we only transfer pane focus here.
	onLeftForward := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		a.onForwardedLeftDown(ev.Get("x").Float(), ev.Get("y").Float())
		return nil
	})
	// The page in a LIVE URL view tried to open a NEW WINDOW (target=_blank,
	// window.open, ctrl/cmd-click). Main denies the popup and forwards the
	// url here; the pane splits and the link opens as an ephemeral visit in
	// the lower half (issue #111).
	onOpenBelow := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		a.openLinkBelow(jsString(ev.Get("paneId")), jsString(ev.Get("url")))
		return nil
	})
	// The user picked "Freeze Page" in a live view's context menu (issue
	// #237): freeze the pane's view and store the standing intent.
	onFreezeURL := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		a.freezeURLPaneByIntent(jsString(ev.Get("paneId")))
		return nil
	})
	// The content-zoom chord was pressed while a LIVE URL view owned OS
	// keyboard focus (issue #170): the window-level keydown never fires, so
	// main intercepts the chord in before-input-event and relays it here,
	// keyed by pane. Routed through the same one zoom owner as the canvas
	// chord (applyContentZoom: cache + live surface + persistence).
	onZoomKey := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		a.zoomKeyRelays++
		a.contentZoomKeyFromView(jsString(ev.Get("paneId")), jsString(ev.Get("key")))
		return nil
	})
	// The Electron main process reports every webview/session/sidecar failure
	// it detects (issue #46) over this one channel; feed it straight into the
	// same error surface every other failure path uses (a.reportErr) — one
	// owner, whether the failure originated in wasm or in the native host.
	onError := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		source := jsString(ev.Get("source"))
		message := jsString(ev.Get("message"))
		a.reportErr(errsurface.Error, source, message)
		return nil
	})
	// Shell transport pushes (2026-07-26): PTY output and unexpected stream
	// ends, routed to the pane's terminal by id.
	onShellData := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		a.onShellData(jsString(ev.Get("paneId")), ev.Get("data"))
		return nil
	})
	onShellExit := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		a.onShellExit(jsString(ev.Get("paneId")), jsString(ev.Get("message")), ev.Get("sessionGone").Bool())
		return nil
	})
	g.Call("onShellData", onShellData)
	g.Call("onShellExit", onShellExit)
	g.Call("onFrame", onFrame)
	g.Call("onNav", onNav)
	g.Call("onRightForward", onRightForward)
	g.Call("onMiddleForward", onMiddleForward)
	g.Call("onLeftForward", onLeftForward)
	g.Call("onOpenBelow", onOpenBelow)
	g.Call("onFreezeURL", onFreezeURL)
	g.Call("onZoomKey", onZoomKey)
	g.Call("onError", onError)
	// Listeners live for the lifetime of the app; no Release.
}

// decodeBase64 turns a JS base64 string value into bytes. Empty/invalid →
// nil.
func decodeBase64(v js.Value) []byte {
	s := jsString(v)
	if s == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

// jsString safely reads a JS string value, returning "" for null/undefined.
func jsString(v js.Value) string {
	if v.Type() != js.TypeString {
		return ""
	}
	return v.String()
}
