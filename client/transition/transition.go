// Package transition owns the viewport animation a pane runs when it descends
// through a doorway or ascends back out.
//
// Two decisions live here, and both used to be spelled in the shim as a single
// pointer field:
//
//   - A transition belongs to a PANE, not to the app. Two panes may animate at
//     once, and starting one pane's transition says nothing about another's.
//   - A transition that is displaced or cleared LANDS. Its destination place is
//     installed and its landing runs, so the frame push a descent was animating
//     towards is never conditional on the animation being allowed to finish. A
//     descent that is visibly animated and then dropped is the "it just didn't
//     happen" bug: the pane is stranded on the animation's scratch viewport,
//     with the place it left already gone.
//
// The package holds no DOM and no timers. The shim supplies two callbacks —
// one that installs a segment's start state on its pane, one that runs a
// landing — and drives the clock.
package transition

import "github.com/josephburnett/gridwell/client/pane"

// Segment is one leg of a transition: the place the pane is in for the leg,
// and the viewport motion within it.
//
// Place is a snapshot installed when the segment begins. Path-switch points
// (the descent's frame push, the ascent's pop) are segment boundaries, so the
// viewport the animation writes is always scratch in the segment's own place,
// and the frame the pane will ascend onto keeps the viewport it was left at.
type Segment struct {
	Place                    *pane.Stack
	FromCx, FromCy, FromZoom float64
	ToCx, ToCy, ToZoom       float64
	DurationMs               float64
}

// End is the segment as a start state at its own destination: the same place
// with the viewport already at the end of the motion. Installing it is how a
// transition jumps to where it was going.
func (s Segment) End() Segment {
	s.FromCx, s.FromCy, s.FromZoom = s.ToCx, s.ToCy, s.ToZoom
	return s
}

// Transition is one pane's animation: the segments in order, plus what the
// arrival means.
//
// OnComplete runs once the last segment lands. A content descent pushes its
// frame there, so the tile's controls do not pop into view mid-animation —
// which is why a lost landing loses the descent itself. TraceTileID, set on
// ascents only, arms the fading "you just came from here" outline.
type Transition struct {
	PaneID      string
	Segments    []Segment
	OnComplete  func()
	TraceTileID string

	current int
	startMs float64
}

// Segment is the leg currently animating.
func (t *Transition) Segment() Segment { return t.Segments[t.current] }

// StartMs is when the current leg began, on the clock the caller ticks with.
func (t *Transition) StartMs() float64 { return t.startMs }

// Set is every live transition, one per pane at most.
//
// enter installs a segment's place and viewport on its pane; land is
// everything arrival means (the selection, the refetch, the trace, and the
// transition's own OnComplete). Both are the shim's, because both touch state
// the shim owns; the sequencing is here, so no caller can perform half of one.
type Set struct {
	enter func(paneID string, seg Segment)
	land  func(t *Transition)
	live  []*Transition
}

// New builds an empty set over the two callbacks.
func New(enter func(paneID string, seg Segment), land func(t *Transition)) *Set {
	return &Set{enter: enter, land: land}
}

// Get returns paneID's live transition, or nil.
func (s *Set) Get(paneID string) *Transition {
	for _, t := range s.live {
		if t.PaneID == paneID {
			return t
		}
	}
	return nil
}

// Active reports whether paneID is animating. It is the one question every
// "not while the viewport is scratch" guard asks — most importantly the
// framing writeback, which must never persist an animation's intermediate
// viewport as the user's durable framing.
func (s *Set) Active(paneID string) bool { return s.Get(paneID) != nil }

// Any reports whether any pane is animating: the gesture gate, which stays
// whole-window so a tree-and-viewport change is atomic across the zoom.
func (s *Set) Any() bool { return len(s.live) > 0 }

// List is a snapshot of the live transitions, safe to iterate while landing
// them.
func (s *Set) List() []*Transition {
	out := make([]*Transition, len(s.live))
	copy(out, s.live)
	return out
}

// Start installs t as its pane's transition and primes the first segment.
//
// Whatever that pane was already animating is cancelled — landed on its
// destination first, never dropped. Callers that compute their segments from
// the pane's current place must cancel before they read it, or they build on
// the outgoing animation's scratch state; Cancel here is the backstop that
// keeps a displaced landing from vanishing whichever door started the new one.
func (s *Set) Start(t *Transition, now float64) {
	if len(t.Segments) == 0 {
		return
	}
	s.Cancel(t.PaneID)
	t.current = 0
	t.startMs = now
	s.live = append(s.live, t)
	s.enter(t.PaneID, t.Segments[0])
}

// Advance ends the current segment of paneID's transition, now: the next
// segment begins, or the transition lands. Reports whether it is still
// animating.
func (s *Set) Advance(paneID string, now float64) bool {
	t := s.Get(paneID)
	if t == nil {
		return false
	}
	t.current++
	if t.current >= len(t.Segments) {
		s.finish(t)
		return false
	}
	t.startMs = now
	s.enter(t.PaneID, t.Segments[t.current])
	return true
}

// Cancel ends paneID's transition early, ON ITS DESTINATION: the final
// segment's place and end viewport are installed and the landing runs. A
// cancelled descent is a completed descent that skipped the animation, which
// is the whole point — "the frame push happened" is not conditional on the
// clock. Reports whether there was one.
func (s *Set) Cancel(paneID string) bool {
	t := s.Get(paneID)
	if t == nil {
		return false
	}
	t.current = len(t.Segments) - 1
	s.enter(t.PaneID, t.Segments[t.current].End())
	s.finish(t)
	return true
}

// CancelAll lands every live transition on its destination. The unload flush
// and the level swaps use it: the window is about to stop drawing, and what
// the user asked for must have happened before it does.
func (s *Set) CancelAll() {
	for _, t := range s.List() {
		s.Cancel(t.PaneID)
	}
}

// Drop forgets paneID's transition WITHOUT landing it. Only for a pane that is
// going away: a landing installs a place on a pane and re-engages its content,
// and a closed pane has neither. Every other clearing is a Cancel.
func (s *Set) Drop(paneID string) { s.remove(paneID) }

// finish retires t and runs its landing. The retire comes first, so the
// landing sees a pane that is no longer animating — a framing write from
// inside a landing is the user's real destination, not scratch — and so a
// landing that starts the next transition cannot recurse into this one.
func (s *Set) finish(t *Transition) {
	s.remove(t.PaneID)
	s.land(t)
}

func (s *Set) remove(paneID string) {
	for i, t := range s.live {
		if t.PaneID == paneID {
			s.live = append(s.live[:i], s.live[i+1:]...)
			return
		}
	}
}
