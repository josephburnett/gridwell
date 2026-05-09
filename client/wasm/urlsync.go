//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/josephburnett/ascent/client/url"
	"github.com/josephburnett/ascent/internal/rpc"
)

// urlUpdateDebounceMs is how long we wait after the last state change
// before calling history.replaceState. Long enough that wheel/keystroke
// bursts coalesce into one URL update; short enough that a quick
// bookmark / copy-paste reflects the latest state.
const urlUpdateDebounceMs = 150

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
	if a.user == nil {
		return
	}
	state := a.encodeFocusedPaneURL()
	raw := url.Encode(state)
	js.Global().Get("history").Call("replaceState", nil, "", raw)
}

// encodeFocusedPaneURL builds a url.State from the focused pane.
//   - If the pane is in file mode: NodeIDs = path + FileFocus.
//     For text mode, fill cursor (col, row) read from the textarea.
//   - Otherwise: NodeIDs = path; viewport from Cx, Cy, Zoom.
func (a *App) encodeFocusedPaneURL() url.State {
	p := a.tree.FocusedPane()
	if p == nil {
		return url.State{}
	}
	var s url.State
	if p.FileFocus != 0 {
		s.NodeIDs = append(append([]int64(nil), p.Path...), p.FileFocus)
		if p.FileMode == "text" {
			col, row := a.textareaCursorRowCol()
			s.CursorMode = true
			s.Col = col
			s.Row = row
		}
		return s
	}
	s.NodeIDs = append([]int64(nil), p.Path...)
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
// node-id list against the user's grids to set up the focused pane.
// Stale ids → truncate. After applying, replaceState the (possibly
// truncated) URL so what's in the bar matches what's on screen.
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
	rootID := a.user.RootGridID
	if len(state.NodeIDs) == 0 {
		a.fetchGrid(rootID)
		a.draw()
		// Apply viewport even when the path is empty — the user might
		// have bookmarked a viewport at root.
		if state.X != 0 || state.Y != 0 || state.Zoom != 0 {
			p := a.tree.FocusedPane()
			if p != nil {
				p.Cx = state.X
				p.Cy = state.Y
				if state.Zoom > 0 {
					p.Zoom = state.Zoom
				}
			}
			a.draw()
		}
		a.scheduleURLUpdate()
		return
	}

	// Walk the path, fetching each grid as we go. If a node id can't
	// be resolved (missing from its parent grid), truncate at that
	// point — silently, no error, just stop.
	gid := rootID
	resolvedPath := []int64{}
	var fileNodeID int64
	for i, id := range state.NodeIDs {
		// Ensure the current grid is cached (synchronously is impossible
		// — we have to fetch and wait).
		if _, ok := a.c.Grid(gid); !ok {
			if !a.fetchGridSync(gid) {
				break
			}
		}
		g, _ := a.c.Grid(gid)
		n, ok := g.Nodes[id]
		if !ok {
			break // truncate
		}
		isLast := i == len(state.NodeIDs)-1
		switch n.Type {
		case "well":
			if n.Capped {
				break
			}
			resolvedPath = append(resolvedPath, id)
			gid = n.ChildGridID
		case "file":
			if !isLast {
				// File mid-path is nonsense; truncate.
				break
			}
			fileNodeID = id
		}
	}

	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	p.Path = resolvedPath
	if fileNodeID != 0 {
		p.FileFocus = fileNodeID
		if state.CursorMode {
			p.FileMode = "text"
			a.fileLastMode[fileNodeID] = "text"
		} else {
			p.FileMode = "rendered"
		}
		p.FileZoom = fileInitialZoom(a.width, a.height)
		a.fetchBlobAndSetCursor(fileNodeID, state)
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
	a.c.PutGrid(resp.Grid, resp.Nodes)
	return true
}

// fetchBlobAndSetCursor pulls the file's bytes and, once they're in
// the cache, places the cursor at (state.Col, state.Row) inside the
// textarea. Asynchronous because GetBlob is over the wire.
func (a *App) fetchBlobAndSetCursor(fileNodeID int64, state url.State) {
	gid := a.gridIDForPath(a.tree.FocusedPane().Path)
	g, ok := a.c.Grid(gid)
	if !ok {
		return
	}
	file, ok := g.Nodes[fileNodeID]
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
