package pane

import (
	"reflect"
	"testing"
)

// chainPane is a pane four namespace levels deep, with wells and content
// descents mixed in: home well 12, a link into n5, a text descent there,
// a link into mnt7, two wells, and a text descent at the leaf.
func chainPane() *Pane {
	p := &Pane{ID: "p1", Stack: NewStack("k3x9m2q/1")}
	p.Push(Frame{Door: "12", Zoom: 1})
	p.Push(Frame{GridID: "n5aaaaa/1", Door: "lnk1", Zoom: 1})
	p.Push(Frame{Door: "33", Content: true, Zoom: 1})
	p.Push(Frame{GridID: "mnt7abc/9", Door: "lnk2", Zoom: 1})
	p.Push(Frame{Door: "4", Zoom: 1})
	p.Push(Frame{Door: "7", Zoom: 1})
	p.Push(Frame{Door: "21", Content: true, Zoom: 1})
	return p
}

// One crumb per frame, in order: a frame that opens a namespace level shows
// its root grid, every other frame shows the tile it came through.
func TestCrumbsAreOnePerFrame(t *testing.T) {
	got := chainPane().Crumbs()
	want := []Crumb{
		{Level: 0, Anchor: "k3x9m2q/1", ParentAnchor: "k3x9m2q/1"},
		{Level: 1, TileID: "12", ParentAnchor: "k3x9m2q/1"},
		{Level: 2, Anchor: "n5aaaaa/1", ParentAnchor: "n5aaaaa/1"},
		{Level: 3, TileID: "33", Text: true, ParentAnchor: "n5aaaaa/1"},
		{Level: 4, Anchor: "mnt7abc/9", ParentAnchor: "mnt7abc/9"},
		{Level: 5, TileID: "4", ParentAnchor: "mnt7abc/9"},
		{Level: 6, TileID: "7", ParentAnchor: "mnt7abc/9", ParentPath: []string{"4"}},
		{Level: 7, TileID: "21", Text: true, ParentAnchor: "mnt7abc/9", ParentPath: []string{"4", "7"}},
	}
	if len(got) != len(want) {
		t.Fatalf("chain length = %d, want %d\n%+v", len(got), len(want), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Level != w.Level || g.Anchor != w.Anchor || g.TileID != w.TileID ||
			g.Text != w.Text || g.ParentAnchor != w.ParentAnchor ||
			!samePath(g.ParentPath, w.ParentPath) {
			t.Errorf("crumb %d = %+v, want %+v", i, g, w)
		}
	}
}

func samePath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	return len(a) == 0 || reflect.DeepEqual(a, b)
}

func TestCrumbsBootBlankIsEmpty(t *testing.T) {
	if c := (&Pane{ID: "p1"}).Crumbs(); c != nil {
		t.Fatalf("boot-blank pane chain = %+v, want nil", c)
	}
}

func TestCrumbsRootOnly(t *testing.T) {
	p := &Pane{ID: "p1", Stack: NewStack("k3x9m2q/1")}
	c := p.Crumbs()
	if len(c) != 1 || c[0].Anchor != "k3x9m2q/1" || c[0].TileID != "" {
		t.Fatalf("root-only chain = %+v", c)
	}
}

// The ascent arithmetic: n pops reach the crumb n levels out, the current
// crumb is 0 (clicking where you are does nothing), and popping n really
// lands on that crumb's level.
func TestAscentsToLandsOnTheCrumb(t *testing.T) {
	full := chainPane().Crumbs()
	for target, c := range full {
		p := chainPane()
		n := p.AscentsTo(c)
		if want := len(full) - 1 - target; n != want {
			t.Fatalf("crumb %d: AscentsTo = %d, want %d", target, n, want)
		}
		for i := 0; i < n; i++ {
			if !p.Pop() {
				t.Fatalf("crumb %d: ran out of frames after %d pops", target, i)
			}
		}
		got := p.Crumbs()
		if len(got) != target+1 {
			t.Fatalf("crumb %d: landed at depth %d, want %d", target, len(got), target+1)
		}
		if last := got[len(got)-1]; last.Level != c.Level || last.TileID != c.TileID || last.Anchor != c.Anchor {
			t.Errorf("crumb %d: landed on %+v, want %+v", target, last, c)
		}
	}
}

// The innermost crumb is where the pane already is.
func TestAscentsToCurrentIsZero(t *testing.T) {
	p := chainPane()
	full := p.Crumbs()
	if n := p.AscentsTo(full[len(full)-1]); n != 0 {
		t.Fatalf("AscentsTo(current) = %d, want 0", n)
	}
	// A crumb from a deeper chain than the pane has (stale bar click) never
	// asks for a negative ascent.
	if n := p.AscentsTo(Crumb{Level: 99}); n != 0 {
		t.Fatalf("AscentsTo(stale deeper crumb) = %d, want 0", n)
	}
}
