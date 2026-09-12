//go:build js && wasm

// Package main is the WASM entry point for the Gridwell client: canvas, DOM,
// and the RPC calls. The code here reaches into syscall/js and is exercised
// only in a browser, so every decision belongs in one of the pure, tested
// client/* packages instead — ARCHITECTURE.md, "The client".
package main

import (
	"context"
	"fmt"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"maps"
	"strconv"
	"syscall/js"
	"time"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/gridpath"
	"github.com/josephburnett/gridwell/client/inflight"
	"github.com/josephburnett/gridwell/client/menu"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/outbox"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panestate"
	"github.com/josephburnett/gridwell/client/preview"
	"github.com/josephburnett/gridwell/client/shellstream"
	"github.com/josephburnett/gridwell/client/shellws"
	"github.com/josephburnett/gridwell/client/textedit"
	"github.com/josephburnett/gridwell/client/touchgest"
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

	views viewCaches

	// ws is the window's level stack: which pane tile the user is inside and
	// what outer tree each descent restores. a.tree displays its top.
	ws pane.Levels

	// caps is derived once at boot; nothing else asks the bridge for a decision.
	caps caps.Caps

	// origin is the serving origin and contentToken the /content/ door's path
	// capability. A page tile's address is derived at use time, never persisted.
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

	// traces holds the per-pane ascent-trace highlight, ephemeral like selection.
	traces map[string]traceState

	overlays overlayState

	// zoomKeyRelays counts zoom chords from the main-process relay. e2e-only:
	// with the registry's counter it brackets the IPC hop.
	zoomKeyRelays int

	// tileMutates counts tile mutations in flight, so the descent or placement
	// that follows one has not happened yet. postTileMutate owns it.
	tileMutates int

	// renderedPanePaints is e2e attribution: an unfocused pane paints raster.
	renderedPanePaints map[string]int
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
	renderedView    js.Value
	renderedReady   bool
	lastRenderedKey string

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

	// renderedPrev caches rasterized rendered-mode previews by tile id.
	renderedPrev map[string]*renderedPreview

	// paneLayouts memoizes the decode, invalidated by blob generation; the
	// truth is the tile row plus its content bytes.
	paneLayouts map[string]*paneLayoutEntry

	// menuCtxs is keyed by the grid-stamped node_ns; "" is a.plugins, a.caps.
	menuCtxs map[string]*menuContext
}

// newViewCaches is the one place the group is constructed.
func newViewCaches() viewCaches {
	return viewCaches{
		urlPreview:   preview.NewCache(preview.NewJSDecoder()),
		wrapCache:    map[string][]string{},
		renderedPrev: map[string]*renderedPreview{},
		paneLayouts:  map[string]*paneLayoutEntry{},
		menuCtxs:     map[string]*menuContext{},
	}
}

// fetchState owns whether a read is outstanding or has failed. A claim kept
// elsewhere is how a swallowed request holds a key for the life of the page.
type fetchState struct {
	// gridLoadFailed lets the renderer say so and stops the URL walk retrying
	// in a tight loop. loadGrid is the one writer.
	gridLoadFailed map[string]bool

	// gridFetch dedupes GetGrid, which every draw fires on a cache miss. A
	// request lost with its link used to hold its id forever.
	gridFetch *inflight.Set

	// contentFetch: without the claim one absent body spawns a fetch per frame,
	// and a reply older than one already landed would repaint stale bytes.
	contentFetch *inflight.Set

	// tileFetch: a routable id may name a tile whose grid was never visited.
	tileFetch *inflight.Set
	// tileLoadFailed stops a missing id re-firing GetTile every frame forever.
	tileLoadFailed map[string]bool

	// previewFetch dedupes GetTilePreview, fired on every draw until decoded.
	previewFetch *inflight.Set

	// menuFetch is keyed by a source NAME, so cache.Reaches is what scopes it.
	menuFetch *inflight.Set
}

// newFetchState is the one place the group is constructed.
func newFetchState() fetchState {
	return fetchState{
		gridLoadFailed: map[string]bool{},
		gridFetch:      inflight.New(inflight.Deadline),
		contentFetch:   inflight.New(inflight.Deadline),
		tileFetch:      inflight.New(inflight.Deadline),
		tileLoadFailed: map[string]bool{},
		previewFetch:   inflight.New(inflight.Deadline),
		menuFetch:      inflight.New(inflight.Deadline),
	}
}

// debounce is one coalescing deferred callback. Its js.Func is allocated once,
// by set(), so re-arming never leaks a handle.
type debounce struct {
	pending bool
	cb      js.Func
}

// set is called once, at startup.
func (d *debounce) set(body func()) {
	d.cb = js.FuncOf(func(this js.Value, args []js.Value) any {
		d.pending = false
		body()
		return nil
	})
}

// arm only coalesces; caller-side conditions stay at the call site.
func (d *debounce) arm(ms int) {
	if d.pending {
		return
	}
	d.pending = true
	js.Global().Call("setTimeout", d.cb, ms)
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

// newPersistState is the one place the group is built. The debounces get their
// bodies at boot, in afterBootstrap, since those close over the App.
func newPersistState() persistState {
	return persistState{
		textSaves:        textedit.NewSaveQueue(),
		wellWheelPending: map[string]wellWheelDrift{},
		persistPosts:     map[string]int{},
		out:              outbox.New(),
	}
}

type scheduler struct {
	rafScheduled bool

	// wsSave's callback encodes, hash-diffs, and posts the layout on a change.
	wsSave debounce

	urlUpdate debounce

	// framingSave's callback flushes settled framing through the no-op-guarded
	// writers.
	framingSave debounce

	textSave debounce

	// errExpire lets one-shot notices leave the strip without polling.
	errExpire debounce
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
	panestate.State
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
		pl = &paneLocal{State: panestate.New()}
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

// traceDurMs is how long the ascent-trace outline takes to fade out.
const traceDurMs = 2000.0

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
	app = &App{
		doc:                js.Global().Get("document"),
		win:                js.Global().Get("window"),
		origin:             origin,
		cl:                 rpc.NewDefaultClient(origin),
		c:                  cache.New(),
		locals:             map[string]*paneLocal{},
		menu:               menu.New(),
		errs:               errsurface.New(),
		caps:               caps.Derive(bridgeCaps(), false),
		fetch:              newFetchState(),
		persist:            newPersistState(),
		views:              newViewCaches(),
		shellAlive:         map[string]bool{},
		shellAliveProbing:  map[string][]func(bool){},
		traces:             map[string]traceState{},
		renderedPanePaints: map[string]int{},
	}
	app.trans = transition.New(app.enterSegment, app.landTransition)
	app.nav = nav.New()
	app.canvas = app.doc.Call("getElementById", "canvas")
	app.cctx = app.canvas.Call("getContext", "2d")
	app.tree = pane.NewTree()
	app.tree.FocusedPane().Zoom = 1.0
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
	backoff := time.Second
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
		time.Sleep(backoff)
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
	a.plugins = rpc.MenuRows(plugins)
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

	a.persist.sched.wsSave.set(a.flushWorkspaceSave)
	a.persist.sched.urlUpdate.set(a.writeURLNow)
	a.persist.sched.framingSave.set(a.flushFramingSave)
	a.persist.sched.errExpire.set(func() {
		if a.errs.Expire(time.Now()) {
			a.scheduleFrame() // strip shrank; panes reclaim the height on redraw
		}
		a.scheduleErrExpiry()
	})

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

// loadGrid is the one GetGrid-to-cache hop, so gridLoadFailed has one writer:
// the renderer's fetchGrid and the restore walk's awaited read both come here.
func (a *App) loadGrid(ctx context.Context, id string) error {
	resp, err := a.cl.GetGrid(ctx, id)
	if err != nil {
		// A verdict latches: the same ask gets the same answer, so fetchGrid
		// stops until a retry-justifying path clears it. Transport does not.
		if clientsync.Of(err) != clientsync.OutcomeTransport {
			a.fetch.gridLoadFailed[id] = true
		}
		a.reportErr(errsurface.Error, "grid:"+id, "grid unavailable: "+rpcErrText(err))
		return err
	}
	a.resolveErr("grid:" + id)
	delete(a.fetch.gridLoadFailed, id)
	if resp.Grid.Id != id {
		// The cache keys by the answered name and every frame resolves by the
		// asked one, so answering under another id strands the pane on 200s.
		a.fetch.gridLoadFailed[id] = true
		a.reportErr(errsurface.Error, "grid:"+id,
			"asked for grid "+id+", was answered "+resp.Grid.Id+" — the view of "+id+" cannot load")
	}
	a.c.PutGrid(resp.Grid, resp.Tiles)
	return nil
}

// fetchGrid loads a grid in the background, deduped per id: the renderer fires
// it on every cache miss every frame, which would otherwise dogpile the server.
func (a *App) fetchGrid(id string) {
	// A latched grid is not re-asked: the paths that justify a retry clear the
	// latch. fetchGrid clearing it turned one verdict into a per-frame loop.
	if id == "" || a.fetch.gridLoadFailed[id] {
		return
	}
	// A grid in a namespace this node does not declare is never asked for: the
	// latch stands in for the answer, and no verdict reaches the strip.
	if a.deadNamespace(id) {
		a.fetch.gridLoadFailed[id] = true
		return
	}
	ctx, done, ok := a.fetch.gridFetch.Begin(id)
	if !ok {
		return
	}
	go func() {
		defer done()
		if a.loadGrid(ctx, id) != nil {
			a.draw()
			return
		}
		// Coalesced repaint: completions land in bursts, and one draw per
		// child-grid read would be hundreds of repaints for a big directory.
		a.scheduleFrame()
	}()
}

// fetchTileByID resolves a routable tile id whose grid is not cached: GetTile
// locates it, then fetchGrid pulls its grid in so findTileByID hits.
func (a *App) fetchTileByID(tileID string) {
	if tileID == "" || a.fetch.tileLoadFailed[tileID] {
		return
	}
	// Same rule as fetchGrid: an undeclared namespace is not asked. A leaf link
	// into a removed plugin stays its own dead face.
	if a.deadNamespace(tileID) {
		a.fetch.tileLoadFailed[tileID] = true
		return
	}
	ctx, done, ok := a.fetch.tileFetch.Begin(tileID)
	if !ok {
		return
	}
	go func() {
		defer done()
		tile, err := a.cl.GetTile(ctx, tileID)
		if err != nil || tile == nil {
			// Latch only on a server verdict: a broken reference answers the
			// same way every time. A transport failure latches nothing.
			if clientsync.Of(err) != clientsync.OutcomeTransport {
				a.fetch.tileLoadFailed[tileID] = true
				// The asker is a crumb or a descent, which without this draw
				// an empty content box named "unnamed" and say nothing. An
				// outage is not named once per id: the same read's grid says
				// it once under "grid:".
				detail := "the row is gone"
				if err != nil {
					detail = rpcErrText(err)
				}
				a.reportErr(errsurface.Error, "tile:"+tileID, "tile unavailable: "+detail)
			}
			return
		}
		a.resolveErr("tile:" + tileID)
		a.fetchGrid(tile.GridId)
	}()
}

func nowMs() float64 {
	return js.Global().Get("Date").Call("now").Float()
}

// taggedLog prefixes every message with tag. The prefixes are what a log reader
// and the e2e suite grep for, so they are output, not decoration.
func taggedLog(tag string) func(format string, args ...any) {
	return func(format string, args ...any) {
		js.Global().Get("console").Call("log", tag+" "+fmt.Sprintf(format, args...))
	}
}

func (a *App) scheduleFrame() {
	if a.persist.sched.rafScheduled {
		return
	}
	a.persist.sched.rafScheduled = true
	js.Global().Call("requestAnimationFrame", js.FuncOf(func(this js.Value, args []js.Value) any {
		a.persist.sched.rafScheduled = false
		a.frame()
		return nil
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
			a.scheduleFrame()
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
			a.scheduleFrame()
		}
	}
	// Ascent-trace fades need frames until they run out.
	if a.pruneTraces(now) {
		a.scheduleFrame()
	}
	a.draw()
}

// pruneTraces reports whether any trace is still fading.
func (a *App) pruneTraces(now float64) bool {
	alive := false
	for paneID, tr := range a.traces {
		if anim.FadeAlpha(now, tr.startMs, traceDurMs) <= 0 {
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
	a.scheduleFrame()
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
	a.fetch.gridLoadFailed = map[string]bool{}
	a.fetchGrid(a.gridIDForPane(p))
	if tr.TraceTileID != "" {
		// Keep the frame loop alive for the fade.
		a.traces[p.ID] = traceState{tileID: tr.TraceTileID, startMs: nowMs()}
		a.scheduleFrame()
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

// startSSE reconnects after a backoff, and a reconnect after a gap fires the
// retry kick, because Subscribe has no cursor and the gap's events are gone.
func (a *App) startSSE() {
	gap := false
	for {
		stream, err := a.cl.Subscribe(context.Background())
		if err != nil {
			// Until this reconnects, everything on screen is silently going
			// stale. It coalesces, and resolves itself on reconnect below.
			a.reportErr(errsurface.Error, "events", "live updates disconnected — retrying")
			gap = true
			time.Sleep(time.Second)
			continue
		}
		a.resolveErr("events")
		if gap {
			gap = false
			// The gap swallowed events without saying whose, so nothing scopes.
			a.retryKick(true, cache.EverySource)
		}
		for {
			ev, ok, err := stream.Recv()
			if err != nil {
				a.reportErr(errsurface.Error, "events", "live updates disconnected — retrying")
				gap = true
				break
			}
			if !ok {
				// A clean EOF is still a gap: no cursor to resume from.
				gap = true
				break
			}
			if a.c.Apply(ev) {
				a.draw()
			}
			// A removed tile's decoded preview and its object URL must be
			// released, or deleting tiles leaks browser image resources.
			if r := ev.GetTileRemoved(); r != nil {
				a.views.urlPreview.Drop(r.TileId)
				a.dropRenderedPreview(r.TileId)
			}
			// GridChanged is the one per-grid signal, so it also clears that
			// grid's latch. Unconditional: the next descent would read stale.
			if g := ev.GetGridChanged(); g != nil {
				delete(a.fetch.gridLoadFailed, g.GridId)
				a.fetchGrid(g.GridId)
			}
			// A plugin's own event stream, not this client's connection, went
			// dark or recovered. The source is per uuid, so plugins do not
			// clear each other.
			if h := ev.GetPluginHealth(); h != nil {
				a.reportPluginHealth(h)
			}
		}
		stream.Close()
		time.Sleep(500 * time.Millisecond)
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
		// Failure latches are gap state: a grid that failed while the link was
		// down deserves a fresh attempt. This source's only.
		maps.DeleteFunc(a.fetch.tileLoadFailed, func(id string, _ bool) bool { return served(id) })
		maps.DeleteFunc(a.fetch.gridLoadFailed, func(id string, _ bool) bool { return served(id) })
		// So is a fetch still in flight: a request that dies with its link never
		// returns, and its claim would keep every retry away forever. Re-ask for
		// the grids by name, since a pane waiting on one it never received is
		// not in the cache for the sweep below to find.
		stuck := a.fetch.gridFetch.CancelIf(served)
		a.fetch.tileFetch.CancelIf(served)
		a.fetch.contentFetch.CancelIf(served)
		a.fetch.previewFetch.CancelIf(served)
		// The menu set is keyed by a source name, so its predicate is
		// cache.Reaches: a connection's flap covers the nodes behind it.
		a.fetch.menuFetch.CancelIf(func(ns string) bool { return cache.Reaches(ns, source) })
		for _, gid := range append(stuck, a.c.ResyncSet(source)...) {
			a.fetchGrid(gid)
		}
	}
	a.syncContentOutbox()
	for _, retry := range a.persist.out.Drain() {
		retry()
	}
}

// retryBackstop re-posts what the outbox holds without waiting for a reconnect
// that may never come: the stream survives blips a unary write does not.
func (a *App) retryBackstop() {
	for {
		time.Sleep(30 * time.Second)
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
	// The walk is the pure gridpath.ResolveLeafGrid; the closure does the cache
	// read and kicks a background fetch on a miss.
	return gridpath.ResolveLeafGrid(anchor, p,
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
	a.errs.Report(sev, source, message, time.Now())
	a.scheduleErrExpiry()
	a.scheduleFrame()
}

// scheduleErrExpiry arms one setTimeout for the soonest deadline; the callback
// prunes and re-arms, so a pushed-out deadline fires early, never late.
func (a *App) scheduleErrExpiry() {
	if a.persist.sched.errExpire.pending {
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
	a.persist.sched.errExpire.arm(ms)
}

// resolveErr clears a source's notice when its condition heals.
func (a *App) resolveErr(source string) {
	a.errs.Resolve(source)
	a.scheduleFrame()
}

// reportPluginHealth surfaces a health transition, because a plugin's stream
// being down means its tiles stopped updating with no other signal.
func (a *App) reportPluginHealth(h *gridwellv1.EventPluginHealth) {
	source := "plugin:" + h.PluginUuid
	if h.Healthy {
		// A recovered plugin is a healed gap for its tiles: the fan-in resumed
		// with no backlog, so this client missed its events too.
		a.resolveErr(source)
		a.retryKick(true, h.PluginUuid)
		return
	}
	label := h.PluginUuid
	if pl, ok := a.pluginByUUID(h.PluginUuid); ok && pl.Label != "" {
		label = pl.Label
	}
	a.reportErr(errsurface.Error, source, label+": live updates stopped — "+h.Detail)
	// A source going down changes what its grids are: a connection's rooms
	// become the node's stale memory, and nothing says so until a re-read.
	a.retryKick(true, h.PluginUuid)
}
