package rpc

import (
	"fmt"
	"math"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Framing is how a grid looked when it was last left through a doorway: a
// float center in the grid's own coordinates plus a pane-size-independent
// zoom, so a window resize never moves a saved view. NewFraming is its one
// constructor, so a held Framing is a finite center at a finite zoom above
// zero, and "never visited" is not a Framing but a View without one.
type Framing struct{ cx, cy, zoom float64 }

// NewFraming refuses a center that is not a point or a zoom that is not a
// size, with the reason.
func NewFraming(cx, cy, zoom float64) (Framing, error) {
	if !Finite(cx) || !Finite(cy) {
		return Framing{}, fmt.Errorf("framing center (%v, %v) is not a point on the grid", cx, cy)
	}
	if !Finite(zoom) || zoom <= 0 {
		return Framing{}, fmt.Errorf("framing zoom %v is not a size: it must be a finite number above zero", zoom)
	}
	return Framing{cx, cy, zoom}, nil
}

// FramingOf is the wire decode of the one framing write.
func FramingOf(req *pb.SetFramingRequest) (Framing, error) {
	return NewFraming(req.GetCx(), req.GetCy(), req.GetZoom())
}

// Finite is neither NaN nor infinite.
func Finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func (f Framing) Cx() float64   { return f.cx }
func (f Framing) Cy() float64   { return f.cy }
func (f Framing) Zoom() float64 { return f.zoom }

// framingEpsilon is how close two framings count as the same picture.
const framingEpsilon = 0.001

// SameAs is the same-framing test, within framingEpsilon. Every persister
// consults it, so a settle tick never churns the store.
func (f Framing) SameAs(g Framing) bool {
	return math.Abs(f.cx-g.cx) < framingEpsilon &&
		math.Abs(f.cy-g.cy) < framingEpsilon &&
		math.Abs(f.zoom-g.zoom) < framingEpsilon
}

// View is a doorway's saved framing, or none: the grid was never visited
// through it. The zero View is none.
type View struct {
	f  Framing
	ok bool
}

// Saved is the View holding f.
func Saved(f Framing) View { return View{f, true} }

// ViewOf reads a framing carried as three numbers on a row or an event. A
// doorway never visited carries none, which proto3 sends as absent zeros and
// storage as NULL; whatever NewFraming refuses reads as none too.
func ViewOf(cx, cy, zoom float64) View {
	f, err := NewFraming(cx, cy, zoom)
	if err != nil {
		return View{}
	}
	return Saved(f)
}

// Framing is the saved framing, false when never visited.
func (v View) Framing() (Framing, bool) { return v.f, v.ok }

// Wire is the three numbers a row or an event carries: zeros, the absent
// fields, for none.
func (v View) Wire() (cx, cy, zoom float64) {
	if !v.ok {
		return 0, 0, 0
	}
	return v.f.cx, v.f.cy, v.f.zoom
}

// SameAs holds for two Views that are both none, or both the same picture.
func (v View) SameAs(w View) bool {
	if v.ok != w.ok {
		return false
	}
	return !v.ok || v.f.SameAs(w.f)
}

// Reframe writes a root grid's framing onto every doorway rooted at gridID, a
// row's own and a declared entry's alike, and reports whether any value moved
// by SameAs.
func Reframe(gridID string, f Framing, plugins []*pb.PluginInfo) bool {
	if gridID == "" {
		return false
	}
	changed := false
	for _, pl := range plugins {
		if pl.RootGridId == gridID && !Saved(f).SameAs(ViewOf(pl.RootViewCx, pl.RootViewCy, pl.RootViewZoom)) {
			pl.RootViewCx, pl.RootViewCy, pl.RootViewZoom = f.cx, f.cy, f.zoom
			changed = true
		}
		for _, e := range pl.MenuEntries {
			if e.GridId == gridID && !Saved(f).SameAs(ViewOf(e.ViewCx, e.ViewCy, e.ViewZoom)) {
				e.ViewCx, e.ViewCy, e.ViewZoom = f.cx, f.cy, f.zoom
				changed = true
			}
		}
	}
	return changed
}
