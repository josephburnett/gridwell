package cache

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/api/rpc"
)

// A placement is optimistic: the cache holds the move from the release, and
// only the write's own echo, its refusal, or a read after it landed decides
// the tile's layout again. Layout claims no version, so the hold is what
// keeps an older row from putting a newer placement back.
func TestAPlacementHoldsUntilItsOwnEchoOrVerdict(t *testing.T) {
	const id = "t"
	at := func(grid string, x, y int64) Placement { return Placement{GridID: grid, X: x, Y: y, W: 1, H: 1} }
	origin, moved, again, foreign := at("a", 0, 0), at("a", 3, 0), at("a", 5, 0), at("a", 7, 7)
	across := at("b", 1, 1)
	row := func(p Placement, name string) *gridwellv1.Tile {
		n := &gridwellv1.Tile{Id: id, Kind: rpc.KindText, Version: 2, AltText: name}
		p.set(n)
		return n
	}
	echo := func(p Placement) func(*Cache) {
		return func(c *Cache) {
			c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
				TileChanged: &gridwellv1.TileChanged{Tile: row(p, "echo")}}})
		}
	}
	removed := func(grid string) func(*Cache) {
		return func(c *Cache) {
			c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileRemoved{
				TileRemoved: &gridwellv1.TileRemoved{GridId: grid, TileId: id}}})
		}
	}
	read := func(grid string, p *Placement) func(*Cache) {
		return func(c *Cache) {
			var tiles []*gridwellv1.Tile
			if p != nil {
				tiles = append(tiles, row(*p, "read"))
			}
			c.PutGrid(&gridwellv1.Grid{Id: grid}, tiles)
		}
	}
	type step func(c *Cache, pls []*Placing)
	ev := func(f func(*Cache)) step { return func(c *Cache, _ []*Placing) { f(c) } }
	landed := func(i int, p Placement) step {
		return func(_ *Cache, pls []*Placing) { pls[i].Landed(row(p, "answer")) }
	}
	failed := func(i int) step { return func(_ *Cache, pls []*Placing) { pls[i].Failed() } }

	cases := []struct {
		name   string
		places []Placement
		steps  []step
		want   Placement
		// gone names a grid the row must not be in.
		gone string
	}{
		{name: "the move is the cache's before any event", places: []Placement{moved}, want: moved},
		{name: "a cross-grid move leaves its old grid", places: []Placement{across}, want: across, gone: "a"},
		{name: "an older echo cannot put it back",
			places: []Placement{moved}, steps: []step{ev(echo(origin))}, want: moved},
		{name: "a grid read from before the write cannot put it back",
			places: []Placement{moved}, steps: []step{ev(read("a", &origin))}, want: moved},
		{name: "a stale read of the old grid leaves a cross-grid move where it went",
			places: []Placement{across}, steps: []step{ev(read("a", &origin))}, want: across, gone: "a"},
		{name: "a stale read of the new grid that lacks it keeps it",
			places: []Placement{across}, steps: []step{ev(read("b", nil))}, want: across, gone: "a"},
		{name: "an older removal from where it went is not its removal",
			places: []Placement{across}, steps: []step{ev(removed("b"))}, want: across},
		{name: "its own echo releases it, so a later move by someone else shows",
			places: []Placement{moved}, steps: []step{ev(echo(moved)), ev(echo(foreign))}, want: foreign},
		{name: "the answer releases nothing: an older echo still behind it is held",
			places: []Placement{moved}, steps: []step{landed(0, moved), ev(echo(origin))}, want: moved},
		{name: "two moves: the first's echo is older than the second",
			places: []Placement{moved, again}, steps: []step{ev(echo(moved))}, want: again},
		{name: "two moves: the second's echo releases",
			places: []Placement{moved, again},
			steps:  []step{ev(echo(moved)), ev(echo(again)), ev(echo(foreign))}, want: foreign},
		{name: "a refusal snaps it back and releases it",
			places: []Placement{moved}, steps: []step{failed(0), ev(echo(foreign))}, want: foreign},
		{name: "a refusal puts a cross-grid move back in its grid",
			places: []Placement{across}, steps: []step{failed(0)}, want: origin, gone: "b"},
		{name: "a refusal of the older of two moves leaves the newer",
			places: []Placement{moved, again}, steps: []step{failed(0), ev(echo(origin))}, want: again},
		{name: "a read after it landed is the node's word, its echo lost in a gap",
			places: []Placement{moved}, steps: []step{landed(0, moved), ev(read("a", &foreign))}, want: foreign},
		{name: "a read while the write is unanswered is not",
			places: []Placement{moved}, steps: []step{ev(read("a", &foreign))}, want: moved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New()
			c.PutGrid(&gridwellv1.Grid{Id: "a"}, []*gridwellv1.Tile{row(origin, "start")})
			c.PutGrid(&gridwellv1.Grid{Id: "b"}, nil)
			var pls []*Placing
			for _, p := range tc.places {
				pl := c.Place(id, p)
				if pl == nil {
					t.Fatalf("Place(%v) held nothing", p)
				}
				pls = append(pls, pl)
			}
			for _, s := range tc.steps {
				s(c, pls)
			}
			g, _ := c.Grid(tc.want.GridID)
			got, ok := g.Tiles[id]
			if !ok {
				t.Fatalf("tile not in grid %s", tc.want.GridID)
			}
			if PlacementOf(got) != tc.want {
				t.Errorf("placement = %+v, want %+v", PlacementOf(got), tc.want)
			}
			if tc.gone != "" {
				if g, _ := c.Grid(tc.gone); g.Tiles[id] != nil {
					t.Errorf("tile still in grid %s", tc.gone)
				}
			}
		})
	}
}

// A held row still takes every field a placement does not own: the hold is
// on layout, not on the tile.
func TestAHeldRowTakesWhatThePlacementDidNotSet(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "a"}, []*gridwellv1.Tile{
		{Id: "t", GridId: "a", Kind: rpc.KindText, W: 1, H: 1, Version: 2, AltText: "old"}})
	c.Place("t", Placement{GridID: "a", X: 4, W: 1, H: 1})
	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{
		Tile: &gridwellv1.Tile{Id: "t", GridId: "a", Kind: rpc.KindText, W: 1, H: 1, Version: 3, AltText: "renamed"}}}})
	got := mustRow(t, c, "a", "t")
	if got.X != 4 || got.AltText != "renamed" || got.Version != 3 {
		t.Errorf("row = %v, want x 4 held with the echo's name and version", got)
	}
}

// A plugin mints the row a placement writes into, so its answer names another
// id and no echo will ever name this one: the answer releases the hold.
func TestAnAnswerUnderAMintedIdReleasesTheHold(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "a"}, []*gridwellv1.Tile{
		{Id: "key:x", GridId: "a", Kind: rpc.KindText, W: 1, H: 1}})
	pl := c.Place("key:x", Placement{GridID: "a", X: 2, W: 1, H: 1})
	pl.Landed(&gridwellv1.Tile{Id: "k3a9z1q", GridId: "a", Kind: rpc.KindText, X: 2, W: 1, H: 1})
	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{
		Tile: &gridwellv1.Tile{Id: "key:x", GridId: "a", Kind: rpc.KindText, X: 6, W: 1, H: 1}}}})
	if got := mustRow(t, c, "a", "key:x"); got.X != 6 {
		t.Errorf("x = %d, want 6: the hold outlived an answer under the minted id", got.X)
	}
}

// Nothing cached, nothing held: a placement of a row this client never read
// is the echo's to show.
func TestPlacingAnUncachedRowHoldsNothing(t *testing.T) {
	c := New()
	if pl := c.Place("t", Placement{GridID: "a", W: 1, H: 1}); pl != nil {
		t.Errorf("Place on an empty cache = %v, want nil", pl)
	}
	var pl *Placing
	pl.Landed(nil)
	pl.Failed()
}
