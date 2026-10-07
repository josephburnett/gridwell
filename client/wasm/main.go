//go:build js && wasm

// Package main is the WASM entry point for the Gridwell client: glue over
// syscall/js. Every decision belongs in a pure, tested client/* package.
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
	"github.com/josephburnett/gridwell/client/interest"
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
	"github.com/josephburnett/gridwell/client/theme"
	"github.com/josephburnett/gridwell/client/touchgest"
	"github.com/josephburnett/gridwell/client/trace"
	"github.com/josephburnett/gridwell/client/traceevent"
	"github.com/josephburnett/gridwell/client/transition"
	"github.com/josephburnett/gridwell/client/urlview"
)

const (
	cellPx = pane.CellPx
	// zoomMin is the grid zoom floor for every gesture and client: cells at 2px,
	// below the line where drawGridLinesIn stops painting.
	zoomMin    = 0.03125
	zoomMax    = 8.0
	zoomFactor = 1.1

	// wellZoomRatio* clamp the hover-wheel well zoom in intrinsic-ratio units;
	// the min keeps the preview above the renderer's 0.5px floor.
	wellZoomRatioMin = 1.0 / 64.0
	wellZoomRatioMax = 1.0

	// textFixedScale renders text at one scale, so grid zoom reveals more lines
	// instead of magnifying the type.
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

	// locals is the per-pane session-local state; a.forgetPane removes an entry.
	locals map[string]*paneLocal

	// menu is the single owner of the + menu's open state, pane and hover; never
	// assign its fields directly. It survives a descent on Frame.MenuOpen.
	menu menu.State

	// errs is the single owner of user-visible failure notices; every failure
	// reports through a.reportErr or a.resolveErr.
	errs *errsurface.Surface

	// tr is this client's trace ring and pump the one post in flight.
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
	// capability; a served page's address is derived at use, never persisted.
	origin       string
	contentToken string

	// unloading switches framing writes to sendBeacon; see unload.go.
	unloading bool

	// touchDownTarget is where synthetic MouseDowns route; owned by touch.go.
	touch           *touchgest.Machine
	touchTimerCb    js.Func
	touchDownTarget js.Value

	fetch fetchState

	// ghost renders a dragged tile at sub-cell screen precision.
	ghost *ghost

	// trans holds at most one zoom animation per pane. One displaced or cleared
	// lands on its destination, so a descent is never voided after animating.
	trans *transition.Set

	// nav is every descent and ascent decision, as data. nav.go gathers the
	// world it plans against; nav_exec.go runs the effects it returns.
	nav *nav.Machine

	rightDrag *rightDragState

	// leftResize clamps to the pane minimum; the release decides a close.
	leftResize *leftResizeState

	// shellAlive caches the ShellSessionAlive probe by session (missing: unknown);
	// shellAliveProbing single-flights it.
	shellAlive        map[string]bool
	shellAliveProbing map[string][]func(alive bool)

	shells *shellstream.Registry

	// mirrors is what each mirror was last told; see syncMirrors.
	mirrors mirrorState

	// interest is what the node was told this client shows; see syncInterest.
	interest     interest.Tracker
	interestKick chan struct{}

	// shellMirrorPasses counts shell mirror snapshots, for e2e.
	shellMirrorPasses int

	// traces holds the per-pane ascent-trace highlight, ephemeral like selection.
	traces map[string]traceState

	overlays overlayState

	// zoomKeyRelays counts relayed zoom chords, for e2e: it brackets the IPC hop.
	zoomKeyRelays int

	urlGens urlview.Gens

	// writes counts dispatched mutations that have not settled.
	writes inflight.Writes

	// renderedPanePaints is e2e attribution: an unfocused pane paints raster.
	renderedPanePaints map[string]int
	textFaces          map[string]textFace
	// bannerTexts is e2e attribution: the last banner line each tile drew.
	bannerTexts map[string]string

	// backstop is the outbox re-post cadence, retry.Backstop until the e2e
	// lowers it to bound one.
	backstop *retry.Interval
}

// overlayState holds the DOM singletons layered over the canvas, created
// lazily and reused for the life of the page.
type overlayState struct {
	// textTextarea shows only over a focused pane in TextMode "text".
	textTextarea         js.Value
	textTextareaInputCb  js.Func
	textTextareaScrollCb js.Func

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

	wsExpand *wsExpandState

	// textToggleBtn is a DOM element, not a canvas button, so it can sit above
	// the textarea and let the text fill the pane edge to edge.
	textToggleBtn js.Value
	textToggleCb  js.Func

	urlModalOpen bool

	// lastTextareaTileID is what the textarea is bound to: the same tile on a
	// refresh preserves typing, a different one is re-seeded.
	lastTextareaTileID string

	// textareaReady says the textarea holds the focused tile's content, so the
	// canvas paints through the loading race.
	textareaReady bool
}

// viewCaches holds derived views, never facts: every one is recomputable, so
// the group may be dropped wholesale without losing anything the user made.
type viewCaches struct {
	urlPreview *preview.Cache

	wrapCache map[string][]string

	// renderedPrev caches rasterized rendered-mode previews by version and width.
	renderedPrev *rasterprev.Cache

	// paneLayouts memoizes the decode, invalidated by blob generation.
	paneLayouts *panepreview.Layouts

	// menuCtxs is keyed by the grid-stamped node_ns; "" is a.plugins, a.caps.
	menuCtxs map[string]*menuContext
}

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
	// grids, tiles, contents, previews and menus are the reads every draw fires
	// on a miss; inflight.Reads owns why each failure latches and what clears it.
	grids    *inflight.Reads
	tiles    *inflight.Reads
	contents *inflight.Reads
	previews *inflight.Reads
	menus    *inflight.Reads
}

func newFetchState() fetchState {
	return fetchState{
		grids:    inflight.NewReads(),
		tiles:    inflight.NewReads(),
		contents: inflight.NewReads(),
		previews: inflight.NewReads(),
		menus:    inflight.NewReads(),
	}
}

// oneShot hands the host a callback it calls once. An unreleased js.Func
// stays in syscall/js's funcs map for the life of the page.
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

// oneShotsArmed, oneShotsLive and framesArmed are the e2e evidence that no
// js.Func outlives its call; framesArmed separates frames, whose count is
// the host's to decide.
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

// persistState executes the navigation machine's Flush* effects.
type persistState struct {
	sched scheduler

	// contentSaves chains content writes per key instead of racing them: see
	// outbox.SaveQueue.
	contentSaves *outbox.SaveQueue

	// wellWheelPending batches a scroll burst into one SetFraming per tile.
	wellWheelPending map[string]wellWheelDrift

	// persistPosts and framingFlushes are e2e-only.
	persistPosts   map[string]int
	framingFlushes int

	// out records unacknowledged writes in order; retryKick and unload drain it.
	out *outbox.Outbox
}

// newPersistState takes the App because a debounce holds its body from
// construction; see client/debounce.
func newPersistState(a *App) persistState {
	return persistState{
		sched:            newScheduler(a),
		contentSaves:     outbox.NewSaveQueue(),
		wellWheelPending: map[string]wellWheelDrift{},
		persistPosts:     map[string]int{},
		out:              outbox.New(),
	}
}

type scheduler struct {
	frameAsks traceevent.FrameAsks

	wsSave *debounce.Debounce

	urlUpdate *debounce.Debounce

	framingSave *debounce.Debounce

	textSave *debounce.Debounce

	// urlAddress posts a live page's landed address once it rests, through the
	// content sweep.
	urlAddress *debounce.Debounce

	errExpire *debounce.Debounce

	// traceFlush posts the records owed; nothing owed arms nothing.
	traceFlush *debounce.Debounce

	healthResync *events.Resyncs
}

// newScheduler binds every settle timer before the App can draw: draw() arms
// two of them, and a timer bound later would never fire. Modes come from
// client/cadence.
func newScheduler(a *App) scheduler {
	return scheduler{
		wsSave:       debounce.New(setTimeoutMs, nowMs, cadence.WorkspaceSaveMode, func() { a.flushWorkspaceSave(nil) }),
		urlUpdate:    debounce.New(setTimeoutMs, nowMs, cadence.URLUpdateMode, a.writeURLNow),
		framingSave:  debounce.New(setTimeoutMs, nowMs, cadence.FramingSaveMode, a.flushFramingSave),
		textSave:     debounce.New(setTimeoutMs, nowMs, cadence.TextSaveMode, a.flushDirtyText),
		urlAddress:   debounce.New(setTimeoutMs, nowMs, cadence.URLAddressMode, a.flushDirtyText),
		traceFlush:   debounce.New(setTimeoutMs, nowMs, cadence.TraceFlushMode, a.flushTrace),
		healthResync: events.NewResyncs(setTimeoutMs, nowMs, func(source string) { a.retryKick(true, source) }),
		// A notice's own deadline, and a throttle: a settle would push the window
		// out on every new notice.
		errExpire: debounce.New(setTimeoutMs, nowMs, debounce.Throttle, func() {
			if a.errs.Expire(time.Now()) {
				a.scheduleFrame(traceevent.WhyNotice) // strip shrank; panes reclaim the height on redraw
			}
			a.scheduleErrExpiry()
		}),
	}
}

// wellWheelDrift is one well's not-yet-persisted hover-wheel view. The flush
// never re-reads the cache row, which a refetch would have replaced.
type wellWheelDrift struct {
	gridID  string
	cx, cy  float64
	ratio   float64
	version int64
}

// paneLocal is the single owner of one pane's session-local state.
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

// forgetPane is the single cleanup point on pane drop.
func (a *App) forgetPane(paneID string) {
	a.closeURLStream(paneID, true)
	a.closeShellStream(paneID, true)
	delete(a.locals, paneID)
	// Level-scoped pane ids recur across descents, so every pane-keyed map
	// clears here.
	delete(a.traces, paneID)
	// A dropped pane is the one case a transition is cancelled, not landed: no
	// pane is left to install a place on.
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

// The transition's shape lives in client/transition; zoomtrans calibrates it.

// traceState is one armed ascent-trace highlight, held per pane in App.traces.
type traceState struct {
	tileID  string
	startMs float64
}

// ghost is a transient floating render of a tile within one pane; its Flight
// is its screen position and its landing. displayedCellSize lerps toward
// targetCellSize each frame.
type ghost struct {
	anim.Flight
	tile              *gridwellv1.Tile
	paneID            string
	displayedCellSize float64
	targetCellSize    float64

	// hiddenTileID and hiddenPaneID hide the source tile while the ghost stands in;
	// the hide outlasts a.dragging.
	hiddenTileID string
	hiddenPaneID string

	displayedFragmentation float64
	targetFragmentation    float64

	// forbidden is set over a drop target the server would reject, and mouseup
	// then snaps back with no RPC.
	forbidden bool

	// link is set while a left-drag hovers a different id namespace: the drop
	// links and the source stays.
	link bool
}

// dragState tracks an in-progress drag; started is false until the cursor
// leaves dragThreshold.
type dragState struct {
	// menuNS is the node whose menu offered a template: primitives create there.
	menuNS       string
	originPaneID string
	// splitNav records ctrl at left-press: a bare click descends in a new split.
	splitNav     bool
	tileID       string
	cellOffsetX  float64
	cellOffsetY  float64
	startScreenX float64
	startScreenY float64
	curScreenX   float64
	curScreenY   float64
	started      bool
	// intent is fixed by the press. A creating drag commits only through the
	// right-button release.
	intent dragdrop.Intent
	// snapshotTile is never nil: a press that grabbed no tile carries an empty row.
	snapshotTile  *gridwellv1.Tile
	originScreenX float64
	originScreenY float64

	// item is a palette drag's grabbed entry: a primitive to create or a plugin
	// to mount as an exit-well link.
	isTemplate bool
	item       paletteItem

	// The source grid, set at mousedown, for when source and dest differ in a pane.
	srcGridID   string
	srcCellSize float64

	// The origin pane's viewport at the press, which a cancelled pan restores.
	pressView rpc.View
}

// dragThreshold is the single owner of the drag threshold. The native layer
// keeps two forced copies, RIGHT_DRAG_THRESHOLD in
// apps/desktop/src/main/viewutil.ts and an inlined one in
// src/preload/urlview-preload.ts; gesture-threshold.test.ts pins both.
const dragThreshold = 4.0

func main() {
	origin := js.Global().Get("location").Get("origin").String()
	// Before the App: the rpc client's interceptor records into it.
	tr := trace.New(trace.DefaultCapacity, trace.NewRequestID())
	app = &App{
		doc:                js.Global().Get("document"),
		win:                js.Global().Get("window"),
		origin:             origin,
		cl:                 rpc.NewDefaultClient(origin, connect.WithInterceptors(trace.Interceptor(tr, time.Now))),
		c:                  cache.New(),
		interestKick:       make(chan struct{}, 1),
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
		textFaces:          map[string]textFace{},
		bannerTexts:        map[string]string{},
		backstop:           retry.NewInterval(retry.Backstop),
	}
	// Before anything can draw: the settle timers close over the App.
	app.persist = newPersistState(app)
	// The flush timer exists now; the interceptor's records reach the ring no
	// other way.
	tr.OnEmit = app.armTraceFlush
	app.emit(traceevent.Boot(tracewire.BuildCommit(), runtime.Version(),
		jsString(js.Global().Get("navigator").Get("userAgent"))))
	app.views = newViewCaches(app.previewDecodeFailed, app.renderedRasterFailed, app.paneLayoutUnreadable)
	app.trans = transition.New(app.enterSegment, app.landTransition)
	app.nav = nav.New()
	app.canvas = app.doc.Call("getElementById", "canvas")
	app.cctx = app.canvas.Call("getContext", "2d")
	app.tree = pane.NewTree()
	// After the canvas and the tree, because applying a palette redraws.
	app.applyTheme(app.storedTheme())
	app.resize()

	app.win.Call("addEventListener", "resize", js.FuncOf(func(this js.Value, args []js.Value) any {
		app.resize()
		app.draw()
		return nil
	}))

	// Mobile browsers resize the visual viewport without a layout resize.
	if vv := app.win.Get("visualViewport"); vv.Truthy() {
		vvCb := js.FuncOf(func(this js.Value, args []js.Value) any {
			app.resize()
			app.draw()
			return nil
		})
		vv.Call("addEventListener", "resize", vvCb)
	}

	// Close every URL stream so the server's final preview write is not missed.
	app.win.Call("addEventListener", "beforeunload", js.FuncOf(func(this js.Value, args []js.Value) any {
		// Everything durable rides beacons (unload.go), so it survives the
		// dying page. An animating transition lands on its destination first.
		app.flushOnUnload()
		app.closeAllURLStreams()
		app.closeAllShellStreams()
		return nil
	}))

	// The restore marks the URL its own, so a pending debounced write does not
	// clobber it.
	app.win.Call("addEventListener", "popstate", js.FuncOf(func(this js.Value, args []js.Value) any {
		app.runGesture(nav.Gesture{Kind: nav.GestureRestoreFromHistory, Raw: locationPath()})
		return nil
	}))

	// PTY bytes ride the /shell WebSocket on this page's origin and cookie.
	app.shells = shellstream.New(
		shellws.Dialer(shellws.Options{Origin: origin}),
		func(key string, data []byte) { app.onShellData(key, data) },
		func(e shellstream.Exit) { app.onShellExit(e.Key, e.Message, e.SessionGone) },
	)

	app.installCanvasInput()
	app.installWebviewListeners()
	app.installTestHook() // read-only window.__gridwellTest, only under ?e2e=1

	go app.bootstrap()

	select {}
}

// bootstrap loads the plugin list, then starts the rest of the client.
func (a *App) bootstrap() {
	// The handshake retries until it lands, or one blip empties the page.
	backoff := retry.Backoff{First: retry.HandshakeFirst, Max: retry.HandshakeMax}
	var plugins *gridwellv1.HandshakeResponse
	for {
		// Bounded: an unbounded handshake the network swallows never returns.
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
	a.emit(traceevent.NodeBuild(plugins.Build))
	a.plugins = plugins.Plugins
	// shells_disabled folds into caps at boot, the one owner of what this client
	// can do.
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
	p.Reset(pane.Frame{GridID: a.home, View: p.View})
	if a.home != "" {
		a.fetchGrid(a.home)
	}

	go a.sendInterest()
	go a.startSSE()
	// The slow retry net behind the reconnect kick.
	go a.retryBackstop()
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
	o := clientsync.Of(err)
	r := clientsync.ReactGridRead(id, resp.GetGrid().GetId(), o)
	a.fetch.grids.Settle(id, r.Latch)
	switch {
	case r.Surface:
		a.reportErr(errsurface.Error, "grid:"+id, "grid unavailable: "+rpcErrText(err))
	case r.Renamed:
		a.reportErr(errsurface.Error, "grid:"+id,
			"asked for grid "+id+", was answered "+resp.Grid.Id+" — the view of "+id+" cannot load")
	case r.Resolve:
		a.resolveErr("grid:" + id)
	}
	if r.Store {
		a.c.PutGrid(resp.Grid, resp.Tiles)
	}
	return err
}

// fetchGrid loads a grid in the background, deduped per id, since the
// renderer fires it every frame on a miss. It returns the end of the read
// that answers id, or nil when id is not asked for.
func (a *App) fetchGrid(id string) <-chan struct{} {
	if id == "" {
		return nil
	}
	// An undeclared namespace is never asked: the latch stands in for the answer.
	if a.deadNamespace(id) {
		a.fetch.grids.Settle(id, inflight.Refused)
		return nil
	}
	ctx, done, end, ok := a.fetch.grids.Join(id)
	if !ok {
		return end
	}
	go func() {
		err := a.loadGrid(ctx, id)
		// see inflight.Reads.Change
		owed := done()
		if err != nil {
			a.draw()
		} else {
			// Coalesced: completions land in bursts.
			a.scheduleFrame(traceevent.WhyGridLoaded)
		}
		if owed {
			a.fetchGrid(id)
		}
	}()
	return end
}

// refetchGrid is fetchGrid for a caller that knows id changed, so a read
// already in flight is owed a re-ask rather than refused.
func (a *App) refetchGrid(id string) {
	a.fetch.grids.Change(id)
	a.fetchGrid(id)
}

// fetchTileByID resolves a routable tile id whose grid is not cached: a place
// a pane stands in, whose refusal is said (clientsync.PlaceReadSurfaces).
func (a *App) fetchTileByID(tileID string) {
	a.readTileByID(tileID, clientsync.PlaceReadSurfaces)
}

// fetchLinkTarget is fetchTileByID for a link's target, which draws dead
// rather than say so (clientsync.TargetReadSurfaces).
func (a *App) fetchLinkTarget(tileID string) {
	a.readTileByID(tileID, clientsync.TargetReadSurfaces)
}

func (a *App) readTileByID(tileID string, surfaces func(inflight.Verdict) bool) {
	if tileID == "" {
		return
	}
	// An undeclared namespace is not asked; see fetchGrid.
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
		// clientsync.ReactRead owns the latch; an outage is said once, under "grid:".
		v := clientsync.ReactRead(o)
		a.fetch.tiles.Settle(tileID, v)
		switch {
		case surfaces(v):
			// The asker is a crumb, a descent or a link's target, which would
			// otherwise say nothing.
			detail := "the row is gone"
			if err != nil {
				detail = rpcErrText(err)
			}
			a.reportErr(errsurface.Error, "tile:"+tileID, "tile unavailable: "+detail)
		case v == inflight.Answered:
			a.resolveErr("tile:" + tileID)
			a.fetchGrid(tile.GridId)
		}
	}()
}

func nowMs() float64 {
	return js.Global().Get("Date").Call("now").Float()
}

// consoleLog prefixes every message with tag, which log readers and the e2e
// suite grep for.
func consoleLog(tag string) func(format string, args ...any) {
	return func(format string, args ...any) {
		js.Global().Get("console").Call("log", tag+" "+fmt.Sprintf(format, args...))
	}
}

// taggedLog is consoleLog plus a record. A site with its own record uses
// consoleLog, so one operation leaves one record.
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

func (a *App) frame() {
	now := nowMs()
	if a.ghost != nil {
		if landing, done := a.ghost.Step(now); done {
			// The render hides live on the ghost and die with it, so the cache
			// is the source of truth again.
			a.ghost = nil
		} else if landing {
			a.scheduleFrame(traceevent.WhyAnimation)
		}
	}
	for _, tr := range a.trans.List() {
		seg := tr.Segment()
		t := anim.Progress(now, tr.StartMs(), seg.DurationMs)
		eased := anim.EaseOutCubic(t)
		if p := a.tree.FindPane(tr.PaneID); p != nil {
			p.SetView(anim.Lerp(seg.FromCx, seg.ToCx, eased), anim.Lerp(seg.FromCy, seg.ToCy, eased),
				anim.LerpExp(seg.FromZoom, seg.ToZoom, eased))
		}
		if t >= 1 {
			a.trans.Advance(tr.PaneID, now)
		}
		if a.trans.Active(tr.PaneID) {
			a.scheduleFrame(traceevent.WhyTransition)
		}
	}
	if a.pruneTraces(now) {
		a.scheduleFrame(traceevent.WhyTraceFade)
	}
	a.draw()
}

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

// startTransition lands, rather than voids, whatever that pane was animating.
func (a *App) startTransition(t *transition.Transition) {
	a.trans.Start(t, nowMs())
	a.scheduleFrame(traceevent.WhyTransition)
}

// enterSegment is the one writer of the scratch viewport an animation drives.
func (a *App) enterSegment(paneID string, seg transition.Segment) {
	p := a.tree.FindPane(paneID)
	if p == nil {
		return
	}
	if seg.Place != nil {
		p.Stack = seg.Place.Clone()
	}
	p.SetView(seg.FromCx, seg.FromCy, seg.FromZoom)
}

// landTransition is what arriving means; a content descent pushes its frame
// there.
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
	// The navigation already read its grid, so landing asks only on a miss.
	gid := a.gridIDForPane(p)
	if _, ok := a.c.Grid(gid); !ok {
		a.fetchGrid(gid)
	}
	if tr.TraceTileID != "" {
		a.traces[p.ID] = traceState{tileID: tr.TraceTileID, startMs: nowMs()}
		a.scheduleFrame(traceevent.WhyTraceFade)
	}
	if tr.OnComplete != nil {
		tr.OnComplete()
	}
	a.scheduleURLUpdate()
	a.draw()
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

// startSSE keeps one event stream open for the life of the page;
// retry.Reconnect decides the waits and kicks.
func (a *App) startSSE() {
	var pace retry.Reconnect
	for {
		// The node forgets this session's interest when its stream closes.
		a.interest.Reopened()
		a.kickInterest()
		stream, err := a.cl.Subscribe(context.Background())
		if err != nil {
			// Until this reconnects, everything on screen is silently going stale.
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
			// events.Route and events.Owe are the tables; this runs their arms.
			if g := events.Owe(ev); g != "" {
				a.fetch.grids.Change(g)
			}
			plan := events.Route(ev)
			if plan.DropPreviews != "" {
				a.views.urlPreview.Drop(plan.DropPreviews)
				a.views.renderedPrev.Drop(plan.DropPreviews)
			}
			if plan.Reload != "" {
				a.owePageReload(plan.Reload)
			}
			if plan.ClearLatch != "" {
				a.fetch.grids.Change(plan.ClearLatch)
			}
			if plan.ClearContent != "" {
				a.fetch.contents.Change(plan.ClearContent)
				a.fetch.previews.Change(plan.ClearContent)
			}
			if plan.Revive != "" {
				served := func(id string) bool { return cache.ServedBy(id, plan.Revive) }
				a.reask(nil, a.fetch.tiles.ReviveIf(served), a.fetch.contents.ReviveIf(served),
					a.fetch.previews.ReviveIf(served))
			}
			if plan.Fetch != "" {
				a.emit(traceevent.EventRefetch(plan.Fetch))
				a.fetchGrid(plan.Fetch)
			}
			if f := plan.Reframe; f != nil {
				fr, ok := rpc.ViewOf(f.GetViewCx(), f.GetViewCy(), f.GetViewZoom()).Framing()
				if ok && a.cacheDoorwayFraming(f.GetGridId(), fr) {
					a.emit(traceevent.EventApplied(ev))
					a.draw()
				}
			}
			if plan.Health != nil {
				a.reportPluginHealth(plan.Health)
			}
		}
		stream.Close()
		time.Sleep(pace.StreamEnded())
	}
}

// retryKick drains what a transport gap or a settled health transition
// (events.Resyncs) left behind. cache.ServedBy owns what a scope covers; the
// outbox drain is never scoped, because a parked write is the user's bytes.
func (a *App) retryKick(resync bool, source string) {
	if resync {
		served := func(id string) bool { return cache.ServedBy(id, source) }
		// Failure latches are gap state, asked again by name. The menu set's
		// predicate is cache.Reaches: a connection's flap covers the nodes behind it.
		reaches := func(ns string) bool { return cache.Reaches(ns, source) }
		a.reask(a.fetch.grids.ClearIf(served), a.fetch.tiles.ClearIf(served),
			a.fetch.contents.ClearIf(served), a.fetch.previews.ClearIf(served),
			a.fetch.menus.ClearIf(reaches))
		// A request that dies with its link never returns, and its claim would block
		// every retry; re-ask the grids by name. A cancelled read says nothing
		// (clientsync.OutcomeAbandoned).
		stuck := a.fetch.grids.CancelIf(served)
		a.fetch.tiles.CancelIf(served)
		a.fetch.contents.CancelIf(served)
		a.fetch.previews.CancelIf(served)
		a.fetch.menus.CancelIf(reaches)
		for _, gid := range append(stuck, a.c.ResyncSet(source)...) {
			a.fetchGrid(gid)
		}
		a.refetchMenus()
	}
	a.syncContentOutbox()
	a.drainOutbox()
}

// reask asks again for reads whose latches were just cleared; previews and
// menus are asked by the draw.
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

// drainOutbox re-posts everything owed, in parked order. Unload takes it too,
// so quit and reconnect treat what is owed the same.
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

// retryBackstop re-posts the outbox without waiting for a reconnect: the
// stream survives blips a unary write does not.
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

// Session-local state: the URL captures only the focused pane's place, so a
// restored pane ascends onto persisted framing.

// gridIDForPane walks the pane's anchor down its doorway path; "" when blank.
func (a *App) gridIDForPane(p *pane.Pane) string {
	return a.gridIDForPathFrom(p.Anchor(), p.Path())
}

// gridIDForPathFrom walks path from anchor to the leaf grid id: anchor for an
// empty or stale path.
func (a *App) gridIDForPathFrom(anchor string, p []string) string {
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

// refetchGridOnConflict also posts an Info notice: the user's optimistic
// change is being replaced.
func (a *App) refetchGridOnConflict(gridID string, where string) {
	a.reportErr(errsurface.Info, "conflict:"+where, where+": changed elsewhere — reloaded")
	a.refetchGrid(gridID)
}

// reportErr is the one wasm entry into the error surface; it also logs.
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

// scheduleErrExpiry arms one timer for the soonest deadline.
func (a *App) scheduleErrExpiry() {
	if a.persist.sched.errExpire.Pending() {
		return
	}
	d, ok := a.errs.NextDeadline(time.Now())
	if !ok {
		return
	}
	ms := int(d/time.Millisecond) + 1
	if ms < 1 {
		ms = 1
	}
	a.persist.sched.errExpire.Arm(ms)
}

// resolveErr clears a source's notice when its condition heals; the repaint
// rides the surface's verdict.
func (a *App) resolveErr(source string) {
	if a.errs.Resolve(source) {
		a.scheduleFrame(traceevent.WhyNotice)
	}
}

// reportPluginHealth runs events.ReactHealth's plan for a health event: the
// notices at once, the resync once the health holds.
func (a *App) reportPluginHealth(h *gridwellv1.EventPluginHealth) {
	label := h.PluginUuid
	if pl, ok := a.pluginByUUID(h.PluginUuid); ok && pl.Label != "" {
		label = pl.Label
	}
	// The client's one copy of which sources are not answering.
	wasDark := a.c.NoteHealth(h.PluginUuid, h.Healthy)
	r := events.ReactHealth(h, label, wasDark)
	for _, n := range []events.StickyNotice{r.Dark, r.LiveOff} {
		if n.Message == "" {
			a.resolveErr(n.Source)
		} else {
			a.reportErr(errsurface.Error, n.Source, n.Message)
		}
	}
	if r.Resync != "" {
		a.persist.sched.healthResync.Transition(r.Resync)
	}
}
