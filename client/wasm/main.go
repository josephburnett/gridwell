//go:build js && wasm

// Package main is the WASM entry point for the Gridwell client.
//
// This file is intentionally a thin wiring shim: anything testable lives in
// client/pane, client/markdown, client/dragdrop, client/cache. The code here
// reaches into syscall/js and is exercised manually in a browser.
package main

import (
	"context"
	"fmt"
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
	// zoomMin is the grid zoom floor: one value for every zoom gesture,
	// since wheel and pinch both land in zoomtrans.WheelZoom with it, and
	// for every client, desktop and browser alike. 1/32 puts cells at 2px:
	// below the 4px line where drawGridLinesIn stops painting (render.go)
	// the deep end is a line-less overview — tiles stay visible as color
	// blocks, which is the point of the altitude.
	zoomMin    = 0.03125
	zoomMax    = 8.0
	zoomFactor = 1.1

	// wellZoomRatio* clamp the hover-wheel well zoom in the intrinsic
	// ratio's units: previewCell = parentCell × ratio, and the unvisited
	// default is 1/PreviewFactor = 0.125. The min keeps the preview above
	// the renderer's 0.5px visibility floor at ordinary cell sizes; the max
	// of 1.0 renders child cells at full parent-cell size.
	wellZoomRatioMin = 1.0 / 64.0
	wellZoomRatioMax = 1.0

	// textFixedScale is the constant render scale for text content, both
	// descended and previewed. The preview renders at this same constant
	// scale times the tile's content_zoom, wrapped to the tile, so grid zoom
	// reveals more lines instead of magnifying the type. The descended pane
	// is a plain window onto the document at this scale, scrolled
	// vertically.
	textFixedScale = 1.0
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

	// plugins is the plugin list from the Handshake, used for health tints,
	// plugin glyphs, and the e2e launcher hook. Order is config order.
	plugins []rpc.PluginInfo

	// home is the qualified grid id "/" means: the home grid the handshake
	// names (rpc.HomeGrid, the one derivation). Every "empty anchor means
	// home" reader — boot, URL decode and encode, pane-tile leaf restore —
	// reads this.
	home string

	tree *pane.Tree
	c    *cache.Cache

	// persist is what executes a write on its way out: the debounce
	// schedulers, the per-tile save queue, the well-wheel settle state, the
	// outbox, and the two e2e counters that instrument them. See
	// persistState.
	persist persistState

	width, height float64

	dragging *dragState

	// locals is the per-pane session-local client state, one entry per live
	// pane, keyed by pane id: the selection and the live URL and shell
	// handles. The pane's place lives on the pane itself (client/pane,
	// place.go). Created on demand by a.local and removed atomically on pane
	// drop by a.forgetPane. See paneLocal and client/panestate.
	locals map[string]*paneLocal

	// The + creation-menu state. menu is the single owner of open or closed,
	// which pane, and the hovered item. Never assign menu fields directly;
	// go through its methods (client/menu). Persistence across a descent
	// rides on the place frame you left, Frame.MenuOpen.
	menu menu.State

	// errs is the single owner of user-visible failure notices. Every
	// failure path reports through a.reportErr or a.resolveErr; only the
	// notice-strip render and its click handler read it. Error state lives
	// nowhere else.
	errs *errsurface.Surface

	// paneLayouts memoizes each pane tile's decoded tree, keyed by tile id
	// and invalidated by blob generation (see paneTileLayout). It is a cache
	// and a view of the server blob, never an authority: the decode is what
	// is memoized, and the truth is the tile row plus its content bytes.
	paneLayouts map[string]*paneLayoutEntry

	// ws is the window's level stack: the one owner of which pane tile the
	// user is inside and what outer tree each descent restores. The rules
	// are client/pane's Levels. a.tree is the display of its top, and the
	// persisted layout is owned by the server blob.
	ws pane.Levels

	// caps is the host capability set (client/caps), derived once at boot
	// from bridge presence. Feature gates read a.caps; nothing else asks the
	// bridge to make a behavior decision.
	caps caps.Caps

	// origin is the serving origin (location.origin), captured once at boot:
	// the base every derived address builds on. contentToken is the
	// /content/ door's path capability from the handshake, so
	// rpc.PageURL(origin, contentToken, tileID) is a serves_page tile's
	// address, derived at use time and never persisted, because the desktop
	// origin is an ephemeral port. webAddress is the one reader.
	origin       string
	contentToken string

	// unloading marks the beforeunload flush: framing writes switch to
	// navigator.sendBeacon so they survive the dying page (unload.go).
	unloading bool

	// touch is the touch→mouse gesture classifier (client/touchgest);
	// touchTimerCb is its retained long-press timer callback;
	// touchDownTarget is the element the current gesture started on, where
	// synthetic MouseDowns route, mirroring browser hit-testing for a real
	// mouse. Owned by touch.go; nothing else feeds or reads them.
	touch           *touchgest.Machine
	touchTimerCb    js.Func
	touchDownTarget js.Value

	// fetch is the one owner of "a read is outstanding or has failed": the
	// three inflight claim sets and the two verdict latches. See fetchState.
	fetch fetchState

	// ghost is the in-flight visual representation of a node being dragged
	// or animated to/from somewhere. The dragged node renders here at
	// sub-cell screen precision instead of at its stored cell position.
	ghost *ghost

	// animation is the current ghost animation, if any (snap-to-target on
	// drop or snap-back-to-origin on failure).
	animation *anim.Animation

	// trans holds every pane's descent/ascent zoom animation, at most one per
	// pane (client/transition). Two panes animate independently, and a
	// transition that is displaced or cleared lands on its destination rather
	// than vanishing, so a descent is never voided after visibly animating.
	trans *transition.Set

	// nav is the navigation state machine (client/nav): every descent and
	// ascent decision, as data. This file's half is resolution and execution
	// only — nav.go gathers the world it plans against and nav_exec.go runs
	// the effects it returns.
	nav *nav.Machine

	// rightDrag is the in-flight right-button gesture, if any. Right
	// button is dedicated to pane management — split, resize, close,
	// swap. See right_button.go.
	rightDrag *rightDragState

	// leftResize is the in-flight left-button pane-boundary resize, if any.
	// The drag itself clamps to the pane minimum and never closes a pane;
	// the release decides a crush-through close.
	leftResize *leftResizeState

	// urlPreview caches decoded HTMLImageElement values for URL and
	// shell tile previews. Keyed by tile id; auto-invalidates when a
	// tile's PreviewBlobID changes server-side — see client/preview.
	urlPreview *preview.Cache

	// wrapCache memoizes raw-text soft-wrap results per (content id,
	// version, length, columns) — a render cache (memoWrap), reset
	// wholesale when full, never a fact.
	wrapCache map[string][]string

	// shellAlive caches the result of the ShellSessionAlive probe per tile
	// id. The refresh button shows when preview_blob_id is 0, or when
	// shellAlive[id] is true. shellAliveProbing single-flights the in-flight
	// probe per tile: an entry holds the callbacks waiting on the answer, so
	// a rapid sequence of redraws does not fan out into many RPCs and no
	// caller's callback is dropped. A missing shellAlive key means unknown —
	// the renderer kicks off a probe and hides the button until the result
	// lands.
	shellAlive        map[string]bool
	shellAliveProbing map[string][]func(alive bool)

	// shells is the per-pane registry of live PTY attachments
	// (client/shellstream over client/shellws — the /shell WebSocket on
	// this page's own origin). It owns the lifecycle rules; this file only
	// hands it a dialer and the two callbacks.
	shells *shellstream.Registry

	// traces holds the per-pane ascent-trace highlight: the fading "you just
	// came from here" outline. Armed by completeTransition when the finished
	// transition was an ascent, and pruned by frame() as each fade runs out.
	// Ephemeral view state, like the selection.
	traces map[string]traceState

	// overlays holds the DOM singletons that sit over the canvas and the
	// "is this one showing" flags that go with them. See overlayState.
	overlays overlayState

	// renderedPrev caches rasterized rendered-mode grid previews by tile id
	// (rendered_preview.go): SVG foreignObject images of
	// markdown.RenderHTML's output, decoded async and drawn by
	// drawMarkdownNode when a tile's stored text_mode is "rendered".
	renderedPrev map[string]*renderedPreview

	// zoomKeyRelays counts zoom chords that arrived over the main-process
	// relay, from its before-input-event interception. e2e-only
	// introspection: together with the registry's zoomChordRelays counter it
	// brackets the IPC hop, so a chord lost between interception and the
	// wasm zoom owner is attributable instead of a silent no-op.
	zoomKeyRelays int

	// tileMutates counts the tile mutations in flight — a Create* or a resize
	// whose row does not exist yet, so the descent, visit, or placement that
	// follows it has not happened. postTileMutate owns it; the e2e idle
	// signal reads it, so a spec can tell "the gesture finished" from "the
	// gesture is still waiting on the server".
	tileMutates int

	// menuCtxs caches each remote node's + menu (menuctx.go), keyed by
	// the grid-stamped node_ns. "" (the local node) is a.plugins/a.caps.
	menuCtxs map[string]*menuContext

	// renderedPanePaints counts rendered-raster paints of a descended pane by
	// tile id (markdown_render.go): e2e attribution that an unfocused
	// rendered pane paints the raster, never raw. Exposed by the
	// renderedPreviews testhook.
	renderedPanePaints map[string]int
}

// overlayState holds the DOM singletons layered over the canvas — the text
// textarea, the rendered-HTML view, the rendered/raw toggle — their retained
// listeners, and the flags saying which one is showing and what it holds.
// Each element is created lazily by its own file (text_overlay.go,
// rendered_overlay.go) and reused for the life of the page, so a descent
// never allocates fresh DOM. Nothing here is a fact the user can change; it
// is what the page is currently displaying.
type overlayState struct {
	// textTextarea is the lazily-created <textarea> element used for
	// markdown text-mode editing. It is positioned over the focused pane
	// when the pane's place is a content frame in TextMode "text", and hidden
	// otherwise. We hold it as a single shared element to avoid creating
	// fresh DOM nodes on every descent.
	textTextarea js.Value
	// textTextareaInputCb is the input event listener that mirrors the
	// textarea's value into a per-frame redraw. Held so we can release
	// it cleanly if the App is torn down (currently never).
	textTextareaInputCb  js.Func
	textTextareaScrollCb js.Func

	// renameEditing marks the shared inline rename input as open (the bar's
	// current crumb hides its name text underneath it — bottombar.go).
	renameEditing bool

	// renderedView is the singleton read-only rendered-HTML overlay div
	// (rendered_overlay.go). renderedReady mirrors textareaReady for
	// rendered mode: the canvas paints raw source until the overlay holds
	// content. lastRenderedKey caches the render (tile, version, org-ness)
	// so scrolling never re-renders.
	renderedView    js.Value
	renderedReady   bool
	lastRenderedKey string

	// wsExpand is the in-flight first-descent capture animation
	// (workspace.go); nil when none.
	wsExpand *wsExpandState

	// textToggleBtn is the floating rendered/raw toggle for a markdown
	// descent. A DOM element (not a canvas button) so it can sit above
	// the textarea overlay — letting the text content fill the pane
	// edge-to-edge instead of reserving a strip for a canvas button.
	textToggleBtn js.Value
	textToggleCb  js.Func

	// urlModalOpen tracks whether the URL-entry modal is currently open.
	// A second openURLModal call while this is true is a no-op.
	urlModalOpen bool

	// lastTextareaTileID tracks which text-tile id the singleton
	// textarea is currently bound to (i.e., whose blob it holds in its
	// value). "" means "bound to nothing" (textarea is hidden or never
	// seeded). refreshFileOverlay uses this to decide whether to re-seed
	// from the cached blob on a focus shift: same tile → preserve typing,
	// different tile → fresh content.
	lastTextareaTileID string

	// textareaReady tracks whether the single textarea holds the focused
	// tile's content, as against being empty from a recent pane switch or a
	// pending blob fetch. Set true when refreshFileOverlay seeds the
	// textarea with actual content, or when the user types; set false when
	// the textarea is cleared on a tile switch or mode toggle.
	// drawMarkdownInPane reads it through textedit.CanvasHiddenByOverlay, so
	// the canvas keeps painting during the loading race instead of going
	// blank.
	textareaReady bool
}

// fetchState owns whether a read is outstanding or has failed: the three
// dedupe claim sets and the two failure latches that go with them. Nothing
// else in the client answers "is this already being fetched?" or "did this
// last fail?", so a reach from an unrelated file reads as a.fetch.… Every
// claim's life is client/inflight's; this struct only holds the sets.
type fetchState struct {
	// gridLoadFailed records grids whose last GetGrid failed (loadGrid is
	// the one writer), so the renderer can say so and the URL walk does
	// not retry in a tight loop.
	gridLoadFailed map[string]bool

	// gridFetch holds the grid ids with a pending GetGrid so repeated draws
	// (which call fetchGrid on every cache miss) don't dogpile the server.
	// client/inflight owns the claim's whole life: bounded, cancellable, and
	// released by the fetch that made it — a request lost with the link used
	// to hold its id forever, which is "loading …" with no error and no
	// retry.
	gridFetch *inflight.Set

	// contentFetch holds the tile ids with a pending ReadContent, deduped
	// like gridFetch: tileBody fires fetchTileContent on every cache miss
	// every frame, so without the claim one absent body spawns a fetch per
	// frame, and any reply older than one that already landed would repaint
	// stale bytes into the overlay. The cache's PutFetchedContent version
	// guard is the backstop.
	contentFetch *inflight.Set

	// tileFetch holds the qualified tile ids with a pending GetTile
	// (findTileByID misses). A globally-routable id may name a tile whose
	// grid was never visited (so it isn't cached); resolving it locates the
	// tile's grid and fetches it. Deduped like gridFetch.
	tileFetch *inflight.Set
	// tileLoadFailed records tile ids whose GetTile failed (the tile was
	// deleted, or its plugin isn't mounted). Without it a missing id
	// re-fires GetTile every frame forever — the same dogpile
	// gridLoadFailed prevents for grids. Cleared only by a reload.
	tileLoadFailed map[string]bool
}

// newFetchState builds the fetch group — the one place it is constructed.
func newFetchState() fetchState {
	return fetchState{
		gridLoadFailed: map[string]bool{},
		gridFetch:      inflight.New(inflight.Deadline),
		contentFetch:   inflight.New(inflight.Deadline),
		tileFetch:      inflight.New(inflight.Deadline),
		tileLoadFailed: map[string]bool{},
	}
}

// debounce is one coalescing deferred callback: arm() starts a setTimeout
// unless a run is already pending, and the retained js.Func — allocated once,
// by set() — clears the pending guard before running the body. Repeated arms
// inside the window collapse into a single run, and re-arming never leaks a
// handle.
type debounce struct {
	// pending guards a timer already in flight.
	pending bool
	// cb is the retained callback; set() allocates it once for the life of
	// the page.
	cb js.Func
}

// set installs the debounce's body. Called once, at startup.
func (d *debounce) set(body func()) {
	d.cb = js.FuncOf(func(this js.Value, args []js.Value) any {
		d.pending = false
		body()
		return nil
	})
}

// arm schedules the body ms milliseconds out, unless a run is already
// pending. Caller-side conditions (is there anything to save? is there a
// deadline?) stay at the call site; arm() only coalesces.
func (d *debounce) arm(ms int) {
	if d.pending {
		return
	}
	d.pending = true
	js.Global().Call("setTimeout", d.cb, ms)
}

// persistState is the write-out side of the client: the debounce schedulers
// that decide when a settled change is posted, the per-tile save queue, the
// hover-wheel drift waiting on its settle, the outbox of writes the server
// has not acknowledged, and the two counters that let a spec see which stage
// of the chain went quiet. The navigation machine emits the Flush* effects;
// this group is what executes them.
type persistState struct {
	// sched holds the debounce / requestAnimationFrame bookkeeping: each
	// debounce guards a pending callback so repeated triggers coalesce into
	// one, and its retained js.Func is allocated once so re-scheduling never
	// leaks handles. See scheduler below.
	sched scheduler

	// textSaves serializes content writes per tile so pipelined saves chain
	// versions instead of racing; see enqueueTextSave.
	textSaves *textedit.SaveQueue

	// wellWheelPending holds well tiles whose preview framing the hover wheel
	// changed but has not persisted yet: tile id to the grid the tile sits
	// in. The cache is patched per notch, since the renderer reads it live,
	// and the settle persister's flush posts one SetFraming per tile from
	// the cached row, so a scroll burst is one write.
	wellWheelPending map[string]wellWheelDrift

	// persistPosts counts optimistic-persist dispatches by label
	// ("SetFraming" and the rest) and framingFlushes counts settle-persister
	// flush passes. e2e-only introspection (the persistPosts testhook): the
	// settle chain — gesture, debounce, flush, post — is otherwise silent at
	// every stage, so a spec waiting on its effect could not say which stage
	// went quiet.
	persistPosts   map[string]int
	framingFlushes int

	// out is the one record of writes the server has not acknowledged —
	// framing, captures, layout, and the user's unsaved bytes alike — in the
	// order they were made. retryKick and the unload flush are its two
	// drains. See client/outbox.
	out *outbox.Outbox
}

// newPersistState builds the persist group — the one place it is
// constructed. The debounces get their bodies at boot (afterBootstrap),
// since those close over the App.
func newPersistState() persistState {
	return persistState{
		textSaves:        textedit.NewSaveQueue(),
		wellWheelPending: map[string]wellWheelDrift{},
		persistPosts:     map[string]int{},
		out:              outbox.New(),
	}
}

// scheduler holds the debounce / requestAnimationFrame bookkeeping.
type scheduler struct {
	// rafScheduled tracks a pending requestAnimationFrame so we don't
	// queue redundant frames.
	rafScheduled bool

	// wsSave debounces the pane-layout persister (see
	// scheduleWorkspaceSave): draw() arms it while inside a pane tile, and
	// the callback encodes, hash-diffs, and posts the layout WriteContent on
	// a change.
	wsSave debounce

	// urlUpdate debounces the URL replaceState: multiple state changes
	// within the window coalesce into one.
	urlUpdate debounce

	// framingSave debounces the grid-framing persister (see
	// scheduleFramingSave): draw() arms it; the callback flushes every
	// pane's settled framing through the no-op-guarded writers.
	framingSave debounce

	// textSave debounces the text-tile content save.
	textSave debounce

	// errExpire arms one timer for the error surface's soonest expiry
	// deadline, so stale one-shot notices leave the strip without polling.
	// See scheduleErrExpiry.
	errExpire debounce
}

// wellWheelDrift is one well's in-flight hover-wheel state, the one owner of
// the not-yet-persisted view: the grid to persist under, the float view
// center accumulated across the burst, and the ratio and version the flush
// posts from. The center is float all the way to the store, so nothing rounds
// the cursor-anchored drift away. The flush never re-reads the cache row: any
// refetch inside the settle window — a conflict resync, an arriving event —
// replaces the patch with server values, and a cache-reading flush would
// faithfully revert the wheel.
type wellWheelDrift struct {
	gridID  string
	cx, cy  float64
	ratio   float64
	version int64
}

// paneLocal is the single owner of one pane's session-local client state: the
// plain-data part (panestate.State, embedded, holding the selection) plus the
// native live URL and shell handles. One per live pane in App.locals, created
// on demand by App.local and removed atomically when the pane is dropped
// (App.forgetPane), so none of this state can outlive its pane or be orphaned
// from it.
type paneLocal struct {
	panestate.State
	// urlView is the live native WebContentsView handle when this pane is
	// descended into a live URL tile; nil otherwise. Closed via closeURLStream.
	urlView *urlView
	// shellConn is the live shell session — the /shell WebSocket plus the
	// xterm.js overlay — when this pane is descended into a live shell tile;
	// nil otherwise. Closed through closeShellStream and
	// releaseShellStream.
	shellConn *shellStreamConn
}

// shellConnFor returns paneID's live shell session, or nil when the pane has no
// live shell descent.
func (a *App) shellConnFor(paneID string) *shellStreamConn {
	if pl, ok := a.localIf(paneID); ok {
		return pl.shellConn
	}
	return nil
}

// urlViewFor returns paneID's live URL view handle, or nil when the pane has no
// live URL descent. The liveness check used throughout the input/render paths.
func (a *App) urlViewFor(paneID string) *urlView {
	if pl, ok := a.localIf(paneID); ok {
		return pl.urlView
	}
	return nil
}

// local returns the per-pane state for paneID, creating an empty one on first
// use. Use for any access that may also write; reads that must not materialize
// state use localIf.
func (a *App) local(paneID string) *paneLocal {
	pl := a.locals[paneID]
	if pl == nil {
		pl = &paneLocal{State: panestate.New()}
		a.locals[paneID] = pl
	}
	return pl
}

// localIf returns the per-pane state for paneID only if it already exists.
func (a *App) localIf(paneID string) (*paneLocal, bool) {
	pl, ok := a.locals[paneID]
	return pl, ok
}

// forgetPane tears down and removes all per-pane state for a pane that is
// going away, collapsed or closed: it freezes and closes any live URL or
// shell session, then deletes the pane's entry from a.locals, so no per-pane
// state outlives its pane. The single atomic cleanup point on pane drop.
func (a *App) forgetPane(paneID string) {
	a.closeURLStream(paneID, true)
	a.closeShellStream(paneID, true)
	delete(a.locals, paneID)
	// Level-scoped pane ids recur across successive descents — "w1:p1"
	// again on the next one — so every pane-keyed map clears here, or a
	// stale entry would greet the next level's pane of the same id.
	delete(a.traces, paneID)
	// A pane that is going away is the one case a transition is dropped
	// rather than landed: there is no pane left to install a place on or to
	// re-engage. Every other clearing goes through Cancel, which lands.
	a.trans.Drop(paneID)
	// And with it every navigation continuation that was waiting on this
	// pane: the drop means no landing will ever retire them.
	a.nav.Forget(paneID)
}

// selectedFor returns the selected tile id in paneID, or "" if nothing is
// selected (or the pane has no state yet) — a read that never materializes state.
func (a *App) selectedFor(paneID string) string {
	if pl, ok := a.localIf(paneID); ok {
		return pl.Selected
	}
	return ""
}

// clearSelected drops the selection in paneID, if any (no-op when the pane has
// no state).
func (a *App) clearSelected(paneID string) {
	if pl, ok := a.localIf(paneID); ok {
		pl.Selected = ""
	}
}

// The transition itself — its shape, its per-pane bookkeeping, and what
// displacing one means — lives in client/transition. Descent uses two
// segments: one for the parent zoom-in, then a zero-duration "install"
// segment that lands on the calibrated child state. Ascent uses two: a child
// zoom-out to the calibrated state, then a parent zoom-out from "well
// overtakes" back to the saved state. The visual continuity at each boundary
// comes from the calibrated start/end states zoomtrans hands back.

// traceState is one armed ascent-trace highlight: the tile to outline in the
// pane's grid view and the fade clock's start. Held per pane in App.traces.
type traceState struct {
	tileID  string
	startMs float64
}

// traceDurMs is how long the ascent-trace outline takes to fade out.
const traceDurMs = 2000.0

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

	// hiddenTileID and hiddenPaneID suppress the source tile's normal render
	// while this ghost represents it in a move drag; a clone does not hide,
	// because the source stays. They live on the ghost because their
	// lifetime is the ghost's: the tile stays hidden through the snap-back
	// animation after a.dragging is already nil, and reappears exactly when
	// the ghost dies.
	hiddenTileID string
	hiddenPaneID string

	// fragmentation animates the "going into a black hole" effect.
	// 0 = intact, 1 = fully fragmented (shards drifted outward,
	// alpha cut, slight rotation). Lerps the same way cell size
	// does, so dragging in and back out smoothly reassembles.
	displayedFragmentation float64
	targetFragmentation    float64

	// forbidden is set when the cursor is over a drop target that would be
	// rejected: a same-namespace cross-grid move with a source-backed
	// endpoint, or a solid well right-dragged across a namespace, whose deep
	// copy the server refuses. The renderer paints the "no entry" badge over
	// the ghost, and mouseup snaps back without an RPC.
	forbidden bool

	// link is set while a left-drag hovers a target in a different id
	// namespace: the drop creates a link and the source stays put, because
	// there is no cross-plugin move. The renderer paints the ghost dashed
	// with the chain badge so the user learns the meaning mid-drag; without
	// it the source's survival after the drop would read as a surprise
	// duplicate.
	link bool
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
	// menuNS is the node whose menu offered a template item: primitives
	// create on that node only.
	menuNS       string
	originPaneID string
	// originFocused records whether the origin pane was already focused when
	// the press landed. A bare click on an unfocused pane is focus-only: it
	// must not also navigate or select, the same rule the bar slot follows.
	// Without it, a click meant to focus a pane descends whenever it happens
	// to hit a tile.
	originFocused bool
	// splitNav records ctrl held at left-press time: a bare click then asks
	// for its descent in a new split pane (dragdrop.DropNavigateSplit).
	// Fixed at press like every drag fact; a started drag ignores it.
	splitNav     bool
	tileID       string
	cellOffsetX  float64
	cellOffsetY  float64
	startScreenX float64
	startScreenY float64
	curScreenX   float64
	curScreenY   float64
	started      bool
	// intent is what this drag means to leave at the destination, fixed by
	// the press that armed it: move for a left-drag, copy for a right-drag,
	// link for ctrl + right-drag (armRightClone). It is the ONE owner of that
	// fact — the ghost preview and the commit both read it from here, so
	// neither can decide on a flavor the other did not. A creating drag
	// (dragdrop.Intent.Creates) commits only through the right-button release
	// path; the left-button move-commit refuses it, so a stray non-right
	// release cannot silently turn a copy or a link into a move.
	intent        dragdrop.Intent
	snapshotTile  rpc.Tile
	originScreenX float64
	originScreenY float64

	// Palette drag from the + menu: tileID is "" (no real node yet) but
	// isTemplate is true and item carries the grabbed palette entry — a
	// tile primitive (drop creates it) or a plugin (drop mounts it as an
	// exit-well link; a click with no drag enters the plugin instead).
	isTemplate bool
	item       paletteItem

	// Source-grid info — set at mousedown; same as the focused pane's
	// grid for parent-grid drags, or the well's child grid for "pull
	// out of well" drags. Carried separately so the drop commit names
	// the right source grid even when source and dest are different
	// grids inside the same pane.
	srcGridID   string
	srcCellSize float64
}

// dragThreshold is the cursor-movement distance (CSS px) that turns a press into
// a drag. Below this, mousedown→mouseup is treated as a click (select).
//
// This is the single owner of the drag threshold. The native layer keeps two
// forced copies — RIGHT_DRAG_THRESHOLD in apps/desktop/src/main/viewutil.ts,
// and an inlined copy in src/preload/urlview-preload.ts, because a sandboxed
// preload may not require modules — so a live URL view tells a right-drag
// from a right-click exactly as the canvas does. gesture-threshold.test.ts
// fails the build if either copy drifts from this value.
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
		urlPreview:         preview.NewCache(preview.NewJSDecoder()),
		wrapCache:          map[string][]string{},
		shellAlive:         map[string]bool{},
		shellAliveProbing:  map[string][]func(bool){},
		traces:             map[string]traceState{},
		paneLayouts:        map[string]*paneLayoutEntry{},
		renderedPrev:       map[string]*renderedPreview{},
		menuCtxs:           map[string]*menuContext{},
		renderedPanePaints: map[string]int{},
	}
	app.trans = transition.New(app.enterSegment, app.landTransition)
	app.nav = nav.New()
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

	// Mobile browsers resize the visual viewport (on-screen keyboard,
	// collapsing URL bar) without firing a layout resize, leaving the canvas
	// and the text-overlay geometry stale — the textarea can end up under
	// the keyboard. Re-run the same resize path on visualViewport changes;
	// desktop browsers fire both events for real resizes and resize() is
	// idempotent, so double-firing is harmless.
	if vv := app.win.Get("visualViewport"); vv.Truthy() {
		vvCb := js.FuncOf(func(this js.Value, args []js.Value) any {
			app.resize()
			app.draw()
			return nil
		})
		vv.Call("addEventListener", "resize", vvCb)
	}

	// beforeunload: close every URL stream cleanly so the server's
	// save-and-destroy path fires before the connection dies. Without it the
	// socket still drops, but server-side cleanup runs after a delay and the
	// user's final state can miss the preview write.
	app.win.Call("addEventListener", "beforeunload", js.FuncOf(func(this js.Value, args []js.Value) any {
		// Everything durable rides beacons (unload.go) so it survives the
		// dying page: framing in its settle window, dirty text through the
		// streaming-envelope beacon, and a live page's navigation state. An
		// animating transition lands on its destination first.
		app.flushOnUnload()
		app.closeAllURLStreams()
		app.closeAllShellStreams()
		return nil
	}))

	// popstate: the browser's back and forward traverse descents and
	// ascents, since writeURLNow pushes an entry per structural navigation.
	// The restore runs HERE, in the callback: it suspends on its reads rather
	// than blocking, and planning it marks the URL the restore's, so a
	// pending debounced write firing after this callback returns finds the
	// writer suppressed instead of clobbering the entry the browser just
	// navigated to.
	app.win.Call("addEventListener", "popstate", js.FuncOf(func(this js.Value, args []js.Value) any {
		app.runGesture(nav.Gesture{Kind: nav.GestureRestoreFromHistory, Raw: locationPath()})
		return nil
	}))

	// The shell transport: PTY bytes ride the /shell WebSocket on this
	// page's own origin, authenticated by the cookie that served the page.
	// The registry owns replace-on-open, exactly-once exit, and
	// no-op-after-close; the terminal glue below only reads and writes
	// bytes.
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
// landing page is home — the grid the handshake names (rpc.HomeGrid) — so
// panes anchor there, and plugins are reached from the + menu.
func (a *App) bootstrap() {
	// The handshake retries until it lands. Firing it exactly once would
	// leave one blip at boot as a permanently empty shell — no plugins, no
	// home, no content token — until a manual reload. It runs on its own
	// goroutine before anything renders content, so backing off blocks
	// nothing but the empty landing page, which carries the notice
	// explaining itself.
	backoff := time.Second
	var plugins rpc.PluginList
	for {
		var err error
		plugins, err = a.cl.Handshake(context.Background())
		if err == nil {
			a.resolveErr("rpc:Handshake")
			break
		}
		// The landing page renders empty meanwhile: say why, or it reads as
		// "all my plugins vanished".
		a.reportErr(errsurface.Error, "rpc:Handshake", "plugin list failed — retrying: "+rpcErrText(err))
		a.draw()
		time.Sleep(backoff)
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
	a.plugins = rpc.MenuRows(plugins)
	// Fold the node's shells_disabled fact into the capability set, still at
	// boot time — nothing has rendered or accepted input yet — and immutable
	// afterward. caps stays the one owner of what this client can do;
	// nothing else reads the handshake flag.
	a.caps = caps.Derive(bridgeCaps(), plugins.ShellsDisabled)
	// The /content/ door capability rides the same handshake; boot-time,
	// immutable, read only by webAddress.
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

	// Subscribe to the event stream.
	go a.startSSE()

	// The slow retry net behind the reconnect kick.
	go a.retryBackstop()

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

// loadGrid is the one GetGrid-to-cache hop: one call, one failure flag, one
// error key ("grid:<id>") surfaced and resolved through the strip. The async
// renderer path (fetchGrid) and the restore walk's awaited read
// (RequestGetGrid) both come here, so gridLoadFailed has one writer.
func (a *App) loadGrid(ctx context.Context, id string) error {
	resp, err := a.cl.GetGrid(ctx, id)
	if err != nil {
		// A verdict latches: the server answered, and the same ask gets the
		// same answer until something changes, so fetchGrid stops asking
		// until a path that justifies a retry clears the latch — GridChanged
		// for this grid, a reconnect resync, a navigation. Without the latch
		// a dangling doorway (a link into an unconfigured plugin) is
		// refetched and re-reported every frame the well is on screen. A
		// transport failure latches nothing — the server never spoke and the
		// next caller retries — the same rule fetchTileByID applies.
		if clientsync.Of(err) != clientsync.OutcomeTransport {
			a.fetch.gridLoadFailed[id] = true
		}
		a.reportErr(errsurface.Error, "grid:"+id, "grid unavailable: "+rpcErrText(err))
		return err
	}
	a.resolveErr("grid:" + id)
	delete(a.fetch.gridLoadFailed, id)
	if resp.Grid.ID != id {
		// The cache keys by the answered name and every frame resolves by the
		// asked one, so a server that answers under a different id strands the
		// pane on "loading" with nothing but 200s on the wire — for days, once.
		// Say so instead, and latch: re-asking gets the same unusable answer,
		// which is what a verdict is. The answer still lands under its own
		// name; this report is the difference between a visible contract break
		// and silence.
		a.fetch.gridLoadFailed[id] = true
		a.reportErr(errsurface.Error, "grid:"+id,
			"asked for grid "+id+", was answered "+resp.Grid.ID+" — the view of "+id+" cannot load")
	}
	a.c.PutGrid(resp.Grid, resp.Tiles)
	return nil
}

// fetchGrid loads a grid in the background. In-flight requests for the
// same grid id are deduped: drawNodeWithPreview fires fetchGrid on every
// cache miss every frame, so without the guard a single descent into a
// parent of many wells would dogpile the server.
func (a *App) fetchGrid(id string) {
	// A latched grid is not re-asked: the paths that justify a retry — the
	// GridChanged handler, retryKick's resync, completeTransition — clear
	// the latch first. fetchGrid clearing it itself defeated the latch for
	// the per-frame draw path and turned one honest verdict into an
	// every-frame refetch-and-report loop.
	if id == "" || a.fetch.gridLoadFailed[id] {
		return
	}
	// A grid in a namespace this node does not declare is never asked for.
	// The answer is already known — no plugin by that id, no connection by
	// that name — so asking would spend a round trip to be told so and put
	// a verdict on the error strip for a link the user can see is dead. The
	// latch stands in for the answer we did not need: the in-pane wording
	// reads "unavailable" rather than a "loading…" that never ends.
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
		// Coalesced repaint (scheduleFrame): fetch completions land in
		// bursts, and one draw() per completed child-grid read would mean
		// hundreds of full repaints while a big directory loads.
		a.scheduleFrame()
	}()
}

// fetchTileByID resolves a globally-routable tile id whose grid isn't
// cached: GetTile locates the tile (the server resolves a qualified id
// directly, no descent path needed), then fetchGrid pulls in its grid so
// findTileByID then hits. Deduped per tile id; a no-op once the tile's grid
// is cached. Background, like fetchGrid.
func (a *App) fetchTileByID(tileID string) {
	if tileID == "" || a.fetch.tileLoadFailed[tileID] {
		return
	}
	// Same rule as fetchGrid: a namespace this node does not declare is not
	// asked. A leaf link into a removed plugin resolves nowhere and stays
	// its own dead face.
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
			// Latch only on a server verdict: a broken reference — a
			// deleted tile, an unmounted plugin — answers the same way
			// every time, and the latch stops the per-frame draw dogpiling
			// the server. A transport failure latches nothing, because the
			// server never spoke, and the next caller retries.
			if clientsync.Of(err) != clientsync.OutcomeTransport {
				a.fetch.tileLoadFailed[tileID] = true
			}
			return
		}
		a.fetchGrid(tile.GridID)
	}()
}

// nowMs returns the current time in milliseconds since the epoch as
// reported by the browser. Used for animation timing.
func nowMs() float64 {
	return js.Global().Get("Date").Call("now").Float()
}

// taggedLog returns a console logger that prefixes every message with tag.
// The live-surface clients trace through it; the prefixes ("[urlview]",
// "[shellstream]") are what a log reader — and the e2e suite — greps for, so
// they are part of the output, not decoration.
func taggedLog(tag string) func(format string, args ...any) {
	return func(format string, args ...any) {
		js.Global().Get("console").Call("log", tag+" "+fmt.Sprintf(format, args...))
	}
}

// scheduleFrame ensures a draw happens on the next animation frame. While
// dragging or animating, the frame loop continues until the state settles.
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
	// Every animating pane advances on the same tick; they are independent,
	// so one landing never touches another's motion.
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
	// Ascent-trace fades need frames until they run out; prune the expired.
	if a.pruneTraces(now) {
		a.scheduleFrame()
	}
	a.draw()
}

// pruneTraces drops expired ascent-trace highlights and reports whether any
// are still fading (i.e. the frame loop must keep ticking).
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

// startTransition hands the transition to the per-pane set, which primes its
// first segment and displaces — landing, never voiding — anything that pane
// was already animating, and wakes the frame loop.
func (a *App) startTransition(t *transition.Transition) {
	a.trans.Start(t, nowMs())
	a.scheduleFrame()
}

// enterSegment installs a segment's place and viewport on its pane: the one
// writer of the scratch viewport an animation drives. Called by
// client/transition when a segment begins, and once more with the final
// segment's end state when a transition is cut short.
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

// landTransition is what arriving means, whether the pane animated the whole
// way there or was cut short: the selection clears, latched grid verdicts get
// their retry, the grid is fetched, an ascent's trace arms, and the
// transition's own landing runs — a content descent pushes its frame there,
// so this is not optional. The pane's place and viewport are already the
// destination's; this is everything else.
func (a *App) landTransition(tr *transition.Transition) {
	p := a.tree.FindPane(tr.PaneID)
	if p == nil {
		return
	}
	a.clearSelected(p.ID)
	a.fetch.gridLoadFailed = map[string]bool{}
	a.fetchGrid(a.gridIDForPane(p))
	if tr.TraceTileID != "" {
		// The ascent landed: light the trace on the tile the pane came out
		// of and keep the frame loop alive for the fade.
		a.traces[p.ID] = traceState{tileID: tr.TraceTileID, startMs: nowMs()}
		a.scheduleFrame()
	}
	if tr.OnComplete != nil {
		tr.OnComplete()
	}
	a.scheduleURLUpdate()
	a.draw()
}

// animationDone is called when the active animation reaches its target. It
// clears the ghost and any associated render hides so the cache becomes
// the source of truth again.
func (a *App) animationDone() {
	a.animation = nil
	a.ghost = nil // the render hides live on the ghost and die with it
}

// ghostHiddenTile / ghostHiddenPane read the render hide off the ghost — ""
// when no ghost is in flight. One owner (the ghost), one lifecycle.
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

// startSSE opens the Connect-streaming Subscribe RPC and applies each inbound
// event to the local cache. It reconnects after a brief backoff on stream
// termination, and a reconnect after a gap fires the retry kick: Subscribe has
// no cursor, so every event that happened during the gap is gone. Without the
// kick the client renders stale state under a freshly-cleared notice; the kick
// resyncs the cache and drains everything the gap left pending.
func (a *App) startSSE() {
	gap := false
	for {
		stream, err := a.cl.Subscribe(context.Background())
		if err != nil {
			// Surface the stall: until this reconnects, everything on screen
			// is silently going stale. Coalesces (one source), and resolves
			// itself on reconnect below.
			a.reportErr(errsurface.Error, "events", "live updates disconnected — retrying")
			gap = true
			time.Sleep(time.Second)
			continue
		}
		a.resolveErr("events")
		if gap {
			gap = false
			a.retryKick(true)
		}
		for {
			ev, ok, err := stream.Recv()
			if err != nil {
				a.reportErr(errsurface.Error, "events", "live updates disconnected — retrying")
				gap = true
				break
			}
			if !ok {
				// A clean EOF is still a gap: events between now and the
				// reconnect are lost, with no cursor to resume from.
				gap = true
				break
			}
			if a.c.Apply(ev) {
				a.draw()
			}
			// A removed tile's decoded preview image, and its backing object
			// URL, must be released, or deleting url and shell tiles leaks
			// browser image resources for the life of the page. The
			// rendered-markdown preview holds the same pair — a blob URL
			// and a decoded raster — and is released beside it.
			if ev.Kind == rpc.EventTileRemoved && ev.TileRemoved != nil {
				a.urlPreview.Drop(ev.TileRemoved.TileID)
				a.dropRenderedPreview(ev.TileRemoved.TileID)
			}
			// GridChanged: refetch the affected grid. The event is the one
			// per-grid signal that something changed, so it is also what
			// clears a verdict latch for that grid. It is unconditional: a
			// grid nothing is looking at now is one the next descent, preview,
			// or crumb would otherwise read stale from the cache.
			if ev.Kind == rpc.EventGridChanged && ev.GridChanged != nil {
				delete(a.fetch.gridLoadFailed, ev.GridChanged.GridID)
				a.fetchGrid(ev.GridChanged.GridID)
			}
			// PluginHealth: a plugin's own event stream, not this client's
			// connection to the server, went dark or recovered. See
			// fanInEvents and watchPlugin in
			// internal/server/connect_handler.go. The source is distinct per
			// plugin, keyed by uuid, so one plugin's outage neither
			// coalesces with nor clears another's, or the top-level "events"
			// disconnect notice above.
			if ev.Kind == rpc.EventPluginHealth && ev.PluginHealth != nil {
				a.reportPluginHealth(*ev.PluginHealth)
			}
		}
		stream.Close()
		time.Sleep(500 * time.Millisecond)
	}
}

// retryKick drains everything a transport gap left behind. Fired on an event
// stream reconnect after a gap, with a resync, because the gap's events are
// unrecoverable and cached grids must refetch; on a mount's health recovery;
// and by the slow backstop timer, without a resync, because nothing says the
// cache is stale, only that unacknowledged writes exist. The order is: clear
// the latches and launch the refetches, then drain the one outbox in the
// order the writes were made — framing, captures, layout, and the user's
// unsaved bytes through the same door. The async pieces converge through the
// cache's one Apply door: a refetch racing a parked write's echo lands
// whichever finishes last, and the echo of the newer write is what the server
// holds.
func (a *App) retryKick(resync bool) {
	if resync {
		// Failure latches are gap state: a grid that failed while the link
		// was down deserves a fresh attempt, and a tile id latched by a
		// verdict re-verifies once per reconnect, at one GetTile.
		clear(a.fetch.tileLoadFailed)
		clear(a.fetch.gridLoadFailed)
		// So is a fetch still in flight. The link it rode is gone, and a
		// request that dies with a link need never return: nothing answers
		// it, nothing fails it, and its dedupe claim would keep every retry
		// away forever. Cancel them all, and re-ask for the grids by name —
		// a pane waiting on a grid it never received is not in the cache, so
		// the known-grid sweep below cannot speak for it. The cancelled tile
		// and content reads are re-asked by the draw the refetches schedule,
		// off the same cache misses that asked the first time.
		stuck := a.fetch.gridFetch.CancelAll()
		a.fetch.tileFetch.CancelAll()
		a.fetch.contentFetch.CancelAll()
		for _, gid := range append(stuck, a.c.KnownGridIDs()...) {
			a.fetchGrid(gid)
		}
	}
	a.syncContentOutbox()
	for _, retry := range a.persist.out.Drain() {
		retry()
	}
}

// retryBackstop is the slow safety net behind the reconnect kick: when the
// outbox holds anything — a transport failure with no stream gap, since the
// stream can survive a blip a unary write did not — re-post it without
// waiting for a reconnect that may never come.
func (a *App) retryBackstop() {
	for {
		time.Sleep(30 * time.Second)
		a.syncContentOutbox()
		if a.persist.out.Len() > 0 {
			a.retryKick(false)
		}
	}
}

// Session-local state: the full UI state — split layout, per-pane place,
// viewport — rebuilds from the URL on reload. The URL captures only the
// focused pane's place, and the split tree starts fresh. The outer frames of
// a pane's place stack, the viewports an ascent lands on, are session-only,
// so a restored pane ascends onto each grid's persisted framing instead. A
// text tile's mode is persisted on the tile row, so it survives a reload.

// gridIDForPane returns the grid id the pane's place names: its current
// namespace anchor walked down the doorway path (both PROJECTIONS of the
// frame stack). Returns "" when the pane is boot-blank.
func (a *App) gridIDForPane(p *pane.Pane) string {
	return a.gridIDForPathFrom(p.Anchor(), p.Path())
}

// gridIDForPathFrom walks `path` (well row ids) from `anchor` and returns the
// grid id at the leaf. Returns anchor if the path is empty or stale prefixes
// don't resolve, and "" when anchor is "" (start screen).
func (a *App) gridIDForPathFrom(anchor string, p []string) string {
	// The walk (follow each well's child grid, stop at a stale prefix) is the
	// pure gridpath.ResolveLeafGrid; the closure does the cache read and kicks
	// a background fetch on an uncached grid.
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
			return w.ChildGridID, true, true
		})
}

// refetchGridOnConflict handles a 409 version-conflict: refetches the
// affected grid so the cache catches up to the server's authoritative
// version, and posts an Info notice so the reconciliation is visible —
// the user's optimistic change lost a race and is about to be replaced on
// screen, which must not look like spontaneous mutation.
func (a *App) refetchGridOnConflict(gridID string, where string) {
	a.reportErr(errsurface.Info, "conflict:"+where, where+": changed elsewhere — reloaded")
	a.fetchGrid(gridID)
}

// reportErr is the one wasm entry into the error surface: log the failure to
// the console (window.ts forwards renderer warnings/errors to the app's log,
// so every notice is greppable after it leaves the strip), record the notice,
// arm the expiry timer, and schedule a repaint so the strip appears this
// frame. Safe from any goroutine (wasm is single-threaded; goroutines
// interleave, never race).
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

// scheduleErrExpiry arms a single setTimeout for the surface's soonest expiry
// deadline. The callback prunes expired notices and re-arms for the next one.
// A coalesced re-report that pushed a deadline out only makes the pending
// timer fire early, prune nothing, and reschedule — never miss an expiry.
func (a *App) scheduleErrExpiry() {
	if a.persist.sched.errExpire.pending {
		return
	}
	d, ok := a.errs.NextDeadline(time.Now())
	if !ok {
		return
	}
	// +1 rounds up so the timer lands just past the deadline instead of a
	// truncated hair before it (which would prune nothing and re-arm).
	ms := int(d/time.Millisecond) + 1
	if ms < 1 {
		ms = 1
	}
	a.persist.sched.errExpire.arm(ms)
}

// resolveErr clears a source's notice when its condition heals (e.g. the
// event stream reconnects), so stale bad news doesn't linger.
func (a *App) resolveErr(source string) {
	a.errs.Resolve(source)
	a.scheduleFrame()
}

// reportPluginHealth maps an EventPluginHealth transition onto the error
// surface: unhealthy reports, since a plugin's event stream being down means
// its tiles have stopped updating with no other signal, and healthy resolves
// any prior notice for it. Keyed per plugin uuid, so it neither coalesces
// with nor is cleared by an unrelated plugin's outage or the top-level event
// stream notice ("events").
func (a *App) reportPluginHealth(h rpc.PluginHealth) {
	source := "plugin:" + h.PluginUUID
	if h.Healthy {
		// A recovered plugin is a healed gap for its tiles: the server-side
		// fan-in resumed with no backlog, so this client missed that
		// plugin's events too. The kick resyncs and drains, the same
		// reasoning as the stream reconnect kick — one plugin narrower in
		// cause, but the same cure, since a per-plugin resync would need
		// routing state the client deliberately does not keep.
		a.resolveErr(source)
		a.retryKick(true)
		return
	}
	label := h.PluginUUID
	if pl, ok := a.pluginByUUID(h.PluginUUID); ok && pl.Label != "" {
		label = pl.Label
	}
	a.reportErr(errsurface.Error, source, label+": live updates stopped — "+h.Detail)
	// A source going down changes what its grids ARE: a connection's rooms
	// stop being live answers and become the node's memory of them, stamped
	// stale and worn as the bar's cached chip. Nothing on screen says so until
	// the client re-reads, so the down transition resyncs exactly as the up
	// one does — the same blunt cure for the same reason, since which grids
	// belong to which source is routing state the client does not keep.
	a.retryKick(true)
}
