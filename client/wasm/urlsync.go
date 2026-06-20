//go:build js && wasm

package main

import (
	"context"
	"slices"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/textcursor"
	"github.com/josephburnett/gridwell/client/url"
	"github.com/josephburnett/gridwell/client/urlwalk"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// urlUpdateDebounceMs is how long we wait after the last state change
// before calling history.replaceState. Long enough that wheel/keystroke
// bursts coalesce into one URL update; short enough that a quick
// bookmark / copy-paste reflects the latest state.
const urlUpdateDebounceMs = 150

// rootViewSaveDebounceMs is the delay before persisting a changed root
// viewport to the server's default_view. Longer than the URL debounce
// so a continuous pan/zoom doesn't spam the server with intermediate
// values — we only care about the resting state.
const rootViewSaveDebounceMs = 600

// scheduleRootViewSave queues a debounced SetRootView for the user's root
// grid. Cheap to call from any code path that mutates the focused pane's
// viewport; no-op when the focused pane isn't at root.
func (a *App) scheduleRootViewSave() {
	if a.sched.rootViewSaveScheduled {
		return
	}
	if p := a.tree.FocusedPane(); p == nil || len(p.Path) > 0 || p.TextFocus != 0 {
		return
	}
	a.sched.rootViewSaveScheduled = true
	js.Global().Call("setTimeout", a.sched.rootViewSaveCb, rootViewSaveDebounceMs)
}

// flushRootViewSave reads the focused pane's viewport and posts
// SetRootView. Triggered by the debounce timer; safe to call manually.
func (a *App) flushRootViewSave() {
	if a.rootGridID == 0 {
		return
	}
	p := a.tree.FocusedPane()
	if p == nil || len(p.Path) > 0 || p.TextFocus != 0 {
		return
	}
	zoom := p.Zoom
	if zoom <= 0 {
		zoom = 1.0
	}
	a.rootViewCx = p.Cx
	a.rootViewCy = p.Cy
	a.rootViewZoom = zoom
	req := &rpc.SetRootViewRequest{Cx: p.Cx, Cy: p.Cy, Zoom: zoom}
	go func() {
		_ = a.cl.SetRootView(context.Background(), req)
	}()
}

// scheduleURLUpdate marks that the URL is out of date and arranges for
// it to be replaced on the next debounce tick. Cheap to call from any
// state-mutating code path.
func (a *App) scheduleURLUpdate() {
	if a.sched.urlUpdateScheduled {
		return
	}
	a.sched.urlUpdateScheduled = true
	js.Global().Call("setTimeout", a.sched.urlUpdateCb, urlUpdateDebounceMs)
}

// replaceURLNow encodes the focused pane's state and calls
// history.replaceState. Idempotent; safe even when no user change has
// happened.
func (a *App) replaceURLNow() {
	if a.rootGridID == 0 {
		return
	}
	state := a.encodeFocusedPaneURL()
	raw := url.Encode(state)
	js.Global().Get("history").Call("replaceState", nil, "", raw)
}

// encodeFocusedPaneURL builds a url.State from the focused pane.
//   - If the pane is in text mode: TileIDs = path + TextFocus.
//     For text mode, fill cursor (col, row) read from the textarea.
//   - Otherwise: TileIDs = path; viewport from Cx, Cy, Zoom.
func (a *App) encodeFocusedPaneURL() url.State {
	p := a.tree.FocusedPane()
	if p == nil {
		return url.State{}
	}
	var s url.State
	if p.TextFocus != 0 {
		s.TileIDs = append(slices.Clone(p.Path), p.TextFocus)
		if p.TextMode == rpc.TextModeText {
			col, row := a.textareaCursorRowCol()
			s.CursorMode = true
			s.Col = col
			s.Row = row
		}
		return s
	}
	s.TileIDs = slices.Clone(p.Path)
	s.X = p.Cx
	s.Y = p.Cy
	s.Zoom = p.Zoom
	return s
}

// textareaCursorRowCol returns the cursor position in the file
// textarea as (column, row), 0-indexed. Returns (0, 0) if the
// textarea isn't visible.
func (a *App) textareaCursorRowCol() (int, int) {
	if a.fileTextarea.IsUndefined() || a.fileTextarea.IsNull() {
		return 0, 0
	}
	val := a.fileTextarea.Get("value").String()
	off := a.fileTextarea.Get("selectionStart").Int()
	row, col := textcursor.RowColFromOffset(val, off)
	return col, row
}

// applyURLOnBoot reads window.location, decodes it, and walks the
// tile-id list against the user's grids to set up the focused pane.
// Loose on input: an id that's missing from the current grid is
// silently skipped — we stay in the same grid and try the next id.
// This lets URLs like `/g/.../19/9999/15/14/12` (with 9999 invalid)
// still resolve down to 12. After applying, replaceState the cleaned
// URL so what's in the bar matches what's on screen.
func (a *App) applyURLOnBoot() {
	loc := js.Global().Get("location")
	raw := loc.Get("pathname").String()
	if s := loc.Get("search").String(); s != "" {
		raw += s
	}
	state, err := url.Decode(raw)
	if err != nil {
		// Bad URL — drop to root.
		state = url.State{}
	}

	// Always start by fetching the user's root grid so the walk has
	// something to read. If the URL has no path, we're done.
	rootID := a.rootGridID
	if len(state.TileIDs) == 0 {
		a.fetchGridSync(rootID)
		p := a.tree.FocusedPane()
		if p != nil {
			if state.X != 0 || state.Y != 0 || state.Zoom != 0 {
				// URL viewport wins over the bootstrap-supplied root view.
				p.Cx = state.X
				p.Cy = state.Y
				if state.Zoom > 0 {
					p.Zoom = state.Zoom
				}
			} else if a.rootViewZoom > 0 {
				p.Cx = a.rootViewCx
				p.Cy = a.rootViewCy
				p.Zoom = a.rootViewZoom
			}
		}
		a.draw()
		a.scheduleURLUpdate()
		return
	}

	// Walk the path, fetching each grid as we go (the lookup closure does
	// the cache-or-fetch). The pure walk skips ids missing from the current
	// grid, descends at well boundaries, and stops at a content leaf.
	resolvedPath, fileTileID := urlwalk.Walk(rootID, state.TileIDs,
		func(gid int64) (map[int64]urlwalk.Tile, bool) {
			if _, ok := a.c.Grid(gid); !ok {
				if !a.fetchGridSync(gid) {
					return nil, false
				}
			}
			g, _ := a.c.Grid(gid)
			tiles := make(map[int64]urlwalk.Tile, len(g.Tiles))
			for id, n := range g.Tiles {
				tiles[id] = urlwalk.Tile{
					ChildGridID: n.ChildGridID,
					IsWell:      rpc.IsWellKind(n.Kind),
					IsContent:   rpc.IsContentDescentKind(n.Kind),
				}
			}
			return tiles, true
		})

	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	p.Path = resolvedPath
	if fileTileID != 0 {
		p.TextFocus = fileTileID
		// Mode follows the tile's persisted text_mode; a URL that encodes
		// a text cursor forces text mode. Scale is fixed; scroll restores
		// from the tile's stored text_y.
		if file, ok := a.cachedFile(p.Path, fileTileID); ok {
			p.TextMode = file.TextMode
			p.TextScrollY = float64(file.TextY)
		}
		if state.CursorMode {
			p.TextMode = rpc.TextModeText
		}
		if p.TextMode == "" {
			p.TextMode = rpc.TextModeText
		}
		p.TextZoom = fileFixedScale
		a.fetchBlobAndSetCursor(fileTileID, state)
		// Refresh overlay so the textarea (text mode) appears.
		a.refreshFileOverlay()
	} else {
		p.Cx = state.X
		p.Cy = state.Y
		if state.Zoom > 0 {
			p.Zoom = state.Zoom
		}
	}

	a.fetchGrid(a.gridIDForPath(p.Path))
	a.draw()
	// Replace the URL in case we truncated.
	a.scheduleURLUpdate()
}

// cachedFile returns the file tile at the leaf of `path` with id
// tileID, if cached. Used during URL boot to honor a previously
// stored ViewZoom before the blob arrives.
func (a *App) cachedFile(path []int64, tileID int64) (rpc.Tile, bool) {
	gid := a.gridIDForPath(path)
	g, ok := a.c.Grid(gid)
	if !ok {
		return rpc.Tile{}, false
	}
	n, ok := g.Tiles[tileID]
	return n, ok
}

// fetchGridSync fetches a grid and waits for the result. Returns true
// on success. Used during URL walk where we need the cache populated
// before continuing the walk.
func (a *App) fetchGridSync(id int64) bool {
	if a.gridLoadFailed[id] {
		return false
	}
	if _, ok := a.c.Grid(id); ok {
		return true
	}
	resp, err := a.cl.GetGrid(context.Background(), id)
	if err != nil {
		a.gridLoadFailed[id] = true
		return false
	}
	delete(a.gridLoadFailed, id)
	a.c.PutGrid(resp.Grid, resp.Tiles)
	return true
}

// fetchBlobAndSetCursor pulls the file's bytes and, once they're in
// the cache, places the cursor at (state.Col, state.Row) inside the
// textarea. Asynchronous because GetBlob is over the wire.
func (a *App) fetchBlobAndSetCursor(fileTileID int64, state url.State) {
	gid := a.gridIDForPath(a.tree.FocusedPane().Path)
	g, ok := a.c.Grid(gid)
	if !ok {
		return
	}
	file, ok := g.Tiles[fileTileID]
	if !ok {
		return
	}
	go func() {
		data, err := a.cl.GetBlob(context.Background(), file.BlobID)
		if err != nil {
			return
		}
		a.c.PutBlob(file.BlobID, data)
		// Refresh the overlay (in text mode this seeds the textarea
		// from the blob), then place the cursor.
		a.refreshFileOverlay()
		if state.CursorMode {
			a.placeCursorAt(state.Col, state.Row)
		}
		a.draw()
	}()
}

// placeCursorAt converts (col, row) into a character offset and
// applies it to the textarea via setSelectionRange. No-op if the
// textarea isn't ready.
func (a *App) placeCursorAt(col, row int) {
	if a.fileTextarea.IsUndefined() || a.fileTextarea.IsNull() {
		return
	}
	val := a.fileTextarea.Get("value").String()
	off := textcursor.OffsetFromRowCol(val, row, col)
	a.fileTextarea.Call("focus")
	a.fileTextarea.Call("setSelectionRange", off, off)
}
