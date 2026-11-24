package pane

import (
	"math"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

var stayRoot = Rect{X: 0, Y: 0, W: 1200, H: 800}

// screenOf is where p shows world point (wx, wy), through the projection the
// renderer and every hit test read (Pane.Screen).
func screenOf(t *testing.T, tr *Tree, p *Pane, root Rect, wx, wy float64) (float64, float64) {
	t.Helper()
	ps, ok := p.Screen(Layout(tr, root)[p.ID])
	if !ok {
		t.Fatalf("pane %s shows nothing", p.ID)
	}
	return ps.CellToScreen(wx, wy)
}

func onScreen(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestStayPutKeepsEveryWorldPointOnScreen(t *testing.T) {
	v, _ := rpc.NewFraming(3.5, -2, 0.5)
	from := Rect{X: 0, Y: 0, W: 1000, H: 600}
	to := Rect{X: 250, Y: 0, W: 750, H: 300}
	got, ok := StayPut(v, from, to)
	if !ok {
		t.Fatal("refused a view")
	}
	if got.Zoom() != v.Zoom() {
		t.Fatalf("zoom %v, want %v untouched", got.Zoom(), v.Zoom())
	}
	cell := CellPx * v.Zoom()
	for _, w := range [][2]float64{{0, 0}, {3.5, -2}, {10, 7}} {
		bx := from.X + from.W/2 + (w[0]-v.Cx())*cell
		by := from.Y + from.H/2 + (w[1]-v.Cy())*cell
		ax := to.X + to.W/2 + (w[0]-got.Cx())*cell
		ay := to.Y + to.H/2 + (w[1]-got.Cy())*cell
		if !onScreen(ax, bx) || !onScreen(ay, by) {
			t.Fatalf("world %v moved from (%v,%v) to (%v,%v)", w, bx, by, ax, ay)
		}
	}
	odd := Rect{X: 575.0000000000001, Y: 0.1, W: 703.9999999999999, H: 991}
	same, _ := StayPut(v, odd, odd)
	if same != v {
		t.Fatalf("an unmoved rect changed the view to %v", same)
	}
}

var worldPoints = [][2]float64{{0, 0}, {2, 3}, {4, 5}, {-7.5, 11}, {20, -9}}

// screens is where each pane shows each world point, keyed by pane id.
func screens(t *testing.T, tr *Tree, root Rect, ps ...*Pane) map[string][][2]float64 {
	t.Helper()
	out := map[string][][2]float64{}
	for _, p := range ps {
		for _, w := range worldPoints {
			x, y := screenOf(t, tr, p, root, w[0], w[1])
			out[p.ID] = append(out[p.ID], [2]float64{x, y})
		}
	}
	return out
}

// An edge split slices the pane (Joe, 2026-11-24): by any side, from a pane
// at the window's origin or not, and down to a new pane under a cell wide,
// the source and the new pane both show every world point where the source
// showed it, at the zoom it had, and neither writes framing.
func TestReframeAfterASplitSlicesThePane(t *testing.T) {
	for _, nested := range []bool{false, true} {
		for _, side := range []Side{SideLeft, SideRight, SideTop, SideBottom} {
			for _, ratio := range []float64{0.25, 0.6, 0.01} {
				tr := NewTree()
				p := tr.FocusedPane()
				p.SetView(2, 3, 0.75)
				if nested {
					// The source sits right of and below another pane.
					_, _ = tr.SplitOnSideAt(SideLeft, 0.3)
					_ = tr.SetFocus(p.ID)
					_, _ = tr.SplitOnSideAt(SideTop, 0.4)
					_ = tr.SetFocus(p.ID)
					p.SetView(-1.5, 6, 1.25)
				}
				before := screens(t, tr, stayRoot, p)[p.ID]
				f0, _ := p.Live()
				zoom := f0.Zoom()
				pl := tr.Plant(stayRoot)
				np, err := tr.SplitOnSideAt(side, ratio)
				if err != nil {
					t.Fatal(err)
				}
				tr.Reframe(pl, stayRoot)
				after := screens(t, tr, stayRoot, p, np)
				for _, q := range []*Pane{p, np} {
					for k, w := range worldPoints {
						if !onScreen(after[q.ID][k][0], before[k][0]) || !onScreen(after[q.ID][k][1], before[k][1]) {
							t.Fatalf("nested %v side %v ratio %v pane %s: world %v moved from %v to %v",
								nested, side, ratio, q.ID, w, before[k], after[q.ID][k])
						}
					}
					if f, _ := q.Live(); f.Zoom() != zoom {
						t.Fatalf("side %v pane %s: zoom %v, want %v", side, q.ID, f.Zoom(), zoom)
					}
					if q.View != q.settled {
						t.Fatalf("side %v pane %s: the moved centre is not settled, so it would write framing", side, q.ID)
					}
				}
			}
		}
	}
}

// Every lower grid frame of the new pane moves as the source's do, so an
// ascent out of either lands on the same world.
func TestReframeSlicesTheNewPanesLowerFrames(t *testing.T) {
	tr := NewTree()
	p := tr.FocusedPane()
	p.SetView(1, 1, 1)
	p.Push(Frame{Door: "7"})
	p.SetView(5, 5, 2)
	pl := tr.Plant(stayRoot)
	np, _ := tr.SplitOnSideAt(SideRight, 0.5)
	tr.Reframe(pl, stayRoot)
	ps, ns := p.Frames(), np.Frames()
	for i := range ps {
		pf, _ := ps[i].Live()
		nf, _ := ns[i].Live()
		// The two halves of a 1200-wide pane: the centres differ by 600px.
		if !onScreen(nf.Cx()-pf.Cx(), 600/(CellPx*pf.Zoom())) || nf.Cy() != pf.Cy() || nf.Zoom() != pf.Zoom() {
			t.Fatalf("frame %d: new pane %v, source %v", i, nf, pf)
		}
		if ns[i].View != ns[i].settled {
			t.Fatalf("frame %d of the new pane is no longer settled", i)
		}
	}
}

// A split from a zoomed pane unzooms the tree: the panes the zoom hid were
// not edited and keep their views.
func TestReframeLeavesAPaneTheZoomHid(t *testing.T) {
	tr := NewTree()
	p := tr.FocusedPane()
	p.SetView(1, 1, 1)
	q, _ := tr.SplitOnSideAt(SideRight, 0.5)
	q.SetView(9, 9, 2)
	_ = tr.SetFocus(p.ID)
	tr.Zoomed = p.ID
	qv := q.View
	pl := tr.Plant(stayRoot)
	_, _ = tr.SplitOnSideAt(SideBottom, 0.5)
	tr.Reframe(pl, stayRoot)
	if q.View != qv {
		t.Fatalf("the hidden pane's view moved from %v to %v", qv, q.View)
	}
}

// A split of a pane descended into a content tile showed the content, not a
// grid: the new pane keeps its copied views, so its ascent lands as any does.
func TestReframeLeavesANewContentPaneCopied(t *testing.T) {
	tr := NewTree()
	p := tr.FocusedPane()
	p.SetView(1, 1, 1)
	p.Push(ContentFrame("8", Footprint{X: 1, Y: 1, W: 2, H: 2}, 3, "", 0, 0))
	pl := tr.Plant(stayRoot)
	np, _ := tr.SplitOnSideAt(SideRight, 0.5)
	tr.Reframe(pl, stayRoot)
	if f, _ := np.Frames()[0].Live(); f.Cx() != 1 || f.Cy() != 1 || f.Zoom() != 1 {
		t.Fatalf("the new content pane's grid frame moved to %v", f)
	}
	if f, _ := p.Frames()[0].Live(); f.Cx() == 1 {
		t.Fatalf("the source's grid frame did not move: %v", f)
	}
}

// Every grid frame of the stack moves, so an ascent lands consistently; a
// content frame's view is its footprint's and stays.
func TestReframeMovesEveryGridFrameAndNoContentFrame(t *testing.T) {
	tr := NewTree()
	p := tr.FocusedPane()
	p.SetView(1, 1, 1)
	p.Push(Frame{Door: "7"})
	p.SetView(5, 5, 2)
	p.Push(ContentFrame("8", Footprint{X: 1, Y: 1, W: 2, H: 2}, 3, "", 0, 0))
	content := p.View
	pl := tr.Plant(stayRoot)
	_, _ = tr.SplitOnSideAt(SideRight, 0.5)
	tr.Reframe(pl, stayRoot)
	if p.View != content {
		t.Fatalf("the content frame moved to %v", p.View)
	}
	fs := p.Frames()
	dx := -stayRoot.W / 4
	for i, want := range []struct{ cx, zoom float64 }{{1, 1}, {5, 2}} {
		f, _ := fs[i].Live()
		if !onScreen(f.Cx(), want.cx+dx/(CellPx*want.zoom)) || f.Cy() != want.cx {
			t.Fatalf("frame %d at %v, want cx %v", i, f, want.cx+dx/(CellPx*want.zoom))
		}
		if fs[i].View != fs[i].settled {
			t.Fatalf("frame %d is no longer settled", i)
		}
	}
}

// A drag measures from its press, so a cancel that restores the ratios puts
// every view back byte for byte; a wheel mid-drag is kept, not undone.
func TestReframeFromThePressIsExactAndKeepsAMidDragWheel(t *testing.T) {
	tr := NewTree()
	p := tr.FocusedPane()
	p.SetView(2.3, 1.7, 0.6)
	q, _ := tr.SplitOnSideAt(SideRight, 0.5)
	q.SetView(-4.1, 9.9, 1.3)
	press := RatiosOf(tr.Root)
	pv, qv := p.View, q.View
	pl := tr.Plant(stayRoot)
	split := tr.Root.Split
	for _, x := range []float64{550, 431.7, 389.25, 612.3} {
		ResizeThrough(tr.Root, stayRoot, split, x, MinPanePx)
		tr.Reframe(pl, stayRoot)
	}
	press.Restore()
	tr.Reframe(pl, stayRoot)
	if p.View != pv || q.View != qv {
		t.Fatalf("a cancelled drag left %v %v, want %v %v", p.View, q.View, pv, qv)
	}

	ResizeThrough(tr.Root, stayRoot, split, 500, MinPanePx)
	tr.Reframe(pl, stayRoot)
	f, _ := q.Live()
	q.Steer(f.Cx()+1, f.Cy(), 2)
	bx, by := screenOf(t, tr, q, stayRoot, 0, 0)
	ResizeThrough(tr.Root, stayRoot, split, 450, MinPanePx)
	tr.Reframe(pl, stayRoot)
	if g, _ := q.Live(); g.Zoom() != 2 {
		t.Fatalf("the wheel's zoom was undone: %v", g)
	}
	if ax, ay := screenOf(t, tr, q, stayRoot, 0, 0); !onScreen(ax, bx) || !onScreen(ay, by) {
		t.Fatalf("after the wheel the grid slid from (%v,%v) to (%v,%v)", bx, by, ax, ay)
	}
}

// The cause is the edit: a window resize lays the tree out at another root
// and moves no view, so each centre stays in its pane's middle.
func TestWindowResizeKeepsTheCentre(t *testing.T) {
	tr := NewTree()
	p := tr.FocusedPane()
	p.SetView(2, 3, 1)
	smaller := Rect{W: 900, H: 500}
	x, y := screenOf(t, tr, p, smaller, 2, 3)
	if f, _ := p.Live(); f.Cx() != 2 || f.Cy() != 3 || x != 450 || y != 250 {
		t.Fatalf("a resize shows centre %v at (%v,%v), want (2,3) in the middle", f, x, y)
	}
}
