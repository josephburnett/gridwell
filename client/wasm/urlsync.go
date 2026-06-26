//go:build js && wasm

package main

import (
	"context"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/pane"
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

// scheduleRootViewSave is a no-op: there is no privileged root grid anymore, so
// no root viewport is persisted server-side. A pane's viewport lives in the URL
// (replaceURLNow) instead. Kept as a no-op so the viewport-mutating call sites
// don't need to special-case the rootless model.
func (a *App) scheduleRootViewSave() {}

// flushRootViewSave is a no-op (see scheduleRootViewSave).
func (a *App) flushRootViewSave() {}

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
	if p.TextFocus != "" {
		isText := p.TextMode == rpc.TextModeText
		var col, row int
		if isText {
			col, row = a.textareaCursorRowCol()
		}
		s = url.TextState(p.Path, p.TextFocus, isText, col, row)
	} else {
		s = url.GridState(p.Path, p.Cx, p.Cy, p.Zoom)
	}
	// Anchor records which plugin root the pane sits inside (empty = launcher),
	// so a reload re-enters the same plugin and walks the path within it.
	s.Anchor = p.Anchor
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

	p := a.tree.FocusedPane()
	if p == nil {
		return
	}

	// No anchor → the launcher start screen (the pane is already there).
	if state.Anchor == "" {
		p.Anchor = ""
		p.Path = nil
		a.draw()
		a.scheduleURLUpdate()
		return
	}
	p.Anchor = state.Anchor

	// The URL's path segments are bare well ids within the anchor's plugin;
	// qualify them with the anchor's plugin uuid so they match the grid's keys.
	anchorUUID := uuidOf(state.Anchor)
	qualified := make([]string, len(state.TileIDs))
	for i, id := range state.TileIDs {
		qualified[i] = anchorUUID + "/" + id
	}

	// No path → sit at the anchor's root grid.
	if len(qualified) == 0 {
		a.fetchGridSync(state.Anchor)
		if bv := url.BootViewport(state.X, state.Y, state.Zoom, 0, 0, 1); bv.Apply {
			p.Cx = bv.Cx
			p.Cy = bv.Cy
			if bv.SetZoom {
				p.Zoom = bv.Zoom
			}
		}
		a.draw()
		a.scheduleURLUpdate()
		return
	}

	// Walk the path from the anchor, fetching each grid as we go. The pure walk
	// skips ids missing from the current grid, descends at well boundaries, and
	// stops at a content leaf.
	resolvedPath, fileTileID := urlwalk.Walk(state.Anchor, qualified,
		func(gid string) (map[string]urlwalk.Tile, bool) {
			if _, ok := a.c.Grid(gid); !ok {
				if !a.fetchGridSync(gid) {
					return nil, false
				}
			}
			g, _ := a.c.Grid(gid)
			tiles := make(map[string]urlwalk.Tile, len(g.Tiles))
			for id, n := range g.Tiles {
				tiles[id] = urlwalk.Tile{
					ChildGridID: n.ChildGridID,
					IsWell:      rpc.IsWellKind(n.Kind),
					IsContent:   rpc.IsContentDescentKind(n.Kind),
				}
			}
			return tiles, true
		})

	p.Path = resolvedPath
	if fileTileID != "" {
		p.TextFocus = fileTileID
		// Mode follows the tile's persisted text_mode; a URL that encodes
		// a text cursor forces text mode. Scale is fixed; scroll restores
		// from the tile's stored text_y.
		if file, ok := a.cachedFile(p, fileTileID); ok {
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

	a.fetchGrid(a.gridIDForPane(p))
	a.draw()
	// Replace the URL in case we truncated.
	a.scheduleURLUpdate()
}

// cachedFile returns the file tile at the leaf of `path` with id
// tileID, if cached. Used during URL boot to honor a previously
// stored ViewZoom before the blob arrives.
func (a *App) cachedFile(p *pane.Pane, tileID string) (rpc.Tile, bool) {
	gid := a.gridIDForPane(p)
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
func (a *App) fetchGridSync(id string) bool {
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
func (a *App) fetchBlobAndSetCursor(fileTileID string, state url.State) {
	gid := a.gridIDForPane(a.tree.FocusedPane())
	g, ok := a.c.Grid(gid)
	if !ok {
		return
	}
	file, ok := g.Tiles[fileTileID]
	if !ok {
		return
	}
	go func() {
		// Content is routable by tile id (GetTileContent); blob ids carry no
		// plugin namespace and aren't routable on their own.
		data, err := a.cl.GetTileContent(context.Background(), fileTileID)
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
