package nav

import (
	"strconv"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/transition"
)

// The second axis: a pane tile's place is a whole tree of panes, so it is the
// window that descends into one, not a pane. The vocabulary is the frame
// stack's — Push descends, Pop ascends (client/pane, levels.go) — and every
// level stays alive while it is parked.
//
// A descent has two async halves that must join before the swap: the zoom
// animation and the row-plus-layout fetch. They are two arms of one barrier,
// so neither half knows about the other and nothing polls for the second.

// levelData is one level being opened: what the fetch arm learns, and what the
// install needs when the arms join. It travels on the continuation because
// none of it is re-readable at join time — the origin pane's place is by then
// the animation's landing, and the decoded tree is nowhere else.
type levelData struct {
	// Boot marks the ?w= restore: no animation to join, no outer tree to
	// park, and a landing on the pane tile's own grid rather than a capture.
	Boot bool
	// PaneID is the origin pane ("" on boot) and Origin its place, put back
	// before the outer tree is parked so ascent restores what the user left.
	PaneID string
	Origin pane.Stack
	// TileID is the tile whose layout this level is — the pane link's target
	// once the link has been followed. IDPrefix namespaces the pane ids this
	// level mints and decodes, since stacked trees are all alive at once.
	TileID   string
	IDPrefix string
	Barrier  BarrierID
	// Followed latches the one pane-link hop, so links cannot walk forever.
	Followed bool

	// What the fetch arm learns.
	Tile     rpc.Tile
	Data     []byte
	Tree     *pane.Tree
	Capture  bool
	ReadOnly bool
}

// enterLevel descends the window into a pane tile: a zoom into the tile's
// footprint, like every descent, racing the layout fetch, with the swap at
// whichever finishes last.
func (m *Machine) enterLevel(g Gesture, w World) Plan {
	var pl planner
	p, ok := w.Pane(g.PaneID)
	if !ok {
		return pl.plan()
	}
	pt := g.Door
	ld := &levelData{
		PaneID: p.ID,
		// The origin pane's place, for the organize-this default and for the
		// byte-identical viewport restore under the animation.
		Origin:   p.Stack.Clone(),
		TileID:   pt.ID,
		IDPrefix: "w" + strconv.Itoa(w.LevelDepth+1) + ":",
	}
	ld.Barrier = m.mintBarrier(p.ID, 2, ld)

	// The animation arm. A never-arranged tile animates as its face becoming
	// the level outline — an expanding rect, with the content never moving —
	// instead of the zoom, which would read as a jarring descend-and-return
	// over an unchanged view: the first descent captures the window layout, so
	// you keep looking at exactly what you had. The transition still runs, and
	// still times the descent. The CACHED row picks the animation and the
	// fresh row picks the tree; they disagree only across the stale-cache
	// window, where either combination is harmless.
	here := p.Stack.Clone()
	seg := transition.Segment{
		Place:  &here,
		FromCx: p.Cx, FromCy: p.Cy, FromZoom: p.Zoom,
		ToCx: p.Cx, ToCy: p.Cy, ToZoom: p.Zoom,
		DurationMs: w.TransitionMs,
	}
	expand := pt.BlobID == 0
	if !expand {
		// The zoom: pan to the tile's centre while zooming until its footprint
		// fills the pane box, so the preview grows into the live tree.
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

// bootLevel restores the innermost pane tile from a reload (?w=). The outer
// tree is nil by design: nesting membership is session-only, like the outer
// frames of a pane's place. There is no animation, so there is no barrier
// either — the fetch arm installs on its own.
func (m *Machine) bootLevel(tileID string, pl *planner) {
	m.awaitLevelTile(&levelData{Boot: true, TileID: tileID, IDPrefix: "w1:"}, pl)
}

// awaitLevelTile reads the level's row. It is refetched rather than taken from
// the cache: a stale BlobID of 0 — another client's first arrange whose echo
// has not landed here yet — would install the writable default, and the
// persister could then overwrite the fresh arrangement, since layout writes
// carry no version bump to conflict on. One RPC closes the window to genuine
// concurrent edits.
func (m *Machine) awaitLevelTile(ld *levelData, pl *planner) {
	tok := m.mint(cont{Guard: Guard{Kind: GuardAlways}, Step: stepLevelTile,
		Barrier: ld.Barrier, PaneID: ld.PaneID, Level: ld})
	pl.add(Effect{Kind: EffAwait, Token: tok,
		Request: Request{Kind: RequestGetTile, ID: ld.TileID}})
}

// levelTile classifies the row the fetch arm read.
func (m *Machine) levelTile(c cont, r Result, pl *planner) Plan {
	ld := c.Level
	if !r.OK || r.Tile == nil {
		return m.levelFailed(ld, "GetTile", r, pl)
	}
	t := *r.Tile
	if t.LeafLink() && !ld.Followed {
		// A pane link opens the target's arrangement, the one shared layout,
		// and the persister then writes back through the target id too: the
		// same read-through rule as every other content door.
		ld.Followed = true
		ld.TileID = t.ContentID()
		m.awaitLevelTile(ld, pl)
		return pl.plan()
	}
	if ld.Boot && !rpc.IsWorkspaceKind(t.Kind) {
		pl.add(Effect{Kind: EffReport, Severity: errsurface.Error,
			Source: "layout:" + ld.TileID, Message: "?w= names a non-workspace tile"})
		return pl.plan()
	}
	ld.Tile = t
	if t.BlobID == 0 {
		// Never arranged. A descent captures the window layout as it stands at
		// the swap — deferred to install time so the encode reads the tree
		// after the origin pane's place is back. A boot restore has no window
		// worth capturing and opens on the tile's containing grid instead.
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

// levelBody decodes the layout blob. A blob that cannot be read opens the
// default READ-ONLY: the session must never overwrite a blob it could not
// read, since downgrading a newer format would rewrite history.
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

// levelFallbackTree is the single-pane default: the pane tile's containing
// grid centred on the tile for a boot restore, and the origin pane's own place
// for a descent, so an unreadable blob leaves you looking at where you were.
func (m *Machine) levelFallbackTree(ld *levelData) *pane.Tree {
	if ld.Boot {
		t := ld.Tile
		cx, cy := pane.Footprint{X: t.X, Y: t.Y, W: t.W, H: t.H}.Center()
		return pane.TreeAtPlace(ld.IDPrefix, t.GridID, nil, cx, cy, 1)
	}
	return pane.TreeAtPlace(ld.IDPrefix, ld.Origin.Anchor(), ld.Origin.Path(),
		ld.Origin.Cx, ld.Origin.Cy, ld.Origin.Zoom)
}

// levelReady reports the fetch arm: a boot restore installs at once, and a
// descent installs when the animation has landed too.
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

// levelFailed surfaces a read that did not answer and reports the arm as
// failed. The descent then puts the origin viewport back — but only once the
// animation has landed, or the pane would snap back mid-zoom.
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

// installLevel is the joined step: the origin pane's place goes back, and then
// either the swap or nothing at all, when the fetch failed.
func (m *Machine) installLevel(b *barrier, pl *planner) {
	ld := b.Level
	if ld == nil {
		return
	}
	// The animation left the origin pane zoomed into the tile: put its true
	// place back before the outer tree is parked or captured, so ascent
	// restores exactly what the user left. On the failed path that is the
	// whole plan.
	st := ld.Origin.Clone()
	pl.add(Effect{Kind: EffInstallPlace, PaneID: ld.PaneID, Stack: &st})
	if b.Failed {
		return
	}
	m.installLevelData(ld, pl)
}

// installLevelData plans the swap itself: the outer level's animations land,
// its layout flushes, and the new tree takes over with the outer one parked
// alive behind it.
func (m *Machine) installLevelData(ld *levelData, pl *planner) {
	// The outer tree is about to stop being drawn: land every animation it
	// still has, so no pane is parked on an animation's scratch place for as
	// long as this level lasts.
	pl.add(Effect{Kind: EffCancelTransition})
	pl.add(Effect{Kind: EffCloseMenu})
	// Entering a nested level: flush the current one's layout first. Its tree
	// is about to sit un-drawn in a level for an unbounded time, and the
	// debounce must not still be holding its latest arrangement. A no-op at
	// depth 0.
	pl.add(Effect{Kind: EffFlushLayout})
	lvl := pane.Level{
		OriginPane: ld.PaneID,
		TileID:     ld.Tile.ID,
		// Where the pane tile sits, off the row this descent already read: the
		// close-all landing when no tree was parked, and so the face the bar's
		// root crumb wears there instead of an anonymous square.
		GridID: ld.Tile.GridID,
		// Raw alt text: the bar substitutes the generic label at draw time, so
		// the crumb rename can round-trip an empty name honestly.
		Name:     ld.Tile.AltText,
		ReadOnly: ld.ReadOnly,
	}
	pl.add(Effect{Kind: EffInstallLevel, PaneID: ld.PaneID, TileID: ld.Tile.ID,
		Level: &lvl, Tree: ld.Tree, Baseline: ld.Data, KeepOuter: !ld.Boot,
		Capture: ld.Capture, IDPrefix: ld.IDPrefix})
	// The installed tree's focused leaf may be text-descended, from a restored
	// text_focus: rebind the textarea singleton to it, or the overlay keeps
	// showing, and scroll-tracking against, whatever tile it was bound to
	// before the swap.
	pl.add(Effect{Kind: EffRefreshOverlay})
	pl.add(Effect{Kind: EffScheduleURLUpdate})
}

// leaveLevels leaves one pane-tile level and hands the rest back: the pane-tile
// axis of the same pop whose pane-frame axis is ascend(). Each hop flushes the
// layout one last time, freezes and forgets the inner leaves, pops, and
// restores the outer tree verbatim with focus back on the origin pane.
func (m *Machine) leaveLevels(g Gesture, w World) Plan {
	var pl planner
	if g.Count <= 0 || w.LevelTop == nil {
		return pl.plan()
	}
	top := *w.LevelTop
	pl.add(Effect{Kind: EffFlushLayout})
	// The inner tree is about to be dropped: land its animations before the
	// subtree flush reads each leaf's viewport, or a scratch viewport becomes
	// the pane's durable framing.
	pl.add(Effect{Kind: EffCancelTransition})
	pl.add(Effect{Kind: EffCloseMenu})
	pl.add(Effect{Kind: EffFlushDroppedSubtree})
	outer := top.OuterTree != nil
	pop := Effect{Kind: EffPopLevel, OriginPane: top.OriginPane, TileID: top.TileID}
	if !outer {
		// A level with no parked tree, from a boot restore, falls back to a
		// fresh pane at the pane tile's containing grid — the same degradation
		// an ascent has after a reload. The grid is the level's own fact, the
		// row the descent read, so the landing is right on the first frame.
		pop.GridID = top.GridID
		if pop.GridID == "" {
			pop.GridID = w.Home
		}
	}
	pl.add(pop)
	if !outer {
		pl.add(Effect{Kind: EffFetchGrid, GridID: pop.GridID})
	}
	// The landing reads the tree the pop just installed — which pane is
	// focused, where it sits, how big it is — so it is planned against a world
	// gathered after it.
	pl.then(Gesture{Kind: GestureLandLevel, PaneID: top.OriginPane,
		TileID: top.TileID, Outer: outer, Animate: g.Count == 1, Count: g.Count - 1})
	return pl.plan()
}

// landLevel finishes one hop: the return animation onto the pane tile's
// footprint, or the post-reload landing's re-centre, and then the next hop or
// the tail.
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
	// Same rebind as an install: the restored outer tree's focused pane may
	// itself be text-descended, and the singleton must follow the swap.
	pl.add(Effect{Kind: EffRefreshOverlay})
	// The restored outer leaves never froze — the boundary keeps every level
	// alive — so for a still-running pane this is a no-op, the stream openers
	// being idempotent. It matters for the panes that lost their surface to
	// the one-surface rule while a higher level held the same tile: the holder
	// just closed, so the surface is free again and the pane re-engages,
	// through the one owner of that decision.
	for _, p := range w.Panes {
		if id := p.Stack.ContentID(); id != "" {
			pl.add(Effect{Kind: EffReEngage, PaneID: p.ID, TileID: id})
		}
	}
	pl.add(Effect{Kind: EffScheduleURLUpdate})
	return pl.plan()
}

// animateLevelReturn plays the ascent's zoom-out: the origin pane starts
// zoomed into the pane tile's footprint, the reverse of the descent's end, and
// animates back to its restored viewport. Skipped, for an instant landing,
// when the tile row is not in the cached grid: there is nothing to zoom out
// of.
func (m *Machine) animateLevelReturn(g Gesture, w World, pl *planner) {
	p, ok := w.Pane(g.PaneID)
	if !ok || p.Stack.ContentID() != "" {
		return
	}
	if w.Level == nil || w.Level.Tile == nil {
		return
	}
	t := *w.Level.Tile
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
// window just came out of. Only the centring needs the row, and reading it is
// asynchronous, so the pane keeps the landing grid until the answer comes: a
// user who has already navigated wins over the late fetch, which is what the
// untouched guard says.
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

// levelRecentre applies that landing once the row has been read.
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
	t := *r.Tile
	cx, cy := pane.Footprint{X: t.X, Y: t.Y, W: t.W, H: t.H}.Center()
	var st pane.Stack
	st.Reset(pane.Frame{GridID: t.GridID, Cx: cx, Cy: cy, Zoom: p.Zoom})
	pl.add(Effect{Kind: EffInstallPlace, PaneID: c.PaneID, Stack: &st})
	pl.add(Effect{Kind: EffFetchGrid, GridID: t.GridID})
	pl.add(Effect{Kind: EffScheduleURLUpdate})
}
