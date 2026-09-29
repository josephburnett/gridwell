package pane

import (
	"reflect"
	"strings"
	"testing"
)

// fakeLeaf resolves a place the way the cache would: each well id names the
// grid behind it, and "?" is a well whose grid is not cached yet.
func fakeLeaf(anchor string, path []string) string {
	g := anchor
	for _, w := range path {
		if w == "?" {
			return g
		}
		g = "g-" + w
	}
	return g
}

func TestShowing(t *testing.T) {
	cases := []struct {
		name  string
		build func() (*Tree, map[string]Rect)
		want  []string
	}{
		{"a pane in a grid", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "p/1", nil, 0, 0, 1)
			return tr, allLaidOut(tr)
		}, []string{"p/1"}},
		{"a pane through a well shows the well's grid", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "p/1", []string{"w5"}, 0, 0, 1)
			return tr, allLaidOut(tr)
		}, []string{"g-w5"}},
		{"a pane in a content tile shows the grid the tile sits in", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "p/1", []string{"w5"}, 0, 0, 1)
			tr.FocusedPane().Push(ContentFrame("f9", Footprint{W: 1, H: 1}, 1, "", 0, 0))
			return tr, allLaidOut(tr)
		}, []string{"g-w5"}},
		{"two panes on one grid are one entry", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "p/1", nil, 0, 0, 1)
			second, _ := tr.Split(Vertical)
			second.Stack = StackAt("p/1", nil, "t3")
			return tr, allLaidOut(tr)
		}, []string{"p/1"}},
		{"two panes on two grids, sorted", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "q/1", nil, 0, 0, 1)
			second, _ := tr.Split(Vertical)
			second.Stack = StackAt("n/1", []string{"w2"}, "")
			return tr, allLaidOut(tr)
		}, []string{"g-w2", "q/1"}},
		{"a pane the layout hides is not shown", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "q/1", nil, 0, 0, 1)
			second, _ := tr.Split(Vertical)
			second.Stack = StackAt("n/1", nil, "")
			return tr, map[string]Rect{second.ID: {W: 1, H: 1}}
		}, []string{"n/1"}},
		{"a well not yet fetched shows as deep as it resolves", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "p/1", []string{"w5", "?"}, 0, 0, 1)
			return tr, allLaidOut(tr)
		}, []string{"g-w5"}},
		{"a pane with no place shows nothing", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "", nil, 0, 0, 1)
			return tr, allLaidOut(tr)
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr, laid := c.build()
			if got := Showing(tr, laid, fakeLeaf); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Showing = %v, want %v", got, c.want)
			}
		})
	}
}

// Inside a pane tile the window shows that level's tree; the tree it parked
// is off screen, so its grids are not shown.
func TestShowingALevelShowsOnlyItsOwnTree(t *testing.T) {
	outer := TreeAtPlace("w0:", "outer/1", nil, 0, 0, 1)
	var lv Levels
	lv.Push(Level{OuterTree: outer, TileID: "pt/4", GridID: "outer/1"})
	inner := TreeAtPlace("w1:", "inner/1", nil, 0, 0, 1)
	got := Showing(inner, allLaidOut(inner), fakeLeaf)
	if !reflect.DeepEqual(got, []string{"inner/1"}) || strings.Contains(strings.Join(got, ","), "outer") {
		t.Errorf("Showing = %v, want only the level in front", got)
	}
}

func allLaidOut(t *Tree) map[string]Rect {
	return Layout(t, Rect{W: 800, H: 600})
}
