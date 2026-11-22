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
	type step func(c *Cache, pls []*Holding)
	ev := func(f func(*Cache)) step { return func(c *Cache, _ []*Holding) { f(c) } }
	landed := func(i int, p Placement) step {
		return func(_ *Cache, pls []*Holding) { pls[i].Landed(row(p, "answer")) }
	}
	failed := func(i int) step { return func(_ *Cache, pls []*Holding) { pls[i].Failed() } }

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
			var pls []*Holding
			for _, p := range tc.places {
				pl := c.Move(id, p)
				if pl == nil {
					t.Fatalf("Move(%v) held nothing", p)
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
	c.Move("t", Placement{GridID: "a", X: 4, W: 1, H: 1})
	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{
		Tile: &gridwellv1.Tile{Id: "t", GridId: "a", Kind: rpc.KindText, W: 1, H: 1, Version: 3, AltText: "renamed"}}}})
	got := mustRow(t, c, "a", "t")
	if got.X != 4 || got.AltText != "renamed" || got.Version != 3 {
		t.Errorf("row = %v, want x 4 held with the echo's name and version", got)
	}
}

// A source mints the row a placement writes into, so its answer names another
// id and no echo will ever name this one: the answer releases the hold.
func TestAnAnswerUnderAMintedIdReleasesTheHold(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "a"}, []*gridwellv1.Tile{
		{Id: "key:x", GridId: "a", Kind: rpc.KindText, W: 1, H: 1}})
	pl := c.Move("key:x", Placement{GridID: "a", X: 2, W: 1, H: 1})
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
	if pl := c.Move("t", Placement{GridID: "a", W: 1, H: 1}); pl != nil {
		t.Errorf("Move on an empty cache = %v, want nil", pl)
	}
	var pl *Holding
	pl.Landed(nil)
	pl.Failed()
	pl.Released()
}

// A content zoom press reads the zoom the cache shows, so the echo of an
// earlier press must not put an older zoom back while a later one is in
// flight: three quick presses are three steps, whenever the echoes land.
func TestAZoomHoldsUntilItsOwnEcho(t *testing.T) {
	row := func(z float64) *gridwellv1.Tile {
		return &gridwellv1.Tile{Id: "t", GridId: "a", Kind: rpc.KindURL, W: 1, H: 1, ContentZoom: z}
	}
	echo := func(c *Cache, z float64) {
		c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: row(z)}}})
	}
	zoomOf := func(c *Cache) float64 { return mustRow(t, c, "a", "t").GetContentZoom() }
	press := func(c *Cache) *Holding {
		z := zoomOf(c) * 1.1
		return c.Hold("t", WroteZoom, func(n *gridwellv1.Tile) { n.ContentZoom = z })
	}
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "a"}, []*gridwellv1.Tile{row(1)})
	first := press(c)
	press(c)
	first.Landed(row(1.1))
	echo(c, 1.1)
	press(c)
	if got := zoomOf(c); got < 1.33 || got > 1.332 {
		t.Fatalf("zoom after three presses = %v, want 1.331: the first press's echo put its zoom back", got)
	}
	second := 1.1
	second *= 1.1
	echo(c, second)
	echo(c, second*1.1)
	echo(c, 2)
	if got := zoomOf(c); got != 2 {
		t.Errorf("zoom = %v, want 2: the last press's echo did not release the hold", got)
	}

	// A write that did not land releases its hold and leaves the zoom shown,
	// for the outbox to send again or the refusal's re-read to reconcile.
	parked := press(c)
	parked.Released()
	if got := zoomOf(c); got != 2*1.1 {
		t.Errorf("zoom = %v, want 2.2: a release put the zoom back", got)
	}
	c.PutGrid(&gridwellv1.Grid{Id: "a"}, []*gridwellv1.Tile{row(2)})
	if got := zoomOf(c); got != 2 {
		t.Errorf("zoom = %v, want 2: a released hold outlived its write", got)
	}
}

// A local patch is not the node's word: it ends no hold, so a read that
// predates the held write still cannot put the old fact back, and a patch of
// a row read before the write does not put it back either.
func TestAPatchEndsNoHold(t *testing.T) {
	row := func() *gridwellv1.Tile {
		return &gridwellv1.Tile{Id: "t", GridId: "a", Kind: rpc.KindText, W: 1, H: 1, ContentZoom: 1}
	}
	read := func(c *Cache) { c.PutGrid(&gridwellv1.Grid{Id: "a"}, []*gridwellv1.Tile{row()}) }
	scroll := func(n *gridwellv1.Tile) { n.TextY = 40 }
	for _, tc := range []struct {
		name string
		hold func(c *Cache)
		want func(n *gridwellv1.Tile) bool
	}{
		{"content zoom", func(c *Cache) {
			c.Hold("t", WroteZoom, func(n *gridwellv1.Tile) { n.ContentZoom = 1.25 })
		}, func(n *gridwellv1.Tile) bool { return n.ContentZoom == 1.25 }},
		{"placement", func(c *Cache) {
			c.Move("t", Placement{GridID: "a", X: 5, W: 1, H: 1})
		}, func(n *gridwellv1.Tile) bool { return n.X == 5 }},
	} {
		for _, stale := range []bool{false, true} {
			c := New()
			read(c)
			before := mustRow(t, c, "a", "t")
			tc.hold(c)
			patched := mustRow(t, c, "a", "t")
			if stale {
				patched = before
			}
			c.PatchTile(patched, scroll)
			if got := mustRow(t, c, "a", "t"); !tc.want(got) || got.TextY != 40 {
				t.Errorf("%s, stale %v: after the patch row = %v, want the held fact and the scroll", tc.name, stale, got)
			}
			read(c)
			if got := mustRow(t, c, "a", "t"); !tc.want(got) {
				t.Errorf("%s, stale %v: a read that predates the write put it back: %v", tc.name, stale, got)
			}
		}
	}
}

// A delete is drawn at the drop: the row leaves its grid at once and stays
// out through every older row, until its own echo, a read after its answer,
// or its refusal, which puts it back.
func TestADeleteHoldsTheRowOutUntilItsVerdict(t *testing.T) {
	row := func(grid string, x int64) *gridwellv1.Tile {
		return &gridwellv1.Tile{Id: "t", GridId: grid, Kind: rpc.KindText, X: x, W: 1, H: 1, Version: 2}
	}
	changed := func(n *gridwellv1.Tile) func(*Cache) {
		return func(c *Cache) {
			c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: n}}})
		}
	}
	removed := func(c *Cache) {
		c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileRemoved{TileRemoved: &gridwellv1.TileRemoved{GridId: "a", TileId: "t"}}})
	}
	read := func(rows ...*gridwellv1.Tile) func(*Cache) {
		return func(c *Cache) { c.PutGrid(&gridwellv1.Grid{Id: "a"}, rows) }
	}
	type step func(*Cache, *Holding)
	do := func(f func(*Cache)) step { return func(c *Cache, _ *Holding) { f(c) } }
	landed := step(func(_ *Cache, pl *Holding) { pl.Landed(nil) })
	failed := step(func(_ *Cache, pl *Holding) { pl.Failed() })

	cases := []struct {
		name  string
		steps []step
		in    bool // the row is in grid a
		trash bool // the row is in the trash
	}{
		{name: "it leaves its grid at the drop"},
		{name: "an older echo in its grid cannot put it back", steps: []step{do(changed(row("a", 3)))}},
		{name: "a read from before the delete cannot put it back", steps: []step{do(read(row("a", 0)))}},
		{name: "the trash's echo files it in the trash", steps: []step{do(changed(row("trash", 0)))}, trash: true},
		{name: "a read after the answer is the node's word", steps: []step{landed, do(read(row("a", 0)))}, in: true},
		{name: "after its own echo the stream is the node's word", steps: []step{do(removed), do(changed(row("a", 4)))}, in: true},
		{name: "a refusal puts it back", steps: []step{failed}, in: true},
		{name: "a refusal after its own echo leaves it gone", steps: []step{do(removed), failed}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New()
			c.PutGrid(&gridwellv1.Grid{Id: "a"}, []*gridwellv1.Tile{row("a", 0)})
			c.PutGrid(&gridwellv1.Grid{Id: "trash"}, nil)
			pl := c.Remove("t")
			if pl == nil {
				t.Fatal("Remove of a cached row held nothing")
			}
			for _, s := range tc.steps {
				s(c, pl)
			}
			if g, _ := c.Grid("a"); (g.Tiles["t"] != nil) != tc.in {
				t.Errorf("row in its grid = %v, want %v", g.Tiles["t"] != nil, tc.in)
			}
			if g, _ := c.Grid("trash"); (g.Tiles["t"] != nil) != tc.trash {
				t.Errorf("row in the trash = %v, want %v", g.Tiles["t"] != nil, tc.trash)
			}
		})
	}
	if pl := New().Remove("t"); pl != nil {
		t.Errorf("Remove of an uncached row = %v, want nil", pl)
	}
}

// A rename is drawn at the commit: the row shows the new name through every
// older row until the rename's echo or a read after its answer, and a
// refusal puts the old name back.
func TestARenameHoldsTheNameUntilItsVerdict(t *testing.T) {
	row := func(name string, version int64) *gridwellv1.Tile {
		return &gridwellv1.Tile{Id: "t", GridId: "a", Kind: rpc.KindDoor, W: 1, H: 1, Version: version, AltText: name, Named: name != "old"}
	}
	echo := func(n *gridwellv1.Tile) func(*Cache) {
		return func(c *Cache) {
			c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: n}}})
		}
	}
	read := func(n *gridwellv1.Tile) func(*Cache) {
		return func(c *Cache) { c.PutGrid(&gridwellv1.Grid{Id: "a"}, []*gridwellv1.Tile{n}) }
	}
	type step func(*Cache, *Holding)
	do := func(f func(*Cache)) step { return func(c *Cache, _ *Holding) { f(c) } }
	landed := step(func(_ *Cache, pl *Holding) { pl.Landed(row("new", 3)) })
	failed := step(func(_ *Cache, pl *Holding) { pl.Failed() })
	cases := []struct {
		name  string
		steps []step
		want  string
	}{
		{name: "the name shows at the commit", want: "new"},
		{name: "an older echo cannot put the old name back", steps: []step{do(echo(row("old", 2)))}, want: "new"},
		{name: "a read from before the rename cannot either", steps: []step{do(read(row("old", 2)))}, want: "new"},
		{name: "its echo releases it, so a later name shows", steps: []step{do(echo(row("new", 3))), do(echo(row("later", 4)))}, want: "later"},
		{name: "a read after the answer is the node's word", steps: []step{landed, do(read(row("later", 4)))}, want: "later"},
		{name: "a refusal puts the old name back", steps: []step{failed}, want: "old"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New()
			c.PutGrid(&gridwellv1.Grid{Id: "a"}, []*gridwellv1.Tile{row("old", 2)})
			pl := c.Hold("t", WroteName, func(n *gridwellv1.Tile) { n.AltText, n.Named = "new", true })
			for _, s := range tc.steps {
				s(c, pl)
			}
			got := mustRow(t, c, "a", "t")
			if got.AltText != tc.want || got.Named != (tc.want != "old") {
				t.Errorf("name = %q named %v, want %q", got.AltText, got.Named, tc.want)
			}
		})
	}
}
