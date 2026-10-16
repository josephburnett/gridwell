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

func grid14(c *Cache, sel ...string) {
	c.PutGrid(&gridwellv1.Grid{Id: "n/1", Selection: sel},
		[]*gridwellv1.Tile{{Id: "n/4", GridId: "n/1"}, {Id: "n/5", GridId: "n/1"}})
}

func wantSets(t *testing.T, c *Cache, why string, shown, claim []string) {
	t.Helper()
	if got := c.Selection("n/1"); !slices.Equal(got, shown) {
		t.Errorf("%s: drawn %v, want %v", why, got, shown)
	}
	if got := c.SelectionClaim("n/1"); !slices.Equal(got, claim) {
		t.Errorf("%s: claim %v, want %v", why, got, claim)
	}
}

// A select is drawn from the press and claims what the node holds; its
// landing makes the sent set the claim before the echo, so the next write in
// the chain claims what the node now holds, and the echo ends it all.
func TestSelectIsDrawnAheadAndLandsAsTheClaim(t *testing.T) {
	c := New()
	grid14(c)
	c.Select("n/1", []string{"n/4"})
	wantSets(t, c, "put up", []string{"n/4"}, nil)

	c.Select("n/1", []string{"n/4", "n/5"})
	c.SelectionLanded("n/1", []string{"n/4"})
	wantSets(t, c, "first write landed, second still up", []string{"n/4", "n/5"}, []string{"n/4"})

	c.Apply(rpc.SelectionEvent("n/1", []string{"n/4"}))
	wantSets(t, c, "first echo: the newer select stays drawn", []string{"n/4", "n/5"}, []string{"n/4"})

	c.SelectionLanded("n/1", []string{"n/4", "n/5"})
	wantSets(t, c, "second landed", []string{"n/4", "n/5"}, []string{"n/4", "n/5"})
	c.Apply(rpc.SelectionEvent("n/1", []string{"n/5", "n/4"}))
	wantSets(t, c, "second echo", []string{"n/4", "n/5"}, []string{"n/4", "n/5"})
	if len(c.sel) != 0 {
		t.Errorf("a hold outlived its echo: %+v", c.sel["n/1"])
	}
}

// Another writer's set reaches the stream after this one's landing: it is
// newer, so it is both what is drawn and what the next write claims.
func TestTheStreamOutranksALanding(t *testing.T) {
	c := New()
	grid14(c)
	c.Select("n/1", []string{"n/4"})
	c.SelectionLanded("n/1", []string{"n/4"})
	c.Apply(rpc.SelectionEvent("n/1", []string{"n/5"}))
	wantSets(t, c, "another writer after ours", []string{"n/5"}, []string{"n/5"})
}

// A refused select goes back to the node's word, which the refusal's re-read
// brings.
func TestARefusedSelectFallsBackToTheNode(t *testing.T) {
	c := New()
	grid14(c, "n/5")
	c.Select("n/1", []string{"n/4", "n/5"})
	c.SelectionRefused("n/1")
	wantSets(t, c, "refused", []string{"n/5"}, []string{"n/5"})
	grid14(c, "n/4")
	wantSets(t, c, "re-read", []string{"n/4"}, []string{"n/4"})
}
