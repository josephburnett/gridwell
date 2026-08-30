package pane

import (
	"reflect"
	"testing"
)

// Push/pop is the whole navigation model: a descent pushes, an ascent pops,
// and the viewport you left a level at is simply the frame you left. There
// is no second stack to keep in step.
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

// A frame that opens a namespace level carries its grid id; an ordinary
// well frame does not (the grid is derived from the cache). Anchor/Path are
// projections of that one slice.
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

// StackAt is the ONE decoder both encodings use (a URL after its id walk, a
// layout blob): a root grid, a path of doorways, an optional content leaf.
// The frames it builds carry no viewport — nothing encodes the viewports a
// pane would ascend onto (owner decision #13), so the ascent falls back to
// each grid's persisted framing.
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

// Reset is the boot / history-restore door: the whole stack becomes one
// frame, so a restore to a shallower place can never leave a deeper frame
// behind to ascend into.
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
