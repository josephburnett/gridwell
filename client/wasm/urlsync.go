//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/josephburnett/gridwell/client/url"
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

// scheduleRootViewSave queues a debounced SetGridDefaultView for the
// user's root grid. Cheap to call from any code path that mutates the
// focused pane's viewport; no-op when the focused pane isn't at root.
func (a *App) scheduleRootViewSave() {
	if a.rootViewSaveScheduled {
		return
	}
	if p := a.tree.FocusedPane(); p == nil || len(p.Path) > 0 || p.FileFocus != 0 {
		return
	}
	a.rootViewSaveScheduled = true
	js.Global().Call("setTimeout", a.rootViewSaveCb, rootViewSaveDebounceMs)
}

// flushRootViewSave reads the focused pane's viewport and posts
// SetGridDefaultView for the user's root grid. Triggered by the
// debounce timer; safe to call manually.
func (a *App) flushRootViewSave() {
	if a.rootGridID == 0 {
		return
	}
	p := a.tree.FocusedPane()
	if p == nil || len(p.Path) > 0 || p.FileFocus != 0 {
		return
	}
	zoom := p.Zoom
	if zoom <= 0 {
		zoom = 1.0
	}
	req := rpc.SetGridDefaultViewRequest{
		GridID: a.rootGridID,
		Cx:     p.Cx,
		Cy:     p.Cy,
		Zoom:   zoom,
	}
	go func() {
		var resp rpc.SetGridDefaultViewResponse
		_, _ = postJSON("/rpc/SetGridDefaultView", req, &resp)
	}()
}

// scheduleURLUpdate marks that the URL is out of date and arranges for
// it to be replaced on the next debounce tick. Cheap to call from any
// state-mutating code path.
func (a *App) scheduleURLUpdate() {
	if a.urlUpdateScheduled {
		return
	}
	a.urlUpdateScheduled = true
	cb := js.FuncOf(func(this js.Value, args []js.Value) any {
		a.urlUpdateScheduled = false
		a.replaceURLNow()
		return nil
	})
	js.Global().Call("setTimeout", cb, urlUpdateDebounceMs)
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
//   - If the pane is in file mode: TileIDs = path + FileFocus.
//     For text mode, fill cursor (col, row) read from the textarea.
//   - Otherwise: TileIDs = path; viewport from Cx, Cy, Zoom.
func (a *App) encodeFocusedPaneURL() url.State {
	p := a.tree.FocusedPane()
	if p == nil {
		return url.State{}
	}
	var s url.State
	if p.FileFocus != 0 {
		s.TileIDs = append(append([]int64(nil), p.Path...), p.FileFocus)
		if p.FileMode == "text" {
			col, row := a.textareaCursorRowCol()
			s.CursorMode = true
			s.Col = col
			s.Row = row
		}
		return s
	}
	s.TileIDs = append([]int64(nil), p.Path...)
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
	if off > len(val) {
		off = len(val)
	}
	if off < 0 {
		off = 0
	}
	row := 0
	col := 0
	for i := 0; i < off; i++ {
		if val[i] == '\n' {
			row++
			col = 0
		} else {
			col++
		}
	}
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
		// Block on the fetch so we can read the grid's stored
		// DefaultView before drawing — otherwise the user sees a
		// flash of the (0,0,1) default and then jumps to their
		// preferred viewport once the response arrives.
		a.fetchGridSync(rootID)
		p := a.tree.FocusedPane()
		if p != nil {
			if state.X != 0 || state.Y != 0 || state.Zoom != 0 {
				// URL viewport wins over stored default.
				p.Cx = state.X
				p.Cy = state.Y
				if state.Zoom > 0 {
					p.Zoom = state.Zoom
				}
			} else if g, ok := a.c.Grid(rootID); ok && g.Meta.DefaultZoom > 0 {
				p.Cx = g.Meta.DefaultViewCx
				p.Cy = g.Meta.DefaultViewCy
				p.Zoom = g.Meta.DefaultZoom
			}
		}
		a.draw()
		a.scheduleURLUpdate()
		return
	}

	// Walk the path, fetching each grid as we go. An id that's not
	// in the current grid is skipped (we stay put and try the next id).
	// A non-leaf file ends the descent — we stop with what we've got.
	gid := rootID
	resolvedPath := []int64{}
	var fileTileID int64
walk:
	for i, id := range state.TileIDs {
		isLast := i == len(state.TileIDs)-1
		// Ensure the current grid is cached (synchronously is impossible
		// — we have to fetch and wait).
		if _, ok := a.c.Grid(gid); !ok {
			if !a.fetchGridSync(gid) {
				break walk
			}
		}
		g, _ := a.c.Grid(gid)
		n, ok := g.Tiles[id]
		if !ok {
			// Skip unknown id — it might be a stale or bogus entry.
			// Keep the current grid and continue with the next id.
			continue
		}
		switch n.Type {
		case "well":
			resolvedPath = append(resolvedPath, id)
			gid = n.ChildGridID
		case "file":
			if !isLast {
				// File mid-path is nonsense; ignore and keep walking.
				continue
			}
			fileTileID = id
		}
	}

	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	p.Path = resolvedPath
	if fileTileID != 0 {
		p.FileFocus = fileTileID
		// Mode follows the tile's persisted file_mode; a URL that encodes
		// a text cursor forces text mode. Scale is fixed; scroll restores
		// from the tile's stored view_y.
		if file, ok := a.cachedFile(p.Path, fileTileID); ok {
			p.FileMode = file.FileMode
			p.FileScrollY = float64(file.ViewY)
		}
		if state.CursorMode {
			p.FileMode = "text"
		}
		if p.FileMode == "" {
			p.FileMode = "text"
		}
		p.FileZoom = fileFixedScale
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
	var resp rpc.GetGridResponse
	status, err := postJSON("/rpc/GetGrid", rpc.GetGridRequest{GridID: id}, &resp)
	if err != nil || status != 200 {
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
		var resp rpc.GetBlobResponse
		status, err := postJSON("/rpc/GetBlob", rpc.GetBlobRequest{BlobID: file.BlobID}, &resp)
		if err != nil || status != 200 {
			return
		}
		a.c.PutBlob(file.BlobID, resp.Data, resp.MimeType)
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
	off := offsetFromRowCol(val, row, col)
	a.fileTextarea.Call("focus")
	a.fileTextarea.Call("setSelectionRange", off, off)
}

// offsetFromRowCol walks the source counting newlines until row, then
// adds col (clamped to that line's length). Returns the offset within
// the source.
func offsetFromRowCol(src string, row, col int) int {
	if row < 0 {
		row = 0
	}
	if col < 0 {
		col = 0
	}
	r := 0
	lineStart := 0
	for i := 0; i < len(src); i++ {
		if r == row {
			break
		}
		if src[i] == '\n' {
			r++
			lineStart = i + 1
		}
	}
	if r != row {
		// Past end of file — return end.
		return len(src)
	}
	// Find the end of this line.
	lineEnd := lineStart
	for lineEnd < len(src) && src[lineEnd] != '\n' {
		lineEnd++
	}
	if lineStart+col > lineEnd {
		return lineEnd
	}
	return lineStart + col
}
