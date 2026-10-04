package pane

import (
	"reflect"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
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
			tr := TreeAtPlace("", "p/1", nil, rpc.ViewOf(0, 0, 1))
			return tr, allLaidOut(tr)
		}, []string{"p/1"}},
		{"a pane through a well shows the well's grid", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "p/1", []string{"w5"}, rpc.ViewOf(0, 0, 1))
			return tr, allLaidOut(tr)
		}, []string{"g-w5"}},
		{"a pane in a content tile shows the grid the tile sits in", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "p/1", []string{"w5"}, rpc.ViewOf(0, 0, 1))
			tr.FocusedPane().Push(ContentFrame("f9", Footprint{W: 1, H: 1}, 1, "", 0, 0))
			return tr, allLaidOut(tr)
		}, []string{"g-w5"}},
		{"two panes on one grid are one entry", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "p/1", nil, rpc.ViewOf(0, 0, 1))
			second, _ := tr.Split(Vertical)
			second.Stack = StackAt("p/1", nil, "t3")
			return tr, allLaidOut(tr)
		}, []string{"p/1"}},
		{"two panes on two grids, sorted", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "q/1", nil, rpc.ViewOf(0, 0, 1))
			second, _ := tr.Split(Vertical)
			second.Stack = StackAt("n/1", []string{"w2"}, "")
			return tr, allLaidOut(tr)
		}, []string{"g-w2", "q/1"}},
		{"a pane the layout hides is not shown", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "q/1", nil, rpc.ViewOf(0, 0, 1))
			second, _ := tr.Split(Vertical)
			second.Stack = StackAt("n/1", nil, "")
			return tr, map[string]Rect{second.ID: {W: 1, H: 1}}
		}, []string{"n/1"}},
		{"a well not yet fetched shows as deep as it resolves", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "p/1", []string{"w5", "?"}, rpc.ViewOf(0, 0, 1))
			return tr, allLaidOut(tr)
		}, []string{"g-w5"}},
		{"a pane with no place shows nothing", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "", nil, rpc.ViewOf(0, 0, 1))
			return tr, allLaidOut(tr)
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr, laid := c.build()
			if got := Showing(tr, laid, fakeLeaf, noTiles); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Showing = %v, want %v", got, c.want)
			}
		})
	}
}

// Inside a pane tile the window shows that level's tree; the tree it parked
// is off screen, so its grids are not shown.
func TestShowingALevelShowsOnlyItsOwnTree(t *testing.T) {
	outer := TreeAtPlace("w0:", "outer/1", nil, rpc.ViewOf(0, 0, 1))
	var lv Levels
	lv.Push(Level{OuterTree: outer, TileID: "pt/4", GridID: "outer/1"})
	inner := TreeAtPlace("w1:", "inner/1", nil, rpc.ViewOf(0, 0, 1))
	got := Showing(inner, allLaidOut(inner), fakeLeaf, noTiles)
	if !reflect.DeepEqual(got, []string{"inner/1"}) || strings.Contains(strings.Join(got, ","), "outer") {
		t.Errorf("Showing = %v, want only the level in front", got)
	}
}

func noTiles(string) map[string]*gridwellv1.Tile { return nil }

func well(id string, x, y int64, child string) *gridwellv1.Tile {
	return &gridwellv1.Tile{Id: id, Kind: rpc.KindWell, X: x, Y: y, W: 2, H: 2, ChildGridId: child}
}

// A well on screen draws its child grid as a preview, so that grid is shown
// too; one level deep, as the renderer draws it.
func TestShowingWellPreviews(t *testing.T) {
	grids := map[string]map[string]*gridwellv1.Tile{
		"p/1": {
			"w1":   well("w1", 1, 1, "c/1"),
			"link": {Id: "link", Kind: rpc.KindWell, Reference: true, X: -3, Y: -3, W: 2, H: 2, ChildGridId: "far/c"},
			"far":  well("far", 100, 100, "c/off"),
			"text": {Id: "text", Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1},
		},
		"c/1": {"deep": well("deep", 0, 0, "c/2")},
	}
	tiles := func(g string) map[string]*gridwellv1.Tile { return grids[g] }
	cases := []struct {
		name  string
		build func() (*Tree, map[string]Rect)
		want  []string
	}{
		{"a well and a link on screen show their grids, one level deep", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "p/1", nil, rpc.ViewOf(0, 0, 1))
			return tr, allLaidOut(tr)
		}, []string{"c/1", "far/c", "p/1"}},
		{"a well scrolled into view is shown", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "p/1", nil, rpc.ViewOf(100, 100, 1))
			return tr, allLaidOut(tr)
		}, []string{"c/off", "p/1"}},
		{"a pane in a content tile draws no previews", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "p/1", nil, rpc.ViewOf(0, 0, 1))
			tr.FocusedPane().Push(ContentFrame("text", Footprint{W: 1, H: 1}, 1, "", 0, 0))
			return tr, allLaidOut(tr)
		}, []string{"p/1"}},
		{"a hidden pane's previews are not shown", func() (*Tree, map[string]Rect) {
			tr := TreeAtPlace("", "q/1", nil, rpc.ViewOf(0, 0, 1))
			first := tr.FocusedPane()
			second, _ := tr.Split(Vertical)
			second.Stack = StackAt("p/1", nil, "")
			return tr, map[string]Rect{first.ID: {W: 800, H: 600}}
		}, []string{"q/1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr, laid := c.build()
			if got := Showing(tr, laid, fakeLeaf, tiles); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Showing = %v, want %v", got, c.want)
			}
		})
	}
}

func allLaidOut(t *Tree) map[string]Rect {
	return Layout(t, Rect{W: 800, H: 600})
}
