package wsbar

import "testing"

// Render and input read the same rects, so the crumb drawn at a point is
// the crumb a click there resolves to.
func TestLayoutAndHitTestAgree(t *testing.T) {
	segs := Layout(3, 4, 900)
	if len(segs) != 7 {
		t.Fatalf("segments = %d, want 7 (no anchor when workspace crumbs front the chain)", len(segs))
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
	if segs[0].Kind != KindWorkspace {
		t.Fatalf("no anchor block inside a workspace: %+v", segs[0])
	}
}

func TestChainSquares(t *testing.T) {
	segs := Layout(0, 3, 1000)
	// Outside a workspace the chain starts at the band's left edge — the
	// anchor block is gone (owner reversal 2026-07-31, issue #223).
	if segs[0].Kind != KindChain || segs[0].X != 0 {
		t.Fatalf("segment 0 = %+v, want a chain crumb at x=0", segs[0])
	}
	for _, s := range segs {
		if s.W != RowH {
			t.Fatalf("chain crumb w = %v, want square %v", s.W, RowH)
		}
		if s.Kind != KindChain {
			t.Fatalf("segment = %+v, want a chain crumb", s)
		}
	}
	// Workspace crumbs cap at the (deliberately narrow, issue #220)
	// maxCrumbW even when there is room.
	segs = Layout(2, 2, 500)
	ws, _ := WorkspaceSegment(segs, 1)
	if ws.W != maxCrumbW {
		t.Fatalf("workspace crumb w = %v, want the %v cap", ws.W, maxCrumbW)
	}
}

// A too-narrow bar shrinks the chain squares — never the workspace labels —
// and everything stays inside the width.
func TestOverflowShrinksChain(t *testing.T) {
	segs := Layout(0, 100, 320)
	last := segs[len(segs)-1]
	_ = last
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

// The title centers in the free space BETWEEN the crumbs' end and the slot
// (issue #230) — not in the whole band, where growing crumbs crowd it
// one-sidedly and it never recenters.
func TestTitleSpanCentersInFreeSpace(t *testing.T) {
	width, textW := 1000.0, 100.0
	crumbsEnd := 400.0
	x, w, ok := TitleSpan(crumbsEnd, width, textW)
	if !ok {
		t.Fatal("plenty of room, want ok")
	}
	if w != textW {
		t.Fatalf("w = %v, want the full text width %v", w, textW)
	}
	left := crumbsEnd + titlePad
	right := width - SlotW - titlePad
	if got, want := x+w/2, (left+right)/2; got != want {
		t.Fatalf("title center = %v, want the free-space center %v (not the band center %v)",
			got, want, width/2)
	}
	// No crumbs: the free space starts at the band's left edge.
	x, w, ok = TitleSpan(0, width, textW)
	if !ok || x+w/2 != (titlePad+right)/2 {
		t.Fatalf("no crumbs: center = %v ok=%v, want %v", x+w/2, ok, (titlePad+right)/2)
	}
}

func TestTitleSpanClampsAndGivesUp(t *testing.T) {
	// A title wider than the free space is clamped to it.
	x, w, ok := TitleSpan(400, 1000, 5000)
	if !ok {
		t.Fatal("want ok")
	}
	left, right := 400+titlePad, 1000-SlotW-titlePad
	if x != left || w != right-left {
		t.Fatalf("clamp: x=%v w=%v, want x=%v w=%v", x, w, left, right-left)
	}
	// Crumbs leaving almost nothing: no title at all.
	if _, _, ok := TitleSpan(950, 1000, 100); ok {
		t.Fatal("sliver of space must not show a title")
	}
}
