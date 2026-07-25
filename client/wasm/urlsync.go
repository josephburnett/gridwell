//go:build js && wasm

package main

import (
	"context"
	"slices"
	"strings"
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

// framingSaveDebounceMs is the delay before persisting settled grid framing
// (a well's view_*, a plugin's root view) back to the server. Longer than
// the URL debounce so a continuous pan/zoom doesn't spam the server with
// intermediate values — only the resting state matters.
const framingSaveDebounceMs = 600

// scheduleFramingSave arms the debounced framing persister. Armed from
// draw() — every state change redraws, so there is no per-gesture
// persistence hook to forget (the workspace-layout persister's shape,
// charter §1). Before this existed (issue #190) framing was written ONLY
// at ascent, so leaving a grid any other way — descending deeper, a pane
// switch, a portal, a URL edit, a reload — silently lost the viewport.
func (a *App) scheduleFramingSave() {
	if a.sched.framingSaveScheduled {
		return
	}
	a.sched.framingSaveScheduled = true
	js.Global().Call("setTimeout", a.sched.framingSaveCb, framingSaveDebounceMs)
}

// flushFramingSave persists every pane's settled grid framing now. The
// writers it dispatches to no-op when nothing moved, so quiet calls are
// free. Skipped entirely while a transition animates: animated viewport
// values are presentation, not user state — persisting one would store
// framing the user never set (the guiding rule). draw() re-arms the
// debounce on the next frame, so the flush lands after the animation.
func (a *App) flushFramingSave() {
	if a.transition != nil {
		return
	}
	a.tree.Walk(func(p *pane.Pane) { a.persistPaneFraming(p) })
}

// persistPaneFraming writes pane p's current place framing — the same
// writes an ascent flushes, fired without waiting for one:
//   - descended into a child grid: the leaf well's view_* (SetWellView);
//   - at a portal target: the containing link tile's view_* under the
//     frame's anchor (a node-grid tile write routes onto SetRootView);
//   - at a plugin root with no containing tile: the root view directly.
//
// Text-descent framing (SetTextView) stays with the text machinery. No-op
// when the place is unresolvable (uncached parent grid) — the next settle
// retries.
func (a *App) persistPaneFraming(p *pane.Pane) {
	if p.TextFocus != "" {
		return
	}
	if len(p.Path) > 0 {
		parentPath := p.Path[:len(p.Path)-1]
		parentGridID := a.gridIDForPathFrom(p.Anchor, parentPath)
		if parentGridID == "" {
			return
		}
		g, ok := a.c.Grid(parentGridID)
		if !ok {
			return
		}
		w, ok := g.Tiles[p.Path[len(p.Path)-1]]
		if !ok {
			return
		}
		a.persistWellView(p, &w, p.Anchor, slices.Clone(parentPath))
		return
	}
	if f, ok := p.TopFrame(); ok {
		if well := a.portalWellForFrame(p, f); well != nil {
			a.persistWellView(p, well, f.Anchor, slices.Clone(f.Path))
			return
		}
	}
	a.persistPluginRootView(p)
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
//
// url.Encode rebuilds the query from scratch, so any param it doesn't know
// is dropped on the first write — including the e2e harness gate. Preserve
// `e2e=1` explicitly: without it, the FIRST replaceState de-instruments the
// page and any spec that reloads mid-test loses the testhook (found by the
// #193 reload spec; hook-gating.spec's assert was racing the debounce).
func (a *App) replaceURLNow() {
	state := a.encodeFocusedPaneURL()
	raw := url.Encode(state)
	if strings.Contains(js.Global().Get("location").Get("search").String(), "e2e=1") {
		if strings.ContainsRune(raw, '?') {
			raw += "&e2e=1"
		} else {
			raw += "?e2e=1"
		}
	}
	js.Global().Get("history").Call("replaceState", nil, "", raw)
}

// encodeFocusedPaneURL builds a url.State from the focused pane.
//   - If the pane is in text mode: TileIDs = path + TextFocus.
//     For text mode, fill cursor (col, row) read from the textarea.
//   - Otherwise: TileIDs = path; viewport from Cx, Cy, Zoom.
func (a *App) encodeFocusedPaneURL() url.State {
	// Inside a workspace, the pane tile IS the place: the interior (every
	// pane's anchor/path/viewport) is server-owned by the layout blob, so
	// nothing else rides the URL (one fact, one owner — the blob wins).
	if top := a.ws.Top(); top != nil {
		return url.State{Workspace: top.TileID}
	}
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
	// Anchor records which grid namespace the pane sits inside, so a reload
	// re-enters the same place and walks the path within it. Home — the first
	// plugin's root grid — encodes as empty, keeping "/" the home URL.
	if p.Anchor != a.home {
		s.Anchor = p.Anchor
	}
	return s
}

// textareaCursorRowCol returns the cursor position in the file
// textarea as (column, row), 0-indexed. Returns (0, 0) if the
// textarea isn't visible.
func (a *App) textareaCursorRowCol() (int, int) {
	if a.textTextarea.IsUndefined() || a.textTextarea.IsNull() {
		return 0, 0
	}
	val := a.textTextarea.Get("value").String()
	off := a.textTextarea.Get("selectionStart").Int()
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

	// A workspace place restores the innermost pane tile from its blob; the
	// workspace stack stays empty above it (nesting is session-only), so a
	// bar ascent falls back to the pane tile's containing grid.
	if state.Workspace != "" {
		go a.bootWorkspace(state.Workspace)
		return
	}

	p := a.tree.FocusedPane()
	if p == nil {
		return
	}

	// No anchor → home, the landing page (already the pane's boot anchor).
	// The walk below still applies so "/" plus a viewport restores.
	if state.Anchor == "" {
		state.Anchor = a.home
		if state.Anchor == "" {
			// Bootstrap couldn't learn any home (no plugins, no node
			// identity); the error is already on the strip. Nothing to
			// restore into.
			a.draw()
			a.scheduleURLUpdate()
			return
		}
	}
	p.Anchor = state.Anchor

	// The URL's path segments are bare well ids within the anchor's grid
	// namespace; qualify them with the anchor's NAMESPACE — everything up to
	// its last segment — so they match the grid's keys. For a plain plugin
	// root ("uuid/1") that is the plugin uuid; for a remote grid reached
	// through a mount ("ssh1/rp1/1") it is the whole chain prefix.
	anchorPrefix := rpc.NamespaceOf(state.Anchor)
	qualified := make([]string, len(state.TileIDs))
	for i, id := range state.TileIDs {
		qualified[i] = rpc.QualifyID(anchorPrefix, id)
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
	resolvedPath, textTileID := urlwalk.Walk(state.Anchor, qualified,
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
	if textTileID != "" {
		p.TextFocus = textTileID
		// Mode follows the tile's persisted text_mode; a URL that encodes
		// a text cursor forces text mode. Scale is fixed; scroll restores
		// from the tile's stored text_y.
		if file, ok := a.cachedFile(p, textTileID); ok {
			p.TextMode = file.TextMode
			p.TextScrollY = float64(file.TextY)
		}
		if state.CursorMode {
			p.TextMode = rpc.TextModeText
		}
		if p.TextMode == "" {
			p.TextMode = rpc.TextModeText
		}
		p.TextZoom = a.textScaleFor(p) // base × the tile's content zoom (issue #82)
		a.fetchBlobAndSetCursor(textTileID, state)
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
func (a *App) fetchBlobAndSetCursor(textTileID string, state url.State) {
	gid := a.gridIDForPane(a.tree.FocusedPane())
	g, ok := a.c.Grid(gid)
	if !ok {
		return
	}
	if _, ok := g.Tiles[textTileID]; !ok {
		return
	}
	go func() {
		// Content is routable by tile id (GetTileContent); blob ids carry no
		// plugin namespace and aren't routable on their own. Store it in the
		// content store — the single text-body store the overlay reads from.
		data, version, err := a.cl.GetTileContent(context.Background(), textTileID)
		if err != nil {
			// The file the URL pointed at stays blank — say why (charter §6).
			a.surfaceRPCError("GetTileContent", err)
			return
		}
		a.c.PutFetchedContent(textTileID, data, version)
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
	if a.textTextarea.IsUndefined() || a.textTextarea.IsNull() {
		return
	}
	val := a.textTextarea.Get("value").String()
	off := textcursor.OffsetFromRowCol(val, row, col)
	a.textTextarea.Call("focus")
	a.textTextarea.Call("setSelectionRange", off, off)
}
