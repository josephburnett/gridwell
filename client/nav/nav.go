// Package nav owns navigation as data.
//
// A gesture plus a world snapshot goes in; an ordered effect list comes out.
// The shim (client/wasm) gathers the snapshot, executes the effects, and feeds
// async answers back in. No navigation decision lives on the shim side, and
// nothing here touches syscall/js, the DOM, or the network — which is the
// whole point: `make check` executes this package and executes none of the
// shim.
//
// The machine reads pane.Stack (client/pane, place.go) and projects it. It
// never stores a second copy of where a pane is. The one fact it owns is the
// set of suspended continuations: which async answers are still owed, and
// what each one still needs to be true when it lands.
//
// Design: docs/debt/w1-nav-orchestration.md.
package nav

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
)

// Token names one suspended continuation: minted by the machine, carried on
// the effect that starts the wait, and handed back with the answer. Zero is
// "no continuation".
type Token uint64

// BarrierID names a join between two continuations that must both report
// before their shared step runs — the pane-tile descent's animation and fetch
// arms. Zero is "no barrier".
type BarrierID uint64

// barrier is one join. Arms counts down as its continuations report; the
// joined step runs when the last one does, and Failed says whether any arm
// failed, so a descent whose fetch died still waits for the animation to land
// before it puts the origin viewport back.
//
// A barrier lives and dies with its arms: an arm retired by a guard that no
// longer holds takes the barrier with it, so nothing waits on an answer that
// will never come.
type barrier struct {
	PaneID string
	Arms   int
	Failed bool
	Level  *levelData
}

// Plan is what one gesture, or one resumed continuation, asks the shim to do.
//
// Next, when set, is a continuation GESTURE: the shim gathers a fresh world
// and calls Do again with it. It is how a step whose successor must read
// state the effects above it just changed stays honest — a descent that first
// lands a running transition, and each hop of a multi-level ascent, both
// compute from the place the previous effects left, exactly as the sequential
// code they replace did.
type Plan struct {
	Effects []Effect
	Next    *Gesture
}

// Machine is the navigation state machine. Its only mutable state is the
// suspended continuations: session-scoped, never persisted, and every one
// retired by exactly one of a resume, a land, or a guard that no longer
// holds.
type Machine struct {
	next  Token
	conts map[Token]cont

	nextBarrier BarrierID
	barriers    map[BarrierID]*barrier

	// The one history writer's push-against-replace baseline: the structural
	// place the last write named, and whether there has been one. They are
	// navigation facts — "did the user go somewhere, or just pan?" — so they
	// live with the verbs that move the pane rather than beside the DOM call
	// that spends them.
	urlPrevPlace pane.URLPlace
	urlPlaceSeen bool
	// urlRestoring marks a popstate restore in flight, which owns the URL:
	// it re-encodes the place the browser already navigated to, and a write
	// from anywhere else would clobber that entry.
	urlRestoring bool
}

// New returns a machine with nothing outstanding.
func New() *Machine {
	return &Machine{conts: map[Token]cont{}, barriers: map[BarrierID]*barrier{}}
}

// cont is one suspended continuation: what must still be true when the answer
// lands, what to do then, and the gesture-time facts that step needs. The
// data travels on the continuation rather than being re-read at resume,
// because some of it is not re-readable — an ephemeral scratch tile is in no
// cached grid, so a cache lookup at transition end would miss it.
type cont struct {
	Guard   Guard
	Step    step
	Barrier BarrierID

	PaneID string
	TileID string
	Tile   rpc.Tile
	Stack  pane.Stack
	// Restore is set on the restore paths' continuations, whose data is a
	// whole decoded address mid-walk. They leave PaneID empty on purpose:
	// see awaitGrid.
	Restore *restoreData
	// Level is set on a pane-tile descent's or boot restore's continuations:
	// the level being opened, filled in arm by arm.
	Level *levelData
}

// step is the closed set of things the machine does when an answer lands.
type step int

const (
	stepNone step = iota
	// stepDescendContentLand installs the content place a descent animated
	// towards, and engages it.
	stepDescendContentLand
	// stepAscendLand finishes an animated ascent on the frame it landed on.
	stepAscendLand
	// stepProbedShell re-decides a shell descent once its liveness probe
	// answers.
	stepProbedShell
	// stepReEngage applies the go-live verdict to a restored content frame
	// once its row has been read, healing a stale path first.
	stepReEngage
	// stepHealed re-anchors a restored pane once the locate answers, and then
	// engages it.
	stepHealed
	// stepRestoreRoot frames a pathless restore once the anchor grid has been
	// asked for.
	stepRestoreRoot
	// stepRestoreWalk re-runs the URL walk against the warmer snapshot.
	stepRestoreWalk
	// stepRestoreCursor places the text cursor the address encodes, once the
	// body has seeded the textarea.
	stepRestoreCursor
	// stepLevelAnimated reports the pane-tile descent's animation arm.
	stepLevelAnimated
	// stepLevelTile classifies a level's row once it has been read: a pane
	// link redirects to its target, a never-arranged tile captures, and
	// anything else reads its blob.
	stepLevelTile
	// stepLevelBody decodes a level's layout blob.
	stepLevelBody
	// stepLevelRecentre centres a post-reload ascent landing on the pane tile
	// it came out of, once that row has been read.
	stepLevelRecentre
	// stepLinkTarget places a live url view on a link's target row.
	stepLinkTarget
)

// mint registers c and returns its token.
func (m *Machine) mint(c cont) Token {
	m.next++
	m.conts[m.next] = c
	return m.next
}

// take retires the continuation for tok and returns it.
func (m *Machine) take(tok Token) (cont, bool) {
	c, ok := m.conts[tok]
	if ok {
		delete(m.conts, tok)
	}
	return c, ok
}

// Forget retires every continuation waiting on paneID, and any barrier they
// were arms of. A pane that is going away is the one case a transition is
// dropped rather than landed, so nothing else would ever retire them, and a
// guard naming a pane that no longer exists can never hold again.
func (m *Machine) Forget(paneID string) {
	for tok, c := range m.conts {
		if c.PaneID == paneID {
			delete(m.conts, tok)
			m.dropBarrier(c)
		}
	}
}

// dropBarrier retires the join a never-arriving continuation was an arm of:
// an answer that will never land must not leave one waiting forever.
func (m *Machine) dropBarrier(c cont) {
	if c.Barrier != 0 {
		delete(m.barriers, c.Barrier)
	}
}

// mintBarrier registers a join of arms continuations for paneID and returns
// its id. A newer barrier on the same pane supersedes the older one, which is
// dropped without running: its arms then arrive to find nothing waiting, which
// is what a superseded pane-tile descent has always done.
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

// arrive reports one arm of bid, and returns the barrier when it was the last
// one owed. A superseded (or already-resolved) barrier answers false, and the
// arm's answer is dropped.
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

// LevelPending reports whether a pane-tile descent is still between its
// gesture and its install: the animation is running, the layout is in flight,
// or both. The shim reads it for the idle signal the e2e harness polls, and
// for the capture animation's rect, which is drawn exactly while this holds.
func (m *Machine) LevelPending() bool { return len(m.barriers) > 0 }

// Do plans gesture g against world w.
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

// URLWritable reports whether the one history writer may write now. A
// popstate restore in flight owns the URL until its last step hands it back.
func (m *Machine) URLWritable() bool { return !m.urlRestoring }

// URLWrote records the structural place a history write named and answers
// push against replace over the diff. pane.URLPushesEntry owns the rule; the
// machine owns the baseline it diffs against, so no call site carries a
// "structural" bit and a forgotten flag is unrepresentable.
func (m *Machine) URLWrote(place pane.URLPlace) (push bool) {
	push = pane.URLPushesEntry(m.urlPrevPlace, place, m.urlPlaceSeen)
	m.urlPrevPlace = place
	m.urlPlaceSeen = true
	return push
}

// Resume delivers an awaited answer. The guard is evaluated against the fresh
// snapshot: false retires the continuation and the plan is empty. That is the
// whole moved-on rule, spelled once, in one place, for every path.
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
		// Alive → open; dead → stay frozen, and the refresh affordance is the
		// retry.
		if r.Alive {
			pl.add(Effect{Kind: EffOpenStream, PaneID: c.PaneID, TileID: c.TileID,
				Stream: StreamShell})
		}
	case stepReEngage:
		// A leaf whose reference no longer resolves stays frozen.
		if !r.OK || r.Tile == nil {
			break
		}
		// A stale path is healed BEFORE the engagement, because the heal moves
		// the place the surface is opened into.
		if m.healStale(c.PaneID, *r.Tile, w, &pl) {
			break
		}
		m.autoLiveOnDescent(c.PaneID, *r.Tile, w, &pl)
	case stepHealed:
		// An unsearchable tile, from a plugin without Search, keeps the place
		// it was restored with; the engagement happens either way.
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
			TileID: r.Tile.ID, Tile: *r.Tile})
	case stepRestoreCursor:
		// The bytes have landed and seeded the textarea; the cursor the
		// address encodes goes after the seeding, or it lands in an empty
		// document and is lost.
		if r.OK && c.Restore.State.CursorMode {
			pl.add(Effect{Kind: EffPlaceCursor,
				Col: c.Restore.State.Col, Row: c.Restore.State.Row})
		}
	}
	return pl.plan()
}

// Land resumes the continuation a transition carried, whether it ran to the
// end or was cut short. A cancelled transition still lands — that contract is
// client/transition's — so this is called either way.
func (m *Machine) Land(tok Token, w World) Plan {
	c, ok := m.take(tok)
	if !ok || !c.Guard.holds(w) {
		return Plan{}
	}
	var pl planner
	switch c.Step {
	case stepDescendContentLand:
		st := c.Stack.Clone()
		pl.add(Effect{Kind: EffInstallPlace, PaneID: c.PaneID, Stack: &st})
		// base × content zoom (issue #82).
		pl.add(Effect{Kind: EffScaleContent, PaneID: c.PaneID})
		// Unsaved-edit state is untouched here: it lives tile-scoped in the
		// cache, so descending this pane elsewhere cannot strand a previous
		// document's typing.
		pl.add(Effect{Kind: EffRefreshOverlay})
		// Descending is the engagement gesture: a url reopens, a shell
		// reconnects, or creates when fresh. One owner decides; call sites
		// never hand-roll go-live.
		m.autoLiveOnDescent(c.PaneID, c.Tile, w, &pl)
		// The completed descent is the new place, so write it; the one
		// history writer derives push against replace from the diff. A
		// gesture-time write would run mid-transition with the content frame
		// not yet pushed. An editable file would paper over that through
		// later textarea cursor events, but a read-only file has no textarea,
		// so its descent would never reach the URL and a reload would restore
		// the parent grid instead.
		pl.add(Effect{Kind: EffScheduleURLUpdate})
	case stepAscendLand:
		// The pane may have been closed mid-flight, and its place is now the
		// landing the segments installed, so read it fresh.
		p, ok := w.Pane(c.PaneID)
		if !ok {
			return Plan{}
		}
		m.landOnFrame(p.ID, p.Stack, &pl)
	case stepLevelAnimated:
		// The descent's animation arm: it reports and waits, because the
		// install needs the layout too.
		if b, done := m.arrive(c.Barrier, false); done {
			m.installLevel(b, &pl)
		}
	}
	return pl.plan()
}

// planner accumulates a plan in order.
type planner struct {
	effects []Effect
	next    *Gesture
}

func (p *planner) add(e Effect)   { p.effects = append(p.effects, e) }
func (p *planner) then(g Gesture) { c := g; p.next = &c }
func (p *planner) plan() Plan     { return Plan{Effects: p.effects, Next: p.next} }
