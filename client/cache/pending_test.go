package cache

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/api/rpc"
)

// A create is drawn from the gesture: the pending row stands in its grid,
// beside the tiles and not among them, through every read and event, until
// the create's answer files the node's row in its place or its failure takes
// it away.
func TestAPendingRowStandsUntilItsCreateAnswers(t *testing.T) {
	asked := &gridwellv1.Tile{GridId: "a", Kind: rpc.KindDoor, X: 2, Y: 1, W: 1, H: 1}
	minted := &gridwellv1.Tile{Id: "n/7", GridId: "a", Kind: rpc.KindDoor, X: 2, Y: 1, W: 1, H: 1, Version: 1}
	echo := func(c *Cache) {
		c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: minted}}})
	}
	read := func(rows ...*gridwellv1.Tile) func(*Cache) {
		return func(c *Cache) { c.PutGrid(&gridwellv1.Grid{Id: "a"}, rows) }
	}
	type step func(*Cache, *Holding)
	do := func(f func(*Cache)) step { return func(c *Cache, _ *Holding) { f(c) } }
	landed := step(func(_ *Cache, pl *Holding) { pl.Landed(minted) })
	failed := step(func(_ *Cache, pl *Holding) { pl.Failed() })
	released := step(func(_ *Cache, pl *Holding) { pl.Released() })

	cases := []struct {
		name    string
		steps   []step
		pending bool
		row     bool
	}{
		{name: "it is drawn before any answer", pending: true},
		{name: "a read from before the create keeps it", steps: []step{do(read())}, pending: true},
		{name: "the echo before the answer stands beside it", steps: []step{do(echo)}, pending: true, row: true},
		{name: "the answer files the node's row in its place", steps: []step{landed}, row: true},
		{name: "the answer after the echo leaves the one row", steps: []step{do(echo), landed}, row: true},
		{name: "a read after the answer that lacks the row is the node's word", steps: []step{landed, do(read())}},
		{name: "a refusal takes it away", steps: []step{failed}},
		{name: "an unheard create takes it away", steps: []step{released}},
		{name: "a refusal after a read still takes it away", steps: []step{do(read()), failed}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New()
			c.PutGrid(&gridwellv1.Grid{Id: "a"}, nil)
			pl := c.Pend(asked)
			if pl == nil {
				t.Fatal("Pend on a cached grid held nothing")
			}
			for _, s := range tc.steps {
				s(c, pl)
			}
			g, _ := c.Grid("a")
			if got := len(g.Pending); got != map[bool]int{true: 1}[tc.pending] {
				t.Errorf("pending rows = %d, want pending %v", got, tc.pending)
			}
			for id, n := range g.Pending {
				if !Provisional(id) || n.Id != id || PlacementOf(n) != PlacementOf(asked) || n.Kind != asked.Kind {
					t.Errorf("pending row %q = %v, want the asked row under a provisional id", id, n)
				}
				if g.Tiles[id] != nil {
					t.Errorf("pending row %q is among the tiles a gesture hits", id)
				}
			}
			if _, ok := g.Tiles["n/7"]; ok != tc.row {
				t.Errorf("node's row cached = %v, want %v", ok, tc.row)
			}
			if got := len(g.Drawn()); got != len(g.Tiles)+len(g.Pending) {
				t.Errorf("drawn = %d rows, want tiles and pending", got)
			}
		})
	}
}

// Two creates in a row are two pending rows, each settled by its own
// answer, and a grid this client does not hold draws nothing ahead.
func TestTwoCreatesPendApartAndAnUncachedGridPendsNothing(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "a"}, nil)
	first := c.Pend(&gridwellv1.Tile{GridId: "a", Kind: rpc.KindText, W: 1, H: 1})
	second := c.Pend(&gridwellv1.Tile{GridId: "a", Kind: rpc.KindText, X: 1, W: 1, H: 1})
	first.Failed()
	g, _ := c.Grid("a")
	if len(g.Pending) != 1 {
		t.Fatalf("pending after the first's refusal = %d, want the second alone", len(g.Pending))
	}
	for _, n := range g.Pending {
		if n.X != 1 {
			t.Errorf("the pending row left is at x %d, want the second's 1", n.X)
		}
	}
	second.Landed(&gridwellv1.Tile{Id: "n/8", GridId: "a", Kind: rpc.KindText, X: 1, W: 1, H: 1})
	if g, _ := c.Grid("a"); len(g.Pending) != 0 || g.Tiles["n/8"] == nil {
		t.Errorf("after the second's answer: pending %d, row %v", len(g.Pending), g.Tiles["n/8"])
	}
	if pl := c.Pend(&gridwellv1.Tile{GridId: "b", Kind: rpc.KindText}); pl != nil {
		t.Errorf("Pend on an uncached grid = %v, want nil", pl)
	}
	if Provisional("n/8") || rpc.UUIDOf(first.key.id) != "" {
		t.Errorf("a provisional id must be bare and no row's")
	}
}
