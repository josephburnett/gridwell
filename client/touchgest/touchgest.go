// Package touchgest classifies raw touch input into the canvas's existing
// mouse-gesture vocabulary. The wasm client is deliberately mouse-only: every
// interaction is a left/right/middle button press, drag, or wheel event
// (client/wasm/input.go). Rather than teach the gesture engine about touch,
// this machine translates touch streams into that vocabulary and the engine
// stays untouched:
//
//	tap                → left click        (focus / descend / select)
//	drag past slop     → left drag         (pan / move tile / palette drag)
//	long-press (hold)  → right button      (ascend circle / clone / resize /
//	                                        split / swap — the whole right-drag
//	                                        vocabulary, via the press-then-drag)
//	two-finger tap     → middle click      (ascend)
//	two-finger pinch   → wheel at midpoint (zoom; spread = wheel-up = zoom in)
//	two-finger scroll  → wheel at midpoint (doc scroll, matching a trackpad)
//
// The package is js-free and pure: the wasm shell (touch.go) feeds events
// with timestamps and dispatches the returned Actions as synthetic
// MouseEvent/WheelEvent objects; every decision, threshold, and state
// transition lives here under table tests.
package touchgest

import "math"

// Point is a touch position in the canvas's CSS-pixel coordinates.
type Point struct{ X, Y float64 }

// Kind is the type of synthetic input to dispatch.
type Kind int

const (
	MouseDown Kind = iota
	MouseMove
	MouseUp
	Wheel
)

// Action is one synthetic input event for the shell to dispatch at the
// canvas. Button follows MouseEvent.button (0 left, 1 middle, 2 right);
// DeltaY is set only for Wheel.
type Action struct {
	Kind   Kind
	Pos    Point
	Button int
	DeltaY float64
}

// Tunables. Times are milliseconds (event.timeStamp domain), distances CSS px.
const (
	// HoldMs: a press held this long within SlopPx becomes the right button.
	// Long enough that deliberate taps and drag starts never trip it; short
	// enough that "hold to get the pane vocabulary" feels immediate.
	HoldMs = 400.0
	// SlopPx is finger jitter allowance: movement beyond it before HoldMs
	// classifies the press as a left drag (and within a two-finger press,
	// distinguishes a two-finger tap from a pinch/scroll).
	SlopPx = 8.0
	// TwoTapMs: both fingers down and up within this (and within slop)
	// is a two-finger tap = middle click.
	TwoTapMs = 250.0
	// lockPx is the accumulated dominant-axis travel that locks a two-finger
	// gesture as pinch (distance change) or scroll (parallel travel). Locked
	// once so a wandering pinch never flips into a scroll mid-gesture.
	lockPx = 12.0
	// pinchGain / scrollGain convert finger px into wheel deltaY units.
	// WheelZoom clamps its step per event, so per-move deltas just need to
	// land in the same order of magnitude as a physical wheel notch.
	pinchGain  = 1.5
	scrollGain = 1.0
)

type state int

const (
	idle     state = iota
	pending1       // one finger down, unclassified
	dragLeft
	dragRight
	twoDown   // two fingers down, unclassified (tap / pinch / scroll)
	twoLift   // one finger of an unclassified pair lifted — tap still possible
	twoPinch  // locked: distance change → wheel zoom
	twoScroll // locked: parallel travel → wheel scroll
	dead      // gesture over or abandoned; swallow until all fingers lift
)

// Machine converts a stream of touch events into Actions. Not safe for
// concurrent use — the wasm client is single-threaded, like every other
// client-side store.
type Machine struct {
	st     state
	origin Point   // pending1: press point; drags: anchored press point
	last   Point   // most recent single-finger position
	t0     float64 // time the current classification window opened

	// two-finger tracking
	mid     Point   // current midpoint
	dist    float64 // current inter-finger distance
	twoT0   float64
	accDist float64 // accumulated |distance change| (pinch evidence)
	accTrav float64 // accumulated |midpoint travel| (scroll evidence)
}

func New() *Machine { return &Machine{} }

func dist(a, b Point) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

func midpoint(a, b Point) Point { return Point{X: (a.X + b.X) / 2, Y: (a.Y + b.Y) / 2} }

// Start is called on touchstart with the full current touch list.
func (m *Machine) Start(pts []Point, t float64) []Action {
	switch len(pts) {
	case 1:
		if m.st != idle {
			// A finger landed while we're mid/dead — treat as noise.
			return nil
		}
		m.st = pending1
		m.origin = pts[0]
		m.last = pts[0]
		m.t0 = t
		return nil
	case 2:
		switch m.st {
		case pending1, idle:
			// Second finger before classification, or both fingers at once
			// when idle, because the first landed on a DOM overlay that only
			// forwards multi-finger touches (the editing textarea). Either
			// way, a two-finger gesture.
			m.st = twoDown
			m.mid = midpoint(pts[0], pts[1])
			m.dist = dist(pts[0], pts[1])
			m.twoT0 = t
			m.accDist, m.accTrav = 0, 0
			return nil
		case dragLeft, dragRight:
			// A stray extra finger mid-drag: end the drag cleanly and wait
			// for a full lift rather than guess.
			as := []Action{{Kind: MouseUp, Pos: m.last, Button: m.dragButton()}}
			m.st = dead
			return as
		default:
			m.st = dead
			return nil
		}
	default:
		// 3+ fingers: not our vocabulary.
		if m.st == dragLeft || m.st == dragRight {
			as := []Action{{Kind: MouseUp, Pos: m.last, Button: m.dragButton()}}
			m.st = dead
			return as
		}
		m.st = dead
		return nil
	}
}

// Move is called on touchmove with the full current touch list.
func (m *Machine) Move(pts []Point, t float64) []Action {
	switch m.st {
	case pending1:
		if len(pts) != 1 {
			return nil
		}
		m.last = pts[0]
		if dist(m.origin, pts[0]) <= SlopPx {
			return nil
		}
		// Crossed slop before the hold: a left drag, pressed at the ORIGIN so
		// the gesture engine sees the same press point a mouse would.
		m.st = dragLeft
		return []Action{
			{Kind: MouseDown, Pos: m.origin, Button: 0},
			{Kind: MouseMove, Pos: pts[0], Button: 0},
		}
	case dragLeft, dragRight:
		if len(pts) < 1 {
			return nil
		}
		m.last = pts[0]
		return []Action{{Kind: MouseMove, Pos: pts[0], Button: m.dragButton()}}
	case twoDown, twoPinch, twoScroll:
		if len(pts) != 2 {
			return nil
		}
		newMid := midpoint(pts[0], pts[1])
		newDist := dist(pts[0], pts[1])
		dDist := newDist - m.dist
		dTrav := dist(newMid, m.mid)
		prevMidY := m.mid.Y
		m.mid, m.dist = newMid, newDist

		if m.st == twoDown {
			m.accDist += math.Abs(dDist)
			m.accTrav += dTrav
			if m.accDist < lockPx && m.accTrav < lockPx {
				return nil
			}
			if m.accDist >= m.accTrav {
				m.st = twoPinch
			} else {
				m.st = twoScroll
			}
		}
		switch m.st {
		case twoPinch:
			if dDist == 0 {
				return nil
			}
			// Spread (dDist > 0) = zoom in = wheel-up = negative deltaY.
			return []Action{{Kind: Wheel, Pos: newMid, DeltaY: -dDist * pinchGain}}
		default: // twoScroll
			dy := prevMidY - newMid.Y
			if dy == 0 {
				return nil
			}
			// Fingers up = read below = wheel-down = positive deltaY.
			return []Action{{Kind: Wheel, Pos: newMid, DeltaY: dy * scrollGain}}
		}
	default:
		return nil
	}
}

// End is called on touchend/touchcancel with the REMAINING touch list.
func (m *Machine) End(remaining []Point, t float64) []Action {
	if len(remaining) > 0 {
		// Fingers still down: real hardware (and CDP injection) lifts one
		// finger per event, so this is the NORMAL end of a two-finger
		// gesture, not an anomaly.
		switch m.st {
		case dragLeft, dragRight:
			as := []Action{{Kind: MouseUp, Pos: m.last, Button: m.dragButton()}}
			m.st = dead
			return as
		case twoDown:
			// First finger of an unclassified pair lifted: the tap window is
			// still open until the second finger lifts.
			m.st = twoLift
			return nil
		case idle:
			return nil
		default:
			m.st = dead
			return nil
		}
	}
	// All fingers lifted.
	st := m.st
	m.st = idle
	switch st {
	case pending1:
		// A quick tap: the full left click at the press point.
		return []Action{
			{Kind: MouseDown, Pos: m.origin, Button: 0},
			{Kind: MouseUp, Pos: m.origin, Button: 0},
		}
	case dragLeft, dragRight:
		btn := 0
		if st == dragRight {
			btn = 2
		}
		return []Action{{Kind: MouseUp, Pos: m.last, Button: btn}}
	case twoDown, twoLift:
		if t-m.twoT0 <= TwoTapMs && m.accDist < SlopPx && m.accTrav < SlopPx {
			// Two-finger tap: middle click = ascend.
			return []Action{
				{Kind: MouseDown, Pos: m.mid, Button: 1},
				{Kind: MouseUp, Pos: m.mid, Button: 1},
			}
		}
		return nil
	default:
		return nil
	}
}

// Timer is called when a long-press timer armed at a touchstart fires. The
// shell arms one blindly per press and never cancels; the machine ignores
// stale or irrelevant firings (wrong state, or a timer from an earlier press
// that hasn't been held for HoldMs of THIS press).
func (m *Machine) Timer(t float64) []Action {
	if m.st != pending1 || t-m.t0 < HoldMs {
		return nil
	}
	m.st = dragRight
	m.last = m.origin
	return []Action{{Kind: MouseDown, Pos: m.origin, Button: 2}}
}

func (m *Machine) dragButton() int {
	if m.st == dragRight {
		return 2
	}
	return 0
}
