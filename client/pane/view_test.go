package pane

import (
	"testing"

	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The view names every pane on screen in tree order, each with its place as
// the URL grammar names it, the grid it resolves to and whether it has focus;
// the pane-tile level the window is inside, with each pane's bare id in that
// layout; the label; and Showing, the well previews included. A pane the layout hides is not on screen.
func TestView(t *testing.T) {
	grids := map[string]map[string]*gridwellv1.Tile{
		"g-w5": {"w1": well("w1", 0, 0, "c/1")},
	}
	tiles := func(g string) map[string]*gridwellv1.Tile { return grids[g] }

	tr := TreeAtPlace("w1:", "p/1", []string{"w5"}, rpc.ViewOf(0, 0, 1))
	first := tr.FocusedPane()
	second, _ := tr.Split(Vertical)
	second.Stack = StackAt("n/1", []string{"w2"}, "t3")
	third, _ := tr.Split(Horizontal)
	third.Stack = StackAt("q/1", nil, "")
	if err := tr.SetFocus(second.ID); err != nil {
		t.Fatal(err)
	}
	laid := Layout(tr, Rect{W: 800, H: 600})
	delete(laid, third.ID)

	var lv Levels
	lv.Push(Level{TileID: "home/4", GridID: "home/1"})
	lv.Push(Level{TileID: "p/9", GridID: "p/1"})

	got := View(tr, &lv, laid, "desktop", fakeLeaf, tiles)
	want := &gridwellv1.SetViewRequest{
		Label:        "desktop",
		PaneTiles:    []string{"home/4", "p/9"},
		ShownGridIds: []string{"c/1", "g-w2", "g-w5"},
		Panes: []*gridwellv1.ViewPane{
			{Id: first.ID, Anchor: "p/1", Path: []string{"w5"}, GridId: "g-w5"},
			{Id: second.ID, Anchor: "n/1", Path: []string{"w2"}, ContentId: "t3", GridId: "g-w2", Focused: true},
		},
	}
	if !proto.Equal(got, want) {
		t.Errorf("View =\n%v\nwant\n%v", got, want)
	}
}

// Outside every pane tile the level is empty, and a pane whose place does not
// resolve yet still names its place, with no grid.
func TestViewOfTheSessionTree(t *testing.T) {
	tr := TreeAtPlace("", "p/1", []string{"w5", "?"}, rpc.ViewOf(0, 0, 1))
	got := View(tr, &Levels{}, allLaidOut(tr), "browser", func(string, []string) string { return "" }, noTiles)
	want := &gridwellv1.SetViewRequest{
		Label: "browser",
		Panes: []*gridwellv1.ViewPane{{Id: tr.Focus, Anchor: "p/1", Path: []string{"w5", "?"}, Focused: true}},
	}
	if !proto.Equal(got, want) {
		t.Errorf("View =\n%v\nwant\n%v", got, want)
	}
}
