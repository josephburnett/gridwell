//go:build js && wasm

package main

import (
	"encoding/base64"
	"syscall/js"
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

// bridgePlace asks main to create/attach a WebContentsView for paneID
// showing url on the tile's persistent session partition, at bounds.
func bridgePlace(paneID string, tileID int64, objectID, url string, b viewBounds) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	args.Set("tileId", float64(tileID))
	args.Set("objectId", objectID)
	args.Set("url", url)
	args.Set("bounds", b.toJS())
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
func bridgeSetHidden(paneID string, hidden bool) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	args := js.Global().Get("Object").New()
	args.Set("paneId", paneID)
	args.Set("hidden", hidden)
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
		tileID := int64(ev.Get("tileId").Float())
		jpeg := decodeBase64(ev.Get("jpegBase64"))
		if len(jpeg) > 0 {
			a.urlPreview.PutWildcard(tileID, jpeg, func() { a.draw() })
		}
		return nil
	})
	onNav := js.FuncOf(func(_ js.Value, p []js.Value) any {
		ev := p[0]
		tileID := int64(ev.Get("tileId").Float())
		url := jsString(ev.Get("url"))
		if url != "" {
			a.updateCachedTileURL(tileID, url)
			a.draw()
		}
		return nil
	})
	g.Call("onFrame", onFrame)
	g.Call("onNav", onNav)
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
