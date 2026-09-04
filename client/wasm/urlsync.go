//go:build js && wasm

package main

import (
	"context"
	"github.com/josephburnett/gridwell/client/textedit"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/textcursor"
	"github.com/josephburnett/gridwell/client/urlwalk"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// urlUpdateDebounceMs is how long we wait after the last state change
// before calling history.replaceState. Long enough that wheel/keystroke
// bursts coalesce into one URL update; short enough that a quick
// bookmark / copy-paste reflects the latest state.
const urlUpdateDebounceMs = 150

// framingSaveDebounceMs is the delay before persisting settled grid framing
// (a doorway's framing, a root grid's framing) back to the server. Longer than
// the URL debounce so a continuous pan/zoom doesn't spam the server with
// intermediate values — only the resting state matters.
const framingSaveDebounceMs = 600

// scheduleFramingSave arms the debounced framing persister, from draw().
// Every state change redraws, so there is no per-gesture persistence hook to
// forget — the same shape as the pane-layout persister. Writing framing only
// at ascent would lose the viewport whenever a grid is left another way:
// descending deeper, a pane switch, a URL edit, a reload.
func (a *App) scheduleFramingSave() {
	a.sched.framingSave.arm(framingSaveDebounceMs)
}

// flushFramingSave persists every pane's settled grid framing now. The
// writers it dispatches to no-op when nothing moved, so quiet calls are free,
// and persistFraming refuses for any pane that is mid-transition — that
// decision has one owner and this is not it. draw() re-arms the debounce on
// the next frame, so an animating pane's flush lands after its animation
// while its quiet siblings persist on time.
func (a *App) flushFramingSave() {
	a.framingFlushes++
	// One active surface per grid: among panes showing the same grid, only
	// the focused one writes its framing. pane.FramingWriters is the rule.
	var pgs []pane.PaneGrid
	a.tree.Walk(func(p *pane.Pane) {
		pgs = append(pgs, pane.PaneGrid{PaneID: p.ID, GridID: a.gridIDForPane(p)})
	})
	writers := pane.FramingWriters(pgs, a.tree.Focus)
	a.tree.Walk(func(p *pane.Pane) {
		if writers[p.ID] {
			a.persistPaneFraming(p)
		}
	})
	a.flushWellWheelSaves()
}

// flushWellWheelSaves posts the settled hover-wheel well zooms: one
// SetFraming per touched tile, from the pending drift state, the one owner of
// the not-yet-persisted view. Re-reading the cache row instead would let any
// refetch inside the settle window replace the patch with server values and
// silently revert the wheel. The version claim prefers the cache row's, which
// is fresher when an event landed; the drift's wheel-time claim is the
// fallback, and the framing dispatcher's conflict retry covers both being
// stale.
func (a *App) flushWellWheelSaves() {
	for id, st := range a.wellWheelPending {
		gid := st.gridID
		delete(a.wellWheelPending, id)
		tileID := id
		req := &rpc.SetFramingRequest{
			TileID:  tileID,
			Framing: rpc.Framing{Cx: st.cx, Cy: st.cy, Zoom: st.ratio},
		}
		// The unload transport is the dispatcher's business (write.beacon):
		// one place decides whether this write goes as an RPC or as a
		// beacon, so a parked framing write reaches the beacon path too.
		a.postFramingPersist("SetFraming", gid, tileID,
			func(ctx context.Context) error {
				_, err := a.cl.SetFraming(ctx, req)
				return err
			},
			func() (string, []byte, string) {
				path, body := rpc.SetFramingBeacon(req)
				return path, body, rpc.BeaconJSONType
			})
	}
}

// persistPaneFraming writes pane p's current place framing: the same write an
// ascent flushes, fired without waiting for one. Which row owns it is the
// place stack's own projection (pane.FramingTarget): the doorway the pane
// came in by, or the grid row when it came in by nothing.
//
// A content descent settle-persists its scroll, so a reload does not lose
// your place in the doc. A no-op when the place is unresolvable, as with an
// uncached parent grid; the next settle retries.
func (a *App) persistPaneFraming(p *pane.Pane) {
	own := p.FramingTarget()
	switch {
	case own.Content:
		a.persistTextScroll(p)
	case own.TileID == "":
		a.persistFraming(p, nil, "", nil)
	default:
		gid := a.gridIDForPathFrom(own.DoorAnchor, own.DoorPath)
		if gid == "" {
			return
		}
		g, ok := a.c.Grid(gid)
		if !ok {
			return
		}
		w, ok := g.Tiles[own.TileID]
		if !ok {
			// No row for the doorway: a + menu descent. The level's own
			// root grid owns the framing instead, the same fact through
			// the same verb.
			a.persistFraming(p, nil, "", nil)
			return
		}
		a.persistFraming(p, &w, own.DoorAnchor, own.DoorPath)
	}
}

// persistFraming is the one framing writeback. It writes the pane's settled
// place — a float center in the grid it is showing, plus the
// pane-size-independent intrinsic zoom — onto the row that owns it, through
// the one wire verb.
//
// `door` is the doorway tile the pane entered its grid through, living under
// (doorAnchor, doorPath). nil means the pane sits at a root grid, which has no
// doorway, so the grid row owns the framing and the client's copy of it is
// the plugin's Info handshake. The zoom is measured against the doorway's
// footprint — 1×1 for a root, the same synthetic doorway a plugin renders as
// (rpc.PluginWellTile) — so preview and descent agree.
//
// Fired by every ascent flush and by the settle persister
// (flushFramingSave). A no-op when nothing moved (rpc.Framing.SameAs), so
// quiet calls do not churn the store. The doorway arm mutates `door` in place,
// because the local-side ascent transition uses the new values, and patches
// the cache so the parent's preview renders them before the server's event
// arrives. During beforeunload the write rides a beacon instead
// (unload.go).
func (a *App) persistFraming(p *pane.Pane, door *rpc.Tile, doorAnchor string, doorPath []string) {
	// Never a mid-animation viewport. While this pane animates, its centre and
	// zoom are the transition's scratch values inside whatever place the
	// current segment installed — presentation, not something the user set —
	// and storing one would make a frame of an animation the framing they come
	// back to. Every writer asks here, so none can forget: the settle
	// persister, an ascent's leaveFrame, and a pane about to be dropped. A
	// cancelled transition retires before its landing runs, so a write made
	// from a landing is the destination, not scratch.
	if a.trans.Active(p.ID) {
		return
	}
	var (
		req    rpc.SetFramingRequest
		foot   = zoomtrans.Well{W: 1, H: 1}
		cur    rpc.Framing
		gridID string
		commit func(rpc.Framing)
	)
	if door != nil {
		foot = zoomtrans.Well{W: door.W, H: door.H}
		cur = rpc.Framing{Cx: door.ViewCx, Cy: door.ViewCy, Zoom: door.ViewZoom}
		gridID = a.gridIDForPathFrom(doorAnchor, doorPath)
		req = rpc.SetFramingRequest{TileID: door.ID}
		commit = func(f rpc.Framing) {
			door.ViewCx, door.ViewCy, door.ViewZoom = f.Cx, f.Cy, f.Zoom
			updated := *door
			a.c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: updated}})
		}
	} else {
		if len(p.Path()) > 0 || p.ContentID() != "" {
			return
		}
		pl, ok := a.pluginByRoot(p.Anchor())
		if !ok {
			return
		}
		cur = rpc.Framing{Cx: pl.RootViewCx, Cy: pl.RootViewCy, Zoom: pl.RootViewZoom}
		gridID = p.Anchor()
		req = rpc.SetFramingRequest{RootGridID: p.Anchor()}
		commit = func(f rpc.Framing) {
			// The local PluginInfo copy of the root framing, a cache of the
			// Info handshake, reconciles immediately, so the next + menu
			// descent frames to what was just saved.
			for i := range a.plugins {
				if a.plugins[i].UUID == pl.UUID {
					a.plugins[i].RootViewCx = f.Cx
					a.plugins[i].RootViewCy = f.Cy
					a.plugins[i].RootViewZoom = f.Zoom
				}
			}
		}
	}
	r := paneRectFor(a, p)
	next := rpc.Framing{Cx: p.Cx, Cy: p.Cy,
		Zoom: zoomtrans.IntrinsicFromLive(p.Zoom, zoomtrans.OvertakeZoom(foot, r.W, r.H, cellPx))}
	if cur.SameAs(next) {
		return
	}
	commit(next)
	req.Framing = next
	// One dispatcher for both rows a framing can live on: the doorway tile
	// and the root grid. They differ only in which id keys the parked write,
	// since grid ids and tile ids are separate sequences, never in policy;
	// neither carries a claim.
	key := req.TileID
	if key == "" {
		key = req.RootGridID
	}
	a.postFramingPersist("SetFraming", gridID, key,
		func(ctx context.Context) error {
			_, err := a.cl.SetFraming(ctx, &req)
			return err
		},
		func() (string, []byte, string) {
			path, body := rpc.SetFramingBeacon(&req)
			return path, body, rpc.BeaconJSONType
		})
}

// persistTextScroll is the settle persister's text arm: a text descent's
// scroll position persists like grid framing does — framing-class, no version
// bump, one SetTextView when it actually moved. Content stays with the
// keystroke save queue. Read-only host tiles keep session-only scroll,
// because their plugins refuse text framing, and url, shell, and page
// descents carry no text framing at all.
func (a *App) persistTextScroll(p *pane.Pane) {
	file, ok := a.descendedTile(p)
	if !ok || file.Kind != rpc.KindText || file.ServesPage ||
		a.tileReadOnly(&file) || a.isEphemeralTile(p, &file) {
		return
	}
	scrollX := int64(p.TextScrollX + 0.5)
	scrollY := int64(p.TextScrollY + 0.5)
	gid := a.gridIDForPane(p)
	r := paneRectFor(a, p)
	_, _, iw, ih := textInnerBox(r)
	next := textedit.Framing{X: scrollX, Y: scrollY, W: int64(iw + 0.5), H: int64(ih + 0.5), Mode: p.TextMode}
	if !textedit.FramingChanged(textedit.FramingOf(file), next) {
		return
	}
	req := &rpc.SetTextViewRequest{
		TileID: file.ID,
		TextX:  next.X, TextY: next.Y,
		TextW: next.W, TextH: next.H,
		TextMode: next.Mode,
	}
	patched := file
	patched.TextX, patched.TextY = scrollX, scrollY
	patched.TextW, patched.TextH = req.TextW, req.TextH
	patched.TextMode = p.TextMode
	a.c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: patched}})
	a.postFramingPersist("SetTextView", gid, file.ID,
		func(ctx context.Context) error {
			_, err := a.cl.SetTextView(ctx, req)
			return err
		},
		func() (string, []byte, string) {
			path, body := rpc.SetTextViewBeacon(req)
			return path, body, rpc.BeaconJSONType
		})
}

// scheduleURLUpdate marks that the URL is out of date and arranges for
// it to be replaced on the next debounce tick. Cheap to call from any
// state-mutating code path.
func (a *App) scheduleURLUpdate() {
	a.sched.urlUpdate.arm(urlUpdateDebounceMs)
}

// writeURLNow encodes the focused pane's state and writes it to the browser
// history: the one history writer. Push against replace is the tested
// pane.URLPushesEntry decision over the diff between this write's place and
// the last one written. Structural navigation — a descent, an ascent, a
// pane-tile boundary — pushes an entry so back traverses it, while framing
// changes and pane-focus switches replace in place. No call site carries a
// "structural" bit: the intent is derived from state, so a forgotten flag is
// unrepresentable. During a popstate restore (urlRestoring) every write
// replaces, because the restore re-encodes the place the browser already
// navigated to and pushing would corrupt the stack being traversed.
//
// Idempotent; safe even when no user change has happened.
func (a *App) writeURLNow() {
	// A restore in flight owns the URL: a write here would clobber the entry
	// the browser just navigated to with mid-restore pane state. The
	// restore's final step re-runs this with the flag down.
	if a.urlRestoring {
		return
	}
	state := a.encodeFocusedPaneURL()
	raw := a.withE2EParam(pane.EncodeURL(state))
	var paneID string
	if p := a.tree.FocusedPane(); p != nil {
		paneID = p.ID
	}
	place := pane.URLPlaceOf(paneID, state)
	push := pane.URLPushesEntry(a.urlPrevPlace, place, a.urlPlaceSeen)
	a.urlPrevPlace = place
	a.urlPlaceSeen = true
	if push {
		js.Global().Get("history").Call("pushState", nil, "", raw)
	} else {
		js.Global().Get("history").Call("replaceState", nil, "", raw)
	}
}

// withE2EParam re-appends the e2e harness gate. pane.EncodeURL rebuilds the
// query from scratch, so any param it does not know is dropped on the first
// write, `e2e=1` included. Without this the first write de-instruments the
// page and any spec that reloads or history-navigates mid-test loses the
// testhook.
func (a *App) withE2EParam(raw string) string {
	if !strings.Contains(js.Global().Get("location").Get("search").String(), "e2e=1") {
		return raw
	}
	if strings.ContainsRune(raw, '?') {
		return raw + "&e2e=1"
	}
	return raw + "?e2e=1"
}

// restoreFromHistory applies a browser back or forward (popstate): a
// reload-equivalent restore of the focused pane at the URL the browser
// navigated to. It runs on its own goroutine, because fetches block. The
// session scaffolding a reload would lose — the pane's outer frames, live
// streams, selection — resets here too, deliberately: back is navigation to a
// place, and the place's truth (content, framing) is server-owned, so what is
// dropped is transient scaffolding, never data.
//
// The caller, the popstate listener, has already set urlRestoring and
// captured raw. Both must happen synchronously in the event callback, before
// any pending debounced write can fire and clobber the target entry's URL.
func (a *App) restoreFromHistory(raw string) {
	// Leaving the current place: the same boundary flushes every other
	// navigation performs (pending text + framing still in their debounce
	// windows).
	a.flushDirtyText()
	a.flushFramingSave()

	defer func() {
		a.urlRestoring = false
		// Re-seed the diff baseline at the restored place: seen=false makes
		// the write a replace even if the restore truncated the path.
		a.urlPlaceSeen = false
		a.writeURLNow()
	}()

	// The popped URL names the whole place. Navigation inside a pane tile
	// never pushes entries, since the URL is constant there, so a popstate
	// always crosses a place boundary: exit any level stack through its real
	// exit path, layout flushes included, before restoring.
	a.ascendLevels(a.ws.Depth())

	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	// Reload-equivalent per-pane reset: close live streams and clear the
	// pane's place down to one frame. applyURLState installs the decoded
	// place over it, so a deeper frame left standing here would survive a
	// restore to a shallower place.
	a.menu.Close()
	// Land whatever is animating before the reset: a restore replaces the
	// place, and a transition dropped here would leave its descent half done.
	a.trans.CancelAll()
	a.forgetPane(p.ID)
	p.Reset(pane.Frame{GridID: a.home, Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom})
	a.refreshFileOverlay()

	a.applyURLState(raw)
}

// encodeFocusedPaneURL projects the focused pane's place into the URL DTO.
// The projection itself is pane.URLStateOf, the one encode half, unit-tested;
// the only thing the shim adds is the textarea cursor, which is a DOM fact.
func (a *App) encodeFocusedPaneURL() pane.URLState {
	// Inside a pane tile, that tile is the place: the interior — every
	// pane's place and viewport — is server-owned by the layout blob, so
	// nothing else rides the URL.
	if top := a.ws.Top(); top != nil {
		return pane.URLState{Workspace: top.TileID}
	}
	p := a.tree.FocusedPane()
	if p == nil {
		return pane.URLState{}
	}
	isText := p.ContentID() != "" && p.TextMode == rpc.TextModeText
	var col, row int
	if isText {
		col, row = a.textareaCursorRowCol()
	}
	return pane.URLStateOf(&p.Stack, a.home, isText, col, row)
}

// textareaCursorRowCol returns the cursor position in the file
// textarea as (column, row), 0-indexed. Returns (0, 0) if the
// textarea isn't visible.
func (a *App) textareaCursorRowCol() (int, int) {
	if !a.hasTextarea() {
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
	a.applyURLState(locationPath())
}

// locationPath is what the browser's address bar currently says, in the exact
// form pane.DecodeURL reads: path plus query. The one reader of
// window.location — boot and the popstate restore must decode the same
// bytes, or they land in different places from one address.
func locationPath() string {
	loc := js.Global().Get("location")
	raw := loc.Get("pathname").String()
	if s := loc.Get("search").String(); s != "" {
		raw += s
	}
	return raw
}

// applyURLState decodes raw and places the focused pane there — the
// idempotent "decode a URL and go there" routine shared by boot and the
// popstate restore (restoreFromHistory). See applyURLOnBoot for the
// loose-input contract.
func (a *App) applyURLState(raw string) {
	state, err := pane.DecodeURL(raw)
	if err != nil {
		// Bad URL — drop to root.
		state = pane.URLState{}
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
	p.Reset(pane.Frame{GridID: state.Anchor, Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom})

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

	// No path → sit at the anchor's root grid, at its PERSISTED root view
	// (persistFraming's root arm writes it) unless the URL carries its own
	// viewport — this call site passed literal 0,0,1 for years, so every
	// relaunch opened home at the origin no matter what the user left
	// behind (the guiding rule, violated at boot; the parameters were even
	// unit-tested, just never fed).
	if len(qualified) == 0 {
		a.fetchGridSync(state.Anchor)
		rcx, rcy, rz, _ := a.persistedGridView(p, state.Anchor, nil)
		if bv := pane.URLBootViewport(state.X, state.Y, state.Zoom, rcx, rcy, rz); bv.Apply {
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

	// The decoded place, built by the one decoder: a root grid, the walked
	// doorway path, and the content leaf when there is one. The outer frames
	// carry no viewport, because nothing encodes those, so the ascent out of
	// here lands on each grid's persisted framing.
	viewport := p.Frame
	p.Stack = pane.StackAt(state.Anchor, resolvedPath, textTileID)
	p.Cx, p.Cy, p.Zoom = viewport.Cx, viewport.Cy, viewport.Zoom
	if p.Zoom <= 0 {
		p.Zoom = 1
	}
	if textTileID != "" {
		// Mode follows the one descent decision (descentTextMode — a
		// read-only tile always restores RENDERED, the selectable face);
		// a URL that encodes a text cursor forces text mode. Scale is
		// fixed; scroll restores from the tile's stored text_y.
		file, cached := a.cachedFile(p, textTileID)
		if cached {
			p.TextMode = a.descentTextMode(&file, state.CursorMode)
			p.TextScrollY = float64(file.TextY)
		} else {
			// Uncached: no row to read; the one owner decides the default.
			p.TextMode = textedit.DescentMode(textedit.ModeInput{Kind: rpc.KindText, CursorURL: state.CursorMode})
		}
		p.TextZoom = a.textScaleFor(p) // base × the tile's content zoom
		a.fetchBlobAndSetCursor(textTileID, state)
		// Refresh overlay so the textarea (text mode) appears.
		a.refreshFileOverlay()
		// A reload lands back inside the descent, so re-engage it: the shell
		// reconnects and the url reopens, through the same one-owner
		// decision every descent applies.
		a.autoLiveOnRestore(p.ID, textTileID)
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
	// Claim-free (the walk blocks on its own answer, and a background fetch
	// for the same grid must not turn the walk into a no-op) but bounded
	// like every other fetch: a boot that waits forever on a dead socket is
	// a blank screen with no explanation.
	ctx, cancel := a.gridFetch.Context()
	defer cancel()
	return a.loadGrid(ctx, id) == nil
}

// fetchBlobAndSetCursor pulls the file's bytes and, once they're in
// the cache, places the cursor at (state.Col, state.Row) inside the
// textarea. Asynchronous because GetBlob is over the wire.
func (a *App) fetchBlobAndSetCursor(textTileID string, state pane.URLState) {
	gid := a.gridIDForPane(a.tree.FocusedPane())
	g, ok := a.c.Grid(gid)
	if !ok {
		return
	}
	if _, ok := g.Tiles[textTileID]; !ok {
		return
	}
	go func() {
		// Claim-free, like the walk above, and bounded the same way.
		ctx, cancel := a.contentFetch.Context()
		defer cancel()
		// loadTileContent stores the bytes and refreshes the overlay (in
		// text mode that seeds the textarea from the body); the cursor is
		// what this path adds, and it goes after the seeding.
		a.loadTileContent(ctx, textTileID, func() {
			if state.CursorMode {
				a.placeCursorAt(state.Col, state.Row)
			}
			a.draw()
		})
	}()
}

// placeCursorAt converts (col, row) into a character offset and
// applies it to the textarea via setSelectionRange. No-op if the
// textarea isn't ready.
func (a *App) placeCursorAt(col, row int) {
	if !a.hasTextarea() {
		return
	}
	val := a.textTextarea.Get("value").String()
	off := textcursor.OffsetFromRowCol(val, row, col)
	a.textTextarea.Call("focus")
	a.textTextarea.Call("setSelectionRange", off, off)
}
