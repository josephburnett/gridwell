package cache

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"slices"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// The node as a client sees it. Health uuids gain one segment per hop exactly
// as ids do, so every source below names a chain its grids' ids start with.
const (
	node    = "n1abcde"
	fsPl    = "fs9xyzw"
	conn    = node + "/laptop"
	farHome = conn + "/far9xyz"
	farPl   = conn + "/rp9plug"
)

// seedSources caches one grid per source, ids shaped as the wire qualifies
// them.
func seedSources(t *testing.T) *Cache {
	t.Helper()
	c := New()
	for _, id := range []string{
		node + "/7",    // this node's home store
		fsPl + "/1",    // a local plugin
		farHome + "/1", // the far node's home, through the connection
		farPl + "/4",   // a far node's plugin, one hop deeper still
		fsPl + "/12",   // a second grid of the local plugin
	} {
		c.PutGrid(&gridwellv1.Grid{Id: id}, nil)
	}
	c.NoteStream(true)
	return c
}

// One source flapping is news about that source's grids and nothing else.
func TestAFlapResyncsOnlyTheGridsItsSourceServes(t *testing.T) {
	c := seedSources(t)

	got := c.ResyncSet(fsPl)
	want := []string{fsPl + "/1", fsPl + "/12"}
	if !slices.Equal(got, want) {
		t.Fatalf("ResyncSet(%q) = %v, want %v", fsPl, got, want)
	}
	for _, id := range got {
		if !ServedBy(id, fsPl) {
			t.Errorf("%s is in the plugin's resync set but not served by it", id)
		}
	}
	// The other sources' grids are untouched: nothing about them changed.
	for _, other := range []string{node + "/7", farHome + "/1", farPl + "/4"} {
		if slices.Contains(got, other) {
			t.Errorf("one plugin's flap resynced %s, a grid it does not serve", other)
		}
	}
}

// A source is a chain, so a connection's flap owns everything reachable only
// through it. String equality on the owning namespace would resync neither.
func TestAConnectionsFlapOwnsEveryGridChainedThroughIt(t *testing.T) {
	c := seedSources(t)

	got := c.ResyncSet(conn)
	want := []string{farHome + "/1", farPl + "/4"}
	if !slices.Equal(got, want) {
		t.Fatalf("ResyncSet(%q) = %v, want %v", conn, got, want)
	}

	// One hop deeper: the far node's home flapping is news about its grids
	// only.
	got = c.ResyncSet(farHome)
	want = []string{farHome + "/1"}
	if !slices.Equal(got, want) {
		t.Fatalf("ResyncSet(%q) = %v, want %v", farHome, got, want)
	}
}

// A stream gap has no cursor and names no source, so the whole cache is the
// answer.
func TestEverySourceIsTheWholeCache(t *testing.T) {
	c := seedSources(t)

	got := c.ResyncSet(EverySource)
	if len(got) != 5 {
		t.Fatalf("ResyncSet(EverySource) = %v, want all 5 cached grids", got)
	}
	if !slices.Equal(got, c.KnownGridIDs()) {
		t.Errorf("ResyncSet(EverySource) = %v, KnownGridIDs = %v; they are one fact", got, c.KnownGridIDs())
	}
}

// The boundaries a prefix test can get wrong. It answers for tile ids too,
// because the latches and in-flight claims a flap clears are keyed by tile id.
func TestServedBy(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		source string
		want   bool
	}{
		{"a local plugin's grid", fsPl + "/1", fsPl, true},
		{"a local plugin's key-form tile", fsPl + "/" + rpc.KeyTileID("/home/joe"), fsPl, true},
		{"another plugin's grid", "zz9zzzz/1", fsPl, false},
		{"a segment that merely starts the same", fsPl + "x/1", fsPl, false},
		{"the source's own name is not a grid it serves", fsPl, fsPl, false},
		{"the far node's home through the connection", farHome + "/1", conn, true},
		{"the far node's plugin through the connection", farPl + "/4", conn, true},
		{"the connection does not serve this node's home", node + "/7", conn, false},
		{"this node's home serves its own grid", node + "/7", node, true},
		{"a bare unqualified id belongs to no chain", "7", fsPl, false},
		{"every source takes a bare id too", "7", EverySource, true},
		{"every source takes a chained id", farPl + "/4", EverySource, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ServedBy(tc.id, tc.source); got != tc.want {
				t.Errorf("ServedBy(%q, %q) = %v, want %v", tc.id, tc.source, got, tc.want)
			}
		})
	}
}

// Reaches keys on a source name rather than on something a source serves, as
// the + menu's per-node context does. ServedBy would say no to a node's own
// menu and equality would say no to a node behind a connection; both halves
// are how a menu read hung on a dead link gets cancelled.
func TestReachesCoversTheSourceItselfAndEveryNodeBehindIt(t *testing.T) {
	cases := []struct {
		name       string
		ns, source string
		want       bool
	}{
		{"a node's own menu is that node's fact", conn, conn, true},
		{"the far node behind the connection", farHome, conn, true},
		{"a far plugin behind the connection", farPl, conn, true},
		{"the connection is not behind the far node", conn, farHome, false},
		{"a different connection", node + "/other", conn, false},
		{"a segment that merely starts the same", conn + "x", conn, false},
		{"this node's own menu under a connection's flap", node, conn, false},
		{"every source reaches every namespace", conn, EverySource, true},
		{"every source reaches the local context", "", EverySource, true},
		{"no namespace is behind a named source when it is empty", "", conn, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Reaches(tc.ns, tc.source); got != tc.want {
				t.Errorf("Reaches(%q, %q) = %v, want %v", tc.ns, tc.source, got, tc.want)
			}
		})
	}
	// ServedBy, asked about a source's own name, says no: a namespace is not
	// a thing it serves.
	if ServedBy(conn, conn) {
		t.Errorf("ServedBy(%q, %q) = true; a namespace is not something it serves", conn, conn)
	}
}

// A presenter's flap is news about every grid holding a tile it presents,
// whoever serves the grid: the tile's verdict, broken or presented, is the
// presenter's health. Through a connection the presenter is named one hop
// deeper, as its health is.
func TestAPresentersFlapResyncsTheGridsHoldingItsTiles(t *testing.T) {
	c := seedSources(t)
	c.PutGrid(&gridwellv1.Grid{Id: node + "/9"}, []*gridwellv1.Tile{{Id: node + "/10", GridId: node + "/9", PresentedBy: fsPl}})
	c.PutGrid(&gridwellv1.Grid{Id: farHome + "/2"}, []*gridwellv1.Tile{{Id: farHome + "/3", GridId: farHome + "/2", PresentedBy: farPl}})
	if got, want := c.ResyncSet(fsPl), []string{fsPl + "/1", fsPl + "/12", node + "/9"}; !slices.Equal(got, want) {
		t.Errorf("ResyncSet(%q) = %v, want %v", fsPl, got, want)
	}
	if got, want := c.ResyncSet(farPl), []string{farHome + "/2", farPl + "/4"}; !slices.Equal(got, want) {
		t.Errorf("ResyncSet(%q) = %v, want %v", farPl, got, want)
	}
}
