//go:build js && wasm

package main

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"slices"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/inflight"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/shellconn"
	"github.com/josephburnett/gridwell/client/urlnorm"
)

// shellStreamConn is one live shell attachment: the pane's slot on the /shell
// WebSocket plus its xterm.js host. Its js.Func handlers are Released on
// close, because FuncOf-allocated callbacks pin Go memory until released.
type shellStreamConn struct {
	term         js.Value // xterm.Terminal
	fitAddon     js.Value // FitAddon — proposeDimensions + fit
	renderAddon  js.Value // renderer addon: WebglAddon, or zero (DOM fallback)
	rendererKind string   // "webgl" or "dom", whichever attached
	container    js.Value // host <div> in the DOM

	tileID string
	paneID string
	// descentID is the pane frame this stream was opened for; for a shell link
	// that is the link row, not tileID. The per-frame sweep compares it through
	// pane.SurfaceOf; the content id instead parks a link's overlay forever.
	descentID string
	// anchor and path locate the grid holding this shell tile: SetShellPreview
	// would otherwise resolve against the plugin root grid and fail inside a
	// sub-grid. Same contract as urlView's.
	anchor string
	path   []string

	onData         js.Func
	onResize       js.Func
	onMouse        js.Func // right-button gesture, focus, link press
	onLinkProvide  js.Func // xterm link provider: scans lines for http(s) urls
	onLinkActivate js.Func // a link click opens an ephemeral url descent
	onLinkHover    js.Func
	onLinkLeave    js.Func
	onOSCURL       js.Func   // OSC 5522 from the gridwell-open shim
	touchFns       []js.Func // from installOverlayTouch

	// hoveredURL is the link the pointer is on, "" for none. xterm's linkifier
	// owns the hit test and publishes it through the hover and leave callbacks,
	// which fire exactly when a click would activate that link.
	hoveredURL string
	// pendingLink is the url of a press this overlay took from xterm, held
	// until the click that completes it. "" when the press was the terminal's.
	pendingLink string

	closed bool

	lastCols, lastRows uint16

	// lastFit* are the inputs the last fit() derived from: the container box and
	// the font size, since content zoom moves the font without the box. Fitting
	// every frame lets box wobble churn resizes, each a SIGWINCH and a redraw.
	lastFitW, lastFitH float64
	lastFitFont        int
}

var shellLog = taggedLog("[shellstream]")

// isShellDescent is the bar slot's shell arm (barslot.Input.ShellDescent). It
// reads descentKind, the same resolver as isURLDescent, so an ephemeral shell
// visit is a shell descent here too; what the slot then offers for one is
// descendedGridTile's business.
func (a *App) isShellDescent(p *pane.Pane) bool {
	return a.descentKind(p) == rpc.DescentShell
}

func (a *App) hasShellStream(paneID string) bool {
	return a.shellConnFor(paneID) != nil
}

// shellRefreshButtonVisible decides whether the refresh button paints on a
// frozen shell descent, per shellconn.DecideShellRefreshVisible, and starts a
// ShellSessionAlive probe when the answer is not cached.
func (a *App) shellRefreshButtonVisible(tile *gridwellv1.Tile) bool {
	if tile == nil {
		return false
	}
	// A shell link keys by the owner tile's id: one shell, seen from two grids.
	alive, known := a.shellAlive[rpc.ContentID(tile)]
	v := shellconn.DecideShellRefreshVisible(
		tile.Kind == rpc.KindShell, tile.PreviewBlobId != 0, known, alive)
	if v.Probe {
		a.probeShellSessionAlive(rpc.ContentID(tile), nil)
	}
	return v.Show
}

// probeShellSessionAlive caches the ShellSessionAlive verdict for tileID and
// redraws. then, if non-nil, receives it; a failed probe calls nothing.
func (a *App) probeShellSessionAlive(tileID string, then func(alive bool)) {
	// Single-flight coalesces callers and never drops a callback: dropping the
	// later one loses a restore's attach whenever the badge probe fired first.
	if waiters, inflight := a.shellAliveProbing[tileID]; inflight {
		if then != nil {
			a.shellAliveProbing[tileID] = append(waiters, then)
		}
		return
	}
	waiters := []func(bool){}
	if then != nil {
		waiters = append(waiters, then)
	}
	a.shellAliveProbing[tileID] = waiters
	go func() {
		// Bounded, because the waiters are released when this returns: a probe
		// the network swallowed would dedupe every later probe away forever.
		ctx, cancel := inflight.Bounded()
		defer cancel()
		alive, err := a.cl.ShellSessionAlive(ctx, tileID)
		// Clear the flight so a future probe can retry.
		done := a.shellAliveProbing[tileID]
		delete(a.shellAliveProbing, tileID)
		if err != nil {
			shellLog("ShellSessionAlive tile=%s err=%v", tileID, err)
			// Without a verdict the refresh control does not appear.
			a.reportErr(errsurface.Error, "shell", "shell session probe failed: "+rpcErrText(err))
			return
		}
		a.shellAlive[tileID] = alive
		for _, fn := range done {
			fn(alive)
		}
		a.draw()
	}()
}

// setShellAlive overrides the cached probe with firsthand knowledge, today
// onShellExit's sessionGone verdict.
func (a *App) setShellAlive(tileID string, alive bool) {
	cur, ok := a.shellAlive[tileID]
	a.shellAlive[tileID] = alive
	if !ok || cur != alive {
		a.draw()
	}
}

// openShellStream mounts an xterm.js terminal over the pane's content area and
// opens the tile's PTY on the /shell WebSocket. A second call for the same pane
// closes the previous attachment first. disable_shells refuses, preview stays.
func (a *App) openShellStream(p *pane.Pane, tileID string) {
	if !a.caps.LiveShell {
		a.reportErr(caps.ShellNotice())
		return
	}
	// The PTY session, the alive cache and the freeze writeback all key by the
	// id that owns the session.
	tileID = a.contentKey(tileID)
	// Already attached to this session: a keep-alive return.
	if conn := a.shellConnFor(p.ID); conn != nil && conn.tileID == tileID {
		return
	}
	// One live surface per content tile, pane.TakeOver: two attachments would
	// fight over the terminal size, so another pane detaches, with a freeze.
	for _, otherID := range pane.TakeOver(a.shellSurfaces(), p.ID, tileID) {
		a.closeShellStream(otherID, true)
	}
	a.closeShellStream(p.ID, true)

	doc := js.Global().Get("document")
	container := doc.Call("createElement", "div")
	container.Set("className", "gw-shell-host")
	style := container.Get("style")
	style.Set("position", "absolute")
	style.Set("display", "block")
	style.Set("background", "#0c0d11")
	style.Set("zIndex", "5")
	style.Set("overflow", "hidden")
	// Off-screen until syncShellOverlayPosition places it, so no 0x0 terminal
	// flashes during the descent transition.
	style.Set("left", "-9999px")
	style.Set("top", "-9999px")
	style.Set("width", "300px")
	style.Set("height", "200px")
	doc.Get("body").Call("appendChild", container)

	// Assigned below; the handlers read it only when an event arrives.
	var conn *shellStreamConn

	// The overlay paints above the canvas and would otherwise swallow the
	// right-button mousedown that starts a pane gesture. The right button
	// forwards into the canvas gesture pipeline; the left stays with xterm,
	// except for pane focus and a press on a link.
	onMouse := js.FuncOf(func(_ js.Value, args []js.Value) any {
		ev := args[0]
		switch ev.Get("type").String() {
		case "contextmenu":
			ev.Call("preventDefault")
			return nil
		case "mouseup", "click":
			// The tail of a press this overlay took from xterm: xterm must
			// neither report the release nor activate the link itself.
			if conn == nil || conn.pendingLink == "" {
				return nil
			}
			ev.Call("preventDefault")
			ev.Call("stopPropagation")
			if ev.Get("type").String() == "click" {
				url := conn.pendingLink
				conn.pendingLink = ""
				a.shellURLActivate(p.ID, url)
			}
			return nil
		}
		if ev.Get("button").Int() != 2 {
			// A press on a link is Gridwell's alone: xterm would both activate
			// it and report the press, and the application would then open the
			// same url again through its own opener.
			if conn != nil {
				conn.pendingLink = ""
				if ev.Get("button").Int() == 0 && shellconn.DecideLinkPress(
					conn.hoveredURL, mouseTrackingMode(conn.term), modifierHeld(ev)) {
					conn.pendingLink = conn.hoveredURL
					ev.Call("preventDefault")
					ev.Call("stopPropagation")
				}
			}
			// Pane focus still follows the click, because the overlay swallows
			// the mousedown and the canvas path never runs.
			if cur := a.tree.FindPane(p.ID); cur != nil {
				a.focusToPane(cur)
			}
			return nil
		}
		ev.Call("preventDefault")
		ev.Call("stopPropagation")
		a.onMouseDown(js.Null(), args)
		// onRightDown arms the gesture but does not redraw. Park the overlay so
		// the rest of the drag lands on the canvas, not this div.
		a.draw()
		return nil
	})
	// Capture phase so we win over xterm's own inner listeners.
	container.Call("addEventListener", "mousedown", onMouse, true)
	container.Call("addEventListener", "mouseup", onMouse, true)
	container.Call("addEventListener", "click", onMouse, true)
	container.Call("addEventListener", "contextmenu", onMouse, true)

	Terminal := js.Global().Get("Terminal")
	if !Terminal.Truthy() {
		shellLog("xterm.Terminal not loaded; index.html missing script tag?")
		// A console line alone presents as an empty pane that just vanished.
		a.reportErr(errsurface.Error, "shell", "terminal engine unavailable on this host (xterm not loaded)")
		doc.Get("body").Call("removeChild", container)
		return
	}
	opts := js.Global().Get("Object").New()
	// xterm 6 gates parser.registerOscHandler and term.unicode.activeVersion
	// behind this flag; both are used below, and without it they panic the wasm.
	opts.Set("allowProposedApi", true)
	opts.Set("fontFamily", `ui-monospace, "SF Mono", Menlo, Consolas, monospace`)
	// Scaled by the tile's persisted content zoom, so a zoomed terminal comes
	// back at your size on every descent.
	fontSize := int(shellBaseFontPx)
	if t := a.findTileByID(tileID); t != nil {
		fontSize = int(shellBaseFontPx*contentZoomOf(t) + 0.5)
	}
	opts.Set("fontSize", fontSize)
	// No convertEol: the PTY's ONLCR already delivers CRLF, and with it set
	// xterm snaps to column 0 on every bare LF, scattering scroll-region output.
	opts.Set("cursorBlink", true)
	theme := js.Global().Get("Object").New()
	theme.Set("background", "#0c0d11")
	theme.Set("foreground", "#d8d9de")
	theme.Set("cursor", "#c87a5a")
	opts.Set("theme", theme)
	term := Terminal.New(opts)

	// Unicode 11 widths: the default Unicode 6 table gives modern emoji the
	// wrong cell width. Loaded before open, so the first paint measures right.
	if u11 := js.Global().Get("Unicode11Addon"); u11.Truthy() {
		term.Call("loadAddon", u11.Get("Unicode11Addon").New())
		term.Get("unicode").Set("activeVersion", "11")
	}

	// The renderer addon attaches after open, because the WebGL addon requires
	// an opened terminal.
	fitAddon := js.Global().Get("FitAddon").Get("FitAddon").New()
	term.Call("loadAddon", fitAddon)
	term.Call("open", container)
	renderAddon, rendererKind := attachShellRenderer(term)

	// Multi-finger gestures feed the shared translation; single fingers stay
	// native.
	touchFns := a.installOverlayTouch(container, shellTouchClaim())

	// The fit addon overwrites this, but the bind message starts the PTY.
	cols := term.Get("cols").Int()
	rows := term.Get("rows").Int()
	shellLog("open pane=%s tile=%s cols=%d rows=%d", p.ID, tileID, cols, rows)

	conn = &shellStreamConn{
		term:         term,
		fitAddon:     fitAddon,
		renderAddon:  renderAddon,
		rendererKind: rendererKind,
		container:    container,
		tileID:       tileID,
		paneID:       p.ID,
		descentID:    p.ContentID(),
		anchor:       p.Anchor(),
		path:         slices.Clone(p.Path()),
		onMouse:      onMouse,
		touchFns:     touchFns,
		lastCols:     uint16(cols),
		lastRows:     uint16(rows),
	}

	// One shared activate func, so no per-link js.Func allocations leak.
	conn.onLinkActivate = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) >= 2 && args[1].Type() == js.TypeString {
			a.shellURLActivate(p.ID, args[1].String())
		}
		return nil
	})
	// xterm's linkifier owns which link the pointer is on, and nothing else
	// derives it, so no second hit test can disagree.
	conn.onLinkHover = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) >= 2 && args[1].Type() == js.TypeString {
			conn.hoveredURL = args[1].String()
		}
		return nil
	})
	conn.onLinkLeave = js.FuncOf(func(_ js.Value, args []js.Value) any {
		conn.hoveredURL = ""
		return nil
	})
	// A program's own OSC 8 hyperlink never reaches the link provider, and xterm
	// would open it itself through a confirm() and a window.open the desktop
	// denies. linkHandler gives those the same owner as every other link, and
	// xterm keeps its http(s) filter on them.
	linkHandler := js.Global().Get("Object").New()
	linkHandler.Set("activate", conn.onLinkActivate)
	linkHandler.Set("hover", conn.onLinkHover)
	linkHandler.Set("leave", conn.onLinkLeave)
	term.Get("options").Set("linkHandler", linkHandler)
	conn.onLinkProvide = js.FuncOf(func(_ js.Value, args []js.Value) any {
		// args are (1-based line number, callback); ranges are 1-based inclusive.
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
			link.Set("hover", conn.onLinkHover)
			link.Set("leave", conn.onLinkLeave)
			links.Call("push", link)
		}
		cb.Invoke(links)
		return nil
	})
	links := js.Global().Get("Object").New()
	links.Set("provideLinks", conn.onLinkProvide)
	term.Call("registerLinkProvider", links)

	// OSC 5522: the gridwell-open shim, $BROWSER in every session from
	// internal/local/tmux, hands back a url a terminal app tried to open, so it
	// descends here instead of spawning a host browser. It rides the PTY byte
	// stream, so remote shells work unchanged.
	conn.onOSCURL = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) >= 1 && args[0].Type() == js.TypeString {
			a.shellURLActivate(p.ID, args[0].String())
		}
		return true // consumed
	})
	term.Get("parser").Call("registerOscHandler", 5522, conn.onOSCURL)

	// client/shellws queues frames typed before the socket opens, so no queue
	// is needed here.
	conn.onData = js.FuncOf(func(_ js.Value, args []js.Value) any {
		// The Go form of the JS string is the UTF-8 encoding the PTY wants.
		a.shells.Write(conn.paneID, []byte(args[0].String()))
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
		a.shells.Resize(conn.paneID, int(cols), int(rows))
		return nil
	})
	term.Call("onResize", conn.onResize)

	// The pane owns the conn before the dial: a socket that fails instantly
	// reports through onShellExit, which needs the conn to find the pane.
	a.local(p.ID).shellConn = conn
	// Output arrives at onShellData, an unexpected end at onShellExit.
	a.shells.Open(p.ID, tileID, int(cols), int(rows))
	a.syncShellOverlayPosition()
	term.Call("focus")
}

// attachShellRenderer gives the opened terminal the WebGL addon, falling back
// to xterm's DOM renderer when WebGL2 is unavailable or later lost. Both redraw
// whole rows, so neither leaves stale-region artifacts. The caller pins it.
func attachShellRenderer(term js.Value) (js.Value, string) {
	if addon, ok := tryWebglAddon(term); ok {
		return addon, "webgl"
	}
	// The kind is e2e-asserted, so a downgrade can never be silent.
	shellLog("shell renderer: DOM FALLBACK (webgl unavailable)")
	return js.Value{}, "dom"
}

// tryWebglAddon reports ok=false when the addon is missing or throws.
// preserveDrawingBuffer so the freeze capture's toDataURL reads real pixels.
func tryWebglAddon(term js.Value) (addon js.Value, ok bool) {
	defer func() {
		if recover() != nil { // loadAddon throws when WebGL2 is unavailable
			shellLog("webgl renderer unavailable; using the DOM renderer")
			addon, ok = js.Value{}, false
		}
	}()
	ns := js.Global().Get("WebglAddon")
	if !ns.Truthy() {
		return js.Value{}, false
	}
	a := ns.Get("WebglAddon").New(true) // preserveDrawingBuffer
	term.Call("loadAddon", a)
	// A lost GPU context would freeze the terminal mid-session: dispose the
	// addon and xterm continues on its DOM renderer. One-shot.
	var lossCb js.Func
	lossCb = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		shellLog("webgl context lost; falling back to the DOM renderer")
		a.Call("dispose")
		lossCb.Release()
		return nil
	})
	a.Call("onContextLoss", lossCb)
	return a, true
}

// shellContentCanvas returns the canvas terminal content is painted on: the
// WebGL main canvas is class-less while its transparent link layer comes first,
// so the first canvas captures all black. The DOM fallback has none.
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

// onShellData routes PTY output to the pane's terminal, dropping a push for a
// pane with no live conn. A Uint8Array leaves control bytes untouched.
func (a *App) onShellData(paneID string, data []byte) {
	conn := a.shellConnFor(paneID)
	if conn == nil || conn.closed {
		return
	}
	u8 := js.Global().Get("Uint8Array").New(len(data))
	js.CopyBytesToJS(u8, data)
	conn.term.Call("write", u8)
}

// onShellExit handles an unexpected stream end; a local close is suppressed by
// the registry. sessionGone is the server's definitive verdict, so the cache
// flips; any other end carries none, so the cached answer is dropped.
func (a *App) onShellExit(paneID, message string, sessionGone bool) {
	conn := a.shellConnFor(paneID)
	if conn == nil {
		return
	}
	shellLog("exit pane=%s tile=%s gone=%v msg=%q", paneID, conn.tileID, sessionGone, message)
	if sessionGone {
		a.setShellAlive(conn.tileID, false)
	} else {
		delete(a.shellAlive, conn.tileID)
	}
	if message != "" {
		// The terminal just broke under the user's prompt: say why.
		a.reportErr(errsurface.Error, "shell", "shell stream ended: "+message)
	}
	conn.closed = true
	a.releaseShellStream(paneID, conn)
	a.draw()
}

// shellMirrorIntervalMs is how often a live shell is snapshotted into the
// shared preview cache. The URL mirror pump cannot do this job: it lives in
// the Electron main process and cannot see the xterm canvas.
const shellMirrorIntervalMs = 250

// installShellMirror starts the one mirror interval, alive for the app's life.
func (a *App) installShellMirror() {
	cb := js.FuncOf(func(js.Value, []js.Value) any {
		a.mirrorLiveShells()
		return nil
	})
	js.Global().Call("setInterval", cb, shellMirrorIntervalMs)
}

// mirrorLiveShells snapshots every live shell terminal into the preview cache,
// so a shell tile shown elsewhere tracks it instead of its last freeze. Skipped
// while overlays are parked: the redraw would fight the in-flight gesture.
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
		a.views.urlPreview.PutWildcard(conn.tileID, jpeg, func() { a.draw() })
	}
}

// closeShellStream is the freeze path: capture a JPEG, post it, end the stream.
// The close only detaches the tmux client, so a refresh reattaches. An
// ephemeral ascent passes freeze=false: the session is about to be deleted.
func (a *App) closeShellStream(paneID string, freeze bool) {
	conn := a.shellConnFor(paneID)
	if conn == nil {
		return
	}
	conn.closed = true
	// Best-effort: the cwd still persists through the server's close handler.
	if jpegBytes := snapshotShellCanvas(conn.container); freeze && jpegBytes != nil {
		tileID := conn.tileID
		// A wildcard because the blob id is unknown until SetShellPreview
		// returns: Get answers any expected id until a specific Put lands.
		a.views.urlPreview.PutWildcard(tileID, jpegBytes, func() { a.draw() })
		go a.postSetShellPreview(tileID, conn.anchor, slices.Clone(conn.path), jpegBytes)
	}
	a.shells.Close(paneID)
	// The registry suppresses the exit report for a local close.
	a.releaseShellStream(paneID, conn)
}

// closeAllShellStreams runs on beforeunload so the server's freeze-and-destroy
// happens before the tab goes away. shellSurfaces is a snapshot.
func (a *App) closeAllShellStreams() {
	for _, h := range a.shellSurfaces() {
		a.closeShellStream(h.PaneID, true)
	}
}

// mouseTrackingMode reads xterm's modes.mouseTrackingMode: "none" for no
// tracking application, "" when the terminal cannot answer, which
// shellconn.DecideLinkPress also reads as not tracking.
func mouseTrackingMode(term js.Value) string {
	if !term.Truthy() {
		return ""
	}
	modes := term.Get("modes")
	if !modes.Truthy() {
		return ""
	}
	m := modes.Get("mouseTrackingMode")
	if m.Type() != js.TypeString {
		return ""
	}
	return m.String()
}

// modifierHeld reports whether a mouse event carries any modifier: the
// terminal's escape hatch from a mouse-tracking application, so the overlay
// leaves those presses alone.
func modifierHeld(ev js.Value) bool {
	return ev.Get("altKey").Truthy() || ev.Get("shiftKey").Truthy() ||
		ev.Get("ctrlKey").Truthy() || ev.Get("metaKey").Truthy()
}

// releaseShellStream tears down the DOM and the js.Func handlers.
func (a *App) releaseShellStream(paneID string, conn *shellStreamConn) {
	if pl, ok := a.localIf(paneID); ok && pl.shellConn == conn {
		pl.shellConn = nil
	}
	// Dispose before removing the node, so xterm's own listeners do not fire
	// against a removed element.
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
	if conn.onLinkHover.Truthy() {
		conn.onLinkHover.Release()
	}
	if conn.onLinkLeave.Truthy() {
		conn.onLinkLeave.Release()
	}
	if conn.onOSCURL.Truthy() {
		conn.onOSCURL.Release()
	}
	for _, f := range conn.touchFns {
		f.Release()
	}
	// The mouse-routing target must not outlive the container it names.
	if a.touchDownTarget.Truthy() && a.touchDownTarget.Equal(conn.container) {
		a.touchDownTarget = js.Value{}
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

// syncShellOverlayPosition repositions every live shell overlay to track its
// pane's screen rect. The fit addon runs once per size change.
func (a *App) syncShellOverlayPosition() {
	// The xterm host div paints above the canvas and swallows mouse input over
	// its rect, so park every overlay during a canvas gesture: a boundary drag
	// has to cross the shell.
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
		p := a.tree.FindPane(paneID)
		var contentID string
		if p != nil {
			contentID = p.ContentID()
		}
		// Park when the pane is not laid out this frame, and when it is on
		// screen but no longer in the descent this stream was opened for.
		// Unlike the url side the stream stays alive and the session persists.
		if pane.SurfaceOf(ok, contentID, conn.descentID) != pane.SurfaceShow {
			conn.container.Get("style").Set("display", "none")
			continue
		}
		// The same rect the URL view and the canvas fallback use.
		cx, cy, cw, ch := paneContentBox(r)
		cb := pane.Rect{X: cx, Y: cy, W: cw, H: ch}
		if cb.W < 1 || cb.H < 1 {
			conn.container.Get("style").Set("display", "none")
			continue
		}
		style := conn.container.Get("style")
		style.Set("display", "block")
		setBoundsPx(style, cb.X, cb.Y, cb.W, cb.H)
		// Re-fit only when an input the fit depends on changed; see lastFit*
		// for why. The FitAddon emits onResize if the cell grid changed.
		fontPx := 0
		if fs := conn.term.Get("options").Get("fontSize"); fs.Truthy() {
			fontPx = fs.Int()
		}
		if conn.fitAddon.Truthy() &&
			(cb.W != conn.lastFitW || cb.H != conn.lastFitH || fontPx != conn.lastFitFont) {
			conn.lastFitW, conn.lastFitH, conn.lastFitFont = cb.W, cb.H, fontPx
			conn.fitAddon.Call("fit")
		}
	}
}

// snapshotShellCanvas returns the content canvas as JPEG, nil when there is
// none.
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
	// Decoded in Go, not through JS atob; see shellconn.DecodeJPEGDataURL for
	// why atob corrupts the bytes.
	out, ok := shellconn.DecodeJPEGDataURL(dataURL.String())
	if !ok {
		return nil
	}
	return out
}

// postSetShellPreview sends the captured JPEG. anchor and path locate the
// tile's leaf grid, which the server validates the tile against.
func (a *App) postSetShellPreview(tileID, anchor string, path []string, jpeg []byte) {
	// The frozen frame is a capture, no claim and no version bump, so the stream
	// close racing this freeze cannot refuse it.
	req := &gridwellv1.SetTileRequest{TileId: tileID,
		Tile: &gridwellv1.Tile{Kind: rpc.KindShell}, Preview: jpeg}
	a.do(write{
		label: "SetShellPreview", gid: a.gridIDForPathFrom(anchor, path), id: tileID,
		source: "shell", failText: "shell preview save failed",
		call: func(ctx context.Context) error {
			_, err := a.cl.SetTile(ctx, req)
			if err != nil {
				shellLog("SetShellPreview tile=%s err=%v", tileID, err)
			}
			return err
		},
	})
}
