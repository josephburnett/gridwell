package nav

import (
	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/scratch"
	"github.com/josephburnett/gridwell/client/transition"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// The ascent verb: come back out the way you came in. Every hop pops a frame
// and performs the same writebacks, one leaveFrame and one landing, so an
// ascent means the same thing whichever gesture reached it.

// ascend leaves one level and hands the rest back as a continuation, so the
// next hop reads the place this one landed on. Only the last hop animates,
// since animating a multi-level jump reads as a stutter, and Animate=false
// makes even that one instant.
func (m *Machine) ascend(g Gesture, w World) Plan {
	var pl planner
	if g.N <= 0 {
		return pl.plan()
	}
	p, ok := w.Pane(g.PaneID)
	if !ok {
		return pl.plan()
	}
	// Land anything still animating first: the writebacks and the landing
	// viewport are computed from where the pane is, which mid-animation is
	// scratch.
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

// ascendOnce pops one frame: leaveFrame's writebacks, then the landing,
// animated onto the doorway's footprint when that row is cached.
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
			// A content frame's viewport is already in the landing grid's
			// coordinates, so keeping it is where the user was.
		default:
			vp = &Viewport{Cx: 0, Cy: 0, Zoom: 1.0}
		}
		pl.install(p.ID, landing, vp)
		pl.add(Effect{Kind: EffClearSelection, PaneID: p.ID})
		m.landOnFrame(p.ID, landing, pl)
		return
	}
	r := p.Rect
	// The pane may have closed mid-flight, so the landing runs under a guard.
	tok := m.mint(cont{
		Guard:  Guard{Kind: GuardPaneExists, PaneID: p.ID},
		Step:   stepAscendLand,
		PaneID: p.ID,
	})
	if landing.Content || p.Stack.Content {
		// One combined pan and zoom from the tile's footprint at overtake
		// back to where the landing frame was left.
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
	// Out of a child grid: one segment finishing the child's trip to the
	// calibrated switch state, one panning and zooming the parent back to the
	// landing viewport. Pan and zoom interpolate together inside a segment.
	from := zoomtrans.Endpoints{Path: p.Stack.Path(), Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom}
	wl := zoomtrans.WellOf(doorTile)
	// The switch state is the doorway's footprint at overtake, the inverse of
	// the descent's approach. The child grid's coordinates mean nothing out
	// here, so zoomtrans.Ascent hands back the parent-grid center.
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
			{
				Place:  &cur,
				FromCx: from.Cx, FromCy: from.Cy, FromZoom: from.Zoom,
				ToCx: mid.Cx, ToCy: mid.Cy, ToZoom: mid.Zoom,
				DurationMs: durations[0],
			},
			{
				Place:  &land,
				FromCx: switchTo.Cx, FromCy: switchTo.Cy, FromZoom: switchTo.Zoom,
				ToCx: saved.Cx, ToCy: saved.Cy, ToZoom: saved.Zoom,
				DurationMs: durations[1],
			},
		}})
}

// leaveFrame plans every writeback the frame being left owes and resolves the
// doorway row the ascent animates onto, nil when it is not cached. It is the
// one place an ascent saves anything.
func (m *Machine) leaveFrame(p PaneView, w World, pl *planner) (doorID string, doorTile *gridwellv1.Tile) {
	lw := w.Leave
	own := p.Stack.FramingTarget()
	if own.Content {
		file := lw.DescendedTile
		if file == nil {
			// The row vanished or was never cached.
			pl.add(Effect{Kind: EffCloseStream, PaneID: p.ID,
				Streams: StreamBoth, Freeze: true})
			return own.TileID, nil
		}
		pl.add(Effect{Kind: EffSaveText, PaneID: p.ID, TileID: file.Id})
		// Ascending out of an ephemeral tile deletes it, with no freeze for a
		// row about to die. The answer must be a known yes, and no other pane
		// may still show it, since a split clones the visit.
		eph, known := scratch.Ephemeral(p.Scratch, file.GridId)
		ephemeral := eph && known && !w.otherPaneShows(p.ID, file.Id)
		if rpc.WebContent(file) {
			pl.add(Effect{Kind: EffCloseStream, PaneID: p.ID,
				Streams: StreamURL, Freeze: !ephemeral})
		}
		if file.Kind == rpc.KindShell {
			pl.add(Effect{Kind: EffCloseStream, PaneID: p.ID,
				Streams: StreamShell, Freeze: !ephemeral})
		}
		if ephemeral {
			pl.add(Effect{Kind: EffDeleteEphemeral, GridID: file.GridId, TileID: file.Id})
		}
		return own.TileID, file
	}
	if own.TileID == "" {
		pl.add(Effect{Kind: EffPersistFraming, PaneID: p.ID, Owner: own})
		return "", nil
	}
	if !lw.DoorGridCached {
		pl.add(Effect{Kind: EffFetchGrid, GridID: lw.DoorGridID})
		pl.add(Effect{Kind: EffPersistFraming, PaneID: p.ID, Owner: own})
		return own.TileID, nil
	}
	if lw.DoorTile == nil {
		// A + menu descent, for which the origin grid holds no row: the root
		// grid row carries the framing instead, through the same verb, so
		// re-entering from the menu lands at the left-off view.
		pl.add(Effect{Kind: EffPersistFraming, PaneID: p.ID, Owner: own})
		return own.TileID, nil
	}
	// The executor writes the doorway's view region and patches the cache
	// before the ascent is calibrated, so the frame swap matches where the
	// user is rather than snapping back. Hence the row handed back already
	// carries the write.
	pl.add(Effect{Kind: EffPersistFraming, PaneID: p.ID, Owner: own, Door: true})
	t := proto.CloneOf(lw.DoorTile)
	settleFraming(t, p, w.CellPx)
	return own.TileID, t
}

// settleFraming applies to a doorway row the framing PersistFraming is about
// to write. zoomtrans owns the formula, which both this and the executor read
// there, and the no-op guard is rpc.Framing.SameAs.
func settleFraming(door *gridwellv1.Tile, p PaneView, cellPx float64) {
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

// landingView is the viewport an ascent lands at: the frame's own, or the
// grid's persisted framing when a URL or layout blob encoded the place but not
// the viewports above it. Never an arbitrary origin.
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
// content frame is re-engaged through the same one-owner decision every
// descent applies, and the menu comes back if it was open on this level.
func (m *Machine) landOnFrame(paneID string, place pane.Stack, pl *planner) {
	if place.MenuOpen {
		pl.add(Effect{Kind: EffOpenMenu, PaneID: paneID})
	}
	if id := place.ContentID(); id != "" {
		pl.add(Effect{Kind: EffScaleContent, PaneID: paneID})
		pl.add(Effect{Kind: EffRefreshOverlay})
		pl.add(Effect{Kind: EffReEngage, PaneID: paneID, TileID: id})
		// The pane's own grid, though nothing draws it from in here: it
		// carries the scratch stamp, and a restore straight into a content
		// frame is the one path that gets here without having fetched it.
		pl.add(Effect{Kind: EffFetchGrid, PaneID: paneID})
		return
	}
	pl.add(Effect{Kind: EffFetchGrid, PaneID: paneID})
}
