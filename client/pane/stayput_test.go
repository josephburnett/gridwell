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

// A split by any side keeps the source's grid where it was; the new pane is
// the copy it was, centred in its own rect; and neither writes framing.
func TestReframeAfterASplitLeavesTheSourcePut(t *testing.T) {
	for _, side := range []Side{SideLeft, SideRight, SideTop, SideBottom} {
		tr := NewTree()
		p := tr.FocusedPane()
		p.SetView(2, 3, 0.75)
		bx, by := screenOf(t, tr, p, stayRoot, 4, 5)
		var np *Pane
		pl := tr.Plant(stayRoot)
		np, _ = tr.SplitOnSideAt(side, 0.25)
		tr.Reframe(pl, stayRoot)
		ax, ay := screenOf(t, tr, p, stayRoot, 4, 5)
		if !onScreen(ax, bx) || !onScreen(ay, by) {
			t.Fatalf("side %v: the source slid from (%v,%v) to (%v,%v)", side, bx, by, ax, ay)
		}
		if f, _ := np.Live(); f.Cx() != 2 || f.Cy() != 3 || f.Zoom() != 0.75 {
			t.Fatalf("side %v: the new pane shows %v, want the source as it was", side, f)
		}
		if p.View != p.settled {
			t.Fatalf("side %v: the moved centre is not settled, so it would write framing", side)
		}
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
