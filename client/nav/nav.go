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
	"github.com/josephburnett/gridwell/client/pane"
)

// Token names one suspended continuation: minted by the machine, carried on
// the effect that starts the wait, and handed back with the answer. Zero is
// "no continuation".
type Token uint64

// BarrierID names a join between two continuations that must both report
// before their shared step runs — the pane-tile descent's animation and fetch
// arms (wsPending today). Phase C wires the arrival counting, which is why
// there is no arrival function here yet; the field exists now so the
// continuation shape does not change under it.
type BarrierID uint64

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
}

// New returns a machine with nothing outstanding.
func New() *Machine { return &Machine{conts: map[Token]cont{}} }

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

// Forget retires every continuation waiting on paneID. A pane that is going
// away is the one case a transition is dropped rather than landed, so nothing
// else would ever retire them, and a guard naming a pane that no longer
// exists can never hold again.
func (m *Machine) Forget(paneID string) {
	for tok, c := range m.conts {
		if c.PaneID == paneID {
			delete(m.conts, tok)
		}
	}
}

// Do plans gesture g against world w.
func (m *Machine) Do(g Gesture, w World) Plan {
	switch g.Kind {
	case GestureDescend:
		return m.descend(g, w)
	case GestureAscend:
		return m.ascend(g, w)
	case GestureReEngage:
		return m.reEngage(g, w)
	}
	// Restore, RestoreFromHistory, Promote, EnterLevel and LeaveLevels are
	// phases B and C; the shim still owns them and never hands them here.
	return Plan{}
}

// Resume delivers an awaited answer. The guard is evaluated against the fresh
// snapshot: false retires the continuation and the plan is empty. That is the
// whole moved-on rule, spelled once, in one place, for every path.
func (m *Machine) Resume(tok Token, r Result, w World) Plan {
	c, ok := m.take(tok)
	if !ok || !c.Guard.holds(w) {
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
