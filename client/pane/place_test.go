package pane

import (
	"reflect"
	"testing"
)

// A descent pushes and an ascent pops, and the viewport you left a level at is
// the frame you left: no second stack to keep in step.
func TestPushPopRestoresTheViewportYouLeft(t *testing.T) {
	s := NewStack("home/1")
	s.Cx, s.Cy, s.Zoom = 5, 6, 1.5

	s.Push(Frame{Door: "7", Zoom: 1})
	s.Cx, s.Cy, s.Zoom = 100, 200, 3

	if s.Depth() != 2 || s.Anchor() != "home/1" || !reflect.DeepEqual(s.Path(), []string{"7"}) {
		t.Fatalf("after descent: depth=%d anchor=%q path=%v", s.Depth(), s.Anchor(), s.Path())
	}
	if !s.Pop() {
		t.Fatal("pop with a frame below returned false")
	}
	if s.Cx != 5 || s.Cy != 6 || s.Zoom != 1.5 {
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

func TestHasViewMarksAnUnsavedFrame(t *testing.T) {
	if (Frame{Zoom: 1}).HasView() != true || (Frame{}).HasView() != false {
		t.Fatal("HasView must key off a positive zoom")
	}
}

// Reset makes the whole stack one frame, so a restore to a shallower place
// leaves no deeper frame behind to ascend into.
func TestResetClearsEveryFrame(t *testing.T) {
	s := StackAt("home/1", []string{"4", "9"}, "13")
	s.Reset(Frame{GridID: "other/1", Zoom: 1})
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
	if f.Cx != 4 || f.Cy != 6 {
		t.Fatalf("not centred on the footprint: %+v", f)
	}
	if !f.HasView() || f.Zoom != 2.5 {
		t.Fatalf("no viewport: %+v", f)
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
	if p.Zoom <= 0 {
		t.Fatalf("the placeholder must still draw: zoom %v", p.Zoom)
	}

	// Split before the row answers: the clone waits for the same row.
	np, err := tr.Split(Vertical)
	if err != nil {
		t.Fatal(err)
	}
	if !np.ViewPending {
		t.Fatal("a split of a restored leaf took the placeholder as a view")
	}

	if !p.Adopt(Frame{Cx: 4, Cy: 5, Zoom: 2}) {
		t.Fatal("a pending frame refused its owner's view")
	}
	if p.ViewPending || !p.HasView() || p.Cx != 4 || p.Cy != 5 || p.Zoom != 2 {
		t.Fatalf("adopted frame = %+v", p.Frame)
	}
	p.Cx = 9
	if p.Adopt(Frame{Cx: 4, Cy: 5, Zoom: 2}) || p.Cx != 9 {
		t.Fatalf("a settled view was overwritten: %+v", p.Frame)
	}

	// Descending out of a still-pending frame leaves it with no view to come
	// back to, so the ascent lands on the owner row's framing.
	np.Push(Frame{Door: "w2", Zoom: 1})
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
		p.Cx != 3 || p.Cy != 4 || p.Zoom != 3 || p.Door != "t1" || !p.Content {
		t.Fatalf("adopted content frame = %+v", p.Frame)
	}
}
