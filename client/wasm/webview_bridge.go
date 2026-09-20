//go:build js && wasm

package main

import (
	"encoding/base64"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/errsurface"
)

// The wasm-side door to the Electron main process's native URL-tile
// machinery, exposed by the preload as window.gridwell. In a plain browser it
// is absent and URL tiles show their frozen preview.

// bridge returns the window.gridwell object, zero on a non-Electron host.
func bridge() js.Value {
	g := js.Global().Get("gridwell")
	if !g.Truthy() {
		return js.Value{}
	}
	return g
}

// bridgeCaps reads the host's own declaration of which bridge halves it
// implements. A missing bridge, or one declaring nothing, is a plain browser.
// Shells are not on the list: the PTY rides the web door.
func bridgeCaps() caps.Bridge {
	g := bridge()
	if !g.Truthy() {
		return caps.NoBridge()
	}
	c := g.Get("caps")
	if !c.Truthy() {
		return caps.NoBridge()
	}
	return caps.Bridge{
		LiveURL:    c.Get("liveUrl").Truthy(),
		ChoiceMenu: c.Get("choiceMenu").Truthy(),
	}
}

// viewBounds is a content-box rectangle in CSS px, what panebox.ContentBox
// produces and WebContentsView.setBounds consumes.
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
// verb goes through here because every one can reject, and a bare g.Call
// would drop that while the wasm went on believing the pane is live. onFail
// runs after the notice, for a caller that must undo its own optimism.
//
// The two-argument then(onFulfilled, onRejected) is what makes exactly one arm
// fire; chained .then(a).catch(b) would run the catch arm after a throw in
// the fulfilled one, releasing twice.
func (a *App) bridgeCall(g js.Value, method string, args js.Value, onOK func(js.Value), onFail func()) {
	promise := g.Call(method, args)
	if promise.Type() != js.TypeObject || promise.Get("then").Type() != js.TypeFunction {
		// A bridge half that returns nothing has no verdict to wait for.
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
		// The same source main's own failures report under, so a native
		// failure reads the same whichever side noticed it.
		a.reportErr(errsurface.Error, "electron:webview", method+" failed: "+reason)
		if onFail != nil {
			onFail()
		}
		return nil
	})
	promise.Call("then", then, catch)
}

// bridgeVerb is the one call shape: the fields become the verb's single
// argument object, and false says there is no bridge, which a caller whose
// promise carries a payload must answer for itself.
func (a *App) bridgeVerb(method string, fields map[string]any,
	onOK func(js.Value), onFail func()) bool {
	g := bridge()
	if !g.Truthy() {
		return false
	}
	args := js.Global().Get("Object").New()
	for k, v := range fields {
		args.Set(k, v)
	}
	a.bridgeCall(g, method, args, onOK, onFail)
	return true
}

// rejectionText renders a promise rejection reason for the strip.
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

// bridgePlace asks main to create or attach a WebContentsView for paneID, on
// the one host-local session. onFail runs when main refuses: no view exists,
// so the caller's live handle must go.
func (a *App) bridgePlace(paneID string, tileID, url string, b viewBounds, contentZoom float64, history string, durable, hidden, focused bool, onFail func()) {
	a.bridgeVerb("placeWebview", map[string]any{
		"paneId":      paneID,
		"tileId":      tileID,
		"url":         url,
		"bounds":      b.toJS(),
		"contentZoom": contentZoom,
		"history":     history,
		// This frame's gesture-hide verdict, so a view placed mid-drag or
		// under the palette starts parked. The registry never guesses.
		"hidden": hidden,
		// The renderer owns focus, and a view goes live on paths that are not
		// a gesture on the focused pane, so this cannot be inferred. Chromium
		// focuses the new widget as it attaches, so a wrong guess leaks a
		// frame of keystrokes into it.
		"focused": focused,
		// durable gates the context menu's Freeze Page, since an ephemeral
		// visit has nothing to re-descend into.
		"durable": durable,
	}, nil, onFail)
}

func (a *App) bridgeSetBounds(paneID string, b viewBounds) {
	a.bridgeVerb("setBounds", map[string]any{"paneId": paneID, "bounds": b.toJS()}, nil, nil)
}

// bridgeSetHidden parks and unparks the view so canvas overlays can paint
// where the native view would occlude. focused feeds main's focus-steal
// guard.
func (a *App) bridgeSetHidden(paneID string, hidden, focused bool) {
	a.bridgeVerb("setHidden",
		map[string]any{"paneId": paneID, "hidden": hidden, "focused": focused}, nil, nil)
}

// bridgeSetZoom sets the tile's content_zoom on the live view. Main composes
// it with the min-width layout zoom by multiplying, so neither overwrites the
// other.
func (a *App) bridgeSetZoom(paneID string, zoom float64) {
	a.bridgeVerb("setZoom", map[string]any{"paneId": paneID, "zoom": zoom}, nil, nil)
}

// bridgeRemove tears the view down and hands onFreeze the final frame, url
// and title. A missing bridge or failed capture yields empty values.
func (a *App) bridgeRemove(paneID string, onFreeze func(jpeg []byte, url, title, history string)) {
	ok := a.bridgeVerb("removeWebview", map[string]any{"paneId": paneID}, func(res js.Value) {
		jpeg, ok := decodeBase64(res.Get("jpegBase64"))
		if !ok {
			// The tile keeps the face it had, and the user is told why the
			// page they just left is not on it.
			a.reportErr(errsurface.Error, "electron:webview", "the final frame of "+paneID+" was unreadable — the tile keeps its old face")
		}
		onFreeze(jpeg, jsString(res.Get("url")),
			jsString(res.Get("title")), jsString(res.Get("history")))
	}, func() {
		// A refused teardown still releases the caller's closure, which holds
		// the only copy of the freeze. An empty freeze is skipped, so nothing
		// overwrites a good preview.
		onFreeze(nil, "", "", "")
	})
	if !ok {
		onFreeze(nil, "", "", "")
	}
}

// bridgeGoBack is the bar slot's back button.
func (a *App) bridgeGoBack(paneID string) {
	a.bridgeVerb("goBack", map[string]any{"paneId": paneID}, nil, nil)
}

// bridgeShowMenu pops the live view's context menu from the bar circle's
// right-click. A page that hijacks contextmenu makes the in-page menu
// unreachable, and the circle sits outside the view's rect.
func (a *App) bridgeShowMenu(paneID string) {
	a.bridgeVerb("showMenu", map[string]any{"paneId": paneID}, nil, nil)
}

// installWebviewListeners registers the main-to-renderer push handlers.
// Frames update the per-tile preview cache, so every other pane showing the
// tile reflects live navigation.
func (a *App) installWebviewListeners() {
	g := bridge()
	if !g.Truthy() {
		return
	}
	for _, l := range []struct {
		name string
		fn   func(ev js.Value)
	}{
		{"onFrame", func(ev js.Value) {
			// An unreadable preview frame is the same as none: the next one
			// replaces it, and the tile keeps the face it had until then.
			if jpeg, _ := decodeBase64(ev.Get("jpegBase64")); len(jpeg) > 0 {
				a.views.urlPreview.PutWildcard(jsString(ev.Get("tileId")), jpeg, func() { a.draw() })
			}
		}},
		{"onNav", func(ev js.Value) {
			tileID := jsString(ev.Get("tileId"))
			url := jsString(ev.Get("url"))
			title := jsString(ev.Get("title"))
			if url == "" {
				return
			}
			a.updateCachedTileURL(tileID, url)
			// The unload beacon reads navDirty and lastTitle, because it
			// cannot wait for the bridge's freeze reply.
			for _, pl := range a.locals {
				if pl.urlView != nil && pl.urlView.tileID == tileID {
					pl.urlView.navDirty = true
					pl.urlView.lastURL = url
					pl.urlView.lastTitle = title
				}
			}
			a.draw()
		}},
		// The native view owns the press, so the preload forwards it in
		// canvas coords.
		{"onRightForward", func(ev js.Value) {
			a.onForwardedRightDown(ev.Get("x").Float(), ev.Get("y").Float())
		}},
		// The ascend gesture, which the native view swallows, forwarded in
		// canvas coords.
		{"onMiddleForward", func(ev js.Value) {
			a.onForwardedMiddleDown(ev.Get("x").Float(), ev.Get("y").Float())
		}},
		// A focus-transfer intent. The preload did not prevent the click, so
		// in-page interaction stays with the page.
		{"onLeftForward", func(ev js.Value) {
			a.onForwardedLeftDown(ev.Get("x").Float(), ev.Get("y").Float())
		}},
		// The page tried to open a new window. Main denies the popup and
		// forwards the url, and the link opens as an ephemeral visit below.
		{"onOpenBelow", func(ev js.Value) {
			a.openLinkBelow(jsString(ev.Get("paneId")), jsString(ev.Get("url")))
		}},
		// "Freeze Page" in a live view's context menu.
		{"onFreezeURL", func(ev js.Value) {
			a.freezeURLPaneByIntent(jsString(ev.Get("paneId")))
		}},
		// The view swallows a plain right-press, so this is the only signal
		// that focus must move to the pane the menu acts in, before it can
		// act.
		{"onContextMenu", func(ev js.Value) {
			a.onForwardedContextMenu(jsString(ev.Get("paneId")))
		}},
		// The window-level keydown never fires while a live view owns OS
		// keyboard focus, so main relays the chord keyed by pane.
		{"onZoomKey", func(ev js.Value) {
			a.zoomKeyRelays++
			a.contentZoomKeyFromView(jsString(ev.Get("paneId")), jsString(ev.Get("key")))
		}},
		// Main reports every webview, session and sidecar failure over this
		// one channel, into the same error surface every other failure path
		// uses.
		{"onError", func(ev js.Value) {
			a.reportErr(errsurface.Error, jsString(ev.Get("source")), jsString(ev.Get("message")))
		}},
	} {
		fn := l.fn
		// Listeners live for the lifetime of the app, so no Release.
		g.Call(l.name, js.FuncOf(func(_ js.Value, p []js.Value) any {
			fn(p[0])
			return nil
		}))
	}
}

// decodeBase64 reads a bridge frame. ok is false only for bytes that are
// not base64: an absent frame is nil and ok, since the bridge sends none for
// a page that never painted.
func decodeBase64(v js.Value) ([]byte, bool) {
	s := jsString(v)
	if s == "" {
		return nil, true
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, false
	}
	return b, true
}

func jsString(v js.Value) string {
	if v.Type() != js.TypeString {
		return ""
	}
	return v.String()
}
