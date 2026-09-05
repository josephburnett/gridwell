package nav

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/scratch"
	"github.com/josephburnett/gridwell/client/transition"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// The ascent verb: come back out the way you came in. An ascent pops a frame,
// and every hop performs the same writebacks — one leaveFrame and one landing
// — so an ascent means the same thing whichever gesture reached it.

// ascend leaves one level of the pane's place and, when more are asked for,
// hands the rest back as a continuation gesture so the next hop reads the
// place this one landed on. The last hop animates — the zoom-out onto the
// doorway you came in by — and the ones above it are instant, because
// animating a multi-level jump reads as a stutter. Animate=false makes even
// the last hop instant, for the paths that have no footprint to zoom out of.
func (m *Machine) ascend(g Gesture, w World) Plan {
	var pl planner
	if g.N <= 0 {
		return pl.plan()
	}
	p, ok := w.Pane(g.PaneID)
	if !ok {
		return pl.plan()
	}
	// Land anything this pane is still animating before reading its place:
	// the writebacks and the landing viewport are computed from where the
	// pane actually is, which mid-animation is scratch.
	pl.add(Effect{Kind: EffCancelTransition, PaneID: p.ID})
	if w.Animating[p.ID] {
		pl.then(g)
		return pl.plan()
	}
	if p.Stack.Depth() > 1 && w.Leave != nil {
		m.ascendOnce(p, w, &pl, g.Animate && g.N == 1)
		if g.N > 1 {
			next := g
			next.N = g.N - 1
			pl.then(next)
			return pl.plan()
		}
	}
	pl.add(Effect{Kind: EffRefreshOverlay})
	pl.add(Effect{Kind: EffScheduleURLUpdate})
	return pl.plan()
}

// ascendOnce pops exactly one frame: the writeback for the frame being left
// (leaveFrame), then the landing — animated onto the doorway's footprint when
// the doorway row is cached, instant otherwise.
func (m *Machine) ascendOnce(p PaneView, w World, pl *planner, animate bool) {
	door, doorTile := m.leaveFrame(p, w, pl)
	landing := p.Stack.Popped(1)
	saved, haveSaved := landingView(landing, w)
	if !animate || doorTile == nil {
		content := p.Stack.Content
		var vp *Viewport
		switch {
		case haveSaved:
			v := saved
			vp = &v
		case content:
			// Out of a content descent with nothing to restore: the pane's
			// viewport is already in the landing grid's coordinates, since a
			// content frame is zoomed onto the tile, so keeping it is where
			// the user was and moving it would not be.
		default:
			vp = &Viewport{Cx: 0, Cy: 0, Zoom: 1.0}
		}
		land := landing.Clone()
		pl.add(Effect{Kind: EffInstallPlace, PaneID: p.ID, Stack: &land, Viewport: vp})
		pl.add(Effect{Kind: EffClearSelection, PaneID: p.ID})
		m.landOnFrame(p.ID, landing, pl)
		return
	}
	r := p.Rect
	// Every animated ascent lands the same way once its segments finish: the
	// pane may have been closed mid-flight, so the continuation says so.
	tok := m.mint(cont{
		Guard:  Guard{Kind: GuardPaneExists, PaneID: p.ID},
		Step:   stepAscendLand,
		PaneID: p.ID,
	})
	if landing.Content || p.Stack.Content {
		// Out of a content descent, or back onto one stacked below: a single
		// combined pan and zoom from the tile's footprint at overtake back to
		// where the landing frame was left.
		cx := float64(doorTile.X) + float64(doorTile.W)/2
		cy := float64(doorTile.Y) + float64(doorTile.H)/2
		overtake := panebox.FitZoom(r, doorTile.W, doorTile.H, w.TextSideInset, w.CellPx)
		if overtake > p.Zoom {
			overtake = p.Zoom
		}
		if !haveSaved {
			saved = Viewport{Cx: cx, Cy: cy, Zoom: 1.0}
		}
		land := landing.Clone()
		pl.add(Effect{Kind: EffStartTransition, PaneID: p.ID, TraceTileID: door, Land: tok,
			Segments: []transition.Segment{{
				Place:  &land,
				FromCx: cx, FromCy: cy, FromZoom: overtake,
				ToCx: saved.Cx, ToCy: saved.Cy, ToZoom: saved.Zoom,
				DurationMs: w.TransitionMs,
			}}})
		return
	}
	// Out of a child grid: two concurrent-motion segments — one that finishes
	// the child's trip to the calibrated frame-switch state, and one in the
	// parent that pans-and-zooms back to the landing viewport in a single
	// motion. Pan and zoom interpolate together within each segment, so they
	// begin and end at the same moment regardless of which has more
	// "distance".
	from := zoomtrans.Endpoints{Path: p.Stack.Path(), Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom}
	wl := zoomtrans.WellOf(doorTile)
	// The switch state is the doorway's own footprint at overtake: the
	// inverse of the descent's approach, and the same for a well and for a
	// namespace crossing. The child grid's coordinates mean nothing out here,
	// so zoomtrans.Ascent hands back the parent-grid center.
	mid, switchTo := zoomtrans.Ascent(from, wl, landing.Path(), r.W, r.H, w.CellPx)
	if !haveSaved {
		saved = Viewport{Cx: switchTo.Cx, Cy: switchTo.Cy, Zoom: 1.0}
	}
	cur := p.Stack.Clone()
	land := landing.Clone()
	childDist := zoomtrans.PanDist(mid.Cx-from.Cx, mid.Cy-from.Cy, from.Zoom, w.CellPx) +
		zoomtrans.ZoomDist(from.Zoom, mid.Zoom, w.CellPx, w.ZoomDistFactor)
	parentDist := zoomtrans.PanDist(saved.Cx-switchTo.Cx, saved.Cy-switchTo.Cy, saved.Zoom, w.CellPx) +
		zoomtrans.ZoomDist(switchTo.Zoom, saved.Zoom, w.CellPx, w.ZoomDistFactor)
	durations := anim.SplitN([]float64{childDist, parentDist}, w.TransitionMs)
	pl.add(Effect{Kind: EffStartTransition, PaneID: p.ID, TraceTileID: door, Land: tok,
		Segments: []transition.Segment{
			// Child grid: combined pan+zoom to land on the calibrated state.
			{
				Place:  &cur,
				FromCx: from.Cx, FromCy: from.Cy, FromZoom: from.Zoom,
				ToCx: mid.Cx, ToCy: mid.Cy, ToZoom: mid.Zoom,
				DurationMs: durations[0],
			},
			// Parent grid: combined pan+zoom from the doorway back to the
			// viewport the landing frame was left at.
			{
				Place:  &land,
				FromCx: switchTo.Cx, FromCy: switchTo.Cy, FromZoom: switchTo.Zoom,
				ToCx: saved.Cx, ToCy: saved.Cy, ToZoom: saved.Zoom,
				DurationMs: durations[1],
			},
		}})
}

// leaveFrame plans every writeback the frame being left owes, and resolves
// the doorway row the ascent animates onto; nil when it is not cached, and
// the caller then lands instantly. It is the one place an ascent saves
// anything: content buffers and text framing, grid framing onto the doorway
// or the root grid, live-stream teardown, and the ephemeral delete.
func (m *Machine) leaveFrame(p PaneView, w World, pl *planner) (doorID string, doorTile *rpc.Tile) {
	lw := w.Leave
	own := p.Stack.FramingTarget()
	if own.Content {
		// The descended row resolves an ephemeral visit, focused off the
		// pane's grid, so its ascent animates like any other rather than
		// snapping.
		file := lw.DescendedTile
		if file == nil {
			// The row vanished, or was never cached: close the streams and
			// let the caller land instantly.
			pl.add(Effect{Kind: EffCloseStream, PaneID: p.ID,
				Streams: StreamBoth, Freeze: true})
			return own.TileID, nil
		}
		// The buffer and framing save: posts the editor buffer, when dirty,
		// and the framed window back through the dispatcher.
		pl.add(Effect{Kind: EffSaveText, PaneID: p.ID, TileID: file.ID})
		// Ascending out of an ephemeral tile deletes it — gray means gone. No
		// freeze, which is pointless for a tile about to die, and then the row
		// goes away; for a shell the plugin kills its tmux session too. The
		// answer must be a KNOWN yes, and no other pane may still show it,
		// since a split clones the visit.
		eph, known := scratch.Ephemeral(lw.PaneScratch, file.GridID)
		ephemeral := eph && known && !w.otherPaneShows(p.ID, file.ID)
		if file.WebContent() {
			pl.add(Effect{Kind: EffCloseStream, PaneID: p.ID,
				Streams: StreamURL, Freeze: !ephemeral})
		}
		if file.Kind == rpc.KindShell {
			// Capture the JPEG, persist it as the frozen preview, close the
			// socket: the stream close handles all three.
			pl.add(Effect{Kind: EffCloseStream, PaneID: p.ID,
				Streams: StreamShell, Freeze: !ephemeral})
		}
		if ephemeral {
			pl.add(Effect{Kind: EffDeleteEphemeral, GridID: file.GridID, TileID: file.ID})
		}
		return own.TileID, file
	}
	if own.TileID == "" {
		// A root grid with no doorway: the grid row owns the framing.
		pl.add(Effect{Kind: EffPersistFraming, PaneID: p.ID, Owner: own})
		return "", nil
	}
	if !lw.DoorGridCached {
		pl.add(Effect{Kind: EffFetchGrid, GridID: lw.DoorGridID})
		pl.add(Effect{Kind: EffPersistFraming, PaneID: p.ID, Owner: own})
		return own.TileID, nil
	}
	if lw.DoorTile == nil {
		// No containing tile: a + menu descent, for which the origin grid
		// holds no row. The framing writeback still happens, just without a
		// doorway to carry it — the root grid row owns it instead, the same
		// fact through the same verb — so re-entering the plugin from the menu
		// lands at the left-off view.
		pl.add(Effect{Kind: EffPersistFraming, PaneID: p.ID, Owner: own})
		return own.TileID, nil
	}
	// Persist the user's current center as the doorway's view region so the
	// parent-grid preview reflects where they were when they left. The
	// executor mutates the row in place and patches the cache, BEFORE the
	// ascent transition is calibrated, so the frame-swap point matches the
	// user's actual position rather than snapping back to the stored origin —
	// which is why the row handed back below already carries the write.
	pl.add(Effect{Kind: EffPersistFraming, PaneID: p.ID, Owner: own, Door: true})
	t := *lw.DoorTile
	settleFraming(&t, p, w.CellPx)
	return own.TileID, &t
}

// settleFraming applies to a doorway row the framing the PersistFraming
// effect is about to write onto it: the pane's live centre plus the
// pane-size-independent intrinsic zoom, measured against the doorway's own
// footprint. zoomtrans owns the formula; both this projection and the
// executor's write read it from there, so they cannot disagree.
//
// The no-op guard is rpc.Framing.SameAs, the one "did the user actually
// move?" rule: below it the executor writes nothing and the row keeps values
// that describe the same picture anyway.
func settleFraming(door *rpc.Tile, p PaneView, cellPx float64) {
	foot := zoomtrans.Well{W: door.W, H: door.H}
	next := rpc.Framing{Cx: p.Cx, Cy: p.Cy,
		Zoom: zoomtrans.IntrinsicFromLive(p.Zoom,
			zoomtrans.OvertakeZoom(foot, p.Rect.W, p.Rect.H, cellPx))}
	cur := rpc.Framing{Cx: door.ViewCx, Cy: door.ViewCy, Zoom: door.ViewZoom}
	if cur.SameAs(next) {
		return
	}
	door.ViewCx, door.ViewCy, door.ViewZoom = next.Cx, next.Cy, next.Zoom
}

// landingView resolves the viewport an ascent lands at: the frame's own, the
// one the user left it at, or — when that frame carries none, having been
// restored from a URL or a layout blob, which encode the place but not the
// viewports above it — the grid's persisted framing, resolved by the
// gatherer. Never an arbitrary origin: landing at 0,0 and zoom 1 does not
// leave things as the user left them.
func landingView(landing pane.Stack, w World) (Viewport, bool) {
	if landing.HasView() {
		return Viewport{Cx: landing.Cx, Cy: landing.Cy, Zoom: landing.Zoom}, true
	}
	if landing.Content {
		return Viewport{}, false
	}
	if v := w.Leave.LandingView; v != nil {
		return *v, true
	}
	return Viewport{}, false
}

// landOnFrame finishes an ascent on whatever frame the pane landed on. A
// content frame is re-engaged: landing back on a url or shell descent reopens
// it, through the same one-owner decision every descent applies. The menu
// comes back if it was open when the user left this level.
func (m *Machine) landOnFrame(paneID string, place pane.Stack, pl *planner) {
	if place.MenuOpen {
		pl.add(Effect{Kind: EffOpenMenu, PaneID: paneID})
	}
	if id := place.ContentID(); id != "" {
		// base × content zoom (issue #82).
		pl.add(Effect{Kind: EffScaleContent, PaneID: paneID})
		pl.add(Effect{Kind: EffRefreshOverlay})
		pl.add(Effect{Kind: EffReEngage, PaneID: paneID, TileID: id})
		// The pane's own grid, even though nothing draws it from in here: it
		// carries the scratch-grid stamp, and until it is cached nothing can
		// say whether this descent is an ephemeral visit — so the border, the
		// title, and above all the delete on the way back out would all be
		// deciding without it. A restore straight into a content frame is the
		// one path that reaches this level without ever having fetched it.
		pl.add(Effect{Kind: EffFetchGrid, PaneID: paneID})
		return
	}
	pl.add(Effect{Kind: EffFetchGrid, PaneID: paneID})
}
