//go:build js && wasm

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/shellconn"
	"github.com/josephburnett/gridwell/client/urlnorm"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// shellStreamConn is one ShellStream WebSocket + its xterm.js host.
// A DOM container holds the terminal instance; the container's CSS is
// positioned per-frame so it tracks the pane through resize / pan /
// split-tree edits. js.Func handlers are tracked so we Release them on
// close (FuncOf-allocated callbacks pin Go memory until released).
type shellStreamConn struct {
	ws          js.Value
	term        js.Value // xterm.Terminal
	fitAddon    js.Value // FitAddon — proposeDimensions + fit
	renderAddon js.Value // renderer addon: WebglAddon, or CanvasAddon fallback
	container   js.Value // host <div> in the DOM
	circle      js.Value // corner ascend handle painted above the terminal

	tileID string
	paneID string
	// anchor + path are the plugin-root grid id and the descent path to the
	// grid that holds this shell tile, captured when the stream opened. The
	// freeze (SetShellPreview) needs them to resolve this tile's leaf grid —
	// same contract as urlView.anchor/path; without them the writeback
	// resolves against the plugin ROOT grid and fails for any shell inside a
	// descended sub-grid (issue #77).
	anchor string
	path   []string

	onMessage      js.Func
	onOpen         js.Func
	onClose        js.Func
	onError        js.Func
	onData         js.Func // term.onData(bytes string) callback
	onResize       js.Func // term.onResize({cols, rows})
	onMouse        js.Func // container right-button → canvas gesture pipeline
	onLinkProvide  js.Func // xterm link provider: scans lines for http(s) urls
	onLinkActivate js.Func // shared link click handler → ephemeral url descent
	onOSCURL       js.Func // OSC 5522 from the gridwell-open shim → ephemeral url descent

	pending []any // queued sends until WS is OPEN
	closed  bool

	lastCols, lastRows uint16
}

// shellLog writes a tagged debug message to the browser console.
func shellLog(format string, args ...any) {
	msg := "[shellstream] " + fmt.Sprintf(format, args...)
	js.Global().Get("console").Call("log", msg)
}

// isShellDescent reports whether pane p is currently descended into a
// shell tile. The input layer uses this to switch from Gridwell-native
// gestures to PTY input forwarding (and to gate ascent on a freeze
// capture).
func (a *App) isShellDescent(p *pane.Pane) bool {
	if p == nil || p.TextFocus == "" {
		return false
	}
	gid := a.gridIDForPane(p)
	g, ok := a.c.Grid(gid)
	if !ok {
		return false
	}
	t, ok := g.Tiles[p.TextFocus]
	if !ok {
		return false
	}
	return t.Kind == rpc.KindShell
}

// hasShellStream reports whether a live shell stream is attached to
// the given pane. Drives the "frozen vs live" UI distinction.
func (a *App) hasShellStream(paneID string) bool {
	return a.shellConnFor(paneID) != nil
}

// shellRefreshButtonVisible decides whether the lower-right refresh
// button should paint on a frozen shell descent. The rule (per the
// shell-tile design):
//
//   - tile.PreviewBlobID == 0  → fresh tile, refresh creates a new
//     tmux session. Always show the button.
//   - tmux session is alive    → refresh attaches. Show the button.
//   - cached as not alive      → no recovery possible. Hide.
//   - unknown (probe pending)  → hide; the probe is kicked off here
//     and a redraw fires when the result lands.
//
// Side effect: kicks off a ShellSessionAlive probe when the alive
// state for tileID isn't cached and no probe is already in flight.
func (a *App) shellRefreshButtonVisible(tile *rpc.Tile) bool {
	if tile == nil {
		return false
	}
	alive, known := a.shellAlive[tile.ID]
	v := shellconn.DecideShellRefreshVisible(
		tile.Kind == rpc.KindShell, tile.PreviewBlobID != 0, known, alive)
	if v.Probe {
		a.probeShellSessionAlive(tile.ID)
	}
	return v.Show
}

// probeShellSessionAlive fires ShellSessionAlive RPC for tileID,
// caches the result, and triggers a redraw on completion. Idempotent:
// short-circuits if a probe is already in flight.
func (a *App) probeShellSessionAlive(tileID string) {
	if a.shellAliveProbing[tileID] {
		return
	}
	a.shellAliveProbing[tileID] = true
	go func() {
		res, err := a.cl.ShellSessionAlive(context.Background(), &rpc.ShellSessionAliveRequest{TileID: tileID})
		// Probing flag clears regardless so a future probe can retry.
		delete(a.shellAliveProbing, tileID)
		if err != nil {
			shellLog("ShellSessionAlive tile=%s err=%v", tileID, err)
			// Without a verdict the refresh control just doesn't appear —
			// distinguish "probe failed" from "session gone" (charter §6).
			a.reportErr(errsurface.Error, "shell", "shell session probe failed: "+rpcErrText(err))
			return
		}
		a.shellAlive[tileID] = res.Alive
		a.draw()
	}()
}

// setShellAlive overrides the cached probe result for tileID. Used
// when the wasm has firsthand knowledge: a successful WS attach
// means the session IS alive; a WS rejection with PolicyViolation
// means it ISN'T. Triggers a redraw.
func (a *App) setShellAlive(tileID string, alive bool) {
	cur, ok := a.shellAlive[tileID]
	a.shellAlive[tileID] = alive
	if !ok || cur != alive {
		a.draw()
	}
}

// openShellStream mounts an xterm.js terminal in a DOM overlay over
// the pane's content area, opens a WebSocket to /rpc/ShellStream, and
// wires the two together. Idempotent: a second call for the same pane
// closes the previous attachment first.
func (a *App) openShellStream(p *pane.Pane, tileID string) {
	a.closeShellStream(p.ID, true)

	doc := js.Global().Get("document")
	container := doc.Call("createElement", "div")
	style := container.Get("style")
	style.Set("position", "absolute")
	style.Set("display", "block")
	style.Set("background", "#0c0d11")
	style.Set("zIndex", "5")
	style.Set("overflow", "hidden")
	// Sized + positioned by syncShellOverlayPosition each frame; start
	// off-screen so we don't flash a 0x0 terminal during the descent
	// transition.
	style.Set("left", "-9999px")
	style.Set("top", "-9999px")
	style.Set("width", "300px")
	style.Set("height", "200px")
	doc.Get("body").Call("appendChild", container)

	// The overlay paints above the canvas and would otherwise swallow the
	// right-button mousedown that starts a pane gesture — so a split/clone/
	// resize could only be begun from the thin border ring. Forward the
	// right button into the same canvas gesture pipeline every other tile
	// uses; the left button stays with xterm (typing / caret / selection)
	// except for its pane-focus side effect, handled inline below.
	// Once onMouseDown sets a right gesture, draw() parks this overlay
	// (liveOverlaysHidden), so the rest of the drag lands on the canvas.
	onMouse := js.FuncOf(func(_ js.Value, args []js.Value) any {
		ev := args[0]
		if ev.Get("type").String() == "contextmenu" {
			ev.Call("preventDefault")
			return nil
		}
		if ev.Get("button").Int() != 2 {
			// Left/middle stay with xterm (typing, caret, selection), but pane
			// focus must still follow the click: the overlay swallows the
			// mousedown, so the canvas path that normally transfers focus never
			// runs. Without this, clicking into a terminal from another pane
			// leaves Gridwell focus behind — and the focus-gated ascend circle
			// stays hidden over the shell you are typing in (issue #78). Same
			// contract as the URL view's forwarded VIEW_LEFTDOWN.
			if cur := a.tree.FindPane(p.ID); cur != nil {
				a.focusToPane(cur)
			}
			return nil
		}
		ev.Call("preventDefault")
		ev.Call("stopPropagation")
		a.onMouseDown(js.Null(), args)
		// onRightDown arms the gesture but doesn't redraw for a
		// split/swap/resize. Park the overlay now (it consults
		// liveOverlaysHidden) so the rest of the drag — every mousemove and
		// the mouseup — lands on the canvas instead of this still-visible
		// div, which we don't forward.
		a.draw()
		return nil
	})
	// Capture phase so we win over xterm's own inner listeners.
	container.Call("addEventListener", "mousedown", onMouse, true)
	container.Call("addEventListener", "contextmenu", onMouse, true)

	// xterm.Terminal({...}). Options are tuned to match Gridwell's
	// dark palette and the same monospace stack used elsewhere.
	Terminal := js.Global().Get("Terminal")
	if !Terminal.Truthy() {
		shellLog("xterm.Terminal not loaded; index.html missing script tag?")
		doc.Get("body").Call("removeChild", container)
		return
	}
	opts := js.Global().Get("Object").New()
	opts.Set("fontFamily", `ui-monospace, "SF Mono", Menlo, Consolas, monospace`)
	// Base font scaled by the tile's persisted content zoom (issue #82), so a
	// zoomed terminal comes back at your size on every descent.
	fontSize := int(shellBaseFontPx)
	if t := a.findTileByID(tileID); t != nil {
		fontSize = int(shellBaseFontPx*contentZoomOf(t) + 0.5)
	}
	opts.Set("fontSize", fontSize)
	opts.Set("convertEol", true)
	opts.Set("cursorBlink", true)
	// Dark theme tuned to the shell-fill / markdown-body backgrounds.
	theme := js.Global().Get("Object").New()
	theme.Set("background", "#0c0d11")
	theme.Set("foreground", "#d8d9de")
	theme.Set("cursor", "#c87a5a")
	opts.Set("theme", theme)
	term := Terminal.New(opts)

	// Fit addon resizes the buffer dimensions to fit the container, which we
	// then mirror to the server. The renderer addon (WebGL, canvas fallback)
	// is attached AFTER open — the WebGL addon requires an opened terminal.
	fitAddon := js.Global().Get("FitAddon").Get("FitAddon").New()
	term.Call("loadAddon", fitAddon)
	term.Call("open", container)
	renderAddon := attachShellRenderer(term)

	// Corner ascend handle, appended after the terminal so it paints on top of
	// the opaque xterm canvas (a canvas-drawn circle can't, hence the URL tile's
	// native control and this DOM twin). syncShellOverlayPosition shows it only
	// on the focused pane.
	circle := newShellCircle(doc)
	container.Call("appendChild", circle)

	loc := js.Global().Get("location")
	proto := "ws:"
	if loc.Get("protocol").String() == "https:" {
		proto = "wss:"
	}
	host := loc.Get("host").String()
	// Initial size — fit addon will overwrite, but the URL params let
	// the server start the PTY at the right dimensions.
	cols := term.Get("cols").Int()
	rows := term.Get("rows").Int()
	wsURL := proto + "//" + host + "/rpc/ShellStream?tile_id=" +
		tileID +
		"&cols=" + strconv.Itoa(cols) + "&rows=" + strconv.Itoa(rows)
	shellLog("open pane=%s tile=%s url=%s cols=%d rows=%d", p.ID, tileID, wsURL, cols, rows)

	ws := js.Global().Get("WebSocket").New(wsURL)
	ws.Set("binaryType", "arraybuffer")

	conn := &shellStreamConn{
		ws:          ws,
		term:        term,
		fitAddon:    fitAddon,
		renderAddon: renderAddon,
		container:   container,
		circle:      circle,
		tileID:      tileID,
		paneID:      p.ID,
		anchor:      p.Anchor,
		path:        slices.Clone(p.Path),
		onMouse:     onMouse,
		lastCols:    uint16(cols),
		lastRows:    uint16(rows),
	}

	// Make http(s) URLs in the terminal clickable: a click descends into the
	// url as an ephemeral visit (the shell goes inactive; ascending returns to
	// it). Uses xterm's built-in registerLinkProvider — no addon. One shared
	// activate func (xterm passes the link's text as arg 1) so there are no
	// per-link js.Func allocations to leak. Released in releaseShellStream.
	conn.onLinkActivate = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) >= 2 && args[1].Type() == js.TypeString {
			a.shellURLActivate(p.ID, args[1].String())
		}
		return nil
	})
	conn.onLinkProvide = js.FuncOf(func(_ js.Value, args []js.Value) any {
		// (bufferLineNumber 1-based, callback). Resolve the line text, find any
		// urls, and hand xterm a link (1-based inclusive cell range) per url.
		if len(args) < 2 {
			return nil
		}
		lineNo, cb := args[0].Int(), args[1]
		line := term.Get("buffer").Get("active").Call("getLine", lineNo-1)
		if !line.Truthy() {
			cb.Invoke(js.Undefined())
			return nil
		}
		spans := urlnorm.FindURLs(line.Call("translateToString", true).String())
		if len(spans) == 0 {
			cb.Invoke(js.Undefined())
			return nil
		}
		links := js.Global().Get("Array").New()
		for _, s := range spans {
			start := js.Global().Get("Object").New()
			start.Set("x", s.Col0)
			start.Set("y", lineNo)
			end := js.Global().Get("Object").New()
			end.Set("x", s.Col1)
			end.Set("y", lineNo)
			rng := js.Global().Get("Object").New()
			rng.Set("start", start)
			rng.Set("end", end)
			link := js.Global().Get("Object").New()
			link.Set("range", rng)
			link.Set("text", s.URL)
			link.Set("activate", conn.onLinkActivate)
			links.Call("push", link)
		}
		cb.Invoke(links)
		return nil
	})
	provider := js.Global().Get("Object").New()
	provider.Set("provideLinks", conn.onLinkProvide)
	term.Call("registerLinkProvider", provider)

	// OSC 5522: the gridwell-open browser shim ($BROWSER in every session,
	// internal/tmux) hands back a url a terminal app tried to open — emacs
	// browse-url, xdg-open — so it descends into an ephemeral url tile here
	// instead of spawning a browser on the host (issue #90). The sequence
	// rides the PTY byte stream (tmux passthrough → WS → term.write), so it
	// works unchanged for remote shells through ssh mounts.
	conn.onOSCURL = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) >= 1 && args[0].Type() == js.TypeString {
			a.shellURLActivate(p.ID, args[0].String())
		}
		return true // consumed
	})
	term.Get("parser").Call("registerOscHandler", 5522, conn.onOSCURL)

	conn.onMessage = js.FuncOf(func(_ js.Value, args []js.Value) any {
		data := args[0].Get("data")
		switch data.Type() {
		case js.TypeObject:
			// Binary frame: PTY output bytes. Hand straight to xterm
			// as a Uint8Array — its write() accepts that shape.
			u8 := js.Global().Get("Uint8Array").New(data)
			conn.term.Call("write", u8)
		case js.TypeString:
			// V1 has no server→client text messages; log for debug.
			shellLog("text recv pane=%s body=%q", conn.paneID, data.String())
		}
		return nil
	})
	conn.onOpen = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		pending := conn.pending
		conn.pending = nil
		shellLog("onOpen pane=%s tile=%s flushing=%d", p.ID, tileID, len(pending))
		for _, q := range pending {
			conn.ws.Call("send", q)
		}
		return nil
	})
	conn.onClose = js.FuncOf(func(_ js.Value, args []js.Value) any {
		code := -1
		reason := ""
		if len(args) > 0 {
			code = args[0].Get("code").Int()
			reason = args[0].Get("reason").String()
		}
		shellLog("onClose pane=%s tile=%s code=%d reason=%q", p.ID, tileID, code, reason)
		// PolicyViolation (1008) is the server's definitive "session is
		// gone" signal — flip the cache to dead so the refresh button
		// hides. Any other close code (normal teardown, 1006 abnormal,
		// transport error) carries no liveness guarantee — an abnormal
		// close commonly means the attach itself failed against a dead
		// session — so drop any cached liveness and let the next render
		// re-probe the authoritative server rather than assuming alive.
		if shellconn.SessionDeadOnClose(code) {
			a.setShellAlive(tileID, false)
		} else if _, ok := a.shellAlive[tileID]; ok {
			delete(a.shellAlive, tileID)
			a.draw()
		}
		a.releaseShellStream(p.ID, conn)
		a.draw()
		return nil
	})
	conn.onError = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		shellLog("onError pane=%s tile=%s readyState=%d", p.ID, tileID, conn.ws.Get("readyState").Int())
		// The terminal connection just broke under the user's prompt; the
		// onClose path handles liveness, this surfaces the why.
		a.reportErr(errsurface.Error, "shell", "shell connection error")
		return nil
	})

	// xterm → WS: bytes from the user's keypresses, IME, paste.
	conn.onData = js.FuncOf(func(_ js.Value, args []js.Value) any {
		// args[0] is a JS string of bytes; convert to a Uint8Array for
		// binary send so terminal control sequences (arrow keys, ESC)
		// don't get mangled by UTF-8 reinterpretation.
		s := args[0].String()
		u8 := jsBytes(s)
		conn.sendBinary(u8)
		return nil
	})
	term.Call("onData", conn.onData)

	conn.onResize = js.FuncOf(func(_ js.Value, args []js.Value) any {
		sz := args[0]
		cols := uint16(sz.Get("cols").Int())
		rows := uint16(sz.Get("rows").Int())
		if cols == conn.lastCols && rows == conn.lastRows {
			return nil
		}
		conn.lastCols, conn.lastRows = cols, rows
		payload, _ := json.Marshal(rpc.ShellStreamMessage{Kind: "resize", Cols: cols, Rows: rows})
		conn.sendText(string(payload))
		return nil
	})
	term.Call("onResize", conn.onResize)

	ws.Call("addEventListener", "message", conn.onMessage)
	ws.Call("addEventListener", "open", conn.onOpen)
	ws.Call("addEventListener", "close", conn.onClose)
	ws.Call("addEventListener", "error", conn.onError)

	a.local(p.ID).shellConn = conn
	// First sync: position over the pane content area now that we know
	// the rect.
	a.syncShellOverlayPosition()
	// Focus so keystrokes land in the terminal rather than the canvas.
	term.Call("focus")
}

// newShellCircle builds the corner ascend handle for a live shell overlay: a
// small circle painted above the terminal at the same lower-right spot as the
// canvas + / refresh button. Geometry: the canvas button centers PlusInset (24)
// from the pane's bottom-right with PlusRadius (18); the shell container is
// inset by the 1px pane border, so a 36px box pinned right/bottom 5px lands the
// circle exactly over the geometric pointInPlus hit region.
//
// It is purely visual (pointer-events: none): the right-button press that arms
// the ascend gesture is forwarded by the container's capture-phase listener and
// the hit-test is geometric, so the circle adds no event handling — it just
// makes the always-present ascend handle visible over the opaque terminal,
// which a canvas-drawn circle (occluded by the xterm overlay) can't.
func newShellCircle(doc js.Value) js.Value {
	c := doc.Call("createElement", "div")
	c.Set("className", "gw-shell-ascend") // stable hook for e2e visibility asserts
	s := c.Get("style")
	s.Set("position", "absolute")
	s.Set("right", "5px")
	s.Set("bottom", "5px")
	s.Set("width", "36px")
	s.Set("height", "36px")
	s.Set("boxSizing", "border-box")
	s.Set("borderRadius", "50%")
	s.Set("background", colorPlusBg)
	s.Set("border", "1px solid "+colorPaneBorder)
	s.Set("color", colorPlusFg)
	s.Set("display", "none") // shown by syncShellOverlayPosition when focused
	s.Set("alignItems", "center")
	s.Set("justifyContent", "center")
	s.Set("font", "16px/1 sans-serif")
	s.Set("pointerEvents", "none")
	s.Set("zIndex", "6")
	// Up chevron: the corner is the "go up / out" (ascend) handle.
	c.Set("textContent", "⌃")
	return c
}

// attachShellRenderer gives the opened terminal a GPU renderer: the WebGL
// addon, falling back to the legacy canvas addon when WebGL2 is unavailable
// (headless/xvfb without GL, an exotic browser in web mode) or when the GPU
// context is later lost. The canvas renderer's dirty-region tracking misses
// cursor-addressed rewrites and scroll-region scrolls — stale/misaligned
// glyphs until a resize forces a full repaint (issue #84); the WebGL renderer
// redraws from buffer state every frame, so that artifact class cannot occur.
// Returns the active addon (held on shellStreamConn to pin it; the terminal's
// dispose tears it down).
func attachShellRenderer(term js.Value) js.Value {
	if addon, ok := tryWebglAddon(term); ok {
		return addon
	}
	return loadCanvasAddon(term)
}

// tryWebglAddon loads the WebGL renderer, reporting ok=false when the addon
// is missing or throws (no WebGL2 context available). Constructed with
// preserveDrawingBuffer=true so the freeze capture's toDataURL reads real
// pixels rather than a cleared back buffer.
func tryWebglAddon(term js.Value) (addon js.Value, ok bool) {
	defer func() {
		if recover() != nil { // loadAddon throws when WebGL2 is unavailable
			shellLog("webgl renderer unavailable; using canvas renderer")
			addon, ok = js.Value{}, false
		}
	}()
	ns := js.Global().Get("WebglAddon")
	if !ns.Truthy() {
		return js.Value{}, false
	}
	a := ns.Get("WebglAddon").New(true) // preserveDrawingBuffer
	term.Call("loadAddon", a)
	// A lost GPU context would leave the terminal frozen mid-session: dispose
	// the webgl addon and continue on the canvas renderer in place. One-shot;
	// the callback releases itself.
	var lossCb js.Func
	lossCb = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		shellLog("webgl context lost; falling back to canvas renderer")
		a.Call("dispose")
		loadCanvasAddon(term)
		lossCb.Release()
		return nil
	})
	a.Call("onContextLoss", lossCb)
	return a, true
}

// loadCanvasAddon attaches the legacy canvas renderer.
func loadCanvasAddon(term js.Value) js.Value {
	a := js.Global().Get("CanvasAddon").Get("CanvasAddon").New()
	term.Call("loadAddon", a)
	return a
}

// shellContentCanvas returns the canvas the terminal CONTENT is painted on —
// NOT merely the first canvas in the container. The WebGL renderer's main
// canvas is class-less while its link layer (transparent, glyph-free) comes
// first in the DOM; capturing the first canvas produced an all-black preview.
// The canvas renderer paints glyphs on its class="xterm-text-layer" canvas.
func shellContentCanvas(container js.Value) js.Value {
	list := container.Call("querySelectorAll", "canvas")
	n := list.Get("length").Int()
	var textLayer, first js.Value
	for i := 0; i < n; i++ {
		c := list.Call("item", i)
		if !first.Truthy() {
			first = c
		}
		cls := c.Get("className").String()
		if cls == "" {
			return c // the WebGL main canvas
		}
		if cls == "xterm-text-layer" && !textLayer.Truthy() {
			textLayer = c
		}
	}
	if textLayer.Truthy() {
		return textLayer
	}
	if first.Truthy() {
		return first
	}
	return js.Value{}
}

// jsBytes converts a Go-side raw byte string to a JS Uint8Array. Used
// to forward xterm's onData payload as binary so terminal control
// bytes survive untouched.
func jsBytes(s string) js.Value {
	b := []byte(s)
	u8 := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(u8, b)
	return u8
}

// sendBinary forwards a Uint8Array on the WebSocket, queueing if the
// socket is still CONNECTING.
func (c *shellStreamConn) sendBinary(u8 js.Value) {
	switch shellconn.ShellSendAction(c.ws.Get("readyState").Int()) {
	case shellconn.WSQueue:
		c.pending = append(c.pending, u8)
	case shellconn.WSSend:
		c.ws.Call("send", u8)
	}
}

// sendText forwards a string on the WebSocket. Used for control
// messages (resize) that go as text frames per the wire protocol.
func (c *shellStreamConn) sendText(s string) {
	switch shellconn.ShellSendAction(c.ws.Get("readyState").Int()) {
	case shellconn.WSQueue:
		c.pending = append(c.pending, s)
	case shellconn.WSSend:
		c.ws.Call("send", s)
	}
}

// shellMirrorIntervalMs is how often a live shell terminal is snapshotted and
// pushed to the shared preview cache so OTHER panes (and well child-previews)
// showing the same shell tile mirror it live. It's the shell analogue of the
// URL MirrorPump, which lives in the Electron main process and so can't see the
// renderer-side xterm canvas. Modest by design (toDataURL isn't free and a
// mirrored preview doesn't need 60fps); matches MIRROR_INTERVAL_MS.
const shellMirrorIntervalMs = 250

// installShellMirror starts the periodic shell-preview mirror. One interval for
// the whole app, alive for its lifetime. Ticks are nearly free when no shell is
// live (mirrorLiveShells returns immediately).
func (a *App) installShellMirror() {
	cb := js.FuncOf(func(js.Value, []js.Value) any {
		a.mirrorLiveShells()
		return nil
	})
	js.Global().Call("setInterval", cb, shellMirrorIntervalMs)
}

// mirrorLiveShells snapshots every live shell terminal into the preview cache
// (keyed by tile id), so a shell tile shown in another pane or as a well
// child-preview tracks the live terminal instead of its last freeze — the same
// "live mirror" the URL MirrorPump gives url tiles. Skipped while overlays are
// parked for a gesture: the containers are hidden then and the redraw a frame
// would schedule fights the in-flight drag.
func (a *App) mirrorLiveShells() {
	if a.liveOverlaysHidden() {
		return
	}
	for _, pl := range a.locals {
		conn := pl.shellConn
		if conn == nil || conn.closed {
			continue
		}
		jpeg := snapshotShellCanvas(conn.container)
		if jpeg == nil {
			continue
		}
		a.urlPreview.PutWildcard(conn.tileID, jpeg, func() { a.draw() })
	}
}

// closeShellStream is the freeze path: capture a JPEG of the
// terminal (so the next descent shows it as the frozen preview),
// POST it via SetShellPreview, then close the WebSocket. The
// server-side WS-close just detaches the tmux client — the shell keeps
// running inside the tmux session so a future refresh reattaches to
// the same state. An ephemeral shell's ascent passes freeze=false: the
// tile (and its tmux session) is about to be deleted, so there is
// nothing to freeze for (issue #85). Idempotent.
func (a *App) closeShellStream(paneID string, freeze bool) {
	conn := a.shellConnFor(paneID)
	if conn == nil {
		return
	}
	conn.closed = true
	// Best-effort JPEG capture from the renderer's content canvas. If
	// anything goes wrong we just skip the preview update — the cwd
	// still persists via the server's close handler.
	if jpegBytes := snapshotShellCanvas(conn.container); freeze && jpegBytes != nil {
		tileID := conn.tileID
		// Update the local preview cache immediately so the next
		// descent shows this frame instead of the previous one. The
		// server-side blob id this snapshot will get isn't known
		// until SetShellPreview returns, so store as wildcard — Get
		// will satisfy any expected blob id until a specific Put
		// supersedes (which happens on the next GetTilePreview).
		a.urlPreview.PutWildcard(tileID, jpegBytes, func() { a.draw() })
		go a.postSetShellPreview(tileID, conn.anchor, slices.Clone(conn.path), jpegBytes)
	}
	if conn.ws.Truthy() {
		conn.ws.Call("close")
	}
}

// closeAllShellStreams closes every open shell stream. Used on
// beforeunload so the server's freeze-and-destroy runs before the tab
// goes away.
func (a *App) closeAllShellStreams() {
	ids := make([]string, 0, len(a.locals))
	for id, pl := range a.locals {
		if pl.shellConn != nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		a.closeShellStream(id, true)
	}
}

// releaseShellStream tears down the DOM + js.Func handlers after the
// WebSocket fires onClose. Called from the onClose handler.
func (a *App) releaseShellStream(paneID string, conn *shellStreamConn) {
	if pl, ok := a.localIf(paneID); ok && pl.shellConn == conn {
		pl.shellConn = nil
	}
	// Detach handlers and dispose the terminal before removing the
	// DOM node so xterm's internal listeners don't fire against a
	// removed element.
	conn.onMessage.Release()
	conn.onOpen.Release()
	conn.onClose.Release()
	conn.onError.Release()
	conn.onData.Release()
	conn.onResize.Release()
	if conn.onMouse.Truthy() {
		conn.onMouse.Release()
	}
	if conn.onLinkProvide.Truthy() {
		conn.onLinkProvide.Release()
	}
	if conn.onLinkActivate.Truthy() {
		conn.onLinkActivate.Release()
	}
	if conn.onOSCURL.Truthy() {
		conn.onOSCURL.Release()
	}
	if conn.term.Truthy() {
		conn.term.Call("dispose")
	}
	if conn.container.Truthy() {
		if parent := conn.container.Get("parentNode"); parent.Truthy() {
			parent.Call("removeChild", conn.container)
		}
	}
}

// syncShellOverlayPosition repositions every live shell overlay to
// track its pane's screen rect. Called every draw; the underlying CSS
// transforms are cheap. fit addon is called once per resize event so
// xterm's grid dimensions follow the container.
func (a *App) syncShellOverlayPosition() {
	// Like native URL views, the xterm host div paints above the canvas and
	// swallows mouse input over its rect. Park every overlay during a
	// canvas-overlay gesture so a boundary drag (or any other gesture) can
	// cross the shell without the div eating the move/up events.
	if a.liveOverlaysHidden() {
		for _, pl := range a.locals {
			if pl.shellConn != nil {
				pl.shellConn.container.Get("style").Set("display", "none")
			}
		}
		return
	}
	rects := a.layoutPanes()
	for paneID, pl := range a.locals {
		conn := pl.shellConn
		if conn == nil {
			continue
		}
		r, ok := rects[paneID]
		if !ok {
			conn.container.Get("style").Set("display", "none")
			continue
		}
		// Hide the shell overlay (and its ascend handle) when the pane isn't
		// currently descended in THIS shell — e.g. it descended further into an
		// ephemeral url from a shell link. The stream stays alive (the session
		// persists); only the overlay parks, reappearing when the pane returns.
		if p := a.tree.FindPane(paneID); p == nil || p.TextFocus != conn.tileID {
			conn.container.Get("style").Set("display", "none")
			if conn.circle.Truthy() {
				conn.circle.Get("style").Set("display", "none")
			}
			continue
		}
		// Inset by the pane border so the overlay sits flush inside
		// the chrome. Routes through panebox.ContentBox — same path as
		// the URL live view (url_stream_client.go contentViewBounds) so
		// both live-tile kinds always use the same inset (LiveViewInsetPx).
		cb := panebox.ContentBox(r, paneBorderPx)
		if cb.W < 1 || cb.H < 1 {
			conn.container.Get("style").Set("display", "none")
			continue
		}
		style := conn.container.Get("style")
		style.Set("display", "block")
		setBoundsPx(style, cb.X, cb.Y, cb.W, cb.H)
		// The ascend handle belongs to the focused pane only — same rule the
		// canvas applies to every other per-pane control (render.go drawPane)
		// and the live-URL corner control (controlVisible).
		if conn.circle.Truthy() {
			vis := "none"
			if paneID == a.tree.Focus {
				vis = "flex"
			}
			conn.circle.Get("style").Set("display", vis)
		}
		// Ask xterm to re-fit. The FitAddon emits an onResize event if
		// the new dimensions differ from the previous, which we forward
		// via the registered onResize callback.
		if conn.fitAddon.Truthy() {
			conn.fitAddon.Call("fit")
		}
	}
}

// snapshotShellCanvas pulls the canvas element the active renderer paints
// terminal content into and returns its bytes as JPEG. Returns nil if no
// canvas is present (renderer hasn't initialized, or addon failed to load).
func snapshotShellCanvas(container js.Value) []byte {
	if !container.Truthy() {
		return nil
	}
	canvas := shellContentCanvas(container)
	if !canvas.Truthy() {
		return nil
	}
	dataURL := canvas.Call("toDataURL", "image/jpeg", 0.85)
	if !dataURL.Truthy() {
		return nil
	}
	// Prefix-validate + base64-decode in Go (not JS atob) via the pure
	// shellconn.DecodeJPEGDataURL — see there for the atob-corruption reason.
	out, ok := shellconn.DecodeJPEGDataURL(dataURL.String())
	if !ok {
		return nil
	}
	return out
}

// postSetShellPreview sends a SetShellPreview RPC with the captured
// JPEG. The anchor+path locate the tile's leaf grid (the server
// validates the tile against the path — a shell inside a well needs
// the real descent path, issue #77). The previous tile version is
// needed for optimistic concurrency; we look it up from the cache to
// avoid a synchronous GetTile round-trip in the ascent path.
func (a *App) postSetShellPreview(tileID, anchor string, path []string, jpeg []byte) {
	req := &rpc.SetShellPreviewRequest{
		Path:    rpc.Path{WellIDs: path},
		TileID:  tileID,
		Version: a.tileVersionAt(anchor, path, tileID),
		JPEG:    jpeg,
	}
	_, err := a.cl.SetShellPreview(context.Background(), req)
	if err != nil {
		shellLog("SetShellPreview tile=%s err=%v", tileID, err)
		// The terminal frame the user just left is not persisted — the
		// tile's preview will show an older state (charter §6).
		a.reportErr(errsurface.Error, "shell", "shell preview save failed: "+rpcErrText(err))
	}
}
