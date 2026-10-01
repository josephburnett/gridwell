// Package transition owns the viewport animation a pane runs through a
// doorway. A transition belongs to a pane, so two may animate at once, and one
// that is displaced or cleared lands rather than being dropped, which would
// strand the pane on a scratch viewport.
package transition

import "github.com/josephburnett/gridwell/client/pane"

// Segment's Place is installed when the segment begins, and the points where
// the path changes are segment boundaries, so the viewport the animation
// writes is always scratch and the frame the pane will ascend onto keeps the
// viewport it was left at.
type Segment struct {
	Place                    *pane.Stack
	FromCx, FromCy, FromZoom float64
	ToCx, ToCy, ToZoom       float64
	DurationMs               float64
}

// End is how a transition jumps to where it was going.
func (s Segment) End() Segment {
	s.FromCx, s.FromCy, s.FromZoom = s.ToCx, s.ToCy, s.ToZoom
	return s
}

// Transition's OnComplete runs once the last segment lands. A content descent
// pushes its frame there so the tile's controls do not appear mid-animation,
// which is why a lost landing loses the descent. TraceTileID is ascents only.
type Transition struct {
	PaneID      string
	Segments    []Segment
	OnComplete  func()
	TraceTileID string

	current int
	startMs float64
}

func (t *Transition) Segment() Segment { return t.Segments[t.current] }

func (t *Transition) StartMs() float64 { return t.startMs }

// Set is every live transition, at most one per pane. enter and land are the
// shim's; the sequencing lives here so no caller performs half of one.
type Set struct {
	enter func(paneID string, seg Segment)
	land  func(t *Transition)
	live  []*Transition
}

func New(enter func(paneID string, seg Segment), land func(t *Transition)) *Set {
	return &Set{enter: enter, land: land}
}

func (s *Set) Get(paneID string) *Transition {
	for _, t := range s.live {
		if t.PaneID == paneID {
			return t
		}
	}
	return nil
}

// Active is asked by every guard that must not act on a scratch viewport,
// above all the framing writeback.
func (s *Set) Active(paneID string) bool { return s.Get(paneID) != nil }

// Any is the gesture gate, whole-window so a tree-and-viewport change is
// atomic across the zoom.
func (s *Set) Any() bool { return len(s.live) > 0 }

// List is safe to iterate while landing them.
func (s *Set) List() []*Transition {
	out := make([]*Transition, len(s.live))
	copy(out, s.live)
	return out
}

// Start cancels whatever the pane was animating onto its destination. A caller
// that computes segments from the pane's place must cancel before reading it;
// this is the backstop.
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

// Advance ends the current segment, so either the next begins or the
// transition lands, and reports whether the pane is still animating.
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

// Cancel installs the final segment's place and end viewport and runs the
// landing: a cancelled descent is a completed one.
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

// CancelAll is for the unload flush and the level swaps: the window is about
// to stop drawing and what the user asked for must have happened first.
func (s *Set) CancelAll() {
	for _, t := range s.List() {
		s.Cancel(t.PaneID)
	}
}

// Drop is only for a pane that is going away, a landing needing a pane to
// install a place on. Every other clearing is a Cancel.
func (s *Set) Drop(paneID string) { s.remove(paneID) }

// finish retires t before running its landing: a framing write from inside a
// landing is the user's real destination, and a landing that starts the next
// must not recurse into this one.
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
