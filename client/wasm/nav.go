//go:build js && wasm

package main

// Navigation: one descent and one ascent.
//
// Physically there is one gesture — go through a doorway, or come back out
// the way you came in. The place model says the same thing (client/pane,
// place.go): a descent pushes a frame, an ascent pops one. This file is the
// glue between that model and the shim: gestures in, transitions and RPCs
// out. The decisions themselves — where a pop lands, which row owns the
// framing, which crumb is how many ascents away — live in the pure package
// and are unit-tested there.
//
// The data model's ownership boundaries — a well, a link into another
// namespace, a content tile — are wire declarations on the doorway tile, read
// in one switch. They do not become separate descent or ascent verbs.

import (
	"slices"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/pluginhealth"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// descend takes pane p through the doorway tile: the descent verb. Which kind
// of frame it pushes is the tile's declaration, never the call site's. A well
// or a link pushes a grid frame; a text, url, shell, or page tile pushes a
// content frame, because the tile is the place; and a pane tile descends the
// window a level instead, its place being a whole tree (see ascendLevels).
//
// after, when non-nil, runs once the pane is fully descended (the URL-tile
// creation path chains on it).
func (a *App) descend(p *pane.Pane, tile *rpc.Tile, after func()) {
	// The pane is about to change place: flush framing still inside the
	// settle window, while the viewport still belongs to the place it
	// describes. One place asks, so no door can forget.
	a.flushFramingSave()
	switch {
	case rpc.IsWorkspaceKind(tile.Kind):
		a.descendLevel(p, tile)
	case rpc.IsContentDescentKind(tile.Kind):
		a.descendContent(p, tile, after)
	case rpc.IsWellKind(tile.Kind):
		a.descendGrid(p, tile)
	}
}

// descendGrid installs the two-segment transition into a well's child grid
// and pushes the grid frame at the swap.
//
// Phases:
//
//	A. Combined pan+zoom in parent to (wellCenter, OvertakeZoom).
//	B. Atomic install of the calibrated child state at the frame push.
//	C. (Optional) animate the child to the well's stored ViewZoom so
//	   re-descent lands at the same zoom the user left at. Only fires
//	   when well.ViewZoom > 0; the default for never-entered wells is
//	   0 (calibrated zoom).
//
// Total time is split between A and C proportional to motion distance so
// neither feels rushed. C is zero-length when ViewZoom is unset.
func (a *App) descendGrid(p *pane.Pane, well *rpc.Tile) {
	if well.ChildGridID == "" {
		// A link tile whose target is not available: a broken or rootless
		// plugin, or a connection whose remote has not answered. Say why
		// instead of silently doing nothing; pluginhealth owns the wording.
		// The id is the plugin uuid itself on a menu row (chained for a
		// connection, "i9sm6ff/ltvv2f9") and node-qualified on a link tile,
		// so try both shapes or a connection's dial status never surfaces.
		pl, ok := a.pluginByUUID(well.ID)
		if !ok {
			pl, ok = a.pluginByUUID(rpc.LocalOf(well.ID))
		}
		if ok {
			if sev, source, message, ok := pluginhealth.ClickNotice(pl); ok {
				a.reportErr(sev, source, message)
				return
			}
		}
		a.reportErr(errsurface.Info, "descend", "nothing to descend into: "+well.AltText)
		return
	}
	r := paneRectFor(a, p)
	from := zoomtrans.Endpoints{
		Path: slices.Clone(p.Path()),
		Cx:   p.Cx, Cy: p.Cy, Zoom: p.Zoom,
	}
	w := wellOf(well)
	next := pane.Frame{Door: well.ID}
	mid, swap, final := zoomtrans.Descent(from, w, r.W, r.H, cellPx)
	if isLinkTile(well) {
		// A link crosses into another id space: a plugin link tile, a
		// mounted well, a cross-plugin clone. The frame carries the target
		// grid id — the one place that fact is authoritative — so every
		// path id below it stays within one namespace, and the ascent pops
		// back onto this very tile without searching the parent grid for a
		// well whose child matches the anchor.
		next.GridID = well.ChildGridID
		// The + menu comes back with you, just as you left it.
		p.MenuOpen = a.menu.OpenOn(p.ID)
		a.menu.Close()
		// The synthetic well an in-grid + menu descent goes through rounds
		// the launcher tile's position; recentre the parent zoom on the
		// exact footprint center so the descent lands square on it.
		mid.Cx = float64(well.X) + float64(well.W)/2
		mid.Cy = float64(well.Y) + float64(well.H)/2
	}
	a.fetchGrid(well.ChildGridID)

	// The place each segment plays in: the parent zoom happens where the
	// pane already is, and the child segment plays in the pushed frame.
	// Because segments install snapshots, the parent frame keeps the
	// viewport the user actually left it at.
	base := p.Stack.Clone()
	child := base.Clone()
	child.Push(next)

	parentDist := panDist(mid.Cx-from.Cx, mid.Cy-from.Cy, from.Zoom) +
		zoomDist(from.Zoom, mid.Zoom)
	childDist := zoomDist(swap.Zoom, final.Zoom)
	var durations []float64
	if childDist > 0 {
		durations = anim.SplitN([]float64{parentDist, childDist}, totalTransitionMs)
	} else {
		durations = []float64{totalTransitionMs, 0}
	}

	a.startTransition(&paneTransition{
		paneID: p.ID,
		segments: []transSegment{
			// A: parent pan+zoom toward the well/footprint center at Overtake.
			{
				place:  &base,
				fromCx: from.Cx, fromCy: from.Cy, fromZoom: from.Zoom,
				toCx: mid.Cx, toCy: mid.Cy, toZoom: mid.Zoom,
				durationMs: durations[0],
			},
			// C: after the frame push, ease the child zoom out to the stored
			// ratio (zero-length when swap == final).
			{
				place:  &child,
				fromCx: swap.Cx, fromCy: swap.Cy, fromZoom: swap.Zoom,
				toCx: final.Cx, toCy: final.Cy, toZoom: final.Zoom,
				durationMs: durations[1],
			},
		},
	})
}

// descendContent zooms a pane into a content tile (text, url, shell, page)
// in a single concurrent pan+zoom motion, then pushes the content frame.
// Unlike a grid descent nothing is appended to the path — the tile lives in
// the current grid as a leaf — and the meaningful screen area in live mode
// is the inner box (textarea region), not the full pane, so the descent
// targets TextOvertake: the parent zoom that makes the footprint fit the
// inner box. At the frame push the footprint screen size is the inner box,
// and the live TextZoom is reconstructed from the tile's intrinsic ViewZoom
// ratio for visual continuity.
//
// after, if non-nil, is called once the frame is installed. Use it to chain
// actions that need the pane fully descended. Going live is not one of them:
// that decision has one owner, autoLiveOnDescent.
func (a *App) descendContent(p *pane.Pane, file *rpc.Tile, after func()) {
	r := paneRectFor(a, p)
	fromCx, fromCy, fromZoom := p.Cx, p.Cy, p.Zoom
	wellCx := float64(file.X) + float64(file.W)/2
	wellCy := float64(file.Y) + float64(file.H)/2
	target := textFitZoom(r, file.W, file.H)
	if target < fromZoom {
		target = fromZoom
	}

	// Eagerly fetch the blob so it's likely cached by the time the
	// transition lands. URL tiles don't have a blob; their preview
	// path goes through urlPreview instead — and so does a serves_page
	// tile's (its descent is the page, not the document body).
	if file.Kind == rpc.KindText && !file.ServesPage {
		// Source-backed bodies (fs files, the proc @info tile) are host
		// state, not versioned content: their version is always 0, so a
		// cache entry from the first open would match forever and the
		// descent would show stale bytes however the file changed on disk.
		// Every open re-reads — it is all read-only — so drop before
		// fetching, or the fetch does not refetch.
		if a.tileReadOnly(file) {
			a.c.DropTileContent(file.ContentID())
			a.fetchGrid(a.gridIDForPane(p))
		}
		a.fetchTileContent(file.ID)
	}

	// Captured by value for the auto-live decision: an ephemeral
	// scratch-grid tile is in no cached grid, so a cache lookup at
	// transition end would miss it and silently skip going live.
	fileCopy := *file
	base := p.Stack.Clone()
	// Stacking a visit over a live descent (a url opened from a shell): the
	// animation plays in the grid behind the current content, since a
	// content frame's viewport is already in that grid's coordinates, while
	// the frame itself stays on the stack, so one ascent lands right back on
	// it. No stash, no second stack.
	animBase := base.Clone()
	if animBase.Content {
		animBase.Pop()
	}
	landing := base.Clone()
	landing.Push(pane.Frame{
		Door: file.ID, Content: true,
		Cx: wellCx, Cy: wellCy, Zoom: target,
		TextMode:    a.descentTextMode(file, false),
		TextScrollX: float64(file.TextX), TextScrollY: float64(file.TextY),
	})
	wasContent := base.Content
	a.startTransition(&paneTransition{
		paneID: p.ID,
		segments: []transSegment{
			// Single combined pan+zoom segment: pan to the file center
			// while simultaneously zooming to the overtake target.
			{
				place:  &animBase,
				fromCx: fromCx, fromCy: fromCy, fromZoom: fromZoom,
				toCx: wellCx, toCy: wellCy, toZoom: target,
				durationMs: totalTransitionMs,
			},
		},
		onComplete: func() {
			fp := a.tree.FindPane(p.ID)
			if fp == nil {
				return
			}
			fp.Stack = landing.Clone()
			fp.TextZoom = a.textScaleFor(fp) // base × content zoom (issue #82)
			// Unsaved-edit state is untouched here: it lives tile-scoped in
			// the cache, so descending this pane elsewhere cannot strand a
			// previous document's typing.
			a.refreshFileOverlay()
			// Descending is the engagement gesture: a url reopens, a shell
			// reconnects, or creates when fresh. The decision has one
			// owner; call sites never hand-roll go-live.
			a.autoLiveOnDescent(fp.ID, &fileCopy)
			// The completed descent is the new place, so write it; the one
			// history writer derives push against replace from the diff. A
			// gesture-time write would run mid-transition with the content
			// frame not yet pushed. An editable file would paper over that
			// through later textarea cursor events, but a read-only file
			// has no textarea, so its descent would never reach the URL and
			// a reload would restore the parent grid instead.
			a.scheduleURLUpdate()
			if after != nil {
				after()
			}
		},
	})
	if wasContent {
		// The outgoing content's overlay goes now: the animation plays over
		// the grid behind it.
		a.refreshFileOverlay()
	}
}

// ascend leaves n levels of pane p's place: the ascent verb, for one level or
// several. The last hop animates — the zoom-out onto the doorway you came in
// by — and the ones above it are instant, because animating a multi-level
// jump reads as a stutter. animate=false makes even the last hop instant, for
// the paths that have no footprint to zoom out of.
//
// Every hop performs the same writebacks: one leaveFrame and one landing, so
// an ascent means the same thing whichever gesture reached it.
func (a *App) ascend(p *pane.Pane, n int, animate bool) {
	if n <= 0 {
		return
	}
	for i := 0; i < n; i++ {
		if p.Depth() == 1 {
			break
		}
		if !a.ascendOnce(p, animate && i == n-1) {
			break
		}
	}
	a.refreshFileOverlay()
	a.draw()
	a.scheduleURLUpdate()
}

// ascendOnce pops exactly one frame: the writeback for the frame being left
// (leaveFrame), then the landing — animated onto the doorway's footprint
// when the doorway row is cached, instant otherwise. Reports whether it
// moved.
func (a *App) ascendOnce(p *pane.Pane, animate bool) bool {
	if p.Depth() == 1 {
		return false
	}
	door, doorTile := a.leaveFrame(p)
	landing := p.Popped(1)
	saved, haveSaved := a.landingView(p, landing)
	if !animate || doorTile == nil {
		content := p.Content
		p.Stack = landing
		switch {
		case haveSaved:
			p.Cx, p.Cy, p.Zoom = saved.Cx, saved.Cy, saved.Zoom
		case content:
			// Out of a content descent with nothing to restore: the pane's
			// viewport is already in the landing grid's coordinates, since
			// a content frame is zoomed onto the tile, so keeping it is
			// where the user was and moving it would not be.
		default:
			p.Cx, p.Cy, p.Zoom = 0, 0, 1.0
		}
		a.clearSelected(p.ID)
		a.landOnFrame(p)
		return true
	}
	r := paneRectFor(a, p)
	if landing.Content || p.Content {
		// Out of a content descent, or back onto one stacked below: a
		// single combined pan and zoom from the tile's footprint at
		// overtake back to where the landing frame was left.
		cx := float64(doorTile.X) + float64(doorTile.W)/2
		cy := float64(doorTile.Y) + float64(doorTile.H)/2
		overtake := textFitZoom(r, doorTile.W, doorTile.H)
		if overtake > p.Zoom {
			overtake = p.Zoom
		}
		if !haveSaved {
			saved = pane.Frame{Cx: cx, Cy: cy, Zoom: 1.0}
		}
		a.startTransition(&paneTransition{
			paneID:      p.ID,
			traceTileID: door,
			segments: []transSegment{{
				place:  &landing,
				fromCx: cx, fromCy: cy, fromZoom: overtake,
				toCx: saved.Cx, toCy: saved.Cy, toZoom: saved.Zoom,
				durationMs: totalTransitionMs,
			}},
			onComplete: func() {
				if fp := a.tree.FindPane(p.ID); fp != nil {
					a.landOnFrame(fp)
				}
			},
		})
		return true
	}
	// Out of a child grid: two concurrent-motion segments — one that
	// finishes the child's trip to the calibrated frame-switch state, and
	// one in the parent that pans-and-zooms back to the landing viewport in
	// a single motion. Pan and zoom interpolate together within each
	// segment, so they begin and end at the same moment regardless of which
	// has more "distance".
	from := zoomtrans.Endpoints{Path: slices.Clone(p.Path()), Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom}
	w := wellOf(doorTile)
	// The switch state is the doorway's own footprint at overtake: the
	// inverse of the descent's approach, and the same for a well and for a
	// namespace crossing. The child grid's coordinates mean nothing out
	// here, so zoomtrans.Ascent hands back the parent-grid center.
	mid, switchTo := zoomtrans.Ascent(from, w, landing.Path(), r.W, r.H, cellPx)
	if !haveSaved {
		saved = pane.Frame{Cx: switchTo.Cx, Cy: switchTo.Cy, Zoom: 1.0}
	}
	cur := p.Stack.Clone()
	childDist := panDist(mid.Cx-from.Cx, mid.Cy-from.Cy, from.Zoom) +
		zoomDist(from.Zoom, mid.Zoom)
	parentDist := panDist(saved.Cx-switchTo.Cx, saved.Cy-switchTo.Cy, saved.Zoom) +
		zoomDist(switchTo.Zoom, saved.Zoom)
	durations := anim.SplitN([]float64{childDist, parentDist}, totalTransitionMs)
	a.startTransition(&paneTransition{
		paneID:      p.ID,
		traceTileID: door,
		segments: []transSegment{
			// Child grid: combined pan+zoom to land on the calibrated state.
			{
				place:  &cur,
				fromCx: from.Cx, fromCy: from.Cy, fromZoom: from.Zoom,
				toCx: mid.Cx, toCy: mid.Cy, toZoom: mid.Zoom,
				durationMs: durations[0],
			},
			// Parent grid: combined pan+zoom from the doorway back to the
			// viewport the landing frame was left at.
			{
				place:  &landing,
				fromCx: switchTo.Cx, fromCy: switchTo.Cy, fromZoom: switchTo.Zoom,
				toCx: saved.Cx, toCy: saved.Cy, toZoom: saved.Zoom,
				durationMs: durations[1],
			},
		},
		onComplete: func() {
			if fp := a.tree.FindPane(p.ID); fp != nil {
				a.landOnFrame(fp)
			}
		},
	})
	return true
}

// leaveFrame performs every writeback the frame being left owes, and resolves
// the doorway row the ascent animates onto; nil when it is not cached, and
// the caller then lands instantly. It is the one place an ascent saves
// anything: content buffers and text framing, grid framing onto the doorway
// or the root grid, live-stream teardown, and the ephemeral delete.
func (a *App) leaveFrame(p *pane.Pane) (doorID string, doorTile *rpc.Tile) {
	own := p.FramingTarget()
	if own.Content {
		// descendedTile resolves an ephemeral visit, focused off the pane's
		// grid, so its ascent animates like any other rather than snapping.
		file, ok := a.descendedTile(p)
		if !ok {
			// The row vanished, or was never cached: close the streams and
			// let the caller land instantly.
			a.closeURLStream(p.ID, true)
			a.closeShellStream(p.ID, true)
			return own.TileID, nil
		}
		// The buffer and framing save: posts the editor buffer, when dirty,
		// and the framed window back through the dispatcher.
		a.saveTextBeforeAscent(p, file)
		// Ascending out of an ephemeral tile deletes it — gray means gone.
		// No freeze, which is pointless for a tile about to die, and then
		// the row goes away; for a shell the plugin kills its tmux session
		// too.
		ephemeral := a.leavingEphemeral(p, &file)
		if file.WebContent() {
			a.closeURLStream(p.ID, !ephemeral)
		}
		if file.Kind == rpc.KindShell {
			// Capture the JPEG, persist it as the frozen preview, close the
			// socket: closeShellStream handles all three.
			a.closeShellStream(p.ID, !ephemeral)
		}
		if ephemeral {
			a.deleteEphemeralTile(file.ID)
		}
		return own.TileID, &file
	}
	if own.TileID == "" {
		// A root grid with no doorway: the grid row owns the framing.
		a.persistFraming(p, nil, "", nil)
		return "", nil
	}
	gid := a.gridIDForPathFrom(own.DoorAnchor, own.DoorPath)
	g, ok := a.c.Grid(gid)
	if !ok {
		a.fetchGrid(gid)
		a.persistFraming(p, nil, "", nil)
		return own.TileID, nil
	}
	t, ok := g.Tiles[own.TileID]
	if !ok {
		// No containing tile: a + menu descent, for which the origin grid
		// holds no row. The framing writeback still happens, just without a
		// doorway to carry it — the root grid row owns it instead, the same
		// fact through the same verb — so re-entering the plugin from the
		// menu lands at the left-off view.
		a.persistFraming(p, nil, "", nil)
		return own.TileID, nil
	}
	// Persist the user's current center as the doorway's view region so the
	// parent-grid preview reflects where they were when they left. It
	// mutates the row in place and patches the cache, before calibrating the
	// ascent transition, so the frame-swap point matches the user's actual
	// position rather than snapping back to the stored origin.
	a.persistFraming(p, &t, own.DoorAnchor, own.DoorPath)
	return own.TileID, &t
}

// landingView resolves the viewport an ascent lands at: the frame's own, the
// one the user left it at, or — when that frame carries none, having been
// restored from a URL or a layout blob, which encode the place but not the
// viewports above it — the grid's persisted framing. Never an arbitrary
// origin: landing at 0,0 and zoom 1 does not leave things as the user left
// them.
func (a *App) landingView(p *pane.Pane, landing pane.Stack) (pane.Frame, bool) {
	if landing.HasView() {
		return landing.Frame, true
	}
	if landing.Content {
		return pane.Frame{}, false
	}
	if cx, cy, zoom, ok := a.persistedGridView(p, landing.Anchor(), landing.Path()); ok {
		return pane.Frame{Cx: cx, Cy: cy, Zoom: zoom}, true
	}
	return pane.Frame{}, false
}

// landOnFrame finishes an ascent, and a history or boot restore, on whatever
// frame the pane landed on. A content frame is re-engaged: landing back on a
// url or shell descent reopens it, through the same one-owner decision every
// descent applies. The menu comes back if it was open when the user left this
// level.
func (a *App) landOnFrame(p *pane.Pane) {
	if p.MenuOpen {
		a.menu.Open(p.ID)
		p.MenuOpen = false
	}
	if id := p.ContentID(); id != "" {
		p.TextZoom = a.textScaleFor(p) // base × content zoom (issue #82)
		a.refreshFileOverlay()
		a.autoLiveOnRestore(p.ID, id)
		return
	}
	a.fetchGrid(a.gridIDForPane(p))
}

// ascendPane is the one-level ascent gesture: the middle button, and the
// bar's slot. It is ascend(1), named for what the gesture means.
func (a *App) ascendPane(p *pane.Pane) {
	a.ascend(p, 1, true)
}

// panDist is the wasm-side adapter for zoomtrans.PanDist, binding the
// renderer's base cell size.
func panDist(dx, dy, zoom float64) float64 {
	return zoomtrans.PanDist(dx, dy, zoom, cellPx)
}

// zoomDist is the wasm-side adapter for zoomtrans.ZoomDist, binding
// the renderer's base cell size and the zoom-vs-pan weighting factor.
func zoomDist(z1, z2 float64) float64 {
	return zoomtrans.ZoomDist(z1, z2, cellPx, zoomDistFactor)
}

// urlSurfaces and shellSurfaces list the panes currently holding a live
// surface, keyed by the content they show: the input to pane.TakeOver. They
// read straight off the live handles, the one owner of "this pane has a
// surface open"; nothing is mirrored.
func (a *App) urlSurfaces() []pane.Holder {
	var out []pane.Holder
	for id, pl := range a.locals {
		if pl.urlView != nil {
			out = append(out, pane.Holder{PaneID: id, TileID: pl.urlView.tileID})
		}
	}
	return out
}

func (a *App) shellSurfaces() []pane.Holder {
	var out []pane.Holder
	for id, pl := range a.locals {
		if pl.shellConn != nil {
			out = append(out, pane.Holder{PaneID: id, TileID: pl.shellConn.tileID})
		}
	}
	return out
}
