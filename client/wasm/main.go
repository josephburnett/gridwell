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
	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/gridpath"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/menu"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panestate"
	"github.com/josephburnett/gridwell/client/preview"
	"github.com/josephburnett/gridwell/client/textedit"
	"github.com/josephburnett/gridwell/client/touchgest"
	"github.com/josephburnett/gridwell/client/url"
	"github.com/josephburnett/gridwell/client/workspace"
	"github.com/josephburnett/gridwell/internal/rpc"
)

const (
	cellPx     = pane.CellPx
	zoomMin    = 0.25
	zoomMax    = 8.0
	zoomFactor = 1.1

	// textFixedScale is the constant render scale for text-file content
	// when descended. There is no zoom: the descended pane is a plain
	// window onto the document at this scale, scrolled vertically. The
	// parent-grid preview re-renders at this same scale and crops the
	// last-framed window into the tile footprint.
	textFixedScale = 1.0

	// textNaturalContentPx is the FALLBACK logical wrap width for a markdown
	// preview that has no framing yet (a tile never descended into / ascended
	// from). A live or previously-framed doc instead wraps at its own width —
	// the pane's inner box (textContentWidth) or the stored framing width — so
	// it reflows to the pane and the preview stays a scaled copy of it.
	textNaturalContentPx = 800.0
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

	// plugins is the configured plugin list from ListPlugins, used for the
	// node-grid health tints, plugin glyphs, and the e2e launcher hook.
	// Order is config order.
	plugins []rpc.PluginInfo

	// nodeGrid is the qualified id of this node's node grid ("<node-uuid>/0")
	// — the read-only plugin-list grid, still the federation surface an ssh
	// mount lands on but no longer the local landing page. One fact, learned
	// once from ListPlugins (NodeIdentity); "" only while bootstrap hasn't
	// answered (or the server predates node identity).
	nodeGrid string

	// home is the qualified grid id "/" means — the first configured
	// plugin's root grid, node grid as fallback (rpc.HomeGrid, the one
	// derivation). Every "empty anchor means home" reader (boot, URL
	// decode/encode, workspace leaf restore) reads THIS, never nodeGrid.
	home string

	tree *pane.Tree
	c    *cache.Cache

	// textSaves serializes UpdateText posts per tile so pipelined saves
	// chain versions instead of racing (issue #140); see enqueueTextSave.
	textSaves *textedit.SaveQueue

	width, height float64

	dragging *dragState

	// locals is the per-pane session-local client state, one entry per live pane,
	// keyed by pane id. It replaces the former sprawl of parallel per-pane maps
	// (selection, ascent stack, caret, dirty, frozen-URL pan, and — later — the
	// live URL/shell handles). Created on demand by a.local; removed atomically on
	// pane drop by a.forgetPane. See paneLocal / client/panestate.
	locals map[string]*paneLocal

	// Plus-button (+) creation-menu state. menu is the single owner: open/closed,
	// which pane, hovered item. Never assign menu fields directly — go through
	// its methods (see client/menu). Persistence across a portal descent rides in
	// pane.Frame.MenuOpen.
	menu menu.State

	// errs is the single owner of user-visible failure notices (charter §6).
	// Every failure path reports via a.reportErr / a.resolveErr; only the
	// notice-strip render and its click handler read it. Never write error
	// state anywhere else.
	errs *errsurface.Surface

	// paneLayouts memoizes each pane tile's decoded workspace tree, keyed by
	// tile id and invalidated by blob generation (see paneTileLayout). A
	// cache and a view of the server blob, never an authority — the DECODE
	// is what's memoized; the truth is the tile row + content bytes.
	paneLayouts map[string]*paneLayoutEntry

	// ws is the workspace stack — the ONE owner of "which pane tile is the
	// user inside, and what outer tree does each descent restore" (see
	// client/workspace). a.tree is the display of its top; the persisted
	// layout is owned by the server blob.
	ws workspace.Stack

	// wsPending coordinates an in-flight workspace descent (animation ×
	// fetch); nil when none. thIdle reports busy while it exists.
	wsPending *wsPending

	// urlPrevPlace / urlPlaceSeen are the URL writer's push-vs-replace diff
	// baseline: the structural place the last history write named (issue
	// #194 — writeURLNow pushes only when the place changed structurally).
	// urlRestoring marks an in-flight popstate restore, during which every
	// write replaces (pushing would corrupt the stack being traversed).
	// Owned by urlsync.go.
	urlPrevPlace url.Place
	urlPlaceSeen bool
	urlRestoring bool

	// caps is the host capability set (client/caps), derived ONCE at boot
	// from bridge presence. Feature gates read a.caps; nothing else asks
	// bridgeAvailable() to make a behavior decision.
	caps caps.Caps

	// touch is the touch→mouse gesture classifier (client/touchgest);
	// touchTimerCb is its retained long-press timer callback;
	// touchDownTarget is the element the current gesture started on — where
	// synthetic MouseDowns route, mirroring browser hit-testing for a real
	// mouse (issue #191). Owned by touch.go; nothing else feeds or reads
	// them.
	touch           *touchgest.Machine
	touchTimerCb    js.Func
	touchDownTarget js.Value

	// gridLoadFailed records grids whose last fetch returned non-200, so
	// the renderer can show a meaningful message and we don't retry in
	// a tight loop.
	gridLoadFailed map[string]bool

	// gridInflight tracks grid ids with a pending GetGrid request so
	// repeated draws (which call fetchGrid on every cache miss) don't
	// dogpile the server. Cleared in the fetch goroutine after the
	// response lands.
	gridInflight map[string]bool

	// contentInflight tracks tile ids with a pending GetTileContent request.
	// Deduped like gridInflight: tileBody fires fetchTileContent on every
	// cache miss every frame, so without the guard one absent body spawns a
	// fetch per frame — and any reply older than one that already landed
	// would repaint stale bytes into the overlay (issue #189; the cache's
	// PutFetchedContent version guard is the backstop).
	contentInflight map[string]bool

	// tileInflight tracks qualified tile ids with a pending GetTile request.
	// An embed names a globally-routable tile id whose grid may never have been
	// visited (so it isn't cached); resolving it locates the tile's grid and
	// fetches it. Deduped like gridInflight: the embed drawer fires this on
	// every cache miss every frame.
	tileInflight map[string]bool
	// tileLoadFailed records tile ids whose GetTile failed (a broken embed: the
	// tile was deleted, or its plugin isn't mounted). Without it a "missing"
	// embed re-fires GetTile every frame forever — the same dogpile gridLoadFailed
	// prevents for grids. Cleared only by a reload.
	tileLoadFailed map[string]bool

	// ghost is the in-flight visual representation of a node being dragged
	// or animated to/from somewhere. The dragged node renders here at
	// sub-cell screen precision instead of at its stored cell position.
	ghost *ghost

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

	// urlPreview caches decoded HTMLImageElement values for URL and
	// shell tile previews. Keyed by tile id; auto-invalidates when a
	// tile's PreviewBlobID changes server-side — see client/preview.
	urlPreview *preview.Cache

	// mdCache memoizes markdown layout (parse → lower → layout) keyed by
	// content hash + content width, so a frame doesn't re-lay-out every
	// visible text tile. Positions are logical; the painter scales them, so a
	// cached layout is valid across zoom levels. See layoutMarkdown.
	mdCache map[mdCacheKey]markdown.LayoutResult

	// mdImages caches HTMLImageElements for real markdown images (![](src)),
	// keyed by src URL; mdImageState tracks load progress (0 loading, 1 ready,
	// 2 error). See drawMarkdownImage.
	mdImages     map[string]js.Value
	mdImageState map[string]int8

	// shellAlive caches the result of the ShellSessionAlive probe per
	// tile id. The refresh button shows iff (preview_blob_id == 0)
	// || shellAlive[id] is true. shellAliveProbing dedups in-flight
	// probes so a rapid sequence of redraws doesn't fan out into
	// many RPC calls. A missing key means "unknown" — the renderer
	// kicks off a probe and hides the button until the result lands.
	shellAlive        map[string]bool
	shellAliveProbing map[string]bool

	// traces holds the per-pane ascent-trace highlight (the fading "you just
	// came from HERE" outline, issue #83). Armed by completeTransition when
	// the finished transition was an ascent; pruned by frame() as each fade
	// runs out. Ephemeral view state, like selection.
	traces map[string]traceState

	// textTextarea is the lazily-created <textarea> element used for
	// markdown text-mode editing. It is positioned over the focused pane
	// when pane.TextFocus != 0 and pane.TextMode == "text", and hidden
	// otherwise. We hold it as a single shared element to avoid creating
	// fresh DOM nodes on every descent.
	textTextarea js.Value
	// textTextareaInputCb is the input event listener that mirrors the
	// textarea's value into a per-frame redraw. Held so we can release
	// it cleanly if the App is torn down (currently never).
	textTextareaInputCb  js.Func
	textTextareaScrollCb js.Func

	// renamePill / renameEditing back the rename-while-descended affordance
	// (issue #61): one shared DOM pill at the focused pane's top-center (see
	// rename_overlay.go). renameEditing hides the pill while its input twin
	// is open.
	renamePill        js.Value
	renamePillClickCb js.Func
	renameEditing     bool
	// lastNativePill dedupes label pushes to a live pane's native bubble
	// twin (paneID + NUL + label) so the per-draw sync doesn't spam IPC.
	lastNativePill string

	// textToggleBtn is the floating rendered/raw toggle for a markdown
	// descent. A DOM element (not a canvas button) so it can sit above
	// the textarea overlay — letting the text content fill the pane
	// edge-to-edge instead of reserving a strip for a canvas button.
	textToggleBtn js.Value
	textToggleCb  js.Func

	// urlModalOpen tracks whether the URL-entry modal is currently open.
	// A second openURLModal call while this is true is a no-op.
	urlModalOpen bool

	// embedHits collects click-targets for tile-embeds rendered inside
	// text panes this frame. Reset at the start of each draw() and
	// appended to as embeds are painted by drawMarkdownInPane. Queried by
	// the input handler to descend on click. See embed.go.
	embedHits []embedHit

	// lastTextareaTileID tracks which text-tile id the singleton
	// textarea is currently bound to (i.e., whose blob it holds in its
	// value). "" means "bound to nothing" (textarea is hidden or never
	// seeded). refreshFileOverlay uses this to decide whether to re-seed
	// from the cached blob on a focus shift: same tile → preserve typing,
	// different tile → fresh content. Embed drops also consult it to
	// push new content into the textarea when it's already bound to the
	// drop target.
	lastTextareaTileID string

	// textareaReady tracks whether the single textarea currently holds the
	// focused tile's content (vs. being empty from a recent pane switch or
	// pending blob fetch). Set true when refreshFileOverlay seeds the textarea
	// with actual content or when the user types (input event). Set false when
	// the textarea is cleared on a tile switch or mode toggle. drawMarkdownInPane
	// reads this via textedit.CanvasHiddenByOverlay so the canvas keeps painting
	// during the loading race (blank-pane fix, issue #35 mechanism B).
	textareaReady bool
}

// scheduler holds the App's debounce / requestAnimationFrame bookkeeping.
// Each "Scheduled" bool guards a pending callback so repeated triggers
// coalesce into a single deferred run; each js.Func is allocated once and
// retained so re-scheduling never leaks handles.
type scheduler struct {
	// rafScheduled tracks a pending requestAnimationFrame so we don't
	// queue redundant frames.
	rafScheduled bool

	// wsSaveScheduled / wsSaveCb debounce the workspace-layout persister
	// (see scheduleWorkspaceSave): draw() arms it while inside a workspace;
	// the callback encodes, hash-diffs, and posts SetPaneLayout on change.
	wsSaveScheduled bool
	wsSaveCb        js.Func

	// urlUpdateScheduled / urlUpdateCb debounce the URL replaceState:
	// multiple state changes within the window coalesce into one.
	urlUpdateScheduled bool
	urlUpdateCb        js.Func

	// framingSaveScheduled / framingSaveCb debounce the grid-framing
	// persister (see scheduleFramingSave): draw() arms it; the callback
	// flushes every pane's settled framing through the no-op-guarded
	// writers.
	framingSaveScheduled bool
	framingSaveCb        js.Func

	// textSaveScheduled / textSaveCb debounce the text-tile content save.
	textSaveScheduled bool
	textSaveCb        js.Func

	// rootViewSaveScheduled / rootViewSaveCb debounce the root-grid
	// default-view persistence (only fires when the focused pane is at the
	// user's root; well descents persist via SetWellView on ascent).
	rootViewSaveScheduled bool
	rootViewSaveCb        js.Func

	// errExpireScheduled / errExpireCb arm one timer for the error surface's
	// soonest expiry deadline, so stale one-shot notices leave the strip
	// without polling. See scheduleErrExpiry.
	errExpireScheduled bool
	errExpireCb        js.Func
}

// paneState is a captured pane viewport plus, when the descent
// originated from inside a text-tile, the text descent context to
// restore on matching ascent.
//
// TextFocus == "" means "the parent was a plain grid view" — the common
// case. A non-empty TextFocus is set when descending out of a text tile
// (e.g. clicking a tile-embed); on ascent the saved TextMode and scroll
// are reinstalled so a single ascent lands back in the doc rather than
// in the grid behind it.
// paneState is the saved-ascent stack entry, now owned by client/panestate
// (panestate.Saved) so the per-pane state lives in one tested place. Aliased here
// to keep the many `paneState{...}` construction sites unchanged.
type paneState = panestate.Saved

// paneLocal is the single owner of one pane's session-local client state: the
// plain-data part (panestate.State, embedded — selection, ascent stack, caret,
// dirty, frozen-URL pan) plus, added in later commits, the native live URL/shell
// handles. One per live pane in App.locals, created on demand by App.local and
// removed atomically when the pane is dropped (App.forgetPane), so none of this
// state can outlive or be orphaned from its pane.
type paneLocal struct {
	panestate.State
	// urlView is the live native WebContentsView handle when this pane is
	// descended into a live URL tile; nil otherwise. Closed via closeURLStream.
	urlView *urlView
	// shellConn is the live shell session (WebSocket + xterm.js overlay) when
	// this pane is descended into a live shell tile; nil otherwise. Closed via
	// closeShellStream / releaseShellStream.
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

// forgetPane tears down and removes all per-pane state for a pane that is going
// away (a collapsed / closed pane): it freezes and closes any live URL or shell
// session, then deletes the pane's entry from a.locals so no per-pane state
// outlives its pane. The single, atomic cleanup point on pane drop — before this
// the per-pane maps leaked their entries (a bounded leak, since pane ids are
// never reused, but now the lifecycle is explicit and complete).
func (a *App) forgetPane(paneID string) {
	a.closeURLStream(paneID, true)
	a.closeShellStream(paneID, true)
	delete(a.locals, paneID)
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
	// traceTileID, when set (ascents only), arms the ephemeral "you just came
	// from HERE" highlight on that tile once the transition lands — a fading
	// outline so the user can tell which shell/well/url they just left
	// (issue #83). Pure view state; nothing persists.
	traceTileID string
}

// traceState is one armed ascent-trace highlight: the tile to outline in the
// pane's grid view and the fade clock's start. Held per pane in App.traces.
type traceState struct {
	tileID  string
	startMs float64
}

// traceDurMs is how long the ascent-trace outline takes to fade out.
const traceDurMs = 2000.0

type transSegment struct {
	path                     []string
	fromCx, fromCy, fromZoom float64
	toCx, toCy, toZoom       float64
	durationMs               float64
	// setAnchor switches the pane's plugin anchor at this segment's start (a
	// cross-plugin portal jump). anchor is the new plugin-root grid id. Normal
	// same-plugin descents leave the anchor untouched.
	setAnchor bool
	anchor    string
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

	// hiddenTileID/hiddenPaneID suppress the SOURCE tile's normal render
	// while this ghost represents it in a move drag (clones don't hide —
	// the source stays). They live ON the ghost because their lifetime IS
	// the ghost's: the tile must stay hidden through the snap-back
	// animation after a.dragging is already nil, and reappear exactly when
	// the ghost dies — one owner, correct lifecycle by construction
	// (formerly two App-level scratch fields cleared by hand).
	hiddenTileID string
	hiddenPaneID string

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
	// be rejected: a rendered-mode doc (read-only), a same-namespace
	// cross-grid move with a source-backed endpoint, or a solid well
	// right-dragged across a namespace (deep copy unimplemented).
	// Renderer paints the international "no entry" badge over the ghost;
	// mouseup snap-backs without RPC.
	forbidden bool

	// link is set while a left-drag hovers a target in a DIFFERENT id
	// namespace: the drop will create a LINK and the source stays put
	// (owner decision 2026-07-19 — there is no cross-plugin move). The
	// renderer paints the ghost dashed with the chain badge so the user
	// learns the meaning mid-drag; without it the source's survival
	// after the drop would read as a surprise duplicate.
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
	originPaneID string
	// originFocused records whether the origin pane was ALREADY focused when
	// the press landed. A bare click on an unfocused pane is focus-only: it
	// must not also navigate or select — the same rule the + button and the
	// corner circle follow (act only when prevFocus == pane). Without this, a
	// click meant to focus a pane descends if it happens to hit a tile.
	originFocused bool
	tileID        string
	cellOffsetX   float64
	cellOffsetY   float64
	startScreenX  float64
	startScreenY  float64
	curScreenX    float64
	curScreenY    float64
	started       bool
	// clone marks a right-button clone drag (armed by armRightClone). Such
	// a drag commits only through the right-button release path; the
	// left-button move-commit must refuse it so a stray non-right release
	// can't silently turn the clone into a move.
	clone          bool
	snapshotTile   rpc.Tile
	originScreenX  float64
	originScreenY  float64
	originPaneRect pane.Rect

	// Palette drag from the + menu: tileID is "" (no real node yet) but
	// isTemplate is true and item carries the grabbed palette entry — a
	// tile primitive (drop creates it) or a plugin (drop mounts it as an
	// exit-well link; a click with no drag enters the plugin instead).
	isTemplate bool
	item       paletteItem

	// Source-grid info — set at mousedown; same as the focused pane's
	// grid for parent-grid drags, or the well's child grid for "pull
	// out of well" drags. Carried separately so the drop commit can
	// build a MoveTile RPC with the right Path/grid id even when source
	// and dest are different grids inside the same pane.
	srcGridID   string
	srcPath     []string
	srcCellSize float64
}

// dragThreshold is the cursor-movement distance (CSS px) that turns a press into
// a drag. Below this, mousedown→mouseup is treated as a click (select).
//
// This is the SINGLE OWNER of the drag threshold. The native layer keeps two
// forced copies (apps/desktop/src/main/viewutil.ts RIGHT_DRAG_THRESHOLD, and an
// inlined copy in src/preload/urlview-preload.ts — a sandboxed preload may not
// require modules) so a live URL view interprets a right-drag-vs-right-click
// exactly as the canvas does. gesture-threshold.test.ts fails the build if either
// copy drifts from this value, so they cannot silently disagree.
const dragThreshold = 4.0

func main() {
	origin := js.Global().Get("location").Get("origin").String()
	app = &App{
		doc:               js.Global().Get("document"),
		win:               js.Global().Get("window"),
		cl:                rpc.NewDefaultClient(origin),
		c:                 cache.New(),
		textSaves:         textedit.NewSaveQueue(),
		locals:            map[string]*paneLocal{},
		menu:              menu.New(),
		errs:              errsurface.New(),
		caps:              caps.Derive(bridgeAvailable()),
		gridLoadFailed:    map[string]bool{},
		gridInflight:      map[string]bool{},
		contentInflight:   map[string]bool{},
		tileInflight:      map[string]bool{},
		tileLoadFailed:    map[string]bool{},
		urlPreview:        preview.NewCache(preview.NewJSDecoder()),
		shellAlive:        map[string]bool{},
		shellAliveProbing: map[string]bool{},
		traces:            map[string]traceState{},
		paneLayouts:       map[string]*paneLayoutEntry{},
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

	// Mobile browsers resize the VISUAL viewport (on-screen keyboard,
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
	// save-and-destroy path fires before the TCP connection dies.
	// Without this, the WS still drops via TCP FIN — but server-side
	// cleanup runs after a small delay and the user's final state
	// might miss the preview write.
	app.win.Call("addEventListener", "beforeunload", js.FuncOf(func(this js.Value, args []js.Value) any {
		// Post any typing still inside the save-debounce window. The enqueue
		// is async and may not finish before teardown, but firing it here
		// beats guaranteeing the loss by never firing at all.
		app.flushDirtyText()
		// Same for grid framing still inside its settle window (issue #190).
		app.flushFramingSave()
		app.closeAllURLStreams()
		app.closeAllShellStreams()
		return nil
	}))

	// popstate: the browser back/forward traverses descend/ascend (issue
	// #194 — writeURLNow pushes an entry per structural navigation). The
	// flag and the target URL are captured HERE, synchronously: a pending
	// debounced write firing after this callback returns must find the
	// writer suppressed, or it clobbers the entry the browser just
	// navigated to. The restore itself fetches, so it runs on a goroutine.
	app.win.Call("addEventListener", "popstate", js.FuncOf(func(this js.Value, args []js.Value) any {
		app.urlRestoring = true
		loc := js.Global().Get("location")
		raw := loc.Get("pathname").String()
		if s := loc.Get("search").String(); s != "" {
			raw += s
		}
		go app.restoreFromHistory(raw)
		return nil
	}))

	app.installCanvasInput()
	app.installWebviewListeners()
	app.installShellMirror()
	app.installTestHook() // read-only window.__gridwellTest, only under ?e2e=1

	go app.bootstrap()

	select {}
}

// bootstrap loads the plugin list and the node's identity, then starts the
// rest of the client. The landing page is HOME — the first configured
// plugin's root grid (node grid fallback, rpc.HomeGrid) — so panes anchor
// there; plugins are reached from the + menu.
func (a *App) bootstrap() {
	plugins, err := a.cl.ListPlugins(context.Background())
	if err == nil {
		a.plugins = plugins
	} else {
		// The landing page will render empty — say why, or it reads as "all
		// my plugins vanished" (charter §6).
		a.reportErr(errsurface.Error, "rpc:ListPlugins", "plugin list failed: "+rpcErrText(err))
	}
	if _, nodeRoot, err := a.cl.NodeIdentity(context.Background()); err == nil && nodeRoot != "" {
		a.nodeGrid = nodeRoot
	} else if err != nil {
		a.reportErr(errsurface.Error, "rpc:ListPlugins", "node identity failed: "+rpcErrText(err))
	}
	a.home = rpc.HomeGrid(a.plugins, a.nodeGrid)
	a.afterBootstrap()
}

func (a *App) afterBootstrap() {
	a.canvas.Call("focus")
	p := a.tree.FocusedPane()
	p.Anchor = a.home // land at home; applyURLOnBoot may restore a location
	p.Path = nil
	if a.home != "" {
		a.fetchGrid(a.home)
	}

	a.sched.wsSaveCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		a.sched.wsSaveScheduled = false
		a.flushWorkspaceSave()
		return nil
	})
	a.sched.urlUpdateCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		a.sched.urlUpdateScheduled = false
		a.writeURLNow()
		return nil
	})
	a.sched.framingSaveCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		a.sched.framingSaveScheduled = false
		a.flushFramingSave()
		return nil
	})

	a.sched.errExpireCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		a.sched.errExpireScheduled = false
		if a.errs.Expire(time.Now()) {
			a.scheduleFrame() // strip shrank; panes reclaim the height on redraw
		}
		a.scheduleErrExpiry()
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
func (a *App) fetchGrid(id string) {
	if id == "" {
		return
	}
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

// fetchTileByID resolves an embed's globally-routable target whose grid isn't
// cached: GetTile locates the tile (the server resolves a qualified id directly,
// no descent path needed), then fetchGrid pulls in its grid so findTileByID then
// hits and the embed both previews and descends. Deduped per tile id; a no-op
// once the tile's grid is cached. Background, like fetchGrid — the embed paints
// the missing placeholder until the grid lands, then resolves on the next frame.
func (a *App) fetchTileByID(tileID string) {
	if tileID == "" || a.tileInflight[tileID] || a.tileLoadFailed[tileID] {
		return
	}
	a.tileInflight[tileID] = true
	go func() {
		defer delete(a.tileInflight, tileID)
		tile, err := a.cl.GetTile(context.Background(), tileID)
		if err != nil || tile == nil {
			// Broken embed (deleted tile / unmounted plugin): stop re-firing so
			// the per-frame draw doesn't dogpile the server. Cleared by a reload.
			a.tileLoadFailed[tileID] = true
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
	if seg.setAnchor {
		p.Anchor = seg.anchor
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
	a.clearSelected(p.ID)
	a.gridLoadFailed = map[string]bool{}
	a.fetchGrid(a.gridIDForPane(p))
	if tr.traceTileID != "" {
		// The ascent landed: light the trace on the tile the pane came out
		// of and keep the frame loop alive for the fade.
		a.traces[p.ID] = traceState{tileID: tr.traceTileID, startMs: nowMs()}
		a.scheduleFrame()
	}
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

// pushPaneState saves the parent viewport on the stack for paneID. Called at
// the start of descent so the matching ascent can animate the parent back to
// exactly this position.
func (a *App) pushPaneState(paneID string, s paneState) {
	a.local(paneID).PushAscent(s)
}

// popPaneState removes and returns the most recent saved parent viewport for
// paneID, or nil if the stack is empty (e.g. user reloaded mid-descent).
func (a *App) popPaneState(paneID string) *paneState {
	pl, ok := a.localIf(paneID)
	if !ok {
		return nil
	}
	return pl.PopAscent()
}

// startSSE opens the Connect-streaming Subscribe RPC and applies each
// inbound event to the local cache. Reconnects after a brief backoff on
// stream termination so a transient server hiccup doesn't leave the UI
// stale.
func (a *App) startSSE() {
	for {
		stream, err := a.cl.Subscribe(context.Background())
		if err != nil {
			// Surface the stall: until this reconnects, everything on screen
			// is silently going stale. Coalesces (one source), and resolves
			// itself on reconnect below.
			a.reportErr(errsurface.Error, "events", "live updates disconnected — retrying")
			time.Sleep(time.Second)
			continue
		}
		a.resolveErr("events")
		for {
			ev, ok, err := stream.Recv()
			if err != nil {
				a.reportErr(errsurface.Error, "events", "live updates disconnected — retrying")
				break
			}
			if !ok {
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
			// PluginHealth: a plugin's OWN event stream (not this client's SSE
			// connection to the server) went dark or recovered — see
			// fanInEvents/watchPlugin in internal/server/connect_handler.go.
			// Distinct source per plugin (keyed by uuid) so one plugin's outage
			// doesn't coalesce with — or get cleared by — another's or the
			// top-level "events" disconnect notice above.
			if ev.Kind == rpc.EventPluginHealth && ev.PluginHealth != nil {
				a.reportPluginHealth(*ev.PluginHealth)
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

// gridIDForPane returns the grid id at the leaf of the pane's descent path,
// walking from the pane's Anchor (the plugin root it currently sits inside).
// Returns "" when the pane is at the launcher start screen (Anchor == "").
func (a *App) gridIDForPane(p *pane.Pane) string {
	return a.gridIDForPathFrom(p.Anchor, p.Path)
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
// the user's optimistic change lost a race and is about to be replaced
// on screen; that must not look like spontaneous mutation (charter §6).
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
	if a.sched.errExpireScheduled {
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
	a.sched.errExpireScheduled = true
	js.Global().Call("setTimeout", a.sched.errExpireCb, ms)
}

// resolveErr clears a source's notice when its condition heals (e.g. the
// event stream reconnects), so stale bad news doesn't linger.
func (a *App) resolveErr(source string) {
	a.errs.Resolve(source)
	a.scheduleFrame()
}

// reportPluginHealth maps an EventPluginHealth transition onto the error
// surface: unhealthy reports (a plugin's event stream is down, so its tiles
// have stopped updating with no other signal — the silent-disappearance
// class), healthy resolves any prior notice for it. Keyed per plugin uuid so
// it neither coalesces with nor is cleared by an unrelated plugin's outage or
// the top-level SSE-connection notice ("events").
func (a *App) reportPluginHealth(h rpc.PluginHealth) {
	source := "plugin:" + h.PluginUUID
	if h.Healthy {
		a.resolveErr(source)
		return
	}
	label := h.PluginUUID
	if pl, ok := a.pluginByUUID(h.PluginUUID); ok && pl.Label != "" {
		label = pl.Label
	}
	a.reportErr(errsurface.Error, source, label+": live updates stopped — "+h.Detail)
}
