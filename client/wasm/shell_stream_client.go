//go:build js && wasm

package main

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"slices"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cadence"
	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/contentzoom"
	"github.com/josephburnett/gridwell/client/debounce"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/inflight"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/shellconn"
	"github.com/josephburnett/gridwell/client/traceevent"
	"github.com/josephburnett/gridwell/client/urlnorm"
)

// shellStreamConn is one live shell attachment: the pane's slot on the /shell
// WebSocket plus its xterm.js host. Its js.Func handlers are Released on close.
type shellStreamConn struct {
	term         js.Value // xterm.Terminal
	fitAddon     js.Value // FitAddon — proposeDimensions + fit
	renderAddon  js.Value // renderer addon: WebglAddon, or zero (DOM fallback)
	rendererKind string   // "webgl" or "dom", whichever attached
	container    js.Value // host <div> in the DOM

	// tileID is the content tile the socket is bound to, whose face and title
	// the server writes; key is the session it attaches (rpc.ShellSession).
	tileID, key string
	paneID      string
	// descentID is the pane frame this stream was opened for: for a shell link,
	// the link row, not tileID, or a link's overlay parks forever.
	descentID string
	// anchor and path locate the grid holding this shell tile, as urlView's do.
	anchor string
	path   []string

	onData         js.Func
	onResize       js.Func
	onMouse        js.Func   // the release and click tail of a link press
	mouseFns       []js.Func // from installOverlayMouse
	onLinkProvide  js.Func   // xterm link provider: scans lines for http(s) urls
	onLinkActivate js.Func   // a link click opens an ephemeral url descent
	onLinkHover    js.Func
	onLinkLeave    js.Func
	onOSCURL       js.Func   // OSC 5522 from the gridwell-open shim
	touchFns       []js.Func // from installOverlayTouch

	// hoveredURL is the link under the pointer, "" for none, published by xterm's
	// linkifier exactly when a click would activate it.
	hoveredURL string
	// pendingLink is the url of a press this overlay took from xterm, held until
	// its click.
	pendingLink string

	closed bool

	// mirror throttles the snapshots a repaint arms while another pane shows
	// this terminal; see syncMirrors.
	mirror   *debounce.Debounce
	onRender js.Func
	// shown is whether the overlay was on screen last frame, so a park snapshots
	// the face its pane is about to draw.
	shown bool

	lastCols, lastRows uint16

	// lastFit* are the last fit()'s inputs, box and font size: fitting every frame
	// lets box wobble churn resizes, each a SIGWINCH.
	lastFitW, lastFitH float64
	lastFitFont        int
}

var (
	shellLog = taggedLog("[shellstream]")
	// The state changes carry their own record; see taggedLog.
	shellConsole = consoleLog("[shellstream]")
)

// isShellDescent reads descentKind, as isURLDescent does, so an ephemeral
// shell visit counts.
func (a *App) isShellDescent(p *pane.Pane) bool {
	return a.descentKind(p) == rpc.DescentShell
}

func (a *App) hasShellStream(paneID string) bool {
	return a.shellConnFor(paneID) != nil
}

// shellRefreshButtonVisible runs shellconn.DecideShellRefreshVisible, probing
// ShellSessionAlive when the answer is not cached.
func (a *App) shellRefreshButtonVisible(tile *gridwellv1.Tile) bool {
	if tile == nil {
		return false
	}
	// Until a link's target is read there is no session to ask about, and the
	// read findTileByID kicks redraws.
	key, ok := a.shellKey(tile, a.findTileByID)
	if !ok {
		return false
	}
	alive, known := a.shellAlive[key]
	v := shellconn.DecideShellRefreshVisible(
		tile.Kind == rpc.KindShell, tile.PreviewBlobId != 0, known, alive)
	if v.Probe {
		a.probeShellSessionAlive(key, rpc.ContentID(tile), nil)
	}
	return v.Show
}

// probeShellSessionAlive asks the node about tileID and caches the verdict
// under its session key, then calls then, unless the probe failed.
func (a *App) probeShellSessionAlive(key, tileID string, then func(alive bool)) {
	// Single-flight never drops a callback, or a restore's attach is lost when the
	// badge probe fired first.
	if waiters, inflight := a.shellAliveProbing[key]; inflight {
		if then != nil {
			a.shellAliveProbing[key] = append(waiters, then)
		}
		return
	}
	waiters := []func(bool){}
	if then != nil {
		waiters = append(waiters, then)
	}
	a.shellAliveProbing[key] = waiters
	go func() {
		// Bounded: a probe the network swallowed would dedupe every later one away.
		ctx, cancel := inflight.Bounded()
		defer cancel()
		alive, err := a.cl.ShellSessionAlive(ctx, tileID)
		done := a.shellAliveProbing[key]
		delete(a.shellAliveProbing, key)
		if err != nil {
			shellLog("ShellSessionAlive tile=%s err=%v", tileID, err)
			a.reportErr(errsurface.Error, "shell", "shell session probe failed: "+rpcErrText(err))
			return
		}
		a.shellAlive[key] = alive
		for _, fn := range done {
			fn(alive)
		}
		a.draw()
	}()
}

// setShellAlive overrides the cached probe with onShellExit's firsthand verdict.
func (a *App) setShellAlive(key string, alive bool) {
	cur, ok := a.shellAlive[key]
	a.shellAlive[key] = alive
	if !ok || cur != alive {
		a.draw()
	}
}

// shellKey is shellconn.SessionKey with a link's target read through lookup.
func (a *App) shellKey(row *gridwellv1.Tile, lookup func(string) *gridwellv1.Tile) (string, bool) {
	var target *gridwellv1.Tile
	if row.LinkTargetId != "" {
		target = lookup(row.LinkTargetId)
	}
	return shellconn.SessionKey(row, target)
}

// forgetShellAlive drops the cached verdict for the session tileID names, so
// whatever still names it probes again.
func (a *App) forgetShellAlive(tileID string) {
	key := tileID
	if t := a.cachedTileByID(tileID); t != nil {
		if k, ok := a.shellKey(t, a.cachedTileByID); ok {
			key = k
		}
	}
	delete(a.shellAlive, key)
	delete(a.shellAliveProbing, key)
}

// openShellStream puts the tile's shell live in pane p: keep, move another
// pane's terminal, or mount xterm.js on a new PTY, closing any other tile's
// terminal on the same session first. key is the session tileID resolves to
// (shellconn.SessionKey). disable_shells refuses.
func (a *App) openShellStream(p *pane.Pane, tileID, key string) {
	frozen := false
	if t := a.findTileByID(tileID); t != nil {
		frozen = t.UrlFrozen
	}
	// Attaching clears the standing freeze, as the url side does. A shell link
	// never follows its target, so the link row holds and clears the freeze.
	plan, ok := shellconn.DecideGoLive(a.caps.Shells, frozen, false)
	if !ok {
		a.reportErr(caps.ShellNotice())
		return
	}
	if plan.Unfreeze {
		a.postFrozen(tileID, false, nil)
	}
	tileID = a.contentKey(tileID)
	if !a.engage(a.shellSurface(), p, key, tileID) {
		return
	}

	doc := js.Global().Get("document")
	container := doc.Call("createElement", "div")
	container.Set("className", "gw-shell-host")
	style := container.Get("style")
	style.Set("position", "absolute")
	style.Set("display", "block")
	style.Set("background", a.pal.Bg)
	style.Set("zIndex", "5")
	style.Set("overflow", "hidden")
	// Off-screen until placed, so no 0x0 terminal flashes during the transition.
	style.Set("left", "-9999px")
	style.Set("top", "-9999px")
	style.Set("width", "300px")
	style.Set("height", "200px")
	doc.Get("body").Call("appendChild", container)

	// The handlers read the pane off conn, since a takeover moves it.
	conn := &shellStreamConn{tileID: tileID, key: key}
	conn.placeIn(p)

	// installOverlayMouse hands back pane focus and link presses. Every other
	// press stays the terminal's: on Linux the middle one pastes the primary
	// selection, so it cannot be the canvas's ascent.
	mouseFns := a.installOverlayMouse(container, func(ev js.Value, _, _ float64) bool {
		// A press on a link is Gridwell's alone, or xterm and the application would
		// both open it.
		conn.pendingLink = ""
		if ev.Get("button").Int() == 0 && shellconn.DecideLinkPress(
			conn.hoveredURL, mouseTrackingMode(conn.term), modifierHeld(ev)) {
			conn.pendingLink = conn.hoveredURL
			ev.Call("preventDefault")
			ev.Call("stopPropagation")
		}
		// Pane focus still follows: the overlay swallows the mousedown.
		if cur := a.tree.FindPane(conn.paneID); cur != nil {
			a.focusToPane(cur)
		}
		return true
	})

	onMouse := js.FuncOf(func(_ js.Value, args []js.Value) any {
		ev := args[0]
		if conn.pendingLink == "" {
			return nil
		}
		ev.Call("preventDefault")
		ev.Call("stopPropagation")
		if ev.Get("type").String() == "click" {
			url := conn.pendingLink
			conn.pendingLink = ""
			a.shellURLActivate(conn.paneID, url)
		}
		return nil
	})
	container.Call("addEventListener", "mouseup", onMouse, true)
	container.Call("addEventListener", "click", onMouse, true)

	Terminal := js.Global().Get("Terminal")
	if !Terminal.Truthy() {
		shellLog("xterm.Terminal not loaded; index.html missing script tag?")
		a.reportErr(errsurface.Error, "shell", "terminal engine unavailable on this host (xterm not loaded)")
		doc.Get("body").Call("removeChild", container)
		return
	}
	opts := js.Global().Get("Object").New()
	// xterm 6 gates registerOscHandler and unicode.activeVersion behind this
	// flag; without it they panic the wasm.
	opts.Set("allowProposedApi", true)
	opts.Set("fontFamily", `ui-monospace, "SF Mono", Menlo, Consolas, monospace`)
	// Scaled by the tile's persisted content zoom.
	zoom := 1.0
	if t := a.findTileByID(tileID); t != nil {
		zoom = contentzoom.Of(t.GetContentZoom())
	}
	opts.Set("fontSize", contentzoom.ShellFontPx(zoom))
	// No convertEol: the PTY's ONLCR already delivers CRLF, and a bare LF would
	// snap to column 0.
	opts.Set("cursorBlink", true)
	opts.Set("theme", a.termTheme())
	term := Terminal.New(opts)

	// Unicode 11 widths, loaded before open so the first paint measures right.
	if u11 := js.Global().Get("Unicode11Addon"); u11.Truthy() {
		term.Call("loadAddon", u11.Get("Unicode11Addon").New())
		term.Get("unicode").Set("activeVersion", "11")
	}

	fitAddon := js.Global().Get("FitAddon").Get("FitAddon").New()
	term.Call("loadAddon", fitAddon)
	term.Call("open", container)
	renderAddon, rendererKind := attachShellRenderer(term)

	touchFns := a.installOverlayTouch(container, shellTouchClaim())

	cols := term.Get("cols").Int()
	rows := term.Get("rows").Int()
	a.emit(traceevent.ShellOpen(p.ID, tileID))
	shellConsole("open pane=%s tile=%s cols=%d rows=%d", p.ID, tileID, cols, rows)

	conn.term, conn.fitAddon, conn.container = term, fitAddon, container
	conn.renderAddon, conn.rendererKind = renderAddon, rendererKind
	conn.onMouse, conn.mouseFns, conn.touchFns = onMouse, mouseFns, touchFns
	conn.lastCols, conn.lastRows = uint16(cols), uint16(rows)

	conn.onLinkActivate = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) >= 2 && args[1].Type() == js.TypeString {
			a.shellURLActivate(conn.paneID, args[1].String())
		}
		return nil
	})
	// xterm's linkifier is the one owner of which link the pointer is on.
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
	// A program's OSC 8 hyperlink bypasses the link provider; linkHandler routes
	// it to the same owner instead of xterm's confirm() and window.open.
	linkHandler := js.Global().Get("Object").New()
	linkHandler.Set("activate", conn.onLinkActivate)
	linkHandler.Set("hover", conn.onLinkHover)
	linkHandler.Set("leave", conn.onLinkLeave)
	term.Get("options").Set("linkHandler", linkHandler)
	conn.onLinkProvide = js.FuncOf(func(_ js.Value, args []js.Value) any {
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

	// OSC 5522: the gridwell-open shim ($BROWSER, see internal/local/tmux) hands
	// back a url a terminal app opened, to descend here. It rides the PTY stream,
	// so remote shells work unchanged.
	conn.onOSCURL = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) >= 1 && args[0].Type() == js.TypeString {
			a.shellURLActivate(conn.paneID, args[0].String())
		}
		return true // consumed
	})
	term.Get("parser").Call("registerOscHandler", 5522, conn.onOSCURL)

	conn.onData = js.FuncOf(func(_ js.Value, args []js.Value) any {
		a.shells.Write(conn.key, []byte(args[0].String()))
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
		a.shells.Resize(conn.key, int(cols), int(rows))
		return nil
	})
	term.Call("onResize", conn.onResize)

	conn.mirror = debounce.New(setTimeoutMs, nowMs, cadence.ShellMirrorMode, func() { a.mirrorShell(conn) })
	conn.onRender = js.FuncOf(func(js.Value, []js.Value) any {
		if slices.Contains(a.mirrors.shell, conn.paneID) {
			conn.mirror.Arm(cadence.ShellMirrorMs)
		}
		return nil
	})
	term.Call("onRender", conn.onRender)

	// The pane owns the conn before the dial: an instant failure reports through
	// onShellExit, which finds it among the panes'.
	a.local(p.ID).shellConn = conn
	a.shells.Open(key, tileID, int(cols), int(rows))
	a.syncShellOverlayPosition()
	// A stream can open after an await, by when a press may have moved on.
	if p.ID == a.tree.Focus {
		term.Call("focus")
	}
}

// attachShellRenderer gives the terminal the WebGL addon, falling back to
// xterm's DOM renderer.
func attachShellRenderer(term js.Value) (js.Value, string) {
	if addon, ok := tryWebglAddon(term); ok {
		return addon, "webgl"
	}
	// The kind is e2e-asserted, so a downgrade is never silent.
	shellLog("shell renderer: DOM FALLBACK (webgl unavailable)")
	return js.Value{}, "dom"
}

// tryWebglAddon reports ok=false when the addon is missing or throws.
// preserveDrawingBuffer so the freeze capture reads real pixels.
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
	// A lost GPU context disposes the addon; xterm continues on DOM.
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
// WebGL link layer comes first and captures all black.
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

// moveShellStream hands the terminal to pane to, socket and all: no close, no
// freeze, no reattach.
func (a *App) moveShellStream(fromID string, to *pane.Pane) {
	from, ok := a.localIf(fromID)
	if !ok || from.shellConn == nil {
		return
	}
	conn := from.shellConn
	from.shellConn = nil
	conn.placeIn(to)
	a.local(to.ID).shellConn = conn
	a.emit(traceevent.ShellMove(fromID, to.ID, conn.tileID))
	shellConsole("move pane=%s→%s tile=%s", fromID, to.ID, conn.tileID)
	a.syncShellOverlayPosition()
	if to.ID == a.tree.Focus {
		conn.term.Call("focus")
	}
	a.draw()
}

func (conn *shellStreamConn) placeIn(p *pane.Pane) {
	conn.paneID, conn.descentID = p.ID, p.ContentID()
	conn.anchor, conn.path = p.Anchor(), slices.Clone(p.Path())
}

func (a *App) shellConnOfSession(key string) *shellStreamConn {
	for _, pl := range a.locals {
		if pl.shellConn != nil && pl.shellConn.key == key {
			return pl.shellConn
		}
	}
	return nil
}

// onShellData routes PTY output to the session's terminal as a Uint8Array.
func (a *App) onShellData(key string, data []byte) {
	conn := a.shellConnOfSession(key)
	if conn == nil || conn.closed {
		return
	}
	u8 := js.Global().Get("Uint8Array").New(len(data))
	js.CopyBytesToJS(u8, data)
	conn.term.Call("write", u8)
}

// onShellExit handles an unexpected stream end; see shellconn.ExitAlive. The
// verdict keys by session, so one exit reaches every tile naming it.
func (a *App) onShellExit(key, message string, sessionGone bool) {
	conn := a.shellConnOfSession(key)
	if conn == nil {
		return
	}
	a.emit(traceevent.ShellExit(conn.paneID, conn.tileID, message, sessionGone))
	shellConsole("exit pane=%s tile=%s gone=%v msg=%q", conn.paneID, conn.tileID, sessionGone, message)
	if alive, known := shellconn.ExitAlive(sessionGone); known {
		a.setShellAlive(key, alive)
	} else {
		delete(a.shellAlive, key)
	}
	if message != "" {
		a.reportErr(errsurface.Error, "shell", "shell stream ended: "+message)
	}
	conn.closed = true
	a.releaseShellStream(conn.paneID, conn)
	a.draw()
}

// mirrorShell snapshots a live terminal into the tile's preview cache.
func (a *App) mirrorShell(conn *shellStreamConn) {
	if conn.closed {
		return
	}
	a.shellMirrorPasses++
	if jpeg := snapshotShellCanvas(conn.container); jpeg != nil {
		a.views.urlPreview.PutWildcard(conn.tileID, jpeg, func() { a.draw() })
	}
}

// parkShellOverlay takes the overlay off screen, snapshotting the face first.
func (a *App) parkShellOverlay(conn *shellStreamConn) {
	if conn.shown {
		a.mirrorShell(conn)
	}
	conn.shown = false
	conn.container.Get("style").Set("display", "none")
}

// termTheme is xterm's palette, the canvas's three roles. A frozen preview
// keeps the theme it was captured in: rewriting a stored blob for a view
// preference would change bytes the user did not touch.
func (a *App) termTheme() js.Value {
	th := js.Global().Get("Object").New()
	th.Set("background", a.pal.Bg)
	th.Set("foreground", a.pal.TextFg)
	th.Set("cursor", a.pal.ShellCursor)
	return th
}

// closeShellStream is the freeze path: capture, post, detach the tmux client.
// An ephemeral ascent passes freeze=false: the session is about to be deleted.
func (a *App) closeShellStream(paneID string, freeze bool) {
	conn := a.shellConnFor(paneID)
	if conn == nil {
		return
	}
	conn.closed = true
	a.emit(traceevent.ShellClose(paneID, conn.tileID, freeze))
	if jpegBytes := snapshotShellCanvas(conn.container); freeze && jpegBytes != nil {
		tileID := conn.tileID
		// A wildcard: the blob id is unknown until SetShellPreview returns.
		a.views.urlPreview.PutWildcard(tileID, jpegBytes, func() { a.draw() })
		go a.postSetShellPreview(tileID, conn.anchor, slices.Clone(conn.path), jpegBytes)
	}
	a.shells.Close(conn.key)
	a.releaseShellStream(paneID, conn)
}

// freezeShellPaneByIntent runs the bar circle's freeze on a live shell. The
// tmux session keeps running: a freeze is a screenshot, not a kill.
func (a *App) freezeShellPaneByIntent(p *pane.Pane) {
	t, ok := a.descendedGridTile(p)
	if !ok || t.Kind != rpc.KindShell {
		return
	}
	a.postFrozen(t.Id, true, func() {
		// A freeze still owed is the outbox's business.
		a.closeShellStream(p.ID, true)
		a.draw()
	})
}

// closeAllShellStreams runs on beforeunload, ahead of the server's
// freeze-and-destroy.
func (a *App) closeAllShellStreams() {
	for _, h := range a.shellSurfaces() {
		a.closeShellStream(h.PaneID, true)
	}
}

// mouseTrackingMode reads xterm's mouse tracking mode: "none" for none, ""
// when the terminal cannot answer.
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

// modifierHeld reports a modifier on a mouse event: the escape hatch from a
// mouse-tracking application.
func modifierHeld(ev js.Value) bool {
	return ev.Get("altKey").Truthy() || ev.Get("shiftKey").Truthy() ||
		ev.Get("ctrlKey").Truthy() || ev.Get("metaKey").Truthy()
}

// releaseAll releases installed handlers; one never given is the zero
// js.Func and must not be released.
func releaseAll(fns ...js.Func) {
	for _, f := range fns {
		if f.Truthy() {
			f.Release()
		}
	}
}

func (a *App) releaseShellStream(paneID string, conn *shellStreamConn) {
	if pl, ok := a.localIf(paneID); ok && pl.shellConn == conn {
		pl.shellConn = nil
	}
	// Dispose first, so xterm's listeners do not fire on a removed element.
	releaseAll(conn.onData, conn.onResize, conn.onMouse, conn.onLinkProvide,
		conn.onLinkActivate, conn.onLinkHover, conn.onLinkLeave, conn.onOSCURL,
		conn.onRender)
	releaseAll(conn.mouseFns...)
	releaseAll(conn.touchFns...)
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

// syncShellOverlayPosition tracks every live shell overlay to its pane rect.
func (a *App) syncShellOverlayPosition() {
	// The xterm div swallows input over its rect like a native url view, so it
	// parks by the same per-pane verdict.
	g := a.canvasGesture()
	rects := a.layoutPanes()
	for paneID, pl := range a.locals {
		conn := pl.shellConn
		if conn == nil {
			continue
		}
		if pane.ParkSurface(g, paneID) {
			a.parkShellOverlay(conn)
			continue
		}
		r, ok := rects[paneID]
		p := a.tree.FindPane(paneID)
		var contentID string
		if p != nil {
			contentID = p.ContentID()
		}
		// Park when the pane is not laid out, or is out of this stream's descent;
		// the stream stays alive.
		if pane.SurfaceOf(ok, contentID, conn.descentID) != pane.SurfaceShow {
			a.parkShellOverlay(conn)
			continue
		}
		cx, cy, cw, ch := paneContentBox(r)
		cb := pane.Rect{X: cx, Y: cy, W: cw, H: ch}
		if cb.W < 1 || cb.H < 1 {
			a.parkShellOverlay(conn)
			continue
		}
		style := conn.container.Get("style")
		style.Set("display", "block")
		conn.shown = true
		setBoundsPx(style, cb.X, cb.Y, cb.W, cb.H)
		// Re-fit only when an input changed; see lastFit*.
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
	// Decoded in Go; see shellconn.DecodeJPEGDataURL.
	out, ok := shellconn.DecodeJPEGDataURL(dataURL.String())
	if !ok {
		return nil
	}
	return out
}

// postSetShellPreview sends the captured JPEG to the tile's leaf grid.
func (a *App) postSetShellPreview(tileID, anchor string, path []string, jpeg []byte) {
	// A capture is no claim and bumps no version, so a racing close cannot
	// refuse it.
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

// deleteCall is DeleteTile as a write's call. A session the delete left
// running is told, though the delete itself landed.
func (a *App) deleteCall(req *gridwellv1.DeleteTileRequest) func(context.Context) error {
	return func(ctx context.Context) error {
		resp, err := a.cl.DeleteTile(ctx, req)
		if left := resp.GetSessionLeft(); left != "" {
			a.reportErr(errsurface.Error, "shell", "deleted, but "+left)
		}
		return err
	}
}
