// Package nav owns navigation as data: a gesture plus a world snapshot goes
// in, an ordered effect list comes out. No navigation decision lives on the
// shim's side, because `make check` executes this package and none of the
// shim. The machine projects pane.Stack and never copies where a pane is; the
// one fact it owns is the suspended continuations.
package nav

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
)

// Token names one suspended continuation, carried on the effect that starts
// the wait and handed back with the answer. Zero is none.
type Token uint64

// BarrierID names a join between continuations that must both report before
// their shared step runs. Zero is none.
type BarrierID uint64

// barrier is one join. Failed is recorded rather than acted on, so a descent
// whose fetch died still waits for the animation to land before putting the
// origin viewport back. An arm retired by a guard takes the barrier with it,
// so nothing waits on an answer that will never come.
type barrier struct {
	PaneID string
	Arms   int
	Failed bool
	Level  *levelData
}

// Plan is what one gesture, or one resumed continuation, asks the shim to do.
// Next is a continuation gesture the shim replans against a fresh world, so a
// step whose successor must read what the effects above it changed computes
// from the place those effects left.
type Plan struct {
	Effects []Effect
	Next    *Gesture
}

// Machine is the navigation state machine. Its only mutable state is the
// suspended continuations, each retired by exactly one of a resume, a land, or
// a guard that no longer holds.
type Machine struct {
	next  Token
	conts map[Token]cont

	nextBarrier BarrierID
	barriers    map[BarrierID]*barrier

	// The history writer's push-against-replace baseline. Whether the user
	// went somewhere or only panned is a navigation fact, so it lives with the
	// verbs that move the pane rather than beside the DOM call.
	urlPrevPlace pane.URLPlace
	urlPlaceSeen bool
	// urlRestoring marks a popstate restore in flight, which owns the URL: it
	// re-encodes the place the browser already navigated to, and any other
	// write would clobber that entry.
	urlRestoring bool
}

func New() *Machine {
	return &Machine{conts: map[Token]cont{}, barriers: map[BarrierID]*barrier{}}
}

// cont is one suspended continuation. Its facts travel with it because some
// are not re-readable at resume: an ephemeral scratch tile is in no cached
// grid.
type cont struct {
	Guard   Guard
	Step    step
	Barrier BarrierID

	PaneID string
	TileID string
	Tile   *gridwellv1.Tile
	Stack  pane.Stack
	// Restore is set on the restore paths' continuations, whose data is a
	// decoded address mid-walk. They leave PaneID empty; see awaitGrid.
	Restore *restoreData
	// Level is the level being opened, filled in arm by arm.
	Level *levelData
}

// step is the closed set of things the machine does when an answer lands.
type step int

const (
	stepNone step = iota
	// stepDescendContentLand installs the content place a descent animated
	// towards, and engages it.
	stepDescendContentLand
	stepAscendLand  // finishes an animated ascent on the frame it landed on
	stepProbedShell // re-decides a shell descent once its probe answers
	// stepReEngage applies the go-live verdict to a restored content frame
	// once its row has been read, healing a stale path first.
	stepReEngage
	stepHealed      // re-anchors a restored pane once the locate answers
	stepRestoreRoot // frames a pathless restore once its anchor was asked for
	stepRestoreWalk // re-runs the URL walk against the warmer snapshot
	// stepRestoreCursor places the cursor the address encodes, once the body
	// has seeded the textarea.
	stepRestoreCursor
	stepLevelAnimated // reports the pane-tile descent's animation arm
	// stepLevelTile classifies a level's row: a pane link redirects to its
	// target, a never-arranged tile captures, and anything else reads its
	// blob.
	stepLevelTile
	stepLevelBody // decodes a level's layout blob
	// stepLevelRecentre centres a post-reload ascent landing on the pane tile
	// it came out of, once that row has been read.
	stepLevelRecentre
	stepLinkTarget // places a live url view on a link's target row
)

func (m *Machine) mint(c cont) Token {
	m.next++
	m.conts[m.next] = c
	return m.next
}

func (m *Machine) take(tok Token) (cont, bool) {
	c, ok := m.conts[tok]
	if ok {
		delete(m.conts, tok)
	}
	return c, ok
}

// Forget retires every continuation waiting on paneID and any barrier they
// armed. A pane going away is the one case a transition is dropped rather
// than landed, so nothing else would retire them.
func (m *Machine) Forget(paneID string) {
	for tok, c := range m.conts {
		if c.PaneID == paneID {
			delete(m.conts, tok)
			m.dropBarrier(c)
		}
	}
}

// dropBarrier retires the join a never-arriving continuation armed.
func (m *Machine) dropBarrier(c cont) {
	if c.Barrier != 0 {
		delete(m.barriers, c.Barrier)
	}
}

// mintBarrier registers a join of arms continuations for paneID. A newer
// barrier on the same pane supersedes the older one, whose arms then arrive to
// find nothing waiting.
func (m *Machine) mintBarrier(paneID string, arms int, ld *levelData) BarrierID {
	for id, b := range m.barriers {
		if b.PaneID == paneID {
			delete(m.barriers, id)
		}
	}
	m.nextBarrier++
	m.barriers[m.nextBarrier] = &barrier{PaneID: paneID, Arms: arms, Level: ld}
	return m.nextBarrier
}

// arrive reports one arm of bid, returning the barrier when it was the last
// owed. A superseded or resolved barrier answers false and the arm is dropped.
func (m *Machine) arrive(bid BarrierID, failed bool) (*barrier, bool) {
	b, ok := m.barriers[bid]
	if !ok {
		return nil, false
	}
	if failed {
		b.Failed = true
	}
	b.Arms--
	if b.Arms > 0 {
		return nil, false
	}
	delete(m.barriers, bid)
	return b, true
}

// LevelPending is a pane-tile descent between its gesture and its install. The
// shim reads it for the e2e idle signal and the capture animation's rect.
func (m *Machine) LevelPending() bool { return len(m.barriers) > 0 }

func (m *Machine) Do(g Gesture, w World) Plan {
	switch g.Kind {
	case GestureDescend:
		return m.descend(g, w)
	case GestureAscend:
		return m.ascend(g, w)
	case GestureReEngage:
		return m.reEngage(g, w)
	case GestureRestore:
		return m.restore(g, w)
	case GestureRestoreFromHistory:
		return m.restoreFromHistory(g, w)
	case GestureEnterLevel:
		return m.enterLevel(g, w)
	case GestureLeaveLevels:
		return m.leaveLevels(g, w)
	case GestureLandLevel:
		return m.landLevel(g, w)
	case GesturePromote:
		return m.promote(g, w)
	case GestureFollowLink:
		return m.followLink(g, w)
	}
	return Plan{}
}

// URLWritable reports whether the one history writer may write now: a popstate
// restore in flight owns the URL until its last step hands it back.
func (m *Machine) URLWritable() bool { return !m.urlRestoring }

// URLWrote records the place a history write named and answers push against
// replace. pane.URLPushesEntry owns the rule and the machine the baseline, so
// no call site carries a structural bit to forget.
func (m *Machine) URLWrote(place pane.URLPlace) (push bool) {
	push = pane.URLPushesEntry(m.urlPrevPlace, place, m.urlPlaceSeen)
	m.urlPrevPlace = place
	m.urlPlaceSeen = true
	return push
}

// Resume delivers an awaited answer. A guard that no longer holds retires the
// continuation and the plan is empty; that is the whole moved-on rule.
func (m *Machine) Resume(tok Token, r Result, w World) Plan {
	c, ok := m.take(tok)
	if !ok {
		return Plan{}
	}
	if !c.Guard.holds(w) {
		m.dropBarrier(c)
		return Plan{}
	}
	var pl planner
	switch c.Step {
	case stepNone:
	case stepProbedShell:
		// A dead shell stays frozen; the refresh affordance is the retry.
		if r.Alive {
			pl.add(Effect{Kind: EffOpenStream, PaneID: c.PaneID, TileID: c.TileID,
				Stream: StreamShell})
		}
	case stepReEngage:
		// A leaf whose reference no longer resolves stays frozen.
		if !r.OK || r.Tile == nil {
			break
		}
		// The heal moves the place the surface is opened into, so it comes
		// first.
		if m.healStale(c.PaneID, r.Tile, w, &pl) {
			break
		}
		m.autoLiveOnDescent(c.PaneID, r.Tile, w, &pl)
	case stepHealed:
		// A tile from a plugin without Search keeps the place it was restored
		// with; the engagement happens either way.
		if r.OK {
			landHealed(c.PaneID, c.Tile, r.Wells, &pl)
		}
		m.autoLiveOnDescent(c.PaneID, c.Tile, w, &pl)
	case stepRestoreRoot:
		return m.restoreRoot(c.Restore, w, &pl)
	case stepRestoreWalk:
		return m.restoreWalk(c.Restore, w, &pl)
	case stepLevelTile:
		return m.levelTile(c, r, &pl)
	case stepLevelBody:
		return m.levelBody(c, r, &pl)
	case stepLevelRecentre:
		m.levelRecentre(c, r, w, &pl)
	case stepLinkTarget:
		if !r.OK || r.Tile == nil {
			pl.add(Effect{Kind: EffReport, Severity: errsurface.Error,
				Source: "rpc:GetTile", Message: "GetTile failed: " + r.Err})
			break
		}
		pl.add(Effect{Kind: EffPlaceURLView, PaneID: c.PaneID,
			TileID: r.Tile.Id, Tile: r.Tile})
	case stepRestoreCursor:
		// The cursor goes after the seeding, or it lands in an empty document
		// and is lost.
		if r.OK && c.Restore.State.CursorMode {
			pl.add(Effect{Kind: EffPlaceCursor,
				Col: c.Restore.State.Col, Row: c.Restore.State.Row})
		}
	}
	return pl.plan()
}

// Land resumes the continuation a transition carried. A cancelled transition
// still lands, by client/transition's contract, so this is called either way.
func (m *Machine) Land(tok Token, w World) Plan {
	c, ok := m.take(tok)
	if !ok || !c.Guard.holds(w) {
		return Plan{}
	}
	var pl planner
	switch c.Step {
	case stepDescendContentLand:
		pl.install(c.PaneID, c.Stack, nil)
		pl.add(Effect{Kind: EffScaleContent, PaneID: c.PaneID})
		// Unsaved edits are untouched: they live tile-scoped in the cache, so
		// descending this pane elsewhere strands no typing.
		pl.add(Effect{Kind: EffRefreshOverlay})
		// Descending is the engagement gesture, and one owner decides it.
		m.autoLiveOnDescent(c.PaneID, c.Tile, w, &pl)
		// Not at gesture time: that runs mid-transition with the content
		// frame not yet pushed, and a read-only file has no textarea whose
		// cursor events would paper over it.
		pl.add(Effect{Kind: EffScheduleURLUpdate})
	case stepAscendLand:
		// The pane may have closed mid-flight, and its place is now the
		// landing the segments installed, so read it fresh.
		p, ok := w.Pane(c.PaneID)
		if !ok {
			return Plan{}
		}
		m.landOnFrame(p.ID, p.Stack, &pl)
	case stepLevelAnimated:
		// The animation arm reports and waits: the install needs the layout
		// too.
		if b, done := m.arrive(c.Barrier, false); done {
			m.installLevel(b, &pl)
		}
	}
	return pl.plan()
}

type planner struct {
	effects []Effect
	next    *Gesture
}

func (p *planner) add(e Effect)   { p.effects = append(p.effects, e) }
func (p *planner) then(g Gesture) { c := g; p.next = &c }
func (p *planner) plan() Plan     { return Plan{Effects: p.effects, Next: p.next} }

// install installs a pane's place on the plan's own copy of the stack, so what
// the caller does with its own afterwards reaches nobody.
func (p *planner) install(paneID string, st pane.Stack, vp *Viewport) {
	c := st.Clone()
	p.add(Effect{Kind: EffInstallPlace, PaneID: paneID, Stack: &c, Viewport: vp})
}
