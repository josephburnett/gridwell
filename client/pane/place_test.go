package pane

import (
	"math"
	"reflect"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// A descent pushes and an ascent pops, and the viewport you left a level at is
// the frame you left: no second stack to keep in step.
func TestPushPopRestoresTheViewportYouLeft(t *testing.T) {
	s := NewStack("home/1")
	s.SetView(5, 6, 1.5)

	s.Push(viewed(Frame{Door: "7"}, 0, 0, 1))
	s.SetView(100, 200, 3)

	if s.Depth() != 2 || s.Anchor() != "home/1" || !reflect.DeepEqual(s.Path(), []string{"7"}) {
		t.Fatalf("after descent: depth=%d anchor=%q path=%v", s.Depth(), s.Anchor(), s.Path())
	}
	if !s.Pop() {
		t.Fatal("pop with a frame below returned false")
	}
	if !viewIs(s.View, 5, 6, 1.5) {
		t.Fatalf("ascent did not land on the frame we left: %+v", s.Frame)
	}
	if s.Pop() {
		t.Fatal("pop at the bottom returned true")
	}
}

// A frame opening a namespace level carries its grid id; an ordinary well
// frame derives one from the cache. Anchor and Path project that slice.
func TestAnchorAndPathAreProjections(t *testing.T) {
	s := NewStack("home/1")
	s.Push(Frame{Door: "4"})
	s.Push(Frame{Door: "9"})
	if s.Anchor() != "home/1" || !reflect.DeepEqual(s.Path(), []string{"4", "9"}) {
		t.Fatalf("in-namespace: anchor=%q path=%v", s.Anchor(), s.Path())
	}
	// Cross into another namespace: the link's target grid is authoritative,
	// and the path restarts inside it.
	s.Push(Frame{GridID: "k3x9m2q/1", Door: "lnk"})
	if s.Anchor() != "k3x9m2q/1" || len(s.Path()) != 0 {
		t.Fatalf("after portal: anchor=%q path=%v", s.Anchor(), s.Path())
	}
	s.Push(Frame{Door: "12"})
	if s.Anchor() != "k3x9m2q/1" || !reflect.DeepEqual(s.Path(), []string{"12"}) {
		t.Fatalf("inside portal: anchor=%q path=%v", s.Anchor(), s.Path())
	}
	// A content frame is a leaf OF that grid, not a grid of its own.
	s.Push(Frame{Door: "13", Content: true})
	if s.Anchor() != "k3x9m2q/1" || !reflect.DeepEqual(s.Path(), []string{"12"}) {
		t.Fatalf("in content: anchor=%q path=%v", s.Anchor(), s.Path())
	}
	if s.ContentID() != "13" || !s.Content {
		t.Fatalf("content id = %q", s.ContentID())
	}
	// Ascending out of the content leaves the grid place untouched.
	s.Pop()
	if s.Content || s.Anchor() != "k3x9m2q/1" || !reflect.DeepEqual(s.Path(), []string{"12"}) {
		t.Fatalf("after content ascent: %+v", s.Crumbs())
	}
}

// AnchorPathAt answers for any level, which is what a crumb's row lookup
// needs: the grid the crumb's tile lives in.
func TestAnchorPathAtEveryLevel(t *testing.T) {
	s := StackAt("home/1", []string{"4", "9"}, "13")
	cases := []struct {
		level  int
		anchor string
		path   []string
	}{
		{0, "home/1", nil},
		{1, "home/1", []string{"4"}},
		{2, "home/1", []string{"4", "9"}},
		{3, "home/1", []string{"4", "9"}},
	}
	for _, c := range cases {
		a, p := s.AnchorPathAt(c.level)
		if a != c.anchor || !reflect.DeepEqual(p, c.path) {
			t.Errorf("level %d = (%q,%v), want (%q,%v)", c.level, a, p, c.anchor, c.path)
		}
	}
	// Out of range clamps rather than panicking (a stale bar click).
	if a, _ := s.AnchorPathAt(99); a != "home/1" {
		t.Errorf("clamped level anchor = %q", a)
	}
	if a, p := s.AnchorPathAt(-1); a != "" || p != nil {
		t.Errorf("below the bottom = (%q,%v)", a, p)
	}
}

// StackAt is the one decoder both encodings use. Its frames carry no viewport,
// nothing encoding the ones a pane would ascend onto, so the ascent falls back
// to each grid's persisted framing.
func TestStackAtBuildsTheRestoredPlace(t *testing.T) {
	s := StackAt("home/1", []string{"4", "9"}, "13")
	if s.Depth() != 4 {
		t.Fatalf("depth = %d, want 4", s.Depth())
	}
	for i, f := range s.Frames() {
		if f.HasView() {
			t.Errorf("restored frame %d claims a viewport: %+v", i, f)
		}
	}
	if s.ContentID() != "13" {
		t.Fatalf("content = %q", s.ContentID())
	}
	// No content leaf, no content frame.
	if g := StackAt("home/1", []string{"4"}, ""); g.Depth() != 2 || g.Content {
		t.Fatalf("grid place = %+v", g.Crumbs())
	}
}

// A frame has a view only when one was set: a frame a URL restored has none,
// and no zoom, however computed, can stand in for one.
func TestHasViewMarksAnUnsavedFrame(t *testing.T) {
	if !viewed(Frame{}, 0, 0, 1).HasView() || (Frame{}).HasView() {
		t.Fatal("HasView must key off a view having been set")
	}
	var f Frame
	for _, z := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if f.SetView(1, 1, z) || f.HasView() {
			t.Errorf("SetView took zoom %v", z)
		}
	}
	if f.SetView(math.NaN(), 1, 1) || f.HasView() {
		t.Error("SetView took a NaN center")
	}
}

// viewed is f with its view set to a view a test knows is one.
func viewed(f Frame, cx, cy, zoom float64) Frame {
	if !f.SetView(cx, cy, zoom) {
		panic("not a view")
	}
	return f
}

// viewIs holds when v is exactly (cx, cy, zoom).
func viewIs(v rpc.View, cx, cy, zoom float64) bool { return v == rpc.ViewOf(cx, cy, zoom) }

// Reset makes the whole stack one frame, so a restore to a shallower place
// leaves no deeper frame behind to ascend into.
func TestResetClearsEveryFrame(t *testing.T) {
	s := StackAt("home/1", []string{"4", "9"}, "13")
	s.Reset(viewed(Frame{GridID: "other/1"}, 0, 0, 1))
	if s.Depth() != 1 || s.Anchor() != "other/1" || s.Content {
		t.Fatalf("after reset: %+v", s.Crumbs())
	}
}

// Frames/At/Clone are read-only views: mutating what they hand back must
// never reach the live stack.
func TestFramesAndCloneDoNotAlias(t *testing.T) {
	s := StackAt("home/1", []string{"4", "9"}, "")
	fr := s.Frames()
	if len(fr) != 3 {
		t.Fatalf("frames = %d", len(fr))
	}
	fr[0].GridID = "clobbered"
	if s.Anchor() != "home/1" {
		t.Fatal("Frames aliased the live stack")
	}
	c := s.Clone()
	c.Pop()
	if s.Depth() != 3 {
		t.Fatal("Clone aliased the live stack")
	}
}

func TestContentFrameCarriesTheTilesOwnViewport(t *testing.T) {
	f := ContentFrame("u1/9", Footprint{X: 3, Y: 4, W: 2, H: 4}, 2.5, "rendered", 7, 11)
	if f.Door != "u1/9" || !f.Content {
		t.Fatalf("not a content frame: %+v", f)
	}
	if !f.HasView() || !viewIs(f.View, 4, 6, 2.5) {
		t.Fatalf("not centred on the footprint at its zoom: %+v", f)
	}
	if f.TextMode != "rendered" || f.TextScrollX != 7 || f.TextScrollY != 11 {
		t.Fatalf("text state not carried: %+v", f)
	}
}

// A restored leaf's view is its owner row's to give. Until the row is read the
// frame holds a placeholder that is nobody's: it is not a view the pane was
// left at, and adopting the row's replaces it exactly once. After that the
// view is the user's, and a late answer from the row does not move it.
func TestARestoredFrameAdoptsItsOwnersViewOnce(t *testing.T) {
	tr, err := DecodeLayout([]byte(`{"v":1,"root":{"pane":{"id":"p1","anchor":"g1","path":["w1"]}}}`), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	p := tr.FocusedPane()
	if !p.ViewPending || p.HasView() {
		t.Fatalf("a restored leaf reads as holding a view: %+v", p.Frame)
	}
	if _, ok := p.Live(); !ok {
		t.Fatalf("the placeholder must still draw: %+v", p.View)
	}

	// Split before the row answers: the clone waits for the same row.
	np, err := tr.Split(Vertical)
	if err != nil {
		t.Fatal(err)
	}
	if !np.ViewPending {
		t.Fatal("a split of a restored leaf took the placeholder as a view")
	}

	if !p.Adopt(viewed(Frame{}, 4, 5, 2)) {
		t.Fatal("a pending frame refused its owner's view")
	}
	if p.ViewPending || !p.HasView() || !viewIs(p.View, 4, 5, 2) {
		t.Fatalf("adopted frame = %+v", p.Frame)
	}
	p.SetView(9, 5, 2)
	if p.Adopt(viewed(Frame{}, 4, 5, 2)) || !viewIs(p.View, 9, 5, 2) {
		t.Fatalf("a settled view was overwritten: %+v", p.Frame)
	}

	// Descending out of a still-pending frame leaves it with no view to come
	// back to, so the ascent lands on the owner row's framing.
	np.Push(viewed(Frame{Door: "w2"}, 0, 0, 1))
	np.Pop()
	if np.HasView() {
		t.Fatalf("the ascent would land on a placeholder: %+v", np.Frame)
	}
}

// A content leaf's text mode and scroll are its tile row's, adopted the same
// way the grid view is.
func TestARestoredContentFrameAdoptsItsRowsTextState(t *testing.T) {
	tr, err := DecodeLayout([]byte(`{"v":1,"root":{"pane":{"id":"p1","anchor":"g1","text_focus":"t1"}}}`), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	p := tr.FocusedPane()
	if !p.Content || !p.ViewPending {
		t.Fatalf("restored content leaf = %+v", p.Frame)
	}
	cf := ContentFrame("t1", Footprint{X: 2, Y: 3, W: 2, H: 2}, 3, "rendered", 7, 70)
	if !p.Adopt(cf) {
		t.Fatal("refused")
	}
	if p.TextMode != "rendered" || p.TextScrollX != 7 || p.TextScrollY != 70 ||
		!viewIs(p.View, 3, 4, 3) || p.Door != "t1" || !p.Content {
		t.Fatalf("adopted content frame = %+v", p.Frame)
	}
}

// A scroll is clamped at the top and reports whether it moved, because the
// caller redraws on a move and the redraw is what arms the persister.
func TestScrollTextReportsAMove(t *testing.T) {
	f := ContentFrame("7", Footprint{W: 1, H: 1}, 1, "text", 0, 0)
	if f.ScrollText(0, -30) {
		t.Error("a scroll above the top moved the frame")
	}
	if !f.ScrollText(5, 120) || f.TextScrollX != 5 || f.TextScrollY != 120 {
		t.Errorf("a scroll did not move the frame: %+v", f)
	}
	if f.ScrollText(5, 120) {
		t.Error("a scroll to where the frame is reported a move")
	}
	before := FramingFingerprint(&Tree{Root: TreeNode{Pane: &Pane{ID: "p", Stack: Stack{Frame: f}}}}).Value()
	f.ScrollText(5, 121)
	after := FramingFingerprint(&Tree{Root: TreeNode{Pane: &Pane{ID: "p", Stack: Stack{Frame: f}}}}).Value()
	if before == after {
		t.Error("a scroll left the framing fingerprint unchanged, so the persister would not arm")
	}
}
