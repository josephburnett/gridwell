//go:build js && wasm

// Package main is the WASM entry point for the Gridwell client: canvas, DOM,
// and the RPC calls. The code here reaches into syscall/js and is exercised
// only in a browser, so every decision belongs in one of the pure, tested
// client/* packages instead — ARCHITECTURE.md, "The client".
package main

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"syscall/js"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/cadence"
	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/debounce"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/events"
	"github.com/josephburnett/gridwell/client/inflight"
	"github.com/josephburnett/gridwell/client/menu"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/outbox"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panepreview"
	"github.com/josephburnett/gridwell/client/preview"
	"github.com/josephburnett/gridwell/client/rasterprev"
	"github.com/josephburnett/gridwell/client/retry"
	"github.com/josephburnett/gridwell/client/shellstream"
	"github.com/josephburnett/gridwell/client/shellws"
	"github.com/josephburnett/gridwell/client/textedit"
	"github.com/josephburnett/gridwell/client/theme"
	"github.com/josephburnett/gridwell/client/touchgest"
	"github.com/josephburnett/gridwell/client/trace"
	"github.com/josephburnett/gridwell/client/traceevent"
	"github.com/josephburnett/gridwell/client/transition"
)

const (
	cellPx = pane.CellPx
	// zoomMin is the grid zoom floor, one value for every gesture and every
	// client. 1/32 puts cells at 2px, below the 4px line where drawGridLinesIn
	// stops painting, so the deep end is a line-less overview of color blocks.
	zoomMin    = 0.03125
	zoomMax    = 8.0
	zoomFactor = 1.1

	// wellZoomRatio* clamp the hover-wheel well zoom in the intrinsic ratio's
	// units, where previewCell = parentCell times ratio. The min keeps the
	// preview above the renderer's 0.5px floor.
	wellZoomRatioMin = 1.0 / 64.0
	wellZoomRatioMax = 1.0

	// textFixedScale is the constant render scale for text, descended and
	// previewed alike, so grid zoom reveals more lines instead of magnifying
	// the type.
	textFixedScale = 1.0
)

// app is package-level so JS callbacks reach it without closures.
var app *App

type App struct {
	doc, win js.Value
	canvas   js.Value
	cctx     js.Value // 2d context

	cl *rpc.Client

	// plugins is the Handshake's menu-row list, in config order.
	plugins []*gridwellv1.PluginInfo

	// home is the qualified grid id "/" means; every "empty anchor" reader
	// resolves through it.
	home string

	tree *pane.Tree
	c    *cache.Cache

	persist persistState

	width, height float64

	dragging *dragState

	// locals is the per-pane session-local state. The pane's place lives on the
	// pane itself; a.forgetPane removes an entry atomically.
	locals map[string]*paneLocal

	// menu is the single owner of the + menu's open state, pane and hover; never
	// assign its fields directly. It survives a descent on Frame.MenuOpen.
	menu menu.State

	// errs is the single owner of user-visible failure notices; every failure
	// reports through a.reportErr or a.resolveErr.
	errs *errsurface.Surface

	// tr is this client's ring of trace records and pump the one post in
	// flight. The cid is minted at boot, so a dump says which page's records
	// these are.
	tr   *trace.Client
	pump *trace.Pump

	views viewCaches

	// ws is the window's level stack: which pane tile the user is inside and
	// what outer tree each descent restores. a.tree displays its top.
	ws pane.Levels

	// caps is derived once at boot; nothing else asks the bridge for a decision.
	caps caps.Caps

	// pal is the colors every paint reads and themeName is which palette that
	// is. a.setTheme is the one writer: it restyles the DOM and redraws, so no
	// surface can be left wearing the palette before it.
	pal       theme.Palette
	themeName theme.Theme

	// origin is the serving origin and contentToken the /content/ door's path
	// capability. A served page's address is derived at use time, never
	// persisted.
	origin       string
	contentToken string

	// unloading switches framing writes to sendBeacon; see unload.go.
	unloading bool

	// touchDownTarget is the element the gesture started on, where synthetic
	// MouseDowns route. Owned by touch.go.
	touch           *touchgest.Machine
	touchTimerCb    js.Func
	touchDownTarget js.Value

	fetch fetchState

	// ghost renders a dragged tile at sub-cell screen precision.
	ghost *ghost

	animation *anim.Animation

	// trans holds at most one zoom animation per pane. One displaced or cleared
	// lands on its destination, so a descent is never voided after animating.
	trans *transition.Set

	// nav is every descent and ascent decision, as data. nav.go gathers the
	// world it plans against; nav_exec.go runs the effects it returns.
	nav *nav.Machine

	rightDrag *rightDragState

	// leftResize clamps to the pane minimum; the release decides a close.
	leftResize *leftResizeState

	// shellAlive caches the ShellSessionAlive probe; a missing key is unknown.
	// shellAliveProbing single-flights it, dropping no caller's callback.
	shellAlive        map[string]bool
	shellAliveProbing map[string][]func(alive bool)

	// shells owns the PTY lifecycle rules; this file hands it a dialer.
	shells *shellstream.Registry

	// shellMirrorPasses counts mirror ticks. e2e-only: the mirror writes into a
	// cache and nothing else reports that it ran.
	shellMirrorPasses int

	// traces holds the per-pane ascent-trace highlight, ephemeral like selection.
	traces map[string]traceState

	overlays overlayState

	// zoomKeyRelays counts zoom chords from the main-process relay. e2e-only:
	// with the registry's counter it brackets the IPC hop.
	zoomKeyRelays int

	// writes counts dispatched mutations that have not settled, so the
	// descent or placement that follows one has not happened yet. `post` and
	// `do` are its only callers.
	writes inflight.Writes

	// renderedPanePaints is e2e attribution: an unfocused pane paints raster.
	renderedPanePaints map[string]int

	// backstop is the outbox re-post cadence, retry.Backstop until the e2e
	// lowers it to bound one.
	backstop *retry.Interval
}

// overlayState holds the DOM singletons layered over the canvas. Each is
// created lazily and reused for the life of the page, so a descent allocates no
// fresh DOM.
type overlayState struct {
	// textTextarea shows only over a focused pane in TextMode "text".
	textTextarea js.Value
	// Held so they can be released cleanly if the App is torn down (never).
	textTextareaInputCb  js.Func
	textTextareaScrollCb js.Func

	// renameEditing marks the shared inline rename input open.
	renameEditing bool

	// renderedReady mirrors textareaReady for rendered mode; lastRenderedKey
	// caches the render, so scrolling never re-renders.
	renderedView js.Value
	// renderedStyle is the overlay's scoped stylesheet element, kept so a
	// theme switch rewrites it in place rather than stacking a second sheet.
	renderedStyle   js.Value
	renderedReady   bool
	lastRenderedKey string

	// choiceMenu is the DOM popover the circle's right-click opens on a host
	// with no native menu; choiceMenuCbs are its listener removers.
	choiceMenu    js.Value
	choiceMenuCbs []func()

	// wsExpand is the in-flight first-descent capture animation, nil when none.
	wsExpand *wsExpandState

	// textToggleBtn is a DOM element, not a canvas button, so it can sit above
	// the textarea and let the text fill the pane edge to edge.
	textToggleBtn js.Value
	textToggleCb  js.Func

	// urlModalOpen makes a second openURLModal call a no-op.
	urlModalOpen bool

	// lastTextareaTileID is what the singleton textarea is bound to: on a
	// refresh the same tile preserves typing, a different one is re-seeded.
	lastTextareaTileID string

	// textareaReady says the textarea holds the focused tile's content, so the
	// canvas keeps painting through the loading race instead of blanking.
	textareaReady bool
}

// viewCaches holds derived views, never facts: every one is recomputable, so
// the group may be dropped wholesale without losing anything the user made.
type viewCaches struct {
	// urlPreview invalidates itself when a tile's PreviewBlobID changes.
	urlPreview *preview.Cache

	// wrapCache memoizes raw-text soft-wrap results, reset wholesale when full.
	wrapCache map[string][]string

	// renderedPrev caches rasterized rendered-mode previews, invalidating
	// itself when a tile's version or its layout width moves.
	renderedPrev *rasterprev.Cache

	// paneLayouts memoizes the decode, invalidated by blob generation; the
	// truth is the tile row plus its content bytes.
	paneLayouts *panepreview.Layouts

	// menuCtxs is keyed by the grid-stamped node_ns; "" is a.plugins, a.caps.
	menuCtxs map[string]*menuContext
}

// newViewCaches is the one place the group is constructed.
func newViewCaches(onPreviewDecodeErr, onRasterErr func(tileID string), onLayoutErr func(tileID string, err error)) viewCaches {
	return viewCaches{
		urlPreview:   preview.NewCache(preview.NewJSDecoder(), onPreviewDecodeErr),
		wrapCache:    map[string][]string{},
		renderedPrev: rasterprev.NewCache(svgRasterizer{}, onRasterErr),
		paneLayouts:  panepreview.NewLayouts(onLayoutErr),
		menuCtxs:     map[string]*menuContext{},
	}
}

// fetchState owns whether a read is outstanding or has failed. A claim kept
// elsewhere is how a swallowed request holds a key for the life of the page.
type fetchState struct {
	// grids, tiles, contents, previews and menus are the reads every draw
	// fires on a miss: GetGrid by grid id, GetTile by a routable id whose
	// grid was never visited, ReadContent and GetTilePreview by
	// rpc.ContentID, and a remote pane's menu Handshake by node namespace.
	// inflight.Reads owns why each failure latches and what clears it.
	grids    *inflight.Reads
	tiles    *inflight.Reads
	contents *inflight.Reads
	previews *inflight.Reads
	menus    *inflight.Reads
}

// newFetchState is the one place the group is constructed.
func newFetchState() fetchState {
	return fetchState{
		grids:    inflight.NewReads(),
		tiles:    inflight.NewReads(),
		contents: inflight.NewReads(),
		previews: inflight.NewReads(),
		menus:    inflight.NewReads(),
	}
}

// oneShot is the only way the shim hands the host a callback it will be
// called back on once. An unreleased js.Func stays in syscall/js's funcs map
// for the life of the page, and an armed-per-frame callback makes that a leak
// the size of the animation.
func oneShot(fn func()) js.Func {
	oneShotsArmed++
	oneShotsLive++
	var cb js.Func
	cb = js.FuncOf(func(js.Value, []js.Value) any {
		oneShotsLive--
		cb.Release()
		fn()
		return nil
	})
	return cb
}

// oneShotsArmed, oneShotsLive and framesArmed are what the e2e hook asserts
// on: a js.Func that outlives its call is invisible from both sides of the
// boundary, so the count is the only evidence. framesArmed says how many of
// the armed ones were frames, because how many frames a gesture draws is the
// host's to decide, not the spec's.
var oneShotsArmed, oneShotsLive, framesArmed int

// listen adds a DOM listener and returns the remover, which also releases the
// js.Func: a surface opened and closed repeatedly must not leak one per open.
func listen(target js.Value, event string, fn func(js.Value)) func() {
	cb := js.FuncOf(func(_ js.Value, args []js.Value) any {
		ev := js.Undefined()
		if len(args) > 0 {
			ev = args[0]
		}
		fn(ev)
		return nil
	})
	target.Call("addEventListener", event, cb)
	return func() {
		target.Call("removeEventListener", event, cb)
		cb.Release()
	}
}

// setTimeoutMs is the shim's debounce.Schedule. A debounce coalesces, so at
// most one is alive per settle window.
func setTimeoutMs(ms int, fire func()) {
	js.Global().Call("setTimeout", oneShot(fire), ms)
}

// persistState is the write-out side of the client. The navigation machine
// emits the Flush* effects; this group is what executes them.
type persistState struct {
	sched scheduler

	// textSaves chains pipelined saves per document instead of racing them. The
	// key is textedit.SaveQueueKey's, so a link and its target share a chain.
	textSaves *textedit.SaveQueue

	// wellWheelPending: the cache is patched per notch and the settle flush
	// posts one SetFraming per tile, so a scroll burst is one write.
	wellWheelPending map[string]wellWheelDrift

	// persistPosts and framingFlushes are e2e-only: the settle chain is
	// otherwise silent at every stage.
	persistPosts   map[string]int
	framingFlushes int

	// out records unacknowledged writes in order; retryKick and unload drain it.
	out *outbox.Outbox
}

// newPersistState is the one place the group is built. It takes the App
// because a debounce holds its body from construction, and the first draw
// arms two of them: see client/debounce.
func newPersistState(a *App) persistState {
	return persistState{
		sched:            newScheduler(a),
		textSaves:        textedit.NewSaveQueue(),
		wellWheelPending: map[string]wellWheelDrift{},
		persistPosts:     map[string]int{},
		out:              outbox.New(),
	}
}

type scheduler struct {
	frameAsks traceevent.FrameAsks

	// wsSave's body encodes, hash-diffs, and posts the layout on a change.
	wsSave *debounce.Debounce

	urlUpdate *debounce.Debounce

	// framingSave's body flushes settled framing through the no-op-guarded
	// writers.
	framingSave *debounce.Debounce

	textSave *debounce.Debounce

	// errExpire lets one-shot notices leave the strip without polling.
	errExpire *debounce.Debounce

	// traceFlush posts the records owed. Every emit arms it; nothing owed
	// arms nothing.
	traceFlush *debounce.Debounce
}

// newScheduler binds every settle timer to what it runs, before the App can
// draw. draw() ends by arming two of these, so a timer bound any later would
// take that arm with nothing to fire and never accept another. Every mode
// comes from client/cadence, where the wait's own sentence asks for it.
func newScheduler(a *App) scheduler {
	return scheduler{
		wsSave:      debounce.New(setTimeoutMs, nowMs, cadence.WorkspaceSaveMode, a.flushWorkspaceSave),
		urlUpdate:   debounce.New(setTimeoutMs, nowMs, cadence.URLUpdateMode, a.writeURLNow),
		framingSave: debounce.New(setTimeoutMs, nowMs, cadence.FramingSaveMode, a.flushFramingSave),
		textSave:    debounce.New(setTimeoutMs, nowMs, cadence.TextSaveMode, a.flushDirtyText),
		traceFlush:  debounce.New(setTimeoutMs, nowMs, cadence.TraceFlushMode, a.flushTrace),
		// The expiry's wait is a notice's own deadline rather than a cadence,
		// and it is a throttle: a settle would push the window out every time
		// a notice arrived, so the one already due would expire late.
		errExpire: debounce.New(setTimeoutMs, nowMs, debounce.Throttle, func() {
			if a.errs.Expire(time.Now()) {
				a.scheduleFrame(traceevent.WhyNotice) // strip shrank; panes reclaim the height on redraw
			}
			a.scheduleErrExpiry()
		}),
	}
}

// wellWheelDrift is one well's not-yet-persisted hover-wheel view. The center
// is float all the way to the store, so nothing rounds the drift away, and the
// flush never re-reads the cache row, which a refetch would have replaced.
type wellWheelDrift struct {
	gridID  string
	cx, cy  float64
	ratio   float64
	version int64
}

// paneLocal is the single owner of one pane's session-local state. App.local
// creates it and App.forgetPane removes it, so none of it outlives its pane.
type paneLocal struct {
	pane.SessionState
	urlView   *urlView
	shellConn *shellStreamConn
}

func (a *App) shellConnFor(paneID string) *shellStreamConn {
	if pl, ok := a.localIf(paneID); ok {
		return pl.shellConn
	}
	return nil
}

// urlViewFor is the liveness check the input and render paths use.
func (a *App) urlViewFor(paneID string) *urlView {
	if pl, ok := a.localIf(paneID); ok {
		return pl.urlView
	}
	return nil
}

// local materializes an entry; a read that must not uses localIf.
func (a *App) local(paneID string) *paneLocal {
	pl := a.locals[paneID]
	if pl == nil {
		pl = &paneLocal{SessionState: pane.NewSessionState()}
		a.locals[paneID] = pl
	}
	return pl
}

func (a *App) localIf(paneID string) (*paneLocal, bool) {
	pl, ok := a.locals[paneID]
	return pl, ok
}

// forgetPane is the single atomic cleanup point on pane drop, so no per-pane
// state outlives its pane.
func (a *App) forgetPane(paneID string) {
	a.closeURLStream(paneID, true)
	a.closeShellStream(paneID, true)
	delete(a.locals, paneID)
	// Level-scoped pane ids recur across descents, so every pane-keyed map
	// clears here, or a stale entry greets the next pane of the same id.
	delete(a.traces, paneID)
	// A pane going away is the one case a transition is dropped rather than
	// landed: there is no pane left to install a place on. Cancel lands.
	a.trans.Drop(paneID)
	// The drop means no landing will ever retire its continuations.
	a.nav.Forget(paneID)
}

// selectedFor never materializes state.
func (a *App) selectedFor(paneID string) string {
	if pl, ok := a.localIf(paneID); ok {
		return pl.Selected
	}
	return ""
}

func (a *App) clearSelected(paneID string) {
	if pl, ok := a.localIf(paneID); ok {
		pl.Selected = ""
	}
}

// The transition's shape and per-pane bookkeeping live in client/transition.
// Descent is two segments, a parent zoom-in then a zero-duration install on the
// calibrated child state; ascent is the mirror. zoomtrans calibrates both.

// traceState is one armed ascent-trace highlight, held per pane in App.traces.
type traceState struct {
	tileID  string
	startMs float64
}

// ghost is a transient floating render of a tile within one pane.
// displayedCellSize lerps toward targetCellSize each frame, so the ghost
// resizes when the cursor crosses a pane or enters a well.
type ghost struct {
	tile              *gridwellv1.Tile
	paneID            string
	screenX           float64
	screenY           float64
	displayedCellSize float64
	targetCellSize    float64

	// hiddenTileID and hiddenPaneID suppress the source tile's render while this
	// ghost stands in for it, and live here because the hide outlasts a.dragging.
	hiddenTileID string
	hiddenPaneID string

	// fragmentation lerps like cell size, so dragging back out reassembles.
	displayedFragmentation float64
	targetFragmentation    float64

	// forbidden is set over a drop target the server would reject, and mouseup
	// then snaps back with no RPC.
	forbidden bool

	// link is set while a left-drag hovers a different id namespace: the drop
	// creates a link and the source stays, so the ghost draws dashed.
	link bool
}

// dragState tracks an in-progress drag. started is false until the cursor
// leaves dragThreshold, so a bare click is a select rather than a move.
type dragState struct {
	// menuNS is the node whose menu offered a template: primitives create there.
	menuNS       string
	originPaneID string
	// originFocused makes a bare click on an unfocused pane focus-only: without
	// it a click meant to focus descends whenever it happens to hit a tile.
	originFocused bool
	// splitNav records ctrl at left-press: a bare click then asks for its
	// descent in a new split pane. Fixed at press; a started drag ignores it.
	splitNav     bool
	tileID       string
	cellOffsetX  float64
	cellOffsetY  float64
	startScreenX float64
	startScreenY float64
	curScreenX   float64
	curScreenY   float64
	started      bool
	// intent is fixed by the press, and the one owner of that fact. A creating
	// drag commits only through the right-button release.
	intent dragdrop.Intent
	// snapshotTile is never nil: a press that grabbed no tile carries an empty
	// row, so the drop rules read a zero footprint instead of dereferencing nil.
	snapshotTile  *gridwellv1.Tile
	originScreenX float64
	originScreenY float64

	// A palette drag has no tile yet: item carries the grabbed entry, a
	// primitive the drop creates or a plugin it mounts as an exit-well link.
	isTemplate bool
	item       paletteItem

	// The source grid, set at mousedown, carried separately so the commit names
	// the right one when source and dest differ inside a single pane.
	srcGridID   string
	srcCellSize float64
}

// dragThreshold is the single owner of the drag threshold. The native layer
// keeps two forced copies, RIGHT_DRAG_THRESHOLD in
// apps/desktop/src/main/viewutil.ts and an inlined one in
// src/preload/urlview-preload.ts; gesture-threshold.test.ts pins both.
const dragThreshold = 4.0

func main() {
	origin := js.Global().Get("location").Get("origin").String()
	// Before the App, because the rpc client is built with the interceptor
	// that records into it directly.
	tr := trace.New(trace.DefaultCapacity, trace.NewRequestID())
	app = &App{
		doc:                js.Global().Get("document"),
		win:                js.Global().Get("window"),
		origin:             origin,
		cl:                 rpc.NewDefaultClient(origin, connect.WithInterceptors(trace.Interceptor(tr, time.Now))),
		c:                  cache.New(),
		locals:             map[string]*paneLocal{},
		menu:               menu.New(),
		errs:               errsurface.New(),
		tr:                 tr,
		pump:               trace.NewPump(time.Now()),
		caps:               caps.Derive(bridgeCaps(), false),
		fetch:              newFetchState(),
		shellAlive:         map[string]bool{},
		shellAliveProbing:  map[string][]func(bool){},
		traces:             map[string]traceState{},
		renderedPanePaints: map[string]int{},
		backstop:           retry.NewInterval(retry.Backstop),
	}
	// Before anything can draw: the settle timers close over the App.
	app.persist = newPersistState(app)
	// The flush timer exists now, so the ring can say when it is owed
	// something; the interceptor's records reach it no other way.
	tr.OnEmit = app.armTraceFlush
	app.emit(traceevent.Boot(tracewire.BuildCommit(), runtime.Version(),
		jsString(js.Global().Get("navigator").Get("userAgent"))))
	app.views = newViewCaches(app.previewDecodeFailed, app.renderedRasterFailed, app.paneLayoutUnreadable)
	app.trans = transition.New(app.enterSegment, app.landTransition)
	app.nav = nav.New()
	app.canvas = app.doc.Call("getElementById", "canvas")
	app.cctx = app.canvas.Call("getContext", "2d")
	app.tree = pane.NewTree()
	app.tree.FocusedPane().Zoom = 1.0
	// After the canvas and the tree, because applying a palette redraws.
	app.applyTheme(app.storedTheme())
	app.resize()

	app.win.Call("addEventListener", "resize", js.FuncOf(func(this js.Value, args []js.Value) any {
		app.resize()
		app.draw()
		return nil
	}))

	// Mobile browsers resize the visual viewport without a layout resize,
	// leaving the textarea under the keyboard. resize() is idempotent.
	if vv := app.win.Get("visualViewport"); vv.Truthy() {
		vvCb := js.FuncOf(func(this js.Value, args []js.Value) any {
			app.resize()
			app.draw()
			return nil
		})
		vv.Call("addEventListener", "resize", vvCb)
	}

	// Close every URL stream cleanly, so the server's save-and-destroy fires
	// before the connection dies and the final preview write is not missed.
	app.win.Call("addEventListener", "beforeunload", js.FuncOf(func(this js.Value, args []js.Value) any {
		// Everything durable rides beacons (unload.go), so it survives the
		// dying page. An animating transition lands on its destination first.
		app.flushOnUnload()
		app.closeAllURLStreams()
		app.closeAllShellStreams()
		return nil
	}))

	// The restore marks the URL its own, so a pending debounced write finds the
	// writer suppressed instead of clobbering the entry just navigated to.
	app.win.Call("addEventListener", "popstate", js.FuncOf(func(this js.Value, args []js.Value) any {
		app.runGesture(nav.Gesture{Kind: nav.GestureRestoreFromHistory, Raw: locationPath()})
		return nil
	}))

	// PTY bytes ride the /shell WebSocket on this page's own origin, on the
	// cookie that served it. The registry owns replace-on-open and exit-once.
	app.shells = shellstream.New(
		shellws.Dialer(shellws.Options{Origin: origin}),
		func(paneID string, data []byte) { app.onShellData(paneID, data) },
		func(e shellstream.Exit) { app.onShellExit(e.PaneID, e.Message, e.SessionGone) },
	)

	app.installCanvasInput()
	app.installWebviewListeners()
	app.installShellMirror()
	app.installTestHook() // read-only window.__gridwellTest, only under ?e2e=1

	go app.bootstrap()

	select {}
}

// bootstrap loads the plugin list, then starts the rest of the client. The
// landing page is home, so panes anchor there and plugins ride the + menu.
func (a *App) bootstrap() {
	// The handshake retries until it lands: firing it once would leave one blip
	// at boot as a permanently empty shell until a manual reload.
	backoff := retry.Backoff{First: retry.HandshakeFirst, Max: retry.HandshakeMax}
	var plugins *gridwellv1.HandshakeResponse
	for {
		// Bounded, so the backoff loop is what it says: an unbounded handshake
		// the network swallows never returns, and no next attempt is made.
		err := func() error {
			ctx, cancel := inflight.Bounded()
			defer cancel()
			var err error
			plugins, err = a.cl.Handshake(ctx)
			return err
		}()
		if err == nil {
			a.resolveErr("rpc:Handshake")
			break
		}
		// Say why, or an empty landing page reads as "my plugins vanished".
		a.reportErr(errsurface.Error, "rpc:Handshake", "plugin list failed — retrying: "+rpcErrText(err))
		a.draw()
		time.Sleep(backoff.Next())
	}
	a.plugins = plugins.Plugins
	// The node's shells_disabled folds into the capability set at boot and is
	// immutable after, so caps stays the one owner of what this client can do.
	a.caps = caps.Derive(bridgeCaps(), plugins.ShellsDisabled)
	// Boot-time, immutable, read only by webAddress.
	a.contentToken = plugins.ContentToken
	a.home = rpc.HomeGrid(plugins)
	a.afterBootstrap()
}

func (a *App) afterBootstrap() {
	a.canvas.Call("focus")
	p := a.tree.FocusedPane()
	// Land at home; applyURLOnBoot may restore a place over it.
	p.Reset(pane.Frame{GridID: a.home, Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom})
	if a.home != "" {
		a.fetchGrid(a.home)
	}

	go a.startSSE()
	// The slow retry net behind the reconnect kick.
	go a.retryBackstop()
	// On a fresh page load of `/` this only fetches the home grid.
	go a.applyURLOnBoot()
}

func (a *App) resize() {
	dpr := a.win.Get("devicePixelRatio").Float()
	if dpr <= 0 {
		dpr = 1
	}
	w := a.win.Get("innerWidth").Float()
	h := a.win.Get("innerHeight").Float()
	a.width = w
	a.height = h
	a.canvas.Set("width", int(w*dpr))
	a.canvas.Set("height", int(h*dpr))
	a.canvas.Get("style").Set("width", strconv.Itoa(int(w))+"px")
	a.canvas.Get("style").Set("height", strconv.Itoa(int(h))+"px")
	a.cctx.Call("setTransform", dpr, 0, 0, dpr, 0, 0)
}

// loadGrid is the one GetGrid-to-cache hop: the renderer's fetchGrid and the
// restore walk's awaited read both come here.
func (a *App) loadGrid(ctx context.Context, id string) error {
	resp, err := a.cl.GetGrid(ctx, id)
	// clientsync.ReactGridRead is the one table; this runs its arms.
	r := clientsync.ReactGridRead(id, resp.GetGrid().GetId(), clientsync.Of(err))
	a.fetch.grids.Settle(id, r.Latch)
	switch {
	case err != nil:
		a.reportErr(errsurface.Error, "grid:"+id, "grid unavailable: "+rpcErrText(err))
	case r.Renamed:
		a.reportErr(errsurface.Error, "grid:"+id,
			"asked for grid "+id+", was answered "+resp.Grid.Id+" — the view of "+id+" cannot load")
	default:
		a.resolveErr("grid:" + id)
	}
	if r.Store {
		a.c.PutGrid(resp.Grid, resp.Tiles)
	}
	return err
}

// fetchGrid loads a grid in the background, deduped per id: the renderer fires
// it on every cache miss every frame, which would otherwise dogpile the server.
func (a *App) fetchGrid(id string) {
	if id == "" {
		return
	}
	// A grid in a namespace this node does not declare is never asked for: the
	// latch stands in for the answer, and no verdict reaches the strip.
	if a.deadNamespace(id) {
		a.fetch.grids.Settle(id, inflight.Refused)
		return
	}
	ctx, done, ok := a.fetch.grids.Ask(id)
	if !ok {
		return
	}
	go func() {
		err := a.loadGrid(ctx, id)
		// An ask refused while this one was on the wire describes a grid this
		// answer was taken too early to hold, so it is re-asked rather than
		// lost; see inflight.Reads.Ask.
		owed := done()
		if err != nil {
			a.draw()
		} else {
			// Coalesced repaint: completions land in bursts, and one draw per
			// child-grid read would be hundreds of repaints for a big directory.
			a.scheduleFrame(traceevent.WhyGridLoaded)
		}
		if owed {
			a.fetchGrid(id)
		}
	}()
}

// fetchTileByID resolves a routable tile id whose grid is not cached: GetTile
// locates it, then fetchGrid pulls its grid in so findTileByID hits.
func (a *App) fetchTileByID(tileID string) {
	if tileID == "" {
		return
	}
	// Same rule as fetchGrid: an undeclared namespace is not asked. A leaf link
	// into a removed plugin stays its own dead face.
	if a.deadNamespace(tileID) {
		a.fetch.tiles.Settle(tileID, inflight.Refused)
		return
	}
	ctx, done, ok := a.fetch.tiles.Ask(tileID)
	if !ok {
		return
	}
	go func() {
		defer done()
		tile, err := a.cl.GetTile(ctx, tileID)
		o := clientsync.Of(err)
		if err == nil && tile == nil {
			o = clientsync.OutcomeRejected // an empty answer is the server's no
		}
		// clientsync.ReactRead owns the latch; an outage is not named once per
		// id, because the same read's grid says it once under "grid:".
		v := clientsync.ReactRead(o)
		a.fetch.tiles.Settle(tileID, v)
		switch v {
		case inflight.Refused:
			// The asker is a crumb or a descent, which would otherwise draw an
			// empty content box named "unnamed" and say nothing.
			detail := "the row is gone"
			if err != nil {
				detail = rpcErrText(err)
			}
			a.reportErr(errsurface.Error, "tile:"+tileID, "tile unavailable: "+detail)
		case inflight.Answered:
			a.resolveErr("tile:" + tileID)
			a.fetchGrid(tile.GridId)
		}
	}()
}

func nowMs() float64 {
	return js.Global().Get("Date").Call("now").Float()
}

// consoleLog prefixes every message with tag. The prefixes are what a log
// reader and the e2e suite grep for, so they are output, not decoration.
func consoleLog(tag string) func(format string, args ...any) {
	return func(format string, args ...any) {
		js.Global().Get("console").Call("log", tag+" "+fmt.Sprintf(format, args...))
	}
}

// taggedLog is consoleLog and a record of the same line: a diagnostic worth
// writing is worth keeping. A site with a record of its own writes through
// consoleLog instead, so one operation leaves one record.
func taggedLog(tag string) func(format string, args ...any) {
	console := consoleLog(tag)
	return func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		console("%s", msg)
		app.emit(traceevent.Log(tag, msg))
	}
}

// scheduleFrame asks for one paint under why; see traceevent.FrameAsks.
func (a *App) scheduleFrame(why string) {
	if !a.persist.sched.frameAsks.Ask(why) {
		return
	}
	framesArmed++
	js.Global().Call("requestAnimationFrame", oneShot(func() {
		asks := a.persist.sched.frameAsks.Take()
		perf := js.Global().Get("performance")
		start := perf.Call("now").Float()
		a.frame()
		a.emit(asks.Drawn(perf.Call("now").Float() - start))
	}))
}

// frame advances the animations, repaints, and re-arms while motion remains.
func (a *App) frame() {
	now := nowMs()
	if a.animation != nil {
		x, y, done := a.animation.At(now)
		if a.ghost != nil {
			a.ghost.screenX = x
			a.ghost.screenY = y
		}
		if done {
			a.animationDone()
		} else {
			a.scheduleFrame(traceevent.WhyAnimation)
		}
	}
	// Panes are independent, so one landing never touches another's motion.
	for _, tr := range a.trans.List() {
		seg := tr.Segment()
		t := anim.Progress(now, tr.StartMs(), seg.DurationMs)
		eased := anim.EaseOutCubic(t)
		if p := a.tree.FindPane(tr.PaneID); p != nil {
			p.Cx = anim.Lerp(seg.FromCx, seg.ToCx, eased)
			p.Cy = anim.Lerp(seg.FromCy, seg.ToCy, eased)
			p.Zoom = anim.LerpExp(seg.FromZoom, seg.ToZoom, eased)
		}
		if t >= 1 {
			a.trans.Advance(tr.PaneID, now)
		}
		if a.trans.Active(tr.PaneID) {
			a.scheduleFrame(traceevent.WhyTransition)
		}
	}
	// Ascent-trace fades need frames until they run out.
	if a.pruneTraces(now) {
		a.scheduleFrame(traceevent.WhyTraceFade)
	}
	a.draw()
}

// pruneTraces reports whether any trace is still fading.
func (a *App) pruneTraces(now float64) bool {
	alive := false
	for paneID, tr := range a.traces {
		if anim.FadeAlpha(now, tr.startMs, cadence.TraceFadeMs) <= 0 {
			delete(a.traces, paneID)
			continue
		}
		alive = true
	}
	return alive
}

// startTransition displaces, landing rather than voiding, whatever that pane
// was already animating.
func (a *App) startTransition(t *transition.Transition) {
	a.trans.Start(t, nowMs())
	a.scheduleFrame(traceevent.WhyTransition)
}

// enterSegment is the one writer of the scratch viewport an animation drives.
// client/transition calls it per segment, and once more when one is cut short.
func (a *App) enterSegment(paneID string, seg transition.Segment) {
	p := a.tree.FindPane(paneID)
	if p == nil {
		return
	}
	if seg.Place != nil {
		p.Stack = seg.Place.Clone()
	}
	p.Cx = seg.FromCx
	p.Cy = seg.FromCy
	p.Zoom = seg.FromZoom
}

// landTransition is what arriving means, animated the whole way or cut short.
// A content descent pushes its frame there, so it is not optional.
func (a *App) landTransition(tr *transition.Transition) {
	p := a.tree.FindPane(tr.PaneID)
	if p == nil {
		return
	}
	a.clearSelected(p.ID)
	a.fetch.grids.Reset()
	a.fetch.contents.Reset()
	a.fetch.previews.Reset()
	a.fetch.menus.Reset()
	a.fetchGrid(a.gridIDForPane(p))
	if tr.TraceTileID != "" {
		// Keep the frame loop alive for the fade.
		a.traces[p.ID] = traceState{tileID: tr.TraceTileID, startMs: nowMs()}
		a.scheduleFrame(traceevent.WhyTraceFade)
	}
	if tr.OnComplete != nil {
		tr.OnComplete()
	}
	a.scheduleURLUpdate()
	a.draw()
}

// animationDone drops the ghost, so the cache is the source of truth again.
func (a *App) animationDone() {
	a.animation = nil
	a.ghost = nil // the render hides live on the ghost and die with it
}

// The render hide lives on the ghost: one owner, one lifecycle.
func (a *App) ghostHiddenTile() string {
	if a.ghost == nil {
		return ""
	}
	return a.ghost.hiddenTileID
}

func (a *App) ghostHiddenPane() string {
	if a.ghost == nil {
		return ""
	}
	return a.ghost.hiddenPaneID
}

// startSSE keeps one event stream open for the life of the page. retry.Reconnect
// decides the waits and which stream owes a resync kick; this loop sleeps and
// kicks what it is told to.
func (a *App) startSSE() {
	var pace retry.Reconnect
	for {
		stream, err := a.cl.Subscribe(context.Background())
		if err != nil {
			// Until this reconnects, everything on screen is silently going
			// stale. It coalesces, and resolves itself on reconnect below.
			a.reportErr(errsurface.Error, "events", "live updates disconnected — retrying")
			time.Sleep(pace.SubscribeFailed())
			continue
		}
		a.resolveErr("events")
		if pace.Subscribed() {
			// The gap swallowed events without saying whose, so nothing scopes.
			a.retryKick(true, cache.EverySource)
		}
		for {
			ev, ok, err := stream.Recv()
			if err != nil {
				a.reportErr(errsurface.Error, "events", "live updates disconnected — retrying")
				break
			}
			if !ok {
				break
			}
			a.emit(traceevent.EventRecv(ev))
			if a.c.Apply(ev) {
				a.emit(traceevent.EventApplied(ev))
				a.draw()
			}
			// events.Route is the one table; this runs its arms.
			plan := events.Route(ev)
			if plan.DropPreviews != "" {
				a.views.urlPreview.Drop(plan.DropPreviews)
				a.views.renderedPrev.Drop(plan.DropPreviews)
			}
			if plan.ClearLatch != "" {
				a.fetch.grids.Change(plan.ClearLatch)
			}
			if plan.ClearContent != "" {
				a.fetch.contents.Change(plan.ClearContent)
				a.fetch.previews.Change(plan.ClearContent)
			}
			if plan.Fetch != "" {
				a.emit(traceevent.EventRefetch(plan.Fetch))
				a.fetchGrid(plan.Fetch)
			}
			if plan.Health != nil {
				a.reportPluginHealth(plan.Health)
			}
		}
		stream.Close()
		time.Sleep(pace.StreamEnded())
	}
}

// retryKick drains everything a transport gap left behind: a stream reconnect
// resyncs every source, a health transition resyncs the one it names, and the
// backstop timer resyncs nothing, because only the outbox says anything is
// owed. cache.ServedBy owns what a scope covers; the outbox drain is never
// scoped, because a parked write is the user's bytes.
func (a *App) retryKick(resync bool, source string) {
	if resync {
		served := func(id string) bool { return cache.ServedBy(id, source) }
		// Failure latches are gap state: a read that failed while the link was
		// down deserves a fresh attempt, asked by name because a pane waiting
		// on one draws nothing new to ask for it.
		// The menu set is keyed by a source name, so its predicate is
		// cache.Reaches: a connection's flap covers the nodes behind it.
		reaches := func(ns string) bool { return cache.Reaches(ns, source) }
		a.reask(a.fetch.grids.ClearIf(served), a.fetch.tiles.ClearIf(served),
			a.fetch.contents.ClearIf(served), a.fetch.previews.ClearIf(served),
			a.fetch.menus.ClearIf(reaches))
		// So is a fetch still in flight: a request that dies with its link never
		// returns, and its claim would keep every retry away forever. Re-ask for
		// the grids by name, since a pane waiting on one it never received is
		// not in the cache for the sweep below to find.
		stuck := a.fetch.grids.CancelIf(served)
		a.fetch.tiles.CancelIf(served)
		a.fetch.contents.CancelIf(served)
		a.fetch.previews.CancelIf(served)
		a.fetch.menus.CancelIf(reaches)
		for _, gid := range append(stuck, a.c.ResyncSet(source)...) {
			a.fetchGrid(gid)
		}
	}
	a.syncContentOutbox()
	a.drainOutbox()
}

// reask asks again for reads whose latches were just cleared. A preview and
// a menu are asked by the draw, the one site that knows which blob the face
// wants and whether the menu is open.
func (a *App) reask(grids, tiles, contents []string, drawn ...[]string) {
	for _, keys := range drawn {
		if len(keys) > 0 {
			a.scheduleFrame(traceevent.WhyReask)
			break
		}
	}
	for _, id := range grids {
		a.fetchGrid(id)
	}
	for _, id := range tiles {
		a.fetchTileByID(id)
	}
	for _, id := range contents {
		a.fetchTileContent(id)
	}
}

// drainOutbox re-posts everything owed, in the order it was parked. It is the
// one drain: the unload path takes it too, so a quit and a reconnect cannot
// treat what is owed differently.
func (a *App) drainOutbox() {
	owed := a.persist.out.Drain()
	if len(owed) == 0 {
		return
	}
	a.emit(traceevent.OutboxDrain(len(owed)))
	for _, resend := range owed {
		resend()
	}
}

// retryBackstop re-posts what the outbox holds without waiting for a reconnect
// that may never come: the stream survives blips a unary write does not.
func (a *App) retryBackstop() {
	for {
		a.backstop.Wait()
		// An unreachable source is asked again once per tick, never per frame.
		a.reask(a.fetch.grids.Backstop(), a.fetch.tiles.Backstop(),
			a.fetch.contents.Backstop(), a.fetch.previews.Backstop(), a.fetch.menus.Backstop())
		a.syncContentOutbox()
		if a.persist.out.Len() > 0 {
			a.retryKick(false, cache.EverySource)
		}
	}
}

// Session-local state: the whole UI rebuilds from the URL, which captures only
// the focused pane's place. Its outer frames are session-only, so a restored
// pane ascends onto persisted framing instead.

// gridIDForPane walks the pane's anchor down its doorway path, both
// projections of the frame stack. "" when the pane is boot-blank.
func (a *App) gridIDForPane(p *pane.Pane) string {
	return a.gridIDForPathFrom(p.Anchor(), p.Path())
}

// gridIDForPathFrom walks path, of well row ids, from anchor to the leaf grid
// id. It returns anchor for an empty or stale path, and "" when anchor is "".
func (a *App) gridIDForPathFrom(anchor string, p []string) string {
	// The walk is the pure pane.ResolveLeafGrid; the closure does the cache
	// read and kicks a background fetch on a miss.
	return pane.ResolveLeafGrid(anchor, p,
		func(gid, wellID string) (string, bool, bool) {
			g, ok := a.c.Grid(gid)
			if !ok {
				a.fetchGrid(gid)
				return "", false, false
			}
			w, ok := g.Tiles[wellID]
			if !ok {
				return "", true, false
			}
			return w.ChildGridId, true, true
		})
}

// refetchGridOnConflict posts an Info notice as well as refetching: the user's
// optimistic change is about to be replaced, and that must be visible.
func (a *App) refetchGridOnConflict(gridID string, where string) {
	a.reportErr(errsurface.Info, "conflict:"+where, where+": changed elsewhere — reloaded")
	a.fetchGrid(gridID)
}

// reportErr is the one wasm entry into the error surface. It also logs to the
// console, which window.ts forwards, so a notice stays greppable afterward.
func (a *App) reportErr(sev errsurface.Severity, source, message string) {
	method := "error"
	if sev == errsurface.Info {
		method = "warn"
	}
	js.Global().Get("console").Call(method, "gridwell: ["+source+"] "+message)
	a.emit(traceevent.Notice(sev, source, message))
	a.errs.Report(sev, source, message, time.Now())
	a.scheduleErrExpiry()
	a.scheduleFrame(traceevent.WhyNotice)
}

// scheduleErrExpiry arms one setTimeout for the soonest deadline; the callback
// prunes and re-arms, so a pushed-out deadline fires early, never late.
func (a *App) scheduleErrExpiry() {
	if a.persist.sched.errExpire.Pending() {
		return
	}
	d, ok := a.errs.NextDeadline(time.Now())
	if !ok {
		return
	}
	// +1 so the timer lands just past the deadline, not a hair before it.
	ms := int(d/time.Millisecond) + 1
	if ms < 1 {
		ms = 1
	}
	a.persist.sched.errExpire.Arm(ms)
}

// resolveErr clears a source's notice when its condition heals. Every read
// and write that succeeds calls it, and almost none of them had a notice up,
// so the repaint rides the surface's verdict: nothing was on screen to take
// off it.
func (a *App) resolveErr(source string) {
	if a.errs.Resolve(source) {
		a.scheduleFrame(traceevent.WhyNotice)
	}
}

// reportPluginHealth runs events.ReactHealth's plan for a transition: a
// plugin's stream being down means its tiles stopped updating with no other
// signal, and its recovery is a healed gap.
func (a *App) reportPluginHealth(h *gridwellv1.EventPluginHealth) {
	r := events.ReactHealth(h)
	// The client's one copy of which sources are not answering; every room
	// one serves is a memory, which is what the bar draws.
	a.c.NoteHealth(h.PluginUuid, h.Healthy)
	if r.Resolve {
		a.resolveErr(r.Source)
	}
	if r.Report {
		label := h.PluginUuid
		if pl, ok := a.pluginByUUID(h.PluginUuid); ok && pl.Label != "" {
			label = pl.Label
		}
		a.reportErr(errsurface.Error, r.Source, label+": live updates stopped — "+h.Detail)
	}
	a.retryKick(true, r.Resync)
}
