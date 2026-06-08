//go:build js && wasm

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// shellStreamConn is one ShellStream WebSocket + its xterm.js host.
// A DOM container holds the terminal instance; the container's CSS is
// positioned per-frame so it tracks the pane through resize / pan /
// split-tree edits. js.Func handlers are tracked so we Release them on
// close (FuncOf-allocated callbacks pin Go memory until released).
type shellStreamConn struct {
	ws        js.Value
	term      js.Value // xterm.Terminal
	fitAddon  js.Value // FitAddon — proposeDimensions + fit
	canvasAdd js.Value // CanvasAddon — gives the terminal a single <canvas>
	container js.Value // host <div> in the DOM

	tileID int64
	paneID string

	onMessage js.Func
	onOpen    js.Func
	onClose   js.Func
	onError   js.Func
	onData    js.Func // term.onData(bytes string) callback
	onResize  js.Func // term.onResize({cols, rows})

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
	if p == nil || p.TextFocus == 0 {
		return false
	}
	gid := a.gridIDForPath(p.Path)
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
	if a.shellStreams == nil {
		return false
	}
	return a.shellStreams[paneID] != nil
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
	if tile == nil || tile.Kind != rpc.KindShell {
		return false
	}
	if tile.PreviewBlobID == 0 {
		return true
	}
	if alive, ok := a.shellAlive[tile.ID]; ok {
		return alive
	}
	a.probeShellSessionAlive(tile.ID)
	return false
}

// probeShellSessionAlive fires ShellSessionAlive RPC for tileID,
// caches the result, and triggers a redraw on completion. Idempotent:
// short-circuits if a probe is already in flight.
func (a *App) probeShellSessionAlive(tileID int64) {
	if a.shellAliveProbing[tileID] {
		return
	}
	a.shellAliveProbing[tileID] = true
	go func() {
		res, err := a.cl.ShellSessionAlive(context.Background(), &rpc.ShellSessionAliveRequest{TileID: tileID})
		// Probing flag clears regardless so a future probe can retry.
		delete(a.shellAliveProbing, tileID)
		if err != nil {
			shellLog("ShellSessionAlive tile=%d err=%v", tileID, err)
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
func (a *App) setShellAlive(tileID int64, alive bool) {
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
func (a *App) openShellStream(p *pane.Pane, tileID int64) {
	a.closeShellStream(p.ID)

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
	opts.Set("fontSize", 13)
	opts.Set("convertEol", true)
	opts.Set("cursorBlink", true)
	// Dark theme tuned to colorExitFill / colorMarkdownBody.
	theme := js.Global().Get("Object").New()
	theme.Set("background", "#0c0d11")
	theme.Set("foreground", "#d8d9de")
	theme.Set("cursor", "#c87a5a")
	opts.Set("theme", theme)
	term := Terminal.New(opts)

	// Canvas addon gives us one <canvas> element we can toDataURL at
	// freeze; fit addon resizes the buffer dimensions to fit the
	// container, which we then mirror to the server.
	fitAddon := js.Global().Get("FitAddon").Get("FitAddon").New()
	canvasAdd := js.Global().Get("CanvasAddon").Get("CanvasAddon").New()
	term.Call("loadAddon", fitAddon)
	term.Call("loadAddon", canvasAdd)
	term.Call("open", container)

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
		strconv.FormatInt(tileID, 10) +
		"&cols=" + strconv.Itoa(cols) + "&rows=" + strconv.Itoa(rows)
	shellLog("open pane=%s tile=%d url=%s cols=%d rows=%d", p.ID, tileID, wsURL, cols, rows)

	ws := js.Global().Get("WebSocket").New(wsURL)
	ws.Set("binaryType", "arraybuffer")

	conn := &shellStreamConn{
		ws:        ws,
		term:      term,
		fitAddon:  fitAddon,
		canvasAdd: canvasAdd,
		container: container,
		tileID:    tileID,
		paneID:    p.ID,
		lastCols:  uint16(cols),
		lastRows:  uint16(rows),
	}

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
		shellLog("onOpen pane=%s tile=%d flushing=%d", p.ID, tileID, len(pending))
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
		shellLog("onClose pane=%s tile=%d code=%d reason=%q", p.ID, tileID, code, reason)
		// PolicyViolation (1008) is the server's "session is gone"
		// signal — wasm asked to attach but the tmux session no
		// longer exists. Flip the cache so the refresh button hides
		// on this tile until the user does something that creates
		// a fresh session (which today is only "fresh tile, no
		// snapshot" — i.e. never, for a snapshotted tile). Other
		// close codes (NormalClosure, abnormal) mean the session
		// was alive at least until the close: leave the cache as
		// alive=true to skip a probe on the next descent.
		if code == 1008 {
			a.setShellAlive(tileID, false)
		} else {
			a.setShellAlive(tileID, true)
		}
		a.releaseShellStream(p.ID, conn)
		a.draw()
		return nil
	})
	conn.onError = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		shellLog("onError pane=%s tile=%d readyState=%d", p.ID, tileID, conn.ws.Get("readyState").Int())
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

	if a.shellStreams == nil {
		a.shellStreams = map[string]*shellStreamConn{}
	}
	a.shellStreams[p.ID] = conn
	// First sync: position over the pane content area now that we know
	// the rect.
	a.syncShellOverlayPosition()
	// Focus so keystrokes land in the terminal rather than the canvas.
	term.Call("focus")
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
	state := c.ws.Get("readyState").Int()
	switch state {
	case 0:
		c.pending = append(c.pending, u8)
	case 1:
		c.ws.Call("send", u8)
	}
}

// sendText forwards a string on the WebSocket. Used for control
// messages (resize) that go as text frames per the wire protocol.
func (c *shellStreamConn) sendText(s string) {
	state := c.ws.Get("readyState").Int()
	switch state {
	case 0:
		c.pending = append(c.pending, s)
	case 1:
		c.ws.Call("send", s)
	}
}

// closeShellStream is the freeze path: capture a JPEG of the
// terminal (so the next descent shows it as the frozen preview),
// POST it via SetShellPreview, then close the WebSocket. The
// server-side WS-close just detaches the tmux client — bash keeps
// running inside the tmux session so a future refresh reattaches to
// the same state. Idempotent.
func (a *App) closeShellStream(paneID string) {
	conn, ok := a.shellStreams[paneID]
	if !ok {
		return
	}
	conn.closed = true
	// Best-effort JPEG capture from the canvas addon's element. If
	// anything goes wrong we just skip the preview update — the cwd
	// still persists via the server's close handler.
	if jpegBytes := snapshotShellCanvas(conn.container); jpegBytes != nil {
		tileID := conn.tileID
		// Update the local preview cache immediately so the next
		// descent shows this frame instead of the previous one. The
		// server-side blob id this snapshot will get isn't known
		// until SetShellPreview returns, so store as wildcard — Get
		// will satisfy any expected blob id until a specific Put
		// supersedes (which happens on the next GetTilePreview).
		a.urlPreview.PutWildcard(tileID, jpegBytes, func() { a.draw() })
		go a.postSetShellPreview(tileID, jpegBytes)
	}
	if conn.ws.Truthy() {
		conn.ws.Call("close")
	}
}

// closeAllShellStreams closes every open shell stream. Used on
// beforeunload so the server's freeze-and-destroy runs before the tab
// goes away.
func (a *App) closeAllShellStreams() {
	ids := make([]string, 0, len(a.shellStreams))
	for id := range a.shellStreams {
		ids = append(ids, id)
	}
	for _, id := range ids {
		a.closeShellStream(id)
	}
}

// releaseShellStream tears down the DOM + js.Func handlers after the
// WebSocket fires onClose. Called from the onClose handler.
func (a *App) releaseShellStream(paneID string, conn *shellStreamConn) {
	if cur, ok := a.shellStreams[paneID]; ok && cur == conn {
		delete(a.shellStreams, paneID)
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
	if len(a.shellStreams) == 0 {
		return
	}
	rects := a.layoutPanes()
	for paneID, conn := range a.shellStreams {
		r, ok := rects[paneID]
		if !ok {
			conn.container.Get("style").Set("display", "none")
			continue
		}
		// Inset by the pane border so the overlay sits flush inside
		// the chrome.
		ix := r.X + paneBorderPx
		iy := r.Y + paneBorderPx
		iw := r.W - 2*paneBorderPx
		ih := r.H - 2*paneBorderPx
		if iw < 1 || ih < 1 {
			conn.container.Get("style").Set("display", "none")
			continue
		}
		style := conn.container.Get("style")
		style.Set("display", "block")
		style.Set("left", strconv.FormatFloat(ix, 'f', 1, 64)+"px")
		style.Set("top", strconv.FormatFloat(iy, 'f', 1, 64)+"px")
		style.Set("width", strconv.FormatFloat(iw, 'f', 1, 64)+"px")
		style.Set("height", strconv.FormatFloat(ih, 'f', 1, 64)+"px")
		// Ask xterm to re-fit. The FitAddon emits an onResize event if
		// the new dimensions differ from the previous, which we forward
		// via the registered onResize callback.
		if conn.fitAddon.Truthy() {
			conn.fitAddon.Call("fit")
		}
	}
}

// snapshotShellCanvas pulls the canvas element xterm's CanvasAddon
// renders into and returns its bytes as JPEG. Returns nil if no
// canvas is present (renderer hasn't initialized, or addon failed to
// load).
func snapshotShellCanvas(container js.Value) []byte {
	if !container.Truthy() {
		return nil
	}
	// xterm with CanvasAddon paints into one canvas at a known
	// selector; querySelector('canvas') returns the first one.
	canvas := container.Call("querySelector", "canvas")
	if !canvas.Truthy() {
		return nil
	}
	dataURL := canvas.Call("toDataURL", "image/jpeg", 0.85)
	if !dataURL.Truthy() {
		return nil
	}
	s := dataURL.String()
	// "data:image/jpeg;base64,XXXXX..."
	const prefix = "data:image/jpeg;base64,"
	if len(s) <= len(prefix) || s[:len(prefix)] != prefix {
		return nil
	}
	// Decode base64 in Go. NOT through JS atob: atob returns a "binary
	// string" whose code units are byte values, but Go's js.Value.String()
	// re-encodes as UTF-8, doubling every byte >= 0x80. The resulting
	// blob is not a valid JPEG (FF D8 ... becomes C3 BF C3 98 ...).
	out, err := base64.StdEncoding.DecodeString(s[len(prefix):])
	if err != nil {
		return nil
	}
	return out
}

// postSetShellPreview sends a SetShellPreview RPC with the captured
// JPEG. The previous tile version is needed for optimistic
// concurrency; we look it up from the cache to avoid a synchronous
// GetTile round-trip in the ascent path.
func (a *App) postSetShellPreview(tileID int64, jpeg []byte) {
	// Find the tile in any cached grid.
	var version int64
	for _, gid := range a.c.KnownGridIDs() {
		g, ok := a.c.Grid(gid)
		if !ok {
			continue
		}
		if t, ok := g.Tiles[tileID]; ok {
			version = t.Version
			break
		}
	}
	req := &rpc.SetShellPreviewRequest{
		TileID:  tileID,
		Version: version,
		JPEG:    jpeg,
	}
	_, err := a.cl.SetShellPreview(context.Background(), req)
	if err != nil {
		shellLog("SetShellPreview tile=%d err=%v", tileID, err)
	}
}
