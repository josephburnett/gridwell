//go:build js && wasm

// Package main is the WASM entry point for the Ascent client.
//
// This file is intentionally a thin wiring shim: anything testable lives in
// client/pane, client/markdown, client/dragdrop, client/cache. The code here
// reaches into syscall/js and is exercised manually in a browser.
package main

import (
	"encoding/json"
	"strconv"
	"syscall/js"

	"github.com/josephburnett/ascent/client/anim"
	"github.com/josephburnett/ascent/client/cache"
	"github.com/josephburnett/ascent/client/dragdrop"
	"github.com/josephburnett/ascent/client/pane"
	"github.com/josephburnett/ascent/internal/rpc"
)

const (
	cellPx     = 64.0
	zoomMin    = 0.25
	zoomMax    = 8.0
	zoomFactor = 1.1
)

// app is the running client. Held in a package-level var so JS callbacks can
// reach it without closures over reflect.Value.
var app *App

// App is the client state.
type App struct {
	doc, win js.Value
	canvas   js.Value
	cctx     js.Value // 2d context

	user *rpc.WhoamiResponse
	tree *pane.Tree
	c    *cache.Cache

	width, height float64

	dragging *dragState

	// Per-pane selection: paneID → node id (0 means nothing selected).
	selectedNodeID map[string]int64

	// Plus-button popover state.
	menuOpen   bool
	menuPaneID string
	menuHover  int // index of hovered menu item, or -1

	// uploadHandler is the active onchange callback bound to the hidden
	// upload input. We retain it so we can release it before binding a
	// fresh handler on the next upload, preventing leaks of js.FuncOf.
	uploadHandler   js.Func
	uploadHandlerOK bool

	// gridLoadFailed records grids whose last fetch returned non-200, so
	// the renderer can show a meaningful message and we don't retry in
	// a tight loop.
	gridLoadFailed map[int64]bool

	// ghost is the in-flight visual representation of a node being dragged
	// or animated to/from somewhere. The dragged node renders here at
	// sub-cell screen precision instead of at its stored cell position.
	ghost *ghost

	// hiddenObjectID, when non-empty, suppresses rendering of any cached
	// node with this object_id. We use object_id (not row id) because CoW
	// rewrites row ids underneath us; the lineage stays stable.
	hiddenObjectID string
	hiddenPaneID   string

	// animation is the current ghost animation, if any (snap-to-target on
	// drop or snap-back-to-origin on failure).
	animation *anim.Animation

	// rafScheduled tracks whether we have a pending requestAnimationFrame
	// callback so we don't queue redundant frames.
	rafScheduled bool

	// transition is the active descent/ascent zoom animation, if any.
	transition *paneTransition

	// paneStateStack is the saved (Cx, Cy, Zoom) triple for each pane,
	// pushed on descent and popped on ascent, so ascent restores the
	// exact viewport the user was looking at before they descended.
	// Indexed by pane id; the slice's length matches len(pane.Path).
	paneStateStack map[string][]paneState

	// fileLastMode remembers, per file node id, the most recent
	// "text"/"rendered" mode the user left a file in. Used by the parent
	// grid preview to mirror "however you left it" without needing a
	// server-side field. Persisted to localStorage along with the tree.
	fileLastMode map[int64]string

	// fileTextarea is the lazily-created <textarea> element used for
	// markdown text-mode editing. It is positioned over the focused pane
	// when pane.FileFocus != 0 and pane.FileMode == "text", and hidden
	// otherwise. We hold it as a single shared element to avoid creating
	// fresh DOM nodes on every descent.
	fileTextarea js.Value
	// fileTextareaInputCb is the input event listener that mirrors the
	// textarea's value into a per-frame redraw. Held so we can release
	// it cleanly if the App is torn down (currently never).
	fileTextareaInputCb js.Func
	fileTextareaScrollCb js.Func
}

// paneState is a captured viewport: viewport center in cells (sub-cell
// precision) and zoom multiplier.
type paneState struct {
	Cx   float64 `json:"cx"`
	Cy   float64 `json:"cy"`
	Zoom float64 `json:"zoom"`
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
	paneID          string
	segments        []transSegment
	currentSegment  int
	segmentStartMs  float64
	// onComplete, if set, runs after the last segment lands. Used by file
	// descent to install pane.FileFocus only once the visual transition
	// has reached the file's footprint at OvertakeZoom (so the toggle
	// button appearing doesn't pop into view mid-animation).
	onComplete func()
}

type transSegment struct {
	path                            []int64
	fromCx, fromCy, fromZoom        float64
	toCx, toCy, toZoom              float64
	durationMs                      float64
}

// ghost is a transient floating render of a node, positioned in screen
// coordinates within a specific pane. screenX/Y is the top-left corner of
// the node's footprint at zoom-corrected size.
type ghost struct {
	node    rpc.Node
	paneID  string
	screenX float64
	screenY float64
}

// dragState tracks an in-progress drag from a node onto the cursor.
//
// `started` is false until the cursor moves more than dragThreshold pixels
// from the mousedown point, so a bare click (down+up with no movement) can
// be distinguished from a drag and treated as "select" instead of "move".
//
// snapshotNode and origin* fields capture where the dragged node started so
// we can render a smooth ghost at the cursor and animate snap-back to the
// original position if the drop is rejected.
type dragState struct {
	originPaneID   string
	nodeID         int64
	cellOffsetX    float64
	cellOffsetY    float64
	startScreenX   float64
	startScreenY   float64
	curScreenX     float64
	curScreenY     float64
	clone          bool
	started        bool
	snapshotNode   rpc.Node
	originScreenX  float64
	originScreenY  float64
	originPaneRect paneRect
}

// dragThreshold is the cursor-movement distance that turns a press into a
// drag. Below this, mousedown→mouseup is treated as a click (select).
const dragThreshold = 4.0

func main() {
	app = &App{
		doc:            js.Global().Get("document"),
		win:            js.Global().Get("window"),
		c:              cache.New(),
		selectedNodeID: map[string]int64{},
		menuHover:      -1,
		gridLoadFailed: map[int64]bool{},
		paneStateStack: map[string][]paneState{},
		fileLastMode:   map[int64]string{},
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

	// Login form submission.
	form := app.doc.Call("getElementById", "login-card")
	form.Call("addEventListener", "submit", js.FuncOf(func(this js.Value, args []js.Value) any {
		args[0].Call("preventDefault")
		app.tryLogin()
		return nil
	}))

	app.installCanvasInput()

	// Probe session.
	go app.bootstrap()

	select {}
}

// bootstrap calls Whoami; if logged in, hides the login overlay and starts
// the canvas client. Otherwise the login form is the visible UI.
func (a *App) bootstrap() {
	var resp rpc.WhoamiResponse
	if status, err := postJSON("/rpc/Whoami", rpc.WhoamiRequest{}, &resp); err != nil || status != 200 {
		// Stay on the login overlay.
		return
	}
	a.user = &resp
	a.afterLogin()
}

func (a *App) tryLogin() {
	usernameVal := a.doc.Call("getElementById", "username").Get("value").String()
	passwordVal := a.doc.Call("getElementById", "password").Get("value").String()
	errEl := a.doc.Call("getElementById", "login-error")
	go func() {
		var resp rpc.LoginResponse
		status, err := postJSON("/rpc/Login", rpc.LoginRequest{Username: usernameVal, Password: passwordVal}, &resp)
		if err != nil {
			errEl.Set("textContent", err.Error())
			return
		}
		if status != 200 {
			errEl.Set("textContent", "login failed")
			return
		}
		a.user = &rpc.WhoamiResponse{UserID: resp.UserID, Username: resp.Username, RootGridID: resp.RootGridID}
		a.afterLogin()
	}()
}

func (a *App) afterLogin() {
	a.doc.Call("getElementById", "login-overlay").Get("style").Set("display", "none")
	a.canvas.Call("focus")
	// Initialize root pane with the user's root grid path (empty).
	a.tree.FocusedPane().Path = nil

	// Restore pane tree from localStorage if available.
	a.loadTreeFromLocalStorage()

	// Subscribe to SSE.
	go a.startSSE()

	// Fetch the root grid.
	a.fetchGrid(a.user.RootGridID)
	a.draw()
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
// same request inside a render loop.
func (a *App) fetchGrid(id int64) {
	if a.gridLoadFailed[id] {
		return
	}
	go func() {
		var resp rpc.GetGridResponse
		status, err := postJSON("/rpc/GetGrid", rpc.GetGridRequest{GridID: id}, &resp)
		if err != nil || status != 200 {
			a.gridLoadFailed[id] = true
			a.draw()
			return
		}
		delete(a.gridLoadFailed, id)
		a.c.PutGrid(resp.Grid, resp.Nodes)
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
	if a.rafScheduled {
		return
	}
	a.rafScheduled = true
	js.Global().Call("requestAnimationFrame", js.FuncOf(func(this js.Value, args []js.Value) any {
		a.rafScheduled = false
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
	delete(a.selectedNodeID, p.ID)
	a.gridLoadFailed = map[int64]bool{}
	a.fetchGrid(a.gridIDForPath(p.Path))
	if tr.onComplete != nil {
		tr.onComplete()
	}
	a.saveTreeToLocalStorage()
	a.draw()
}

// animationDone is called when the active animation reaches its target. It
// clears the ghost and any associated render hides so the cache becomes
// the source of truth again.
func (a *App) animationDone() {
	a.animation = nil
	a.ghost = nil
	a.hiddenObjectID = ""
	a.hiddenPaneID = ""
}

// pushPaneState saves the given state on the stack for paneID. Called when
// a descent begins so the matching ascent can restore exactly this state.
func (a *App) pushPaneState(paneID string, s paneState) {
	a.paneStateStack[paneID] = append(a.paneStateStack[paneID], s)
}

// popPaneState removes and returns the most recent saved state for paneID,
// or nil if the stack is empty (e.g. localStorage didn't carry the stack
// across reload).
func (a *App) popPaneState(paneID string) *paneState {
	stack := a.paneStateStack[paneID]
	if len(stack) == 0 {
		return nil
	}
	last := stack[len(stack)-1]
	a.paneStateStack[paneID] = stack[:len(stack)-1]
	return &last
}


// startSSE opens the EventSource for /rpc/Subscribe and applies events to
// the cache. Reconnects on close after a backoff.
func (a *App) startSSE() {
	es := js.Global().Get("EventSource").New("/rpc/Subscribe")
	es.Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) any {
		raw := args[0].Get("data").String()
		var ev rpc.Event
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			return nil
		}
		if a.c.Apply(ev) {
			a.draw()
		}
		// GridChanged: refetch the affected grid if any pane is looking at it.
		if ev.Kind == rpc.EventGridChanged && ev.GridChanged != nil {
			a.fetchGrid(ev.GridChanged.GridID)
		}
		return nil
	}))
}

// savedClientState is the JSON shape we persist to localStorage so navigation
// state (descent paths, saved viewports) survives a page reload.
type savedClientState struct {
	Tree  *pane.Tree             `json:"tree"`
	Focus string                 `json:"focus"`
	Stack map[string][]paneState `json:"stack"`
	// FileModes remembers the user's last-used mode per file node id so
	// previews and re-descent open in the same mode the user left.
	FileModes map[int64]string `json:"file_modes,omitempty"`
}

// loadTreeFromLocalStorage restores the saved pane tree (and saved-state
// stack) if present. Treats a missing or malformed entry as no-op.
func (a *App) loadTreeFromLocalStorage() {
	v := a.win.Get("localStorage").Call("getItem", "ascent.tree."+strconv.FormatInt(a.user.UserID, 10))
	if v.IsNull() || v.IsUndefined() {
		return
	}
	var saved savedClientState
	if err := json.Unmarshal([]byte(v.String()), &saved); err != nil {
		return
	}
	if saved.Tree == nil {
		return
	}
	a.tree = saved.Tree
	if saved.Focus != "" && a.tree.FindPane(saved.Focus) != nil {
		a.tree.Focus = saved.Focus
	}
	if saved.Stack != nil {
		a.paneStateStack = saved.Stack
	}
	if saved.FileModes != nil {
		a.fileLastMode = saved.FileModes
	}
}

func (a *App) saveTreeToLocalStorage() {
	if a.user == nil {
		return
	}
	b, err := json.Marshal(savedClientState{
		Tree:      a.tree,
		Focus:     a.tree.Focus,
		Stack:     a.paneStateStack,
		FileModes: a.fileLastMode,
	})
	if err != nil {
		return
	}
	a.win.Get("localStorage").Call("setItem", "ascent.tree."+strconv.FormatInt(a.user.UserID, 10), string(b))
}

// gridIDForPath walks the pane's descent path and returns the grid id at the
// leaf. Returns root if the path is empty or stale prefixes don't resolve.
func (a *App) gridIDForPath(p []int64) int64 {
	if a.user == nil {
		return 0
	}
	gid := a.user.RootGridID
	for _, wellID := range p {
		g, ok := a.c.Grid(gid)
		if !ok {
			a.fetchGrid(gid)
			return gid
		}
		w, ok := g.Nodes[wellID]
		if !ok {
			return gid
		}
		gid = w.ChildGridID
	}
	return gid
}

// paneViewRect computes the framed rectangle for a pane in the leaf grid's
// coordinates. Used as the locality token in mutating RPCs.
func (a *App) paneViewRect(p *pane.Pane, paneScreen dragdrop.Pane) rpc.ViewRect {
	cellSize := paneScreen.CellPx * paneScreen.Zoom
	visW := paneScreen.ScreenW / cellSize
	visH := paneScreen.ScreenH / cellSize
	left := p.Cx - visW/2
	top := p.Cy - visH/2
	return rpc.ViewRect{
		X: int64(left) - 1,
		Y: int64(top) - 1,
		W: int64(visW) + 3,
		H: int64(visH) + 3,
	}
}
