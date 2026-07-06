//go:build js && wasm

package main

import (
	"encoding/base64"
	"github.com/josephburnett/gridwell/internal/rpc"
	"syscall/js"

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
// url at bounds, bound to the owning plugin's session partition (pluginUUID is
// the session boundary). proxyEndpoint ("" = direct) is the grid-stamped
// network context — a remote plugin's tiles browse through the tunnel SOCKS.
func bridgePlace(paneID string, tileID string, objectID, url string, b viewBounds, pluginUUID, proxyEndpoint string) {
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
	args.Set("pluginUuid", pluginUUID)
	args.Set("proxyEndpoint", proxyEndpoint)
	g.Call("placeWebview", args)
}

// pluginUUIDOf returns the SESSION KEY for a qualified tile id: the id's
// namespace chain (everything before the last segment — rpc.NamespaceOf).
// The plugin is the session boundary even through a node mount, so a local
// tile "uuid/7" keys to "uuid" and a remote tile "ssh1/rp1/7" keys to
// "ssh1/rp1" — per REMOTE plugin, not per mount. The Electron side derives
// the partition name from it and addresses GET/PUT /session/<chain>, which
// routes one segment per hop.
func pluginUUIDOf(qualifiedTileID string) string {
	return rpc.NamespaceOf(qualifiedTileID)
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
// focused additionally drives the corner control's visibility: only the
// focused pane shows its back/ascend circle, so the menu handle is on exactly
// one pane at a time (the native control can't honor the canvas focused-only
// rule, since it paints above the canvas).
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

// bridgeRemove tears the view down and invokes onFreeze with the final
// frame (decoded JPEG bytes), url, and title so the caller can persist the
// frozen preview. onFreeze runs asynchronously when the JS promise settles;
// a missing bridge or failed capture yields empty values.
func bridgeRemove(paneID string, onFreeze func(jpeg []byte, url, title string)) {
	g := bridge()
	if !g.Truthy() {
		onFreeze(nil, "", "")
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
		onFreeze(jpeg, url, title)
		return nil
	})
	var catch js.Func
	catch = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		defer catch.Release()
		onFreeze(nil, "", "")
		return nil
	})
	promise.Call("then", then).Call("catch", catch)
}

// bridgeGoBack navigates the view for paneID back in its history.
func bridgeGoBack(paneID string) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	g.Call("goBack", args)
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
	// The live URL tile's corner button is a native overlay view; a
	// right/middle click on it routes here so the ascent (which freezes the
	// tile + tears the view down) runs in the renderer like any other ascend.
	onControlAscend := js.FuncOf(func(_ js.Value, p []js.Value) any {
		paneID := jsString(p[0].Get("paneId"))
		if fp := a.tree.FindPane(paneID); fp != nil {
			a.ascendPane(fp)
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
	g.Call("onFrame", onFrame)
	g.Call("onNav", onNav)
	g.Call("onControlAscend", onControlAscend)
	g.Call("onRightForward", onRightForward)
	g.Call("onMiddleForward", onMiddleForward)
	g.Call("onLeftForward", onLeftForward)
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
