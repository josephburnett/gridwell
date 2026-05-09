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
}

// dragState tracks an in-progress drag from a node onto the cursor.
type dragState struct {
	originPaneID string
	nodeID       int64
	cellOffsetX  float64
	cellOffsetY  float64
	curScreenX   float64
	curScreenY   float64
	clone        bool
}

func main() {
	app = &App{
		doc: js.Global().Get("document"),
		win: js.Global().Get("window"),
		c:   cache.New(),
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

// fetchGrid issues GetGrid and stores the result in the cache.
func (a *App) fetchGrid(id int64) {
	go func() {
		var resp rpc.GetGridResponse
		status, err := postJSON("/rpc/GetGrid", rpc.GetGridRequest{GridID: id}, &resp)
		if err != nil || status != 200 {
			return
		}
		a.c.PutGrid(resp.Grid, resp.Nodes)
		a.draw()
	}()
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

// loadTreeFromLocalStorage restores the saved pane tree if present. Treats a
// missing or malformed entry as no-op.
func (a *App) loadTreeFromLocalStorage() {
	v := a.win.Get("localStorage").Call("getItem", "ascent.tree."+strconv.FormatInt(a.user.UserID, 10))
	if v.IsNull() || v.IsUndefined() {
		return
	}
	var saved struct {
		Tree  *pane.Tree `json:"tree"`
		Focus string     `json:"focus"`
	}
	if err := json.Unmarshal([]byte(v.String()), &saved); err != nil {
		return
	}
	if saved.Tree == nil {
		return
	}
	// Validate: prune stale paths against known wells. We don't know any
	// wells yet because we haven't fetched anything; pane validation
	// happens after each grid fetch.
	a.tree = saved.Tree
	if saved.Focus != "" && a.tree.FindPane(saved.Focus) != nil {
		a.tree.Focus = saved.Focus
	}
}

func (a *App) saveTreeToLocalStorage() {
	if a.user == nil {
		return
	}
	b, err := json.Marshal(struct {
		Tree  *pane.Tree `json:"tree"`
		Focus string     `json:"focus"`
	}{Tree: a.tree, Focus: a.tree.Focus})
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
