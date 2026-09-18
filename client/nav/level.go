package nav

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"strconv"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/transition"
)

// The second axis: a pane tile's place is a whole tree, so the window descends
// into one, not a pane, and every level stays alive while parked. A descent's
// animation and its fetch are two arms of one barrier, so neither knows about
// the other and nothing polls.

// levelData is one level being opened. It travels on the continuation because
// none of it is re-readable at join time: the origin pane's place is by then
// the animation's landing, and the decoded tree is nowhere else.
type levelData struct {
	// Boot marks the ?w= restore: no animation to join, no outer tree to park,
	// and a landing on the pane tile's own grid rather than a capture.
	Boot bool
	// PaneID is the origin pane ("" on boot) and Origin its place, put back
	// before the outer tree is parked so ascent restores what the user left.
	PaneID string
	Origin pane.Stack
	// TileID is the tile whose layout this level is, the pane link's target
	// once followed. IDPrefix namespaces the pane ids this level mints, since
	// stacked trees are all alive at once.
	TileID   string
	IDPrefix string
	Barrier  BarrierID
	// Followed latches the one pane-link hop, so links cannot walk forever.
	Followed bool

	// What the fetch arm learns.
	Tile     *gridwellv1.Tile
	Data     []byte
	Tree     *pane.Tree
	Capture  bool
	ReadOnly bool
}

// enterLevel descends the window into a pane tile: a zoom into the tile's
// footprint racing the layout fetch, with the swap at whichever finishes last.
func (m *Machine) enterLevel(g Gesture, w World) Plan {
	var pl planner
	p, ok := w.Pane(g.PaneID)
	if !ok {
		return pl.plan()
	}
	pt := g.Door
	ld := &levelData{
		PaneID:   p.ID,
		Origin:   p.Stack.Clone(),
		TileID:   pt.Id,
		IDPrefix: "w" + strconv.Itoa(w.LevelDepth+1) + ":",
	}
	ld.Barrier = m.mintBarrier(p.ID, 2, ld)

	// The animation arm. A never-arranged tile expands its face into the level
	// outline instead of zooming, because the first descent captures the
	// window layout and a zoom over an unchanged view reads as a stutter. The
	// cached row picks the animation and the fresh row the tree, which differ
	// only across the stale-cache window, harmlessly.
	here := p.Stack.Clone()
	seg := transition.Segment{
		Place:  &here,
		FromCx: p.Cx, FromCy: p.Cy, FromZoom: p.Zoom,
		ToCx: p.Cx, ToCy: p.Cy, ToZoom: p.Zoom,
		DurationMs: w.TransitionMs,
	}
	expand := pt.BlobId == 0
	if !expand {
		// Pan to the tile's centre while zooming until its footprint fills the
		// pane box, so the preview grows into the live tree.
		cx, cy := pane.Footprint{X: pt.X, Y: pt.Y, W: pt.W, H: pt.H}.Center()
		target := panebox.FitZoom(p.Rect, pt.W, pt.H, w.TextSideInset, w.CellPx)
		if target < p.Zoom {
			target = p.Zoom
		}
		seg.ToCx, seg.ToCy, seg.ToZoom = cx, cy, target
	}
	tok := m.mint(cont{Guard: Guard{Kind: GuardAlways}, Step: stepLevelAnimated,
		Barrier: ld.Barrier, PaneID: p.ID})
	pl.add(Effect{Kind: EffStartTransition, PaneID: p.ID, Land: tok,
		Expand: expand, Tile: pt, Segments: []transition.Segment{seg}})

	// The fetch arm.
	m.awaitLevelTile(ld, &pl)
	return pl.plan()
}

// bootLevel restores the innermost pane tile from a reload. The outer tree is
// nil by design, nesting membership being session-only. With no animation
// there is no barrier: the fetch arm installs on its own.
func (m *Machine) bootLevel(tileID string, pl *planner) {
	m.awaitLevelTile(&levelData{Boot: true, TileID: tileID, IDPrefix: "w1:"}, pl)
}

// awaitLevelTile reads the level's row, refetched rather than cached: a stale
// BlobID of 0 would install the writable default and let the persister
// overwrite a fresh arrangement, layout writes carrying no version to conflict
// on.
func (m *Machine) awaitLevelTile(ld *levelData, pl *planner) {
	tok := m.mint(cont{Guard: Guard{Kind: GuardAlways}, Step: stepLevelTile,
		Barrier: ld.Barrier, PaneID: ld.PaneID, Level: ld})
	pl.add(Effect{Kind: EffAwait, Token: tok,
		Request: Request{Kind: RequestGetTile, ID: ld.TileID}})
}

func (m *Machine) levelTile(c cont, r Result, pl *planner) Plan {
	ld := c.Level
	if !r.OK || r.Tile == nil {
		return m.levelFailed(ld, "GetTile", r, pl)
	}
	t := r.Tile
	if rpc.LeafLink(t) && !ld.Followed {
		// A pane link opens the target's arrangement, the one shared layout,
		// and the persister writes back through the target id: the same
		// read-through rule as every other content door.
		ld.Followed = true
		ld.TileID = rpc.ContentID(t)
		m.awaitLevelTile(ld, pl)
		return pl.plan()
	}
	if ld.Boot && !rpc.IsWorkspaceKind(t.Kind) {
		pl.add(Effect{Kind: EffReport, Severity: errsurface.Error,
			Source: "layout:" + ld.TileID, Message: "?w= names a non-workspace tile"})
		return pl.plan()
	}
	ld.Tile = t
	if t.BlobId == 0 {
		// Never arranged. A descent captures the window layout at the swap,
		// deferred to install time so the encode reads the tree after the
		// origin pane's place is back; a boot restore has none worth capturing.
		if ld.Boot {
			ld.Tree = m.levelFallbackTree(ld)
		} else {
			ld.Capture = true
		}
		return m.levelReady(ld, pl)
	}
	tok := m.mint(cont{Guard: Guard{Kind: GuardAlways}, Step: stepLevelBody,
		Barrier: ld.Barrier, PaneID: ld.PaneID, Level: ld})
	pl.add(Effect{Kind: EffAwait, Token: tok,
		Request: Request{Kind: RequestReadLayout, ID: ld.TileID}})
	return pl.plan()
}

// levelBody decodes the layout blob. An unreadable blob opens the default
// read-only: overwriting a blob this session could not read would downgrade a
// newer format.
func (m *Machine) levelBody(c cont, r Result, pl *planner) Plan {
	ld := c.Level
	if !r.OK {
		return m.levelFailed(ld, "ReadContent", r, pl)
	}
	ld.Data = r.Data
	prefix := pane.ChainPrefix(ld.TileID)
	tree, err := pane.DecodeLayout(r.Data, func(id string) string { return prefix + id }, ld.IDPrefix)
	if err != nil {
		pl.add(Effect{Kind: EffReport, Severity: errsurface.Error,
			Source:  "layout:" + ld.TileID,
			Message: "workspace layout unreadable — opened read-only: " + err.Error()})
		ld.Tree = m.levelFallbackTree(ld)
		ld.ReadOnly = true
		return m.levelReady(ld, pl)
	}
	ld.Tree = tree
	return m.levelReady(ld, pl)
}

// levelFallbackTree is the single-pane default: the pane tile's grid centred
// on the tile for a boot restore, the origin pane's own place for a descent,
// so an unreadable blob leaves you looking at where you were.
func (m *Machine) levelFallbackTree(ld *levelData) *pane.Tree {
	if ld.Boot {
		t := ld.Tile
		cx, cy := pane.Footprint{X: t.X, Y: t.Y, W: t.W, H: t.H}.Center()
		return pane.TreeAtPlace(ld.IDPrefix, t.GridId, nil, cx, cy, 1)
	}
	return pane.TreeAtPlace(ld.IDPrefix, ld.Origin.Anchor(), ld.Origin.Path(),
		ld.Origin.Cx, ld.Origin.Cy, ld.Origin.Zoom)
}

// levelReady reports the fetch arm. A boot restore installs at once; a descent
// waits for the animation.
func (m *Machine) levelReady(ld *levelData, pl *planner) Plan {
	if ld.Boot {
		m.installLevelData(ld, pl)
		return pl.plan()
	}
	if b, done := m.arrive(ld.Barrier, false); done {
		m.installLevel(b, pl)
	}
	return pl.plan()
}

// levelFailed surfaces a read that did not answer and fails the arm. The
// descent puts the origin viewport back only once the animation has landed, or
// the pane would snap back mid-zoom.
func (m *Machine) levelFailed(ld *levelData, label string, r Result, pl *planner) Plan {
	pl.add(Effect{Kind: EffReport, Severity: errsurface.Error,
		Source: "rpc:" + label, Message: label + " failed: " + r.Err})
	if ld.Boot {
		return pl.plan()
	}
	if b, done := m.arrive(ld.Barrier, true); done {
		m.installLevel(b, pl)
	}
	return pl.plan()
}

// installLevel is the joined step: the origin pane's place goes back, then the
// swap, or nothing when the fetch failed.
func (m *Machine) installLevel(b *barrier, pl *planner) {
	ld := b.Level
	if ld == nil {
		return
	}
	// The animation left the origin pane zoomed into the tile, so its true
	// place goes back before the outer tree is parked or captured.
	st := ld.Origin.Clone()
	pl.add(Effect{Kind: EffInstallPlace, PaneID: ld.PaneID, Stack: &st})
	if b.Failed {
		return
	}
	m.installLevelData(ld, pl)
}

// installLevelData plans the swap: the outer level's animations land, its
// layout flushes, and the new tree takes over with the outer one parked alive.
func (m *Machine) installLevelData(ld *levelData, pl *planner) {
	// Land every animation the outer tree still has, so no pane is parked on
	// an animation's scratch place for as long as this level lasts.
	pl.add(Effect{Kind: EffCancelTransition})
	pl.add(Effect{Kind: EffCloseMenu})
	// The current level's tree is about to sit un-drawn for an unbounded time,
	// so the debounce must not still hold its latest arrangement.
	pl.add(Effect{Kind: EffFlushLayout})
	lvl := pane.Level{
		OriginPane: ld.PaneID,
		TileID:     ld.Tile.Id,
		// Where the pane tile sits: the close-all landing when no tree was
		// parked, and the face the bar's root crumb wears there.
		GridID: ld.Tile.GridId,
		// Raw alt text: the bar substitutes the generic label at draw time, so
		// a crumb rename round-trips an empty name honestly.
		Name:     ld.Tile.AltText,
		ReadOnly: ld.ReadOnly,
	}
	pl.add(Effect{Kind: EffInstallLevel, PaneID: ld.PaneID, TileID: ld.Tile.Id,
		Level: &lvl, Tree: ld.Tree, Baseline: ld.Data, KeepOuter: !ld.Boot,
		Capture: ld.Capture, IDPrefix: ld.IDPrefix})
	// The installed tree's focused leaf may be text-descended, so the textarea
	// singleton rebinds or the overlay keeps tracking its pre-swap tile.
	pl.add(Effect{Kind: EffRefreshOverlay})
	pl.add(Effect{Kind: EffScheduleURLUpdate})
}

// leaveLevels leaves one pane-tile level and hands the rest back: the
// pane-tile axis of the pop whose pane-frame axis is ascend. Each hop restores
// the outer tree verbatim with focus on the origin pane.
func (m *Machine) leaveLevels(g Gesture, w World) Plan {
	var pl planner
	if g.Count <= 0 || w.LevelTop == nil {
		return pl.plan()
	}
	top := *w.LevelTop
	pl.add(Effect{Kind: EffFlushLayout})
	// Land the inner tree's animations before the subtree flush reads each
	// leaf's viewport, or a scratch viewport becomes durable framing.
	pl.add(Effect{Kind: EffCancelTransition})
	pl.add(Effect{Kind: EffCloseMenu})
	pl.add(Effect{Kind: EffFlushDroppedSubtree})
	outer := top.OuterTree != nil
	pop := Effect{Kind: EffPopLevel, TileID: top.TileID}
	if !outer {
		// A level with no parked tree falls back to a fresh pane at the pane
		// tile's containing grid, which is the level's own fact off the row
		// the descent read, so the landing is right on the first frame.
		pop.GridID = top.GridID
		if pop.GridID == "" {
			pop.GridID = w.Home
		}
	}
	pl.add(pop)
	if !outer {
		pl.add(Effect{Kind: EffFetchGrid, GridID: pop.GridID})
	}
	// The landing reads the tree the pop just installed, so it is planned
	// against a world gathered after it.
	pl.then(Gesture{Kind: GestureLandLevel, PaneID: top.OriginPane,
		TileID: top.TileID, Outer: outer, Animate: g.Count == 1, Count: g.Count - 1})
	return pl.plan()
}

// landLevel finishes one hop: the return animation onto the pane tile's
// footprint, or the post-reload re-centre, then the next hop or the tail.
func (m *Machine) landLevel(g Gesture, w World) Plan {
	var pl planner
	if g.Outer {
		if g.Animate {
			m.animateLevelReturn(g, w, &pl)
		}
	} else {
		m.recentreLevelLanding(g, w, &pl)
	}
	if g.Count > 0 {
		pl.then(Gesture{Kind: GestureLeaveLevels, Count: g.Count})
		return pl.plan()
	}
	// The restored tree's focused pane may be text-descended too.
	pl.add(Effect{Kind: EffRefreshOverlay})
	// The restored leaves never froze, so this is a no-op for a still-running
	// pane. It matters for one that lost its surface to the one-surface rule
	// while a higher level held the same tile: that holder just closed.
	for _, p := range w.Panes {
		if id := p.Stack.ContentID(); id != "" {
			pl.add(Effect{Kind: EffReEngage, PaneID: p.ID, TileID: id})
		}
	}
	pl.add(Effect{Kind: EffScheduleURLUpdate})
	return pl.plan()
}

// animateLevelReturn plays the ascent's zoom-out, the reverse of the descent's
// end. Skipped when the tile row is not cached: there is nothing to zoom out
// of.
func (m *Machine) animateLevelReturn(g Gesture, w World, pl *planner) {
	p, ok := w.Pane(g.PaneID)
	if !ok || p.Stack.ContentID() != "" {
		return
	}
	if w.Level == nil || w.Level.Tile == nil {
		return
	}
	t := w.Level.Tile
	cx, cy := pane.Footprint{X: t.X, Y: t.Y, W: t.W, H: t.H}.Center()
	overtake := panebox.FitZoom(p.Rect, t.W, t.H, w.TextSideInset, w.CellPx)
	if overtake < p.Zoom {
		overtake = p.Zoom
	}
	here := p.Stack.Clone()
	pl.add(Effect{Kind: EffStartTransition, PaneID: p.ID, Segments: []transition.Segment{{
		Place:  &here,
		FromCx: cx, FromCy: cy, FromZoom: overtake,
		ToCx: p.Cx, ToCy: p.Cy, ToZoom: p.Zoom,
		DurationMs: w.TransitionMs,
	}}})
}

// recentreLevelLanding centres the post-reload landing on the pane tile the
// window came out of. Only the centring needs the row, so the pane keeps the
// landing grid until the read answers and the untouched guard lets a user who
// navigated meanwhile win.
func (m *Machine) recentreLevelLanding(g Gesture, w World, pl *planner) {
	p, ok := w.Pane(w.Focus)
	if !ok {
		return
	}
	tok := m.mint(cont{
		Guard:  Guard{Kind: GuardPaneUntouched, PaneID: p.ID, Anchor: p.Stack.Anchor()},
		Step:   stepLevelRecentre,
		PaneID: p.ID,
		TileID: g.TileID,
	})
	pl.add(Effect{Kind: EffAwait, Token: tok,
		Request: Request{Kind: RequestGetTile, ID: g.TileID}})
}

func (m *Machine) levelRecentre(c cont, r Result, w World, pl *planner) {
	if !r.OK || r.Tile == nil {
		pl.add(Effect{Kind: EffReport, Severity: errsurface.Error,
			Source: "rpc:GetTile", Message: "GetTile failed: " + r.Err})
		return
	}
	p, ok := w.Pane(c.PaneID)
	if !ok {
		return
	}
	t := r.Tile
	cx, cy := pane.Footprint{X: t.X, Y: t.Y, W: t.W, H: t.H}.Center()
	var st pane.Stack
	st.Reset(pane.Frame{GridID: t.GridId, Cx: cx, Cy: cy, Zoom: p.Zoom})
	pl.add(Effect{Kind: EffInstallPlace, PaneID: c.PaneID, Stack: &st})
	pl.add(Effect{Kind: EffFetchGrid, GridID: t.GridId})
	pl.add(Effect{Kind: EffScheduleURLUpdate})
}
