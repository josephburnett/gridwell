//go:build js && wasm

package main

import (
	"context"
	"slices"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/shellconn"
	"github.com/josephburnett/gridwell/client/urlnorm"
)

// shellStreamConn is one live shell attachment: the pane's slot on the /shell
// WebSocket (client/shellstream over client/shellws, on this page's own
// origin) plus its xterm.js host. A DOM container holds the terminal
// instance, and the container's CSS is positioned per frame so it tracks the
// pane through resizes, pans, and split-tree edits. js.Func handlers are
// tracked and Released on close, because FuncOf-allocated callbacks pin Go
// memory until released.
type shellStreamConn struct {
	term         js.Value // xterm.Terminal
	fitAddon     js.Value // FitAddon — proposeDimensions + fit
	renderAddon  js.Value // renderer addon: WebglAddon, or zero (DOM fallback)
	rendererKind string   // "webgl" or "dom" — which renderer actually attached (issue #128)
	container    js.Value // host <div> in the DOM

	tileID string
	paneID string
	// anchor and path are the plugin-root grid id and the descent path to
	// the grid that holds this shell tile, captured when the stream opened.
	// The freeze (SetShellPreview) needs them to resolve this tile's leaf
	// grid, the same contract as urlView.anchor and path. Without them the
	// writeback resolves against the plugin root grid and fails for any
	// shell inside a descended sub-grid.
	anchor string
	path   []string

	onData         js.Func   // term.onData(bytes string) callback
	onResize       js.Func   // term.onResize({cols, rows})
	onMouse        js.Func   // container right-button → canvas gesture pipeline
	onLinkProvide  js.Func   // xterm link provider: scans lines for http(s) urls
	onLinkActivate js.Func   // shared link click handler → ephemeral url descent
	onOSCURL       js.Func   // OSC 5522 from the gridwell-open shim → ephemeral url descent
	touchFns       []js.Func // the container's overlay-touch handlers (installOverlayTouch)

	closed bool

	lastCols, lastRows uint16

	// lastFit* are the inputs the last fit() derived from: the container box
	// and the font size, since content zoom changes the font without
	// touching the box. The per-frame overlay sync re-fits only when one of
	// them changed. An unconditional per-frame fit() forces a style read
	// every frame and lets sub-pixel box wobble churn resizes, each one a
	// render-service clear, a SIGWINCH, and a full tmux redraw interleaved
	// with in-flight output.
	lastFitW, lastFitH float64
	lastFitFont        int
}

// shellLog writes a tagged debug message to the browser console.
var shellLog = taggedLog("[shellstream]")

// isShellDescent reports whether pane p is currently descended into a
// shell tile. The input layer uses this to switch from Gridwell-native
// gestures to PTY input forwarding (and to gate ascent on a freeze
// capture).
func (a *App) isShellDescent(p *pane.Pane) bool {
	if p == nil || p.ContentID() == "" {
		return false
	}
	gid := a.gridIDForPane(p)
	g, ok := a.c.Grid(gid)
	if !ok {
		return false
	}
	t, ok := g.Tiles[p.ContentID()]
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
	// A shell link probes, and attaches to, the target's session: the PTY is
	// keyed by the owner tile's id, and the link is a second door to the
	// same session — one shell, seen from two grids.
	alive, known := a.shellAlive[tile.ContentID()]
	v := shellconn.DecideShellRefreshVisible(
		tile.Kind == rpc.KindShell, tile.PreviewBlobID != 0, known, alive)
	if v.Probe {
		a.probeShellSessionAlive(tile.ContentID(), nil)
	}
	return v.Show
}

// probeShellSessionAlive fires ShellSessionAlive RPC for tileID, caches the
// result, and triggers a redraw on completion. then, if non-nil, receives
// the verdict (it is not called on probe failure — no verdict, no action).
// Idempotent: short-circuits if a probe is already in flight (a coalesced
// caller's continuation is dropped; the auto-live path re-decides from the
// cache on the next descent, and the refresh button remains the retry).
func (a *App) probeShellSessionAlive(tileID string, then func(alive bool)) {
	// Single-flight coalesces callers and never drops a callback. The
	// entry's presence marks the in-flight probe; late callers park their
	// callbacks on it and the one answer fires them all. Dropping the later
	// caller would lose a restore's attach whenever the bar's callback-less
	// badge probe fired first, which is the common case: every pane's slot
	// probes.
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
		res, err := a.cl.ShellSessionAlive(context.Background(), &rpc.ShellSessionAliveRequest{TileID: tileID})
		// Everyone parked on this flight, then clear it so a future
		// probe can retry.
		done := a.shellAliveProbing[tileID]
		delete(a.shellAliveProbing, tileID)
		if err != nil {
			shellLog("ShellSessionAlive tile=%s err=%v", tileID, err)
			// Without a verdict the refresh control does not appear:
			// distinguish "probe failed" from "session gone".
			a.reportErr(errsurface.Error, "shell", "shell session probe failed: "+rpcErrText(err))
			return
		}
		a.shellAlive[tileID] = res.Alive
		for _, fn := range done {
			fn(res.Alive)
		}
		a.draw()
	}()
}

// setShellAlive overrides the cached probe result for tileID — used when
// the wasm has firsthand knowledge; today that is onShellExit's
// sessionGone verdict (alive=false). Triggers a redraw on change.
func (a *App) setShellAlive(tileID string, alive bool) {
	cur, ok := a.shellAlive[tileID]
	a.shellAlive[tileID] = alive
	if !ok || cur != alive {
		a.draw()
	}
}

// openShellStream mounts an xterm.js terminal in a DOM overlay over the
// pane's content area, opens the tile's PTY on the /shell WebSocket, and
// wires the two together. Idempotent: a second call for the same pane
// closes the previous attachment first. The only host that cannot do this
// is one whose NODE refuses shells (server.yaml disable_shells), and then
// the gesture explains itself and the frozen preview stays.
func (a *App) openShellStream(p *pane.Pane, tileID string) {
	if !a.caps.LiveShell {
		a.reportErr(caps.ShellNotice())
		return
	}
	// Resolve a shell link to its target: the PTY session, the alive cache,
	// and the freeze writeback all key by the id that owns the session.
	tileID = a.contentKey(tileID)
	// Idempotent: this pane is already attached to this session, a
	// keep-alive return.
	if conn := a.shellConnFor(p.ID); conn != nil && conn.tileID == tileID {
		return
	}
	// One live surface per content tile: another pane attached to this tmux
	// session detaches, with a freeze, because two attachments would fight
	// over the terminal size. pane.TakeOver is the rule, shared with the url
	// side.
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
	// Sized + positioned by syncShellOverlayPosition each frame; start
	// off-screen so we don't flash a 0x0 terminal during the descent
	// transition.
	style.Set("left", "-9999px")
	style.Set("top", "-9999px")
	style.Set("width", "300px")
	style.Set("height", "200px")
	doc.Get("body").Call("appendChild", container)

	// The overlay paints above the canvas and would otherwise swallow the
	// right-button mousedown that starts a pane gesture, leaving split,
	// clone, and resize reachable only from the thin border ring. Forward
	// the right button into the same canvas gesture pipeline every other
	// tile uses; the left button stays with xterm — typing, caret,
	// selection — except for its pane-focus side effect, handled inline
	// below. Once onMouseDown sets a right gesture, draw() parks this
	// overlay (liveOverlaysHidden), so the rest of the drag lands on the
	// canvas.
	onMouse := js.FuncOf(func(_ js.Value, args []js.Value) any {
		ev := args[0]
		if ev.Get("type").String() == "contextmenu" {
			ev.Call("preventDefault")
			return nil
		}
		if ev.Get("button").Int() != 2 {
			// Left and middle stay with xterm — typing, caret, selection —
			// but pane focus still follows the click: the overlay swallows
			// the mousedown, so the canvas path that normally transfers
			// focus never runs. Without this, clicking into a terminal from
			// another pane leaves Gridwell focus behind. The same contract
			// as the URL view's forwarded left-down.
			if cur := a.tree.FindPane(p.ID); cur != nil {
				a.focusToPane(cur)
			}
			return nil
		}
		ev.Call("preventDefault")
		ev.Call("stopPropagation")
		a.onMouseDown(js.Null(), args)
		// onRightDown arms the gesture but does not redraw for a split,
		// swap, or resize. Park the overlay now — it consults
		// liveOverlaysHidden — so the rest of the drag, every mousemove and
		// the mouseup, lands on the canvas instead of this still-visible
		// div, which is not forwarded.
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
		// The user just descended into a shell: a console line alone
		// presents as an empty pane that "just disappeared", so say it on
		// the error surface, as the alive probe above does.
		a.reportErr(errsurface.Error, "shell", "terminal engine unavailable on this host (xterm not loaded)")
		doc.Get("body").Call("removeChild", container)
		return
	}
	opts := js.Global().Get("Object").New()
	// xterm 6 gates its proposed APIs behind this flag:
	// parser.registerOscHandler (the gridwell-open OSC 5522 hook) and
	// term.unicode.activeVersion (the Unicode 11 widths). Both are
	// load-bearing here; without the flag, Terminal method calls panic the
	// wasm.
	opts.Set("allowProposedApi", true)
	opts.Set("fontFamily", `ui-monospace, "SF Mono", Menlo, Consolas, monospace`)
	// The base font scaled by the tile's persisted content zoom, so a zoomed
	// terminal comes back at your size on every descent.
	fontSize := int(shellBaseFontPx)
	if t := a.findTileByID(tileID); t != nil {
		fontSize = int(shellBaseFontPx*contentZoomOf(t) + 0.5)
	}
	opts.Set("fontSize", fontSize)
	// No convertEol: the PTY line discipline (ONLCR) already delivers CRLF.
	// With it set, xterm snaps the cursor to column 0 on every bare LF too,
	// and TUI output that positions with index or LF — scroll-region feeds —
	// loses its column and scatters text down the left margin.
	opts.Set("cursorBlink", true)
	// Dark theme tuned to the shell-fill / markdown-body backgrounds.
	theme := js.Global().Get("Object").New()
	theme.Set("background", "#0c0d11")
	theme.Set("foreground", "#d8d9de")
	theme.Set("cursor", "#c87a5a")
	opts.Set("theme", theme)
	term := Terminal.New(opts)

	// Unicode 11 widths: the default table is Unicode 6, so modern emoji and
	// wide glyphs get the wrong cell width and shift every following cell on
	// the line. Loaded before open, so the first paint measures correctly.
	if u11 := js.Global().Get("Unicode11Addon"); u11.Truthy() {
		term.Call("loadAddon", u11.Get("Unicode11Addon").New())
		term.Get("unicode").Set("activeVersion", "11")
	}

	// The fit addon resizes the buffer dimensions to fit the container, which
	// is then mirrored to the server. The renderer addon (WebGL, with a DOM
	// fallback) is attached after open, because the WebGL addon requires an
	// opened terminal.
	fitAddon := js.Global().Get("FitAddon").Get("FitAddon").New()
	term.Call("loadAddon", fitAddon)
	term.Call("open", container)
	renderAddon, rendererKind := attachShellRenderer(term)

	// Touch: multi-finger gestures feed the shared translation, and single
	// fingers stay native to the terminal. The ascend handle is the bottom
	// bar's slot, which the canvas touch layer covers.
	touchFns := a.installOverlayTouch(container, shellTouchClaim())

	// Initial size — the fit addon will overwrite it, but the bind message
	// lets the plugin start the PTY at the right dimensions.
	cols := term.Get("cols").Int()
	rows := term.Get("rows").Int()
	shellLog("open pane=%s tile=%s cols=%d rows=%d", p.ID, tileID, cols, rows)

	conn := &shellStreamConn{
		term:         term,
		fitAddon:     fitAddon,
		renderAddon:  renderAddon,
		rendererKind: rendererKind,
		container:    container,
		tileID:       tileID,
		paneID:       p.ID,
		anchor:       p.Anchor(),
		path:         slices.Clone(p.Path()),
		onMouse:      onMouse,
		touchFns:     touchFns,
		lastCols:     uint16(cols),
		lastRows:     uint16(rows),
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
	links := js.Global().Get("Object").New()
	links.Set("provideLinks", conn.onLinkProvide)
	term.Call("registerLinkProvider", links)

	// OSC 5522: the gridwell-open browser shim ($BROWSER in every session,
	// internal/tmux) hands back a url a terminal app tried to open — emacs
	// browse-url, xdg-open — so it descends into an ephemeral url tile here
	// instead of spawning a browser on the host. The sequence rides the PTY
	// byte stream (tmux passthrough, then the socket, then term.write), so
	// it works unchanged for remote shells through ssh mounts.
	conn.onOSCURL = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) >= 1 && args[0].Type() == js.TypeString {
			a.shellURLActivate(p.ID, args[0].String())
		}
		return true // consumed
	})
	term.Get("parser").Call("registerOscHandler", 5522, conn.onOSCURL)

	// xterm → PTY: bytes from the user's keypresses, IME, paste. Frames
	// typed before the socket finishes opening are queued by the dialer,
	// not dropped (client/shellws), so no queue is needed here.
	conn.onData = js.FuncOf(func(_ js.Value, args []js.Value) any {
		// args[0] is a JS string; its Go form is the UTF-8 encoding the
		// PTY wants, and control sequences (arrow keys, ESC) are ASCII in
		// both.
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
	// Open the socket; PTY output arrives at onShellData and an unexpected
	// end at onShellExit, both routed by pane id from the registry.
	a.shells.Open(p.ID, tileID, int(cols), int(rows))
	// First sync: position over the pane content area now that we know
	// the rect.
	a.syncShellOverlayPosition()
	// Focus so keystrokes land in the terminal rather than the canvas.
	term.Call("focus")
}

// attachShellRenderer gives the opened terminal a GPU renderer: the WebGL
// addon, falling back to xterm's DOM renderer when WebGL2 is unavailable —
// headless xvfb without GL, an exotic browser in web mode — or when the GPU
// context is later lost. The WebGL renderer redraws from buffer state every
// frame and the DOM fallback repaints rows wholesale, so neither can leave
// the stale-region artifacts a dirty-region canvas renderer does. Returns the
// active addon, held on shellStreamConn to pin it; the terminal's dispose
// tears it down.
func attachShellRenderer(term js.Value) (js.Value, string) {
	if addon, ok := tryWebglAddon(term); ok {
		return addon, "webgl"
	}
	// No renderer addon means xterm's built-in DOM renderer: slower, but
	// free of dirty-region artifacts. The kind is recorded and e2e-asserted,
	// so a downgrade can never be silent.
	shellLog("shell renderer: DOM FALLBACK (webgl unavailable)")
	return js.Value{}, "dom"
}

// tryWebglAddon loads the WebGL renderer, reporting ok=false when the addon
// is missing or throws (no WebGL2 context available). Constructed with
// preserveDrawingBuffer=true so the freeze capture's toDataURL reads real
// pixels rather than a cleared back buffer.
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
	// A lost GPU context would leave the terminal frozen mid-session: dispose
	// the webgl addon; xterm continues on its built-in DOM renderer in
	// place. One-shot; the callback releases itself.
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

// shellContentCanvas returns the canvas the terminal content is painted on,
// not merely the first canvas in the container. The WebGL renderer's main
// canvas is class-less while its link layer — transparent and glyph-free —
// comes first in the DOM, so capturing the first canvas yields an all-black
// preview. The DOM fallback has no canvas at all; callers handle nil.
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

// onShellData routes PTY output to the pane's terminal. A push for a pane
// with no live conn (torn down a beat ago) is dropped — the registry
// already suppresses replaced-stream stragglers; this guards the tail.
// xterm takes a Uint8Array so terminal control bytes survive untouched.
func (a *App) onShellData(paneID string, data []byte) {
	conn := a.shellConnFor(paneID)
	if conn == nil || conn.closed {
		return
	}
	u8 := js.Global().Get("Uint8Array").New(len(data))
	js.CopyBytesToJS(u8, data)
	conn.term.Call("write", u8)
}

// onShellExit handles an unexpected stream end (the registry suppresses the
// report for a local close — this side already tore down). sessionGone is the
// server's definitive "this tmux session no longer exists": flip the cache
// so the refresh affordance hides. Any other end carries no liveness
// verdict — drop the cached answer and let the next render re-probe.
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

// shellMirrorIntervalMs is how often a live shell terminal is snapshotted and
// pushed to the shared preview cache, so other panes, and well child
// previews, showing the same shell tile mirror it live. It is the shell
// analogue of the URL mirror pump, which lives in the Electron main process
// and cannot see the renderer-side xterm canvas. Modest by design: toDataURL
// is not free and a mirrored preview does not need 60fps.
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

// closeShellStream is the freeze path: capture a JPEG of the terminal, so the
// next descent shows it as the frozen preview, post it through
// SetShellPreview, then end the stream. The registry closes the socket and
// suppresses the exit report, because this side asked. The stream close only
// detaches the tmux client: the shell keeps running inside the tmux session,
// so a future refresh reattaches to the same state. An ephemeral shell's
// ascent passes freeze=false — the tile, and its tmux session, is about to be
// deleted, so there is nothing to freeze for. Idempotent.
func (a *App) closeShellStream(paneID string, freeze bool) {
	conn := a.shellConnFor(paneID)
	if conn == nil {
		return
	}
	conn.closed = true
	// Best-effort JPEG capture from the renderer's content canvas. If
	// anything goes wrong the preview update is skipped; the cwd still
	// persists through the server's close handler.
	if jpegBytes := snapshotShellCanvas(conn.container); freeze && jpegBytes != nil {
		tileID := conn.tileID
		// Update the local preview cache immediately so the next descent
		// shows this frame instead of the previous one. The server-side
		// blob id this snapshot will get is unknown until SetShellPreview
		// returns, so store it as a wildcard: Get satisfies any expected
		// blob id until a specific Put supersedes it, on the next
		// GetTilePreview.
		a.urlPreview.PutWildcard(tileID, jpegBytes, func() { a.draw() })
		go a.postSetShellPreview(tileID, conn.anchor, slices.Clone(conn.path), jpegBytes)
	}
	a.shells.Close(paneID)
	// The registry suppresses the exit report for a local close, so the
	// teardown runs here, synchronously.
	a.releaseShellStream(paneID, conn)
}

// closeAllShellStreams closes every open shell stream. Used on
// beforeunload so the server's freeze-and-destroy runs before the tab
// goes away. shellSurfaces is the snapshot, so closing as we go never walks
// a map being mutated.
func (a *App) closeAllShellStreams() {
	for _, h := range a.shellSurfaces() {
		a.closeShellStream(h.PaneID, true)
	}
}

// releaseShellStream tears down the DOM + js.Func handlers. Called from
// closeShellStream (local close) and onShellExit (unexpected end).
func (a *App) releaseShellStream(paneID string, conn *shellStreamConn) {
	if pl, ok := a.localIf(paneID); ok && pl.shellConn == conn {
		pl.shellConn = nil
	}
	// Detach handlers and dispose the terminal before removing the
	// DOM node so xterm's internal listeners don't fire against a
	// removed element.
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
		// Hide the shell overlay when the pane is not currently descended
		// in this shell — it descended further into an ephemeral url from a
		// shell link, say. The stream stays alive and the session persists;
		// only the overlay parks, reappearing when the pane returns.
		if p := a.tree.FindPane(paneID); p == nil || p.ContentID() != conn.tileID {
			conn.container.Get("style").Set("display", "none")
			continue
		}
		// The one live content box (paneContentBox): the same rect the URL
		// view and the canvas fallback use, so every live-tile kind sits
		// flush inside the chrome.
		cx, cy, cw, ch := paneContentBox(r)
		cb := pane.Rect{X: cx, Y: cy, W: cw, H: ch}
		if cb.W < 1 || cb.H < 1 {
			conn.container.Get("style").Set("display", "none")
			continue
		}
		style := conn.container.Get("style")
		style.Set("display", "block")
		setBoundsPx(style, cb.X, cb.Y, cb.W, cb.H)
		// Ask xterm to re-fit, but only when an input the fit depends on
		// changed: the container box, or the font size content zoom sets.
		// The FitAddon emits an onResize event if the cell grid changed,
		// forwarded through the registered onResize callback. See lastFit*
		// on the struct for why this is guarded.
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
	// Prefix-validate and base64-decode in Go, not through JS atob, using
	// shellconn.DecodeJPEGDataURL; see there for why atob corrupts the
	// bytes.
	out, ok := shellconn.DecodeJPEGDataURL(dataURL.String())
	if !ok {
		return nil
	}
	return out
}

// postSetShellPreview sends a SetShellPreview RPC with the captured JPEG. The
// anchor and path locate the tile's leaf grid; the server validates the tile
// against the path, so a shell inside a well needs the real descent path.
func (a *App) postSetShellPreview(tileID, anchor string, path []string, jpeg []byte) {
	// One dispatcher, keyed in the outbox by the tile. The frozen frame is a
	// capture — no claim, no version bump — so the stream close racing this
	// freeze cannot refuse it. A transport failure parks the closure, which
	// holds the only remaining copy of the jpeg once the live surface is
	// gone. A verdict surfaces and resyncs the grid: the terminal frame the
	// user just left was not persisted, and the preview will show an older
	// state.
	req := &rpc.SetShellPreviewRequest{TileID: tileID, JPEG: jpeg}
	a.do(write{
		label: "SetShellPreview", gid: a.gridIDForPathFrom(anchor, path), id: tileID,
		source: "shell", failText: "shell preview save failed",
		call: func(ctx context.Context) error {
			_, err := a.cl.SetShellPreview(ctx, req)
			if err != nil {
				shellLog("SetShellPreview tile=%s err=%v", tileID, err)
			}
			return err
		},
	})
}
