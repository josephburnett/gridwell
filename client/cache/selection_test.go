package cache

import (
	"slices"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// sel is one select's adds and removes.
type sel struct{ add, remove []string }

func (c *Cache) put(gid string, s sel) { c.Select(gid, s.add, s.remove) }

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

func grid14(c *Cache, sel ...string) {
	c.PutGrid(&gridwellv1.Grid{Id: "n/1", Selection: sel},
		[]*gridwellv1.Tile{{Id: "n/4", GridId: "n/1"}, {Id: "n/5", GridId: "n/1"}})
}

func wantShown(t *testing.T, c *Cache, why string, shown ...string) {
	t.Helper()
	if got := c.Selection("n/1"); !slices.Equal(got, shown) {
		t.Errorf("%s: drawn %v, want %v", why, got, shown)
	}
}

// A select is drawn from the press over the set the node told. A write
// carries every tile still owed when it is sent; its landing keeps them drawn
// until the stream tells a set, and the echo ends it all.
func TestSelectIsDrawnAheadAndLands(t *testing.T) {
	c := New()
	grid14(c)
	c.put("n/1", sel{add: []string{"n/4"}})
	wantShown(t, c, "put up", "n/4")
	first := c.SelectionUp("n/1")

	c.put("n/1", sel{add: []string{"n/5"}})
	c.SelectionLanded("n/1", first)
	wantShown(t, c, "first landed, second still up", "n/4", "n/5")
	second := c.SelectionUp("n/1")
	if !slices.Equal(second.Add, []string{"n/5"}) || len(second.Remove) != 0 {
		t.Errorf("the second write carries %+v, want n/5 alone", second)
	}

	c.Apply(rpc.SelectionEvent("n/1", []string{"n/4"}))
	wantShown(t, c, "first echo: the second stays drawn", "n/4", "n/5")

	c.SelectionLanded("n/1", second)
	wantShown(t, c, "second landed", "n/4", "n/5")
	c.Apply(rpc.SelectionEvent("n/1", []string{"n/5", "n/4"}))
	wantShown(t, c, "second echo", "n/4", "n/5")
	if len(c.sel) != 0 {
		t.Errorf("a hold outlived its echo: %+v", c.sel["n/1"])
	}
}

// A write that is parked and sent again carries every select made meanwhile,
// so a select is never lost behind another's retry.
func TestAResentWriteCarriesEverySelectOwed(t *testing.T) {
	c := New()
	grid14(c, "n/5")
	c.put("n/1", sel{add: []string{"n/4"}})
	c.put("n/1", sel{remove: []string{"n/5"}})
	got := c.SelectionUp("n/1")
	if !slices.Equal(got.Add, []string{"n/4"}) || !slices.Equal(got.Remove, []string{"n/5"}) {
		t.Fatalf("a resent write carries %+v, want add n/4 and remove n/5", got)
	}
	c.SelectionLanded("n/1", got)
	if up := c.SelectionUp("n/1"); !up.Empty() {
		t.Errorf("after it landed %+v is still owed", up)
	}
	wantShown(t, c, "landed", "n/4")
}

// Another writer's select lands beside one this client has up: the drawn set
// is the node's told set with this client's select over it, so both show.
func TestAnotherWritersSelectShowsBesideOurs(t *testing.T) {
	c := New()
	grid14(c)
	c.put("n/1", sel{add: []string{"n/4"}})
	c.Apply(rpc.SelectionEvent("n/1", []string{"n/5"}))
	wantShown(t, c, "another writer's add while ours is up", "n/4", "n/5")
}

// A tile tapped again after a write carrying it was sent stays up, at the
// later word, when that write lands.
func TestTheLaterTapOnATileIsDrawn(t *testing.T) {
	c := New()
	grid14(c)
	c.put("n/1", sel{add: []string{"n/4"}})
	in := c.SelectionUp("n/1")
	c.put("n/1", sel{remove: []string{"n/4"}})
	c.SelectionLanded("n/1", in)
	wantShown(t, c, "the first tap landed, the second is up")
	out := c.SelectionUp("n/1")
	if !slices.Equal(out.Remove, []string{"n/4"}) {
		t.Fatalf("the second write carries %+v, want remove n/4", out)
	}
	c.SelectionLanded("n/1", out)
	wantShown(t, c, "both landed")
}

// Another writer's set reaches the stream after this one's landing: it is
// newer, so it is what is drawn.
func TestTheStreamOutranksALanding(t *testing.T) {
	c := New()
	grid14(c)
	c.put("n/1", sel{add: []string{"n/4"}})
	c.SelectionLanded("n/1", c.SelectionUp("n/1"))
	c.Apply(rpc.SelectionEvent("n/1", []string{"n/5"}))
	wantShown(t, c, "another writer after ours", "n/5")
}

// A refused select (a source the user switched off, a dead grid) stops being
// drawn; one made after the refused write was sent stays.
func TestARefusedSelectFallsBackToTheNode(t *testing.T) {
	c := New()
	grid14(c, "n/5")
	c.put("n/1", sel{add: []string{"n/4"}})
	refused := c.SelectionUp("n/1")
	c.put("n/1", sel{remove: []string{"n/5"}})
	c.SelectionRefused("n/1", refused)
	wantShown(t, c, "one refused, one up")
	c.SelectionRefused("n/1", c.SelectionUp("n/1"))
	wantShown(t, c, "both refused", "n/5")
	if len(c.sel) != 0 {
		t.Errorf("a refused hold stayed: %+v", c.sel["n/1"])
	}
}
