package wsbar

import "testing"

func TestHeightAlwaysReserved(t *testing.T) {
	if Height() != RowH {
		t.Fatal("the bar is permanent chrome (issue #212): one band, always")
	}
}

// Render and input read the same rects, so the crumb drawn at a point is
// the crumb a click there resolves to.
func TestLayoutAndHitTestAgree(t *testing.T) {
	segs := Layout(3, 4, 900)
	if len(segs) != 7 {
		t.Fatalf("segments = %d, want 7", len(segs))
	}
	for _, s := range segs {
		got, ok := At(segs, s.X+s.W/2)
		if !ok || got.Kind != s.Kind || got.Index != s.Index {
			t.Errorf("center of segment %+v hit-tests as %+v (ok=%v)", s, got, ok)
		}
	}
	last := segs[len(segs)-1]
	if _, ok := At(segs, last.X+last.W+50); ok {
		t.Error("beyond the last crumb must hit nothing")
	}
	if _, ok := At(nil, 10); ok {
		t.Error("empty bar must hit nothing")
	}
}

func TestLayoutOrdersWorkspaceThenChain(t *testing.T) {
	segs := Layout(2, 3, 1000)
	wantKinds := []Kind{KindWorkspace, KindWorkspace, KindChain, KindChain, KindChain}
	wantIndex := []int{1, 2, 0, 1, 2}
	for i, s := range segs {
		if s.Kind != wantKinds[i] || s.Index != wantIndex[i] {
			t.Errorf("segment %d = %+v, want kind %v index %d", i, s, wantKinds[i], wantIndex[i])
		}
		if i > 0 && s.X != segs[i-1].X+segs[i-1].W {
			t.Errorf("segment %d does not abut its neighbor", i)
		}
	}
}

func TestWorkspaceCrumbWidthCapped(t *testing.T) {
	segs := Layout(1, 0, 2000)
	if segs[0].W != maxCrumbW {
		t.Fatalf("single workspace crumb w = %v, want capped %v", segs[0].W, maxCrumbW)
	}
	segs = Layout(10, 0, 1000+SlotW)
	if segs[0].W != 100 {
		t.Fatalf("deep nesting divides evenly over the non-slot width: w = %v, want 100", segs[0].W)
	}
}

func TestChainSquaresAndTheNamedCurrent(t *testing.T) {
	segs := Layout(0, 3, 1000)
	for _, s := range segs[:len(segs)-1] {
		if s.W != RowH {
			t.Fatalf("chain crumb w = %v, want square %v", s.W, RowH)
		}
	}
	// The last (current) crumb carries the name extension (issue #213).
	if last := segs[len(segs)-1]; last.W != RowH+NameW {
		t.Fatalf("current crumb w = %v, want %v", last.W, RowH+NameW)
	}
	// Workspace crumbs yield width to the chain + name before capping; the
	// right-end circle slot is reserved off the top (issue #214).
	segs = Layout(2, 2, 500)
	ws, _ := WorkspaceSegment(segs, 1)
	if ws.W != (500-SlotW-2*RowH-NameW)/2 {
		t.Fatalf("workspace crumb w = %v, want %v", ws.W, (500-SlotW-2*RowH-NameW)/2)
	}
}

// A too-narrow bar drops the name extension first, then shrinks the chain
// squares — never the workspace labels — and everything stays inside the
// width.
func TestOverflowShrinksChain(t *testing.T) {
	// Tight but square-fitting: the name extension gives way, squares hold.
	segs := Layout(0, 10, 10*RowH+SlotW+20)
	if last := segs[len(segs)-1]; last.W != RowH+20 {
		t.Fatalf("name extension must absorb the shortfall: last w = %v", last.W)
	}
	if segs[0].W != RowH {
		t.Fatalf("squares must hold while the name absorbs: w = %v", segs[0].W)
	}
	// Truly narrow: the squares shrink too, and nothing overflows.
	segs = Layout(0, 100, 320)
	last := segs[len(segs)-1]
	if last.X+last.W > 320.0001 {
		t.Fatalf("chain overflows the bar: ends at %v", last.X+last.W)
	}
	if segs[0].W >= RowH {
		t.Fatalf("squares did not shrink: w = %v", segs[0].W)
	}
}

func TestWorkspaceSegmentLookup(t *testing.T) {
	segs := Layout(3, 2, 900)
	s, ok := WorkspaceSegment(segs, 2)
	if !ok || s.Index != 2 || s.Kind != KindWorkspace {
		t.Fatalf("WorkspaceSegment(2) = %+v ok=%v", s, ok)
	}
	if _, ok := WorkspaceSegment(segs, 4); ok {
		t.Fatal("missing level must not resolve")
	}
}
