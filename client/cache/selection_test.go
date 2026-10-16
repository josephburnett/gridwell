package cache

import (
	"slices"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A grid's selection is on its cached row, filed by a read and replaced whole
// by SelectionChanged, the set the node holds, with nothing read again. A
// snapshot handed out before keeps what it held.
func TestSelectionChangedReplacesTheCachedSet(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "n/1", Selection: []string{"n/4"}},
		[]*gridwellv1.Tile{{Id: "n/4", GridId: "n/1"}, {Id: "n/5", GridId: "n/1"}})
	before, _ := c.Grid("n/1")

	if !c.Apply(rpc.SelectionEvent("n/1", []string{"n/4", "n/5"})) {
		t.Fatal("a new set was not a visible change")
	}
	g, _ := c.Grid("n/1")
	if !slices.Equal(g.Meta.Selection, []string{"n/4", "n/5"}) {
		t.Errorf("cached selection = %v, want [n/4 n/5]", g.Meta.Selection)
	}
	if len(g.Tiles) != 2 {
		t.Errorf("the event touched the rows: %d left", len(g.Tiles))
	}
	if !slices.Equal(before.Meta.Selection, []string{"n/4"}) {
		t.Errorf("an earlier snapshot changed under it: %v", before.Meta.Selection)
	}
	if c.Apply(rpc.SelectionEvent("n/1", []string{"n/5", "n/4"})) {
		t.Error("the same set in another order redrew")
	}
	if !c.Apply(rpc.SelectionEvent("n/1", nil)) {
		t.Error("clearing was not a visible change")
	}
	if c.Apply(rpc.SelectionEvent("n/9", []string{"n/7"})) {
		t.Error("an event for a grid nobody holds changed something")
	}
}
