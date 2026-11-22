package pane

import "github.com/josephburnett/gridwell/api/rpc"

// A pane boundary the user drags, an edge split's or a divider's, covers or
// uncovers the grids a pane shows and never slides them (Joe, 2026-11-22).
// Only those edits plant and reframe: a window resize changes the root rect
// alone and the split below places its own boundary, so each centre stays in
// its pane's middle.

// StayPut is v at to keeping every world point where v showed it at from:
// the centre moves to the point now under to's middle, the zoom stays.
func StayPut(v rpc.Framing, from, to Rect) (rpc.Framing, bool) {
	cell := CellPx * v.Zoom()
	// Each middle whole first, so a rect back where it was moves by exactly 0.
	dx := (to.X + to.W/2) - (from.X + from.W/2)
	dy := (to.Y + to.H/2) - (from.Y + from.H/2)
	f, err := rpc.NewFraming(v.Cx()+dx/cell, v.Cy()+dy/cell, v.Zoom())
	return f, err == nil
}

// Planted is where each laid-out pane's frames stood when a boundary edit
// began. Reframe measures from it rather than from its own last step, so an
// edit undone, a cancelled drag's, puts every view back exactly.
type Planted map[string]*plantedPane

type plantedPane struct {
	last   Rect
	frames []planted
}

// planted is one frame's view and settled view as shown at at, and set, the
// view Reframe last gave it.
type planted struct {
	at                 Rect
	view, settled, set rpc.View
}

func (e planted) moved(v rpc.View, to Rect) rpc.View {
	f, ok := v.Framing()
	if !ok {
		return v
	}
	if n, ok := StayPut(f, e.at, to); ok {
		return rpc.Saved(n)
	}
	return v
}

// Plant records every laid-out pane's frames at root.
func (t *Tree) Plant(root Rect) Planted {
	rects := Layout(t, root)
	out := Planted{}
	t.Walk(func(p *Pane) {
		r, ok := rects[p.ID]
		if !ok {
			return
		}
		pp := &plantedPane{last: r}
		for _, f := range p.framePtrs() {
			pp.frames = append(pp.frames, planted{at: r, view: f.View, settled: f.settled, set: f.View})
		}
		out[p.ID] = pp
	})
	return out
}

// Reframe gives every grid frame of each planted pane still laid out its
// StayPut view at the pane's rect now, and its settled view with it, so a
// view that had settled writes no framing (Frame.FramingWrite). A pane the
// edit made, a split's new one, was not planted and keeps the view it was
// copied with. A view something else moved since, a wheel mid-drag, is
// planted afresh where it is.
func (t *Tree) Reframe(pl Planted, root Rect) {
	rects := Layout(t, root)
	t.Walk(func(p *Pane) {
		pp, planted := pl[p.ID]
		r, laid := rects[p.ID]
		fs := p.framePtrs()
		if !planted || !laid || len(pp.frames) != len(fs) {
			return
		}
		for i, f := range fs {
			e := &pp.frames[i]
			if f.Content || f.ViewPending {
				continue
			}
			if f.View != e.set {
				e.at, e.view, e.settled = pp.last, f.View, f.settled
			}
			f.View, f.settled = e.moved(e.view, r), e.moved(e.settled, r)
			e.set = f.View
		}
		pp.last = r
	})
}
