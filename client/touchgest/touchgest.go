// Package touchgest classifies raw touch input into the canvas's existing
// mouse-gesture vocabulary, so the gesture engine needs no touch knowledge:
//
//	tap                → left click        (focus / descend / select)
//	drag past slop     → left drag         (pan / move tile / palette drag)
//	long-press (hold)  → right button      (the whole right-drag vocabulary:
//	                                        ascend circle, clone, resize,
//	                                        split, swap)
//	two-finger tap     → middle click      (ascend)
//	two-finger pinch   → wheel at midpoint (zoom; spread = wheel-up = zoom in)
//	two-finger scroll  → wheel at midpoint (doc scroll, matching a trackpad)
//
// It is js-free. client/wasm/touch.go feeds it timestamped events and
// dispatches the returned Actions as synthetic MouseEvent and WheelEvent.
package touchgest

import "math"

// Point is in the canvas's CSS-pixel coordinates.
type Point struct{ X, Y float64 }

type Kind int

const (
	MouseDown Kind = iota
	MouseMove
	MouseUp
	Wheel
)

// Action is one synthetic input event for the shell to dispatch. Button
// follows MouseEvent.button (0 left, 1 middle, 2 right); DeltaY is Wheel only.
type Action struct {
	Kind   Kind
	Pos    Point
	Button int
	DeltaY float64
}

// Tunables. Times are milliseconds (event.timeStamp domain), distances CSS px.
const (
	// HoldMs: a press held this long within SlopPx becomes the right button.
	HoldMs = 400.0
	// SlopPx is the jitter allowance before a press becomes a drag.
	SlopPx = 8.0
	// TwoTapMs bounds a two-finger tap, down to up.
	TwoTapMs = 250.0
	// lockPx is the dominant-axis travel that locks a two-finger gesture as
	// pinch or scroll. It locks once, so a wandering pinch never flips.
	lockPx = 12.0
	// pinchGain and scrollGain convert finger px into wheel deltaY; only the
	// order of magnitude matters, since zoomtrans.WheelZoom clamps its step.
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
	twoLift   // one finger of an unclassified pair lifted; a tap is still possible
	twoPinch  // locked: distance change → wheel zoom
	twoScroll // locked: parallel travel → wheel scroll
	dead      // gesture over or abandoned; swallow until all fingers lift
)

// Machine is not safe for concurrent use; the wasm client is single-threaded.
type Machine struct {
	st     state
	origin Point   // pending1: press point; drags: anchored press point
	last   Point   // most recent single-finger position
	t0     float64 // time the current classification window opened

	mid     Point   // current midpoint
	dist    float64 // current inter-finger distance
	twoT0   float64
	accDist float64 // accumulated |distance change| (pinch evidence)
	accTrav float64 // accumulated |midpoint travel| (scroll evidence)
}

func New() *Machine { return &Machine{} }

func dist(a, b Point) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

func midpoint(a, b Point) Point { return Point{X: (a.X + b.X) / 2, Y: (a.Y + b.Y) / 2} }

// Start takes the full current touch list.
func (m *Machine) Start(pts []Point, t float64) []Action {
	switch len(pts) {
	case 1:
		if m.st != idle {
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
			// Second finger before classification, or both at once when the
			// first landed on a DOM overlay that forwards only multi-finger
			// touches (the editing textarea).
			m.st = twoDown
			m.mid = midpoint(pts[0], pts[1])
			m.dist = dist(pts[0], pts[1])
			m.twoT0 = t
			m.accDist, m.accTrav = 0, 0
			return nil
		case dragLeft, dragRight:
			as := []Action{{Kind: MouseUp, Pos: m.last, Button: m.dragButton()}}
			m.st = dead
			return as
		default:
			m.st = dead
			return nil
		}
	default:
		if m.st == dragLeft || m.st == dragRight {
			as := []Action{{Kind: MouseUp, Pos: m.last, Button: m.dragButton()}}
			m.st = dead
			return as
		}
		m.st = dead
		return nil
	}
}

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
		// Pressed at the origin, as a mouse would be.
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

// End takes the remaining touch list, on touchend and touchcancel.
func (m *Machine) End(remaining []Point, t float64) []Action {
	if len(remaining) > 0 {
		// Hardware and CDP injection both lift one finger per event, so this
		// is the ordinary end of a two-finger gesture.
		switch m.st {
		case dragLeft, dragRight:
			as := []Action{{Kind: MouseUp, Pos: m.last, Button: m.dragButton()}}
			m.st = dead
			return as
		case twoDown:
			// The tap window stays open until the second finger lifts.
			m.st = twoLift
			return nil
		case idle:
			return nil
		default:
			m.st = dead
			return nil
		}
	}
	st := m.st
	m.st = idle
	switch st {
	case pending1:
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

// Timer takes a long-press timer firing. The shell never cancels one, so a
// stale firing is ignored.
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
