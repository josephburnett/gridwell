//go:build js && wasm

// Package main is the WASM entry point for the Gridwell client.
//
// This file is intentionally a thin wiring shim: anything testable lives in
// client/pane, client/markdown, client/dragdrop, client/cache. The code here
// reaches into syscall/js and is exercised manually in a browser.
package main

import (
	"context"
	"strconv"
	"syscall/js"
	"time"

	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/preview"
	"github.com/josephburnett/gridwell/internal/rpc"
)

const (
	cellPx     = 64.0
	zoomMin    = 0.25
	zoomMax    = 8.0
	zoomFactor = 1.1

	// fileFixedScale is the constant render scale for text-file content
	// when descended. There is no zoom: the descended pane is a plain
	// window onto the document at this scale, scrolled vertically. The
	// parent-grid preview re-renders at this same scale and crops the
	// last-framed window into the tile footprint.
	fileFixedScale = 1.0

	// fileNaturalContentPx is the logical width of the rendered markdown
	// canvas at fileFixedScale. Lines wrap at this width in both the
	// descended window and the preview so they match.
	fileNaturalContentPx = 800.0
)

// app is the running client. Held in a package-level var so JS callbacks can
// reach it without closures over reflect.Value.
var app *App

// App is the client state.
type App struct {
	doc, win js.Value
	canvas   js.Value
	cctx     js.Value // 2d context

	// cl is the Connect-RPC client pointed at the same origin we were
	// served from. All server reads and mutations go through it.
	cl *rpc.Client

	rootGridID int64
	// Seeded from Bootstrap and refreshed by scheduleRootViewSave.
	// Used to restore the focused pane's initial framing on boot, and
	// as the fallback target when ascending all the way back to root.
	rootViewCx, rootViewCy, rootViewZoom float64
	tree                                 *pane.Tree
	c                                    *cache.Cache

	width, height float64

	dragging *dragState

	// Per-pane selection: paneID → node id (0 means nothing selected).
	selectedTileID map[string]int64

	// Plus-button popover state.
	menuOpen   bool
	menuPaneID string
	menuHover  int // index of hovered menu item, or -1

	// gridLoadFailed records grids whose last fetch returned non-200, so
	// the renderer can show a meaningful message and we don't retry in
	// a tight loop.
	gridLoadFailed map[int64]bool

	// gridInflight tracks grid ids with a pending GetGrid request so
	// repeated draws (which call fetchGrid on every cache miss) don't
	// dogpile the server. Cleared in the fetch goroutine after the
	// response lands.
	gridInflight map[int64]bool

	// ghost is the in-flight visual representation of a node being dragged
	// or animated to/from somewhere. The dragged node renders here at
	// sub-cell screen precision instead of at its stored cell position.
	ghost *ghost

	// hiddenTileID, when non-zero, suppresses rendering of the cached
	// tile row with this primary-key id in the named pane. Set on drag
	// start so the dragged source doesn't paint underneath its own
	// ghost. Matches the dragged ROW, not its ObjectID — a tile and
	// any clones of it share an ObjectID, and the old by-ObjectID
	// match made every clone vanish whenever its sibling was picked
	// up. See dragdrop.HiddenMatch for the predicate + test.
	hiddenTileID int64
	hiddenPaneID string

	// previewPaneID is the pane currently being painted; set by
	// drawPane before per-node calls and cleared after. Lets the
	// child-preview renderer scope the hidden ObjectID to the right
	// pane (a node only hides in its source pane).
	previewPaneID string

	// previewPaneRect mirrors previewPaneID — the screen rectangle of
	// the pane being painted. drawNodeWithPreview reads it to compute
	// OvertakeZoom (which depends on pane dimensions) for the
	// preview-cell-size formula.
	previewPaneRect pane.Rect

	// animation is the current ghost animation, if any (snap-to-target on
	// drop or snap-back-to-origin on failure).
	animation *anim.Animation

	// sched holds the debounce / requestAnimationFrame bookkeeping: each
	// "Scheduled" bool guards a pending callback so repeated triggers
	// coalesce into one, and each retained js.Func is allocated once so
	// re-scheduling never leaks handles. See scheduler below.
	sched scheduler

	// transition is the active descent/ascent zoom animation, if any.
	transition *paneTransition

	// rightDrag is the in-flight right-button gesture, if any. Right
	// button is dedicated to pane management — split, resize, close,
	// swap. See right_button.go.
	rightDrag *rightDragState

	// leftResize is the in-flight left-button pane-boundary resize, if
	// any. Same divider math as the right-button resize but it never
	// closes a pane (clamps to a recoverable minimum), so the left button
	// can shuffle / minimize pane sizes without risking a live tile.
	leftResize *leftResizeState

	// paneStateStack stores the parent viewport (Cx, Cy, Zoom) saved
	// just before each descent. On ascent, the saved state is popped and
	// used to animate the parent back to where it was — so the parent
	// grid doesn't jump. This is distinct from well.ViewX/Y/Zoom, which
	// describes the child grid's framing and drives the preview, the
	// descent target, and the ascent return value written back to the
	// server. paneStateStack is session-local and not persisted.
	// Indexed by pane id; the slice's length matches len(pane.Path).
	paneStateStack map[string][]paneState

	// urlPreview caches decoded HTMLImageElement values for URL and
	// shell tile previews. Keyed by tile id; auto-invalidates when a
	// tile's PreviewBlobID changes server-side — see client/preview.
	urlPreview *preview.Cache

	// urlStreams holds the live native WebContentsView handle for each
	// pane descended into a live URL tile. One per pane id; multiple panes
	// may host views concurrently. Named "streams" for historical reasons
	// (it was the /rpc/URLStream WebSocket map); it now drives the Electron
	// webview bridge.
	urlStreams map[string]*urlView

	// shellStreams mirrors urlStreams for live shell tile sessions —
	// one /rpc/ShellStream WebSocket plus its xterm.js DOM overlay
	// per pane descended into a live shell tile.
	shellStreams map[string]*shellStreamConn

	// shellAlive caches the result of the ShellSessionAlive probe per
	// tile id. The refresh button shows iff (preview_blob_id == 0)
	// || shellAlive[id] is true. shellAliveProbing dedups in-flight
	// probes so a rapid sequence of redraws doesn't fan out into
	// many RPC calls. A missing key means "unknown" — the renderer
	// kicks off a probe and hides the button until the result lands.
	shellAlive        map[int64]bool
	shellAliveProbing map[int64]bool

	// fileTextarea is the lazily-created <textarea> element used for
	// markdown text-mode editing. It is positioned over the focused pane
	// when pane.TextFocus != 0 and pane.TextMode == "text", and hidden
	// otherwise. We hold it as a single shared element to avoid creating
	// fresh DOM nodes on every descent.
	fileTextarea js.Value
	// fileTextareaInputCb is the input event listener that mirrors the
	// textarea's value into a per-frame redraw. Held so we can release
	// it cleanly if the App is torn down (currently never).
	fileTextareaInputCb  js.Func
	fileTextareaScrollCb js.Func

	// fileToggleBtn is the floating rendered/raw toggle for a markdown
	// descent. A DOM element (not a canvas button) so it can sit above
	// the textarea overlay — letting the text content fill the pane
	// edge-to-edge instead of reserving a strip for a canvas button.
	fileToggleBtn js.Value
	fileToggleCb  js.Func

	// urlModalOpen tracks whether the URL-entry modal is currently open.
	// A second openURLModal call while this is true is a no-op.
	urlModalOpen bool

	// urlPanX / urlPanY hold the per-pane pan offset for frozen URL
	// descents (keyed by pane ID). Live URL panes don't pan — clicks
	// forward to Chromium. Reset on each new descent. Not persisted.
	urlPanX map[string]float64
	urlPanY map[string]float64

	// urlPanDragging is true while the user is dragging inside a frozen
	// URL descent pane. Used to switch the canvas cursor to "grabbing"
	// during the drag and back to "grab" on release.
	urlPanDragging bool

	// embedHits collects click-targets for tile-embeds rendered inside
	// text panes this frame. Reset at the start of each draw() and
	// appended to as embeds are painted by drawMarkdownInPane. Queried by
	// the input handler to descend on click. See embed.go.
	embedHits []embedHit

	// lastTextareaTileID tracks which text-tile id the singleton
	// textarea is currently bound to (i.e., whose blob it holds in its
	// value). 0 means "bound to nothing" (textarea is hidden or never
	// seeded). refreshFileOverlay uses this to decide whether to re-seed
	// from the cached blob on a focus shift: same tile → preserve typing,
	// different tile → fresh content. Embed drops also consult it to
	// push new content into the textarea when it's already bound to the
	// drop target.
	lastTextareaTileID int64
}

// scheduler holds the App's debounce / requestAnimationFrame bookkeeping.
// Each "Scheduled" bool guards a pending callback so repeated triggers
// coalesce into a single deferred run; each js.Func is allocated once and
// retained so re-scheduling never leaks handles.
type scheduler struct {
	// rafScheduled tracks a pending requestAnimationFrame so we don't
	// queue redundant frames.
	rafScheduled bool

	// urlUpdateScheduled / urlUpdateCb debounce the URL replaceState:
	// multiple state changes within the window coalesce into one.
	urlUpdateScheduled bool
	urlUpdateCb        js.Func

	// fileSaveScheduled / fileSaveCb debounce the text-tile content save.
	fileSaveScheduled bool
	fileSaveCb        js.Func

	// rootViewSaveScheduled / rootViewSaveCb debounce the root-grid
	// default-view persistence (only fires when the focused pane is at the
	// user's root; well descents persist via SetWellView on ascent).
	rootViewSaveScheduled bool
	rootViewSaveCb        js.Func
}

// paneState is a captured pane viewport plus, when the descent
// originated from inside a text-tile, the text descent context to
// restore on matching ascent.
//
// TextFocus == 0 means "the parent was a plain grid view" — the common
// case. A non-zero TextFocus is set when descending out of a text tile
// (e.g. clicking a tile-embed); on ascent the saved TextMode and scroll
// are reinstalled so a single ascent lands back in the doc rather than
// in the grid behind it.
type paneState struct {
	Cx          float64 `json:"cx"`
	Cy          float64 `json:"cy"`
	Zoom        float64 `json:"zoom"`
	TextFocus   int64   `json:"text_focus,omitempty"`
	TextMode    string  `json:"text_mode,omitempty"`
	TextScrollX float64 `json:"text_scroll_x,omitempty"`
	TextScrollY float64 `json:"text_scroll_y,omitempty"`
}

// paneTransition is the active per-pane zoom animation. It is a series of
// segments; each segment carries the path that should be installed when it
// starts and animates the viewport from `from*` toward `to*` over
// `durationMs`. Path-switch points (descent / ascent) are segment
// boundaries; the visual continuity at those boundaries is achieved by
// setting up calibrated start/end states in zoomtrans.
//
// Descent uses two segments: one for the parent zoom-in, then a
// zero-duration "install" segment that lands on the calibrated child state.
// Ascent uses two segments: a child zoom-out to the calibrated state, then
// a parent zoom-out from "well overtakes" back to the saved state.
type paneTransition struct {
	paneID         string
	segments       []transSegment
	currentSegment int
	segmentStartMs float64
	// onComplete, if set, runs after the last segment lands. Used by text
	// tile descent to install pane.TextFocus only once the visual transition
	// has reached the tile's footprint at OvertakeZoom (so the toggle
	// button appearing doesn't pop into view mid-animation).
	onComplete func()
}

type transSegment struct {
	path                     []int64
	fromCx, fromCy, fromZoom float64
	toCx, toCy, toZoom       float64
	durationMs               float64
}

// ghost is a transient floating render of a tile, positioned in screen
// coordinates within a specific pane. screenX/Y is the top-left corner of
// the tile's footprint at the rendered cell size.
//
// displayedCellSize is the actual rendered size used by the renderer
// each frame; targetCellSize is what the cursor's current drop target
// wants. Each frame the displayed value lerps toward target so the
// ghost smoothly resizes when the cursor crosses pane boundaries or
// enters/leaves a well's child preview.
type ghost struct {
	tile              rpc.Tile
	paneID            string
	screenX           float64
	screenY           float64
	displayedCellSize float64
	targetCellSize    float64

	// fragmentation animates the "going into a black hole" effect.
	// 0 = intact, 1 = fully fragmented (shards drifted outward,
	// alpha cut, slight rotation). Lerps the same way cell size
	// does, so dragging in and back out smoothly reassembles.
	displayedFragmentation float64
	targetFragmentation    float64

	// overDoc is true while the cursor is hovering a raw-mode text-pane
	// drop target (right-drag-clone semantics → insert markdown link
	// instead of clone). The ghost renderer paints a chain-link badge
	// over the tile so the user sees the action will be "link", not
	// "clone".
	overDoc bool

	// forbidden is set when the cursor is over a drop target that would
	// be rejected: a rendered-mode doc (read-only), or a regular-grid
	// cell while the source comes from a source-backed grid (the
	// left-drag "move" that the server refuses — user must right-drag
	// to clone/link instead). Renderer paints the international "no
	// entry" badge over the ghost; mouseup snap-backs without RPC.
	forbidden bool
}

// dragState tracks an in-progress drag from a tile onto the cursor.
//
// `started` is false until the cursor moves more than dragThreshold pixels
// from the mousedown point, so a bare click (down+up with no movement) can
// be distinguished from a drag and treated as "select" instead of "move".
//
// snapshotTile and origin* fields capture where the dragged tile started so
// we can render a smooth ghost at the cursor and animate snap-back to the
// original position if the drop is rejected.
type dragState struct {
	originPaneID string
	tileID       int64
	cellOffsetX  float64
	cellOffsetY  float64
	startScreenX float64
	startScreenY float64
	curScreenX   float64
	curScreenY   float64
	started      bool
	// clone marks a right-button clone drag (armed by armRightClone). Such
	// a drag commits only through the right-button release path; the
	// left-button move-commit must refuse it so a stray non-right release
	// can't silently turn the clone into a move.
	clone          bool
	snapshotTile   rpc.Tile
	originScreenX  float64
	originScreenY  float64
	originPaneRect pane.Rect

	// Template drag from the + palette: tileID is 0 (no real node yet)
	// but isTemplate is true and template carries the kind that was
	// grabbed. Drop creates the node at the snapped cell.
	isTemplate bool
	template   templateKind

	// Source-grid info — set at mousedown; same as the focused pane's
	// grid for parent-grid drags, or the well's child grid for "pull
	// out of well" drags. Carried separately so the drop commit can
	// build a MoveTile RPC with the right Path/grid id even when source
	// and dest are different grids inside the same pane.
	srcGridID   int64
	srcPath     []int64
	srcCellSize float64
}

// dragThreshold is the cursor-movement distance that turns a press into a
// drag. Below this, mousedown→mouseup is treated as a click (select).
const dragThreshold = 4.0

func main() {
	origin := js.Global().Get("location").Get("origin").String()
	app = &App{
		doc:               js.Global().Get("document"),
		win:               js.Global().Get("window"),
		cl:                rpc.NewDefaultClient(origin),
		c:                 cache.New(),
		selectedTileID:    map[string]int64{},
		menuHover:         -1,
		gridLoadFailed:    map[int64]bool{},
		gridInflight:      map[int64]bool{},
		paneStateStack:    map[string][]paneState{},
		urlPreview:        preview.NewCache(preview.NewJSDecoder()),
		urlStreams:        map[string]*urlView{},
		shellStreams:      map[string]*shellStreamConn{},
		shellAlive:        map[int64]bool{},
		shellAliveProbing: map[int64]bool{},
		urlPanX:           map[string]float64{},
		urlPanY:           map[string]float64{},
	}
	app.canvas = app.doc.Call("getElementById", "canvas")
	app.cctx = app.canvas.Call("getContext", "2d")
	app.tree = pane.NewTree()
	app.tree.FocusedPane().Zoom = 1.0
	app.resize()

	// Resize handler.
	app.win.Call("addEventListener", "resize", js.FuncOf(func(this js.Value, args []js.Value) any {
		app.resize()
		app.draw()
		return nil
	}))

	// beforeunload: close every URL stream cleanly so the server's
	// save-and-destroy path fires before the TCP connection dies.
	// Without this, the WS still drops via TCP FIN — but server-side
	// cleanup runs after a small delay and the user's final state
	// might miss the preview write.
	app.win.Call("addEventListener", "beforeunload", js.FuncOf(func(this js.Value, args []js.Value) any {
		app.closeAllURLStreams()
		app.closeAllShellStreams()
		return nil
	}))

	app.installCanvasInput()
	app.installWebviewListeners()

	go app.bootstrap()

	select {}
}

// bootstrap fetches the current root grid id from the server, then starts
// the rest of the client.
func (a *App) bootstrap() {
	resp, err := a.cl.Bootstrap(context.Background())
	if err != nil {
		return
	}
	a.rootGridID = resp.RootGridID
	a.rootViewCx = resp.RootViewCx
	a.rootViewCy = resp.RootViewCy
	a.rootViewZoom = resp.RootZoom
	a.afterBootstrap()
}

func (a *App) afterBootstrap() {
	a.canvas.Call("focus")
	p := a.tree.FocusedPane()
	p.Path = nil
	// Seed the root pane with the framing the server told us about.
	// URL boot may overwrite this in applyURLOnBoot if the URL pins
	// the focused pane to a specific spot.
	if a.rootViewZoom > 0 {
		p.Cx = a.rootViewCx
		p.Cy = a.rootViewCy
		p.Zoom = a.rootViewZoom
	}

	a.sched.rootViewSaveCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		a.sched.rootViewSaveScheduled = false
		a.flushRootViewSave()
		return nil
	})
	a.sched.urlUpdateCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		a.sched.urlUpdateScheduled = false
		a.replaceURLNow()
		return nil
	})

	// Subscribe to SSE.
	go a.startSSE()

	// Apply whatever the URL says (path / viewport / cursor). On a
	// fresh page load with `/`, this is a no-op aside from fetching
	// the user's root grid.
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

// fetchGrid issues GetGrid and stores the result in the cache. Failures are
// recorded so the renderer can surface them and we can avoid re-issuing the
// same request inside a render loop. In-flight requests for the same grid
// id are deduped: drawNodeWithPreview fires fetchGrid on every cache miss
// every frame, so without the guard a single descent into a parent of
// many wells would dogpile the server.
func (a *App) fetchGrid(id int64) {
	if a.gridInflight[id] {
		return
	}
	a.gridInflight[id] = true
	// Clear any stale failure flag before attempting — a new fetch either
	// succeeds (populates the cache) or fails (re-sets the flag). This
	// prevents a previously-failed grid from staying locked out when an
	// SSE GridChanged event fires and triggers a retry.
	delete(a.gridLoadFailed, id)
	go func() {
		defer delete(a.gridInflight, id)
		resp, err := a.cl.GetGrid(context.Background(), id)
		if err != nil {
			a.gridLoadFailed[id] = true
			a.draw()
			return
		}
		delete(a.gridLoadFailed, id)
		a.c.PutGrid(resp.Grid, resp.Tiles)
		a.draw()
	}()
}

// nowMs returns the current time in milliseconds since the epoch as
// reported by the browser. Used for animation timing.
func nowMs() float64 {
	return js.Global().Get("Date").Call("now").Float()
}

// scheduleFrame ensures a draw happens on the next animation frame. While
// dragging or animating, the frame loop continues until the state settles.
func (a *App) scheduleFrame() {
	if a.sched.rafScheduled {
		return
	}
	a.sched.rafScheduled = true
	js.Global().Call("requestAnimationFrame", js.FuncOf(func(this js.Value, args []js.Value) any {
		a.sched.rafScheduled = false
		a.frame()
		return nil
	}))
}

// frame is the per-tick driver: advance the active animation, request the
// next frame if motion is still in flight, and repaint.
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
	if a.transition != nil {
		seg := a.transition.segments[a.transition.currentSegment]
		t := anim.Progress(now, a.transition.segmentStartMs, seg.durationMs)
		eased := anim.EaseOutCubic(t)
		if p := a.tree.FindPane(a.transition.paneID); p != nil {
			p.Cx = anim.Lerp(seg.fromCx, seg.toCx, eased)
			p.Cy = anim.Lerp(seg.fromCy, seg.toCy, eased)
			p.Zoom = anim.LerpExp(seg.fromZoom, seg.toZoom, eased)
		}
		if t >= 1 {
			a.advanceTransition(now)
		} else {
			a.scheduleFrame()
		}
	}
	a.draw()
}

// startTransition installs the given transition and primes the first
// segment: the pane's path and viewport are set to the segment's start
// state and the per-frame loop is woken up.
func (a *App) startTransition(t *paneTransition) {
	a.transition = t
	a.applySegmentStart(t.segments[0])
	t.segmentStartMs = nowMs()
	a.scheduleFrame()
}

// applySegmentStart updates the pane's path and viewport to the segment's
// starting state. Called when a segment begins (including the very first).
func (a *App) applySegmentStart(seg transSegment) {
	p := a.tree.FindPane(a.transition.paneID)
	if p == nil {
		return
	}
	p.Path = seg.path
	p.Cx = seg.fromCx
	p.Cy = seg.fromCy
	p.Zoom = seg.fromZoom
}

// advanceTransition is called when the current segment finishes. If more
// segments remain, install the next one's start state and continue;
// otherwise the transition is complete.
func (a *App) advanceTransition(now float64) {
	a.transition.currentSegment++
	if a.transition.currentSegment >= len(a.transition.segments) {
		a.completeTransition()
		return
	}
	a.applySegmentStart(a.transition.segments[a.transition.currentSegment])
	a.transition.segmentStartMs = now
	a.scheduleFrame()
}

// completeTransition tears down the active transition once the last
// segment has finished. Pane state has already been set by the segment
// machinery; here we just refresh ancillary state and persist.
func (a *App) completeTransition() {
	tr := a.transition
	a.transition = nil
	if tr == nil {
		return
	}
	p := a.tree.FindPane(tr.paneID)
	if p == nil {
		return
	}
	delete(a.selectedTileID, p.ID)
	a.gridLoadFailed = map[int64]bool{}
	a.fetchGrid(a.gridIDForPath(p.Path))
	if tr.onComplete != nil {
		tr.onComplete()
	}
	a.scheduleURLUpdate()
	a.draw()
}

// animationDone is called when the active animation reaches its target. It
// clears the ghost and any associated render hides so the cache becomes
// the source of truth again.
func (a *App) animationDone() {
	a.animation = nil
	a.ghost = nil
	a.hiddenTileID = 0
	a.hiddenPaneID = ""
}

// pushPaneState saves the parent viewport on the stack for paneID. Called at
// the start of descent so the matching ascent can animate the parent back to
// exactly this position.
func (a *App) pushPaneState(paneID string, s paneState) {
	a.paneStateStack[paneID] = append(a.paneStateStack[paneID], s)
}

// popPaneState removes and returns the most recent saved parent viewport for
// paneID, or nil if the stack is empty (e.g. user reloaded mid-descent).
func (a *App) popPaneState(paneID string) *paneState {
	stack := a.paneStateStack[paneID]
	if len(stack) == 0 {
		return nil
	}
	last := stack[len(stack)-1]
	a.paneStateStack[paneID] = stack[:len(stack)-1]
	return &last
}

// startSSE opens the Connect-streaming Subscribe RPC and applies each
// inbound event to the local cache. Reconnects after a brief backoff on
// stream termination so a transient server hiccup doesn't leave the UI
// stale.
func (a *App) startSSE() {
	for {
		stream, err := a.cl.Subscribe(context.Background())
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		for {
			ev, ok, err := stream.Recv()
			if err != nil || !ok {
				break
			}
			if a.c.Apply(ev) {
				a.draw()
			}
			// A removed tile's decoded preview image (and its backing object
			// URL) must be released, or deleting URL/shell tiles leaks browser
			// image resources for the life of the page.
			if ev.Kind == rpc.EventTileRemoved && ev.TileRemoved != nil {
				a.urlPreview.Drop(ev.TileRemoved.TileID)
			}
			// GridChanged: refetch the affected grid if any pane is looking at it.
			if ev.Kind == rpc.EventGridChanged && ev.GridChanged != nil {
				a.fetchGrid(ev.GridChanged.GridID)
			}
		}
		stream.Close()
		time.Sleep(500 * time.Millisecond)
	}
}

// Session-local state: the full UI state — split layout, per-pane
// navigation, viewport — rebuilds from the URL on reload. The URL
// captures only the focused pane's path and viewport; the split tree
// starts fresh. paneStateStack survives within the session so ascent
// restores the exact pre-descent parent viewport; it is not persisted
// across reloads. Text tile mode (rendered/text) is persisted on the
// tile row, so it survives reload.

// gridIDForPath walks the pane's descent path and returns the grid id at the
// leaf. Returns root if the path is empty or stale prefixes don't resolve.
func (a *App) gridIDForPath(p []int64) int64 {
	if a.rootGridID == 0 {
		return 0
	}
	gid := a.rootGridID
	for _, wellID := range p {
		g, ok := a.c.Grid(gid)
		if !ok {
			a.fetchGrid(gid)
			return gid
		}
		w, ok := g.Tiles[wellID]
		if !ok {
			return gid
		}
		gid = w.ChildGridID
	}
	return gid
}

// refetchGridOnConflict logs a 409 version-conflict and refetches the
// affected grid so the cache catches up to the server's authoritative
// version.
func (a *App) refetchGridOnConflict(gridID int64, where string) {
	js.Global().Get("console").Call("warn", "gridwell: version conflict on "+where+", refetching grid")
	a.fetchGrid(gridID)
}
