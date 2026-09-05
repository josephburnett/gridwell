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
// app is loaded in a plain browser the object is absent and the live path is
// unavailable: URL tiles show their frozen preview and nothing panics.

// bridge returns the window.gridwell object, or a zero js.Value if it isn't
// present (non-Electron host).
func bridge() js.Value {
	g := js.Global().Get("gridwell")
	if !g.Truthy() {
		return js.Value{}
	}
	return g
}

// bridgeCaps reads the host's own capability declaration
// (window.gridwell.caps): which bridge halves it implements. A bridge without
// the field is the full Electron preload. caps.Derive is the one reader.
// Shells are not on this list — the PTY rides the web door, so no host
// implements anything for it.
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
		Present: true,
		LiveURL: c.Get("liveUrl").Truthy(),
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

// bridgeCall invokes one bridge verb and routes what the promise says. Every
// verb goes through here, because every one of them can reject: the handlers
// on the other side are ipcMain.handle, and a throw there — a destroyed
// window, a view that will not attach — comes back as a rejected promise. A
// bare g.Call drops it, which is the "logs and returns" failure in its purest
// form: the wasm goes on believing a pane is live, positioned, parked or
// zoomed, with nothing on screen and nothing said. onFail runs after the
// notice, for the caller that must also undo its own optimism.
//
// Exactly one of the two arms fires: promise.then(onFulfilled, onRejected),
// the two-argument form. The chained form .then(a).catch(b) is not exclusive —
// a throw inside the fulfilled arm rejects the derived promise and runs the
// catch arm too, releasing twice and running the failure path after a success.
// Each arm releases both funcs; a per-arm defer would leak the other on every
// call.
func (a *App) bridgeCall(g js.Value, method string, args js.Value, onOK func(js.Value), onFail func()) {
	promise := g.Call(method, args)
	if promise.Type() != js.TypeObject || promise.Get("then").Type() != js.TypeFunction {
		// A host whose bridge half returns nothing: there is no verdict to
		// wait for, so treat the call as delivered.
		if onOK != nil {
			onOK(js.Undefined())
		}
		return
	}
	var then, catch js.Func
	release := func() { then.Release(); catch.Release() }
	then = js.FuncOf(func(_ js.Value, p []js.Value) any {
		defer release()
		if onOK != nil {
			res := js.Undefined()
			if len(p) > 0 {
				res = p[0]
			}
			onOK(res)
		}
		return nil
	})
	catch = js.FuncOf(func(_ js.Value, p []js.Value) any {
		defer release()
		reason := "rejected"
		if len(p) > 0 {
			reason = rejectionText(p[0])
		}
		// The same source the main process's own failures report under, so a
		// native failure reads the same whichever side noticed it.
		a.reportErr(errsurface.Error, "electron:webview", method+" failed: "+reason)
		if onFail != nil {
			onFail()
		}
		return nil
	})
	promise.Call("then", then, catch)
}

// rejectionText renders a promise rejection reason for the strip: an Error's
// message, a string as itself, anything else through String().
func rejectionText(v js.Value) string {
	if v.Type() == js.TypeObject {
		if m := v.Get("message"); m.Type() == js.TypeString {
			return m.String()
		}
	}
	if v.Type() == js.TypeString {
		return v.String()
	}
	return js.Global().Get("String").Invoke(v).String()
}

// bridgePlace asks main to create or attach a WebContentsView for paneID
// showing url at bounds. Every live view shares the one host-local session:
// there is no per-plugin partition or session key. onFail runs when main
// refuses the placement: no view exists, so the caller's live handle must go.
func (a *App) bridgePlace(paneID string, tileID, url string, b viewBounds, contentZoom float64, history string, durable, hidden, focused bool, onFail func()) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	args.Set("tileId", tileID)
	args.Set("url", url)
	args.Set("bounds", b.toJS())
	args.Set("contentZoom", contentZoom)
	args.Set("history", history)
	// hidden: this frame's gesture-hide verdict. A view placed mid-drag or
	// under the palette starts parked; the registry never guesses.
	args.Set("hidden", hidden)
	// focused: is this pane the focused pane. It rides place for the same
	// reason it rides setHidden — the renderer owns focus — and it must be
	// here rather than inferred, because a view goes live on paths that are
	// not a gesture on the focused pane at all (a workspace restore walking
	// every leaf, an ascent re-engaging every content pane, a promote landing
	// on another pane's grid). Chromium focuses the new widget as it attaches,
	// so a guess of "focused" leaks a frame of keystrokes into it.
	args.Set("focused", focused)
	// durable gates the context menu's Freeze Page: an ephemeral visit has
	// nothing to re-descend into.
	args.Set("durable", durable)
	a.bridgeCall(g, "placeWebview", args, nil, onFail)
}

// bridgeSetBounds repositions/resizes the view for paneID.
func (a *App) bridgeSetBounds(paneID string, b viewBounds) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	args.Set("bounds", b.toJS())
	a.bridgeCall(g, "setBounds", args, nil, nil)
}

// bridgeSetHidden parks and unparks the view so canvas overlays (palette,
// drag ghosts, modals) can paint where the native view would otherwise
// occlude. focused is bookkeeping for main's focus-steal guard.
func (a *App) bridgeSetHidden(paneID string, hidden, focused bool) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	args.Set("hidden", hidden)
	args.Set("focused", focused)
	a.bridgeCall(g, "setHidden", args, nil, nil)
}

// bridgeSetZoom sets the user content zoom for the live view on paneID (the
// tile's content_zoom). The main process composes it with the min-width
// layout zoom: the two multiply, and neither overwrites the other.
func (a *App) bridgeSetZoom(paneID string, zoom float64) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	args.Set("zoom", zoom)
	a.bridgeCall(g, "setZoom", args, nil, nil)
}

// bridgeRemove tears the view down and invokes onFreeze with the final
// frame (decoded JPEG bytes), url, and title so the caller can persist the
// frozen preview. onFreeze runs asynchronously when the JS promise settles;
// a missing bridge or failed capture yields empty values.
func (a *App) bridgeRemove(paneID string, onFreeze func(jpeg []byte, url, title, history string)) {
	g := bridge()
	if !g.Truthy() {
		onFreeze(nil, "", "", "")
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	a.bridgeCall(g, "removeWebview", args, func(res js.Value) {
		onFreeze(decodeBase64(res.Get("jpegBase64")), jsString(res.Get("url")),
			jsString(res.Get("title")), jsString(res.Get("history")))
	}, func() {
		// A refused teardown still has to release the caller's closure, which
		// holds the only copy of the freeze it was going to write. An empty
		// freeze is skipped by the writeback guard, so nothing overwrites a
		// good preview; bridgeCall has already surfaced the refusal.
		onFreeze(nil, "", "", "")
	})
}

// bridgeGoBack navigates the view for paneID back in its history: the bar
// slot's back button.
func (a *App) bridgeGoBack(paneID string) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	a.bridgeCall(g, "goBack", args, nil, nil)
}

// bridgeShowMenu pops the live view's context menu (Freeze Page included)
// with no in-page context — the bar circle's right-click. A page that
// hijacks contextmenu makes the in-page menu unreachable; the circle sits
// on the canvas outside the view's rect, so this door always opens.
func (a *App) bridgeShowMenu(paneID string) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	a.bridgeCall(g, "showMenu", args, nil, nil)
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
		title := jsString(ev.Get("title"))
		if url != "" {
			a.updateCachedTileURL(tileID, url)
			// Mark the live view nav-dirty so the unload beacon knows the
			// server row is behind, and remember the title (the unload path
			// cannot wait for the bridge's freeze reply).
			for _, pl := range a.locals {
				if pl.urlView != nil && pl.urlView.tileID == tileID {
					pl.urlView.navDirty = true
					pl.urlView.lastURL = url
					pl.urlView.lastTitle = title
				}
			}
			a.draw()
		}
		return nil
	})
	// A right-button press over a live URL view cannot reach the canvas,
	// because the native WebContentsView owns it, so the view's preload
	// forwards it here in canvas coords. Begin the same pane gesture the
	// canvas would, then park the view so the rest of the drag lands on the
	// canvas.
	onRightForward := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		a.onForwardedRightDown(ev.Get("x").Float(), ev.Get("y").Float())
		return nil
	})
	// A middle-button press over a live URL view is the ascend gesture; the
	// native view swallows it, so main forwards it here in canvas coords.
	onMiddleForward := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		a.onForwardedMiddleDown(ev.Get("x").Float(), ev.Get("y").Float())
		return nil
	})
	// A left-button press over a live URL view is a focus-transfer intent.
	// The native view swallows the canvas's own mousedown, so main forwards
	// it here in canvas coords. The click was not prevented in the preload —
	// in-page interaction stays with the page — so this only transfers pane
	// focus.
	onLeftForward := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		a.onForwardedLeftDown(ev.Get("x").Float(), ev.Get("y").Float())
		return nil
	})
	// The page in a live URL view tried to open a new window (target=_blank,
	// window.open, ctrl or cmd-click). Main denies the popup and forwards
	// the url here; the pane splits and the link opens as an ephemeral visit
	// in the lower half.
	onOpenBelow := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		a.openLinkBelow(jsString(ev.Get("paneId")), jsString(ev.Get("url")))
		return nil
	})
	// The user picked "Freeze Page" in a live view's context menu: freeze the
	// pane's view and store the standing intent.
	onFreezeURL := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		a.freezeURLPaneByIntent(jsString(ev.Get("paneId")))
		return nil
	})
	// The content-zoom chord was pressed while a live URL view owned OS
	// keyboard focus: the window-level keydown never fires, so main
	// intercepts the chord in before-input-event and relays it here, keyed by
	// pane. Routed through the same zoom owner as the canvas chord
	// (applyContentZoom: cache, live surface, persistence).
	onZoomKey := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		a.zoomKeyRelays++
		a.contentZoomKeyFromView(jsString(ev.Get("paneId")), jsString(ev.Get("key")))
		return nil
	})
	// The Electron main process reports every webview, session, and sidecar
	// failure it detects over this one channel. Feed it straight into the
	// error surface every other failure path uses (a.reportErr), so there is
	// one owner whether the failure originated in wasm or in the native
	// host.
	onError := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		source := jsString(ev.Get("source"))
		message := jsString(ev.Get("message"))
		a.reportErr(errsurface.Error, source, message)
		return nil
	})
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
