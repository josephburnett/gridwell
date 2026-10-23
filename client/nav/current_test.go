package nav

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
)

// The node tells this client every change to a grid it holds, so a plan asks
// for a grid or a row only when the cache does not hold it current: never
// held, or held from a source that is not answering (World.Current).

func currentOf(ids ...string) map[string]bool {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func gridFetches(p Plan) int {
	n := 0
	for _, e := range p.Effects {
		if e.Kind == EffFetchGrid {
			n++
		}
	}
	return n
}

func TestAPlanAsksForAGridOnlyWhenItIsNotCurrent(t *testing.T) {
	door := &gridwellv1.Tile{Id: "w1", Kind: rpc.KindDoor, GridId: "g1",
		X: 2, Y: 3, W: 4, H: 4, ChildGridId: "g2"}
	text := &gridwellv1.Tile{Id: "t1", Kind: rpc.KindText, GridId: "g1", X: 0, Y: 0, W: 3, H: 2}

	cases := []struct {
		name string
		grid string // the grid the plan would ask for
		plan func(t *testing.T, current map[string]bool) Plan
	}{{
		name: "descending into a door",
		grid: "g2",
		plan: func(t *testing.T, current map[string]bool) Plan {
			w := baseWorld(gridPane("pane1", "g1"))
			w.Door = &DoorWorld{}
			w.Current = current
			return New().Do(descendGesture("pane1", door), w)
		},
	}, {
		name: "descending into a read-only text tile",
		grid: "g1",
		plan: func(t *testing.T, current map[string]bool) Plan {
			w := baseWorld(gridPane("pane1", "g1"))
			w.Door = &DoorWorld{ReadOnly: true}
			w.Current = current
			return New().Do(descendGesture("pane1", text), w)
		},
	}, {
		name: "an instant ascent out of a child grid",
		grid: "g1",
		plan: func(t *testing.T, current map[string]bool) Plan {
			w := baseWorld(doorPane("pane1"))
			w.Leave = &LeaveWorld{DoorGridID: "g1", DoorGridCached: true,
				DoorTile: doorRow(), LandingGridID: "g1"}
			w.Current = current
			return New().Do(ascendGesture("pane1", 1, false), w)
		},
	}, {
		name: "an animated ascent's landing",
		grid: "g1",
		plan: func(t *testing.T, current map[string]bool) Plan {
			w := baseWorld(doorPane("pane1"))
			w.Leave = &LeaveWorld{DoorGridID: "g1", DoorGridCached: true,
				DoorTile: doorRow(), LandingGridID: "g1"}
			w.Current = current
			m := New()
			tr := only(t, m.Do(ascendGesture("pane1", 1, true), w), EffStartTransition)
			landed := baseWorld(gridPane("pane1", "g1"))
			landed.Current = current
			return m.Land(tr.Land, landed)
		},
	}, {
		name: "an ascent landing back on content",
		grid: "g1",
		plan: func(t *testing.T, current map[string]bool) Plan {
			w := baseWorld(contentPane("pane1", "t1"))
			w.Leave = &LeaveWorld{DescendedTile: text, LandingGridID: "g1"}
			w.Current = current
			m := New()
			tr := only(t, m.Do(ascendGesture("pane1", 1, true), w), EffStartTransition)
			landed := baseWorld(contentPane("pane1", "t0"))
			landed.Current = current
			return m.Land(tr.Land, landed)
		},
	}, {
		name: "leaving a level that parked no tree",
		grid: "g1",
		plan: func(t *testing.T, current map[string]bool) Plan {
			w := levelWorld(false)
			w.Current = current
			return New().Do(leaveGesture(1), w)
		},
	}, {
		name: "the post-reload re-centre on a workspace tile",
		grid: "g9",
		plan: func(t *testing.T, current map[string]bool) Plan {
			m := New()
			plan := m.Do(leaveGesture(1), levelWorld(false))
			after := baseWorld(gridPane("pane1", "g1"))
			aw := only(t, m.Do(*plan.Next, after), EffAwait)
			after.Current = current
			row := &gridwellv1.Tile{Id: "pt1", GridId: "g9", X: 2, Y: 3, W: 4, H: 4}
			return m.Resume(aw.Token, Result{OK: true, Tile: row}, after)
		},
	}, {
		name: "a healed restore re-anchoring",
		grid: "elsewhere",
		plan: func(t *testing.T, current map[string]bool) Plan {
			m := New()
			tok := healAwait(t, m, urlRowIn("elsewhere"))
			w := baseWorld(engagedPane("pane1"))
			w.Current = current
			return m.Resume(tok, Result{OK: true, Doors: []*gridwellv1.Tile{
				{Id: "w9", GridId: "root9"}}}, w)
		},
	}, {
		name: "a restore onto a grid it did not have to read",
		grid: "p1/3",
		plan: func(t *testing.T, current map[string]bool) Plan {
			w := restoreWorld(map[string]map[string]RestoreTile{
				"p1/1": {"p1/10": doorIn("p1/2")},
				"p1/2": {"p1/20": doorIn("p1/3")},
				"p1/3": {},
			})
			w.Current = current
			return New().Do(bootGesture("/10/20"), w)
		},
	}, {
		name: "engaging a freshly installed tree's leaves",
		grid: "g1",
		plan: func(t *testing.T, current map[string]bool) Plan {
			w := baseWorld(gridPane("pane1", "g1"))
			w.Current = current
			return New().Do(Gesture{Kind: GestureEngageLeaves}, w)
		},
	}}

	for _, c := range cases {
		t.Run(c.name+": held current, nothing asked", func(t *testing.T) {
			if n := gridFetches(c.plan(t, currentOf(c.grid))); n != 0 {
				t.Fatalf("asked for %d grids the cache holds current", n)
			}
		})
		t.Run(c.name+": not held current, asked", func(t *testing.T) {
			if n := gridFetches(c.plan(t, currentOf("unrelated"))); n == 0 {
				t.Fatalf("asked for nothing the cache does not hold current")
			}
		})
	}
}

func TestEngageLeavesReEngagesEveryContentLeaf(t *testing.T) {
	w := baseWorld(contentPane("pane1", "t1"), gridPane("pane2", "g2"))
	w.Current = currentOf("g1")
	plan := New().Do(Gesture{Kind: GestureEngageLeaves}, w)

	if f := only(t, plan, EffFetchGrid); f.PaneID != "pane2" {
		t.Fatalf("fetched %+v, want only the leaf whose grid is not current", f)
	}
	if e := only(t, plan, EffReEngage); e.PaneID != "pane1" || e.TileID != "t1" {
		t.Fatalf("re-engaged %+v, want the content leaf", e)
	}
}

func TestReEngageReadsTheCachedRowWhenItIsCurrent(t *testing.T) {
	engaging := func(row *gridwellv1.Tile, current map[string]bool) World {
		w := baseWorld(engagedPane("pane1"))
		w.Engage = &EngageWorld{Tile: row}
		w.Current = current
		return w
	}
	asks := func(p Plan) bool {
		for _, e := range p.Effects {
			if e.Kind == EffAwait && e.Request.Kind == RequestGetTile {
				return true
			}
		}
		return false
	}

	cases := []struct {
		name  string
		world World
		ask   bool
		want  []EffectKind
	}{{
		name:  "the row is held current: go live with no read",
		world: engaging(urlRowIn("g1"), currentOf("g1")),
		want:  []EffectKind{EffOpenStream},
	}, {
		name:  "the held row moved: locate it with no read",
		world: engaging(urlRowIn("elsewhere"), currentOf("elsewhere")),
		want:  []EffectKind{EffAwait},
	}, {
		name:  "the row's source is not answering: read it",
		world: engaging(urlRowIn("g1"), currentOf()),
		ask:   true,
	}, {
		name:  "no held grid has the row: read it",
		world: engaging(nil, currentOf("g1")),
		ask:   true,
	}, {
		name: "the pane left the tile: nothing",
		world: func() World {
			w := engaging(urlRowIn("g1"), currentOf("g1"))
			w.Panes = []PaneView{gridPane("pane1", "g1")}
			return w
		}(),
		want: nil,
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := New().Do(reEngageGesture("pane1", "r1"), c.world)
			if asks(plan) != c.ask {
				t.Fatalf("GetTile asked = %v, want %v (effects %v)", asks(plan), c.ask, kinds(plan))
			}
			if !c.ask && !sameKinds(kinds(plan), c.want) {
				t.Fatalf("effects = %v, want %v", kinds(plan), c.want)
			}
		})
	}
}

// Across the seam: a source that lists but cannot watch is not dark, yet no
// event carries its changes, so a descent onto its held grid still reads it.
func TestADescentOntoAGridWithLiveUpdatesOffReadsIt(t *testing.T) {
	c := cache.New()
	c.PutGrid(&gridwellv1.Grid{Id: "pl1/2"}, nil)
	c.NoteStream(true)
	door := &gridwellv1.Tile{Id: "pl1/w", Kind: rpc.KindDoor, GridId: "g1",
		X: 2, Y: 3, W: 4, H: 4, ChildGridId: "pl1/2"}
	descend := func() Plan {
		w := baseWorld(gridPane("pane1", "g1"))
		w.Door = &DoorWorld{}
		w.Current = c.Current()
		return New().Do(descendGesture("pane1", door), w)
	}

	if n := gridFetches(descend()); n != 0 {
		t.Fatalf("asked for %d grids while the source's changes are told", n)
	}
	c.NoteLiveUpdates("pl1", false)
	if f := only(t, descend(), EffFetchGrid); f.GridID != "pl1/2" {
		t.Fatalf("fetched %+v, want the grid nothing tells this client about", f)
	}
	c.NoteLiveUpdates("pl1", true)
	if n := gridFetches(descend()); n != 0 {
		t.Fatalf("asked for %d grids once live updates came back", n)
	}
}

// A workspace tile's row comes from the cache when its grid is current: a layout
// write claims its version, so a stale row's write is refused rather than
// overwriting, and asking again would be asking what the stream told.
func TestEnteringALevelReadsTheCachedRowWhenCurrent(t *testing.T) {
	row := arranged(workspace())
	row.Version = 4
	enter := func(current map[string]bool) Plan {
		w := baseWorld(gridPane("pane1", "g1"))
		w.Current = current
		w.Level = &LevelWorld{Tile: row}
		return New().Do(enterGesture("pane1", row), w)
	}
	for _, e := range enter(currentOf("g1")).Effects {
		if e.Kind == EffAwait && e.Request.Kind == RequestGetTile {
			t.Fatal("the row was asked for though its grid is current")
		}
	}
	if e := only(t, enter(currentOf("g1")), EffAwait); e.Request.Kind != RequestReadLayout {
		t.Fatalf("await = %+v, want the layout read straight off the cached row", e.Request)
	}
	asked := false
	for _, e := range enter(nil).Effects {
		asked = asked || (e.Kind == EffAwait && e.Request.Kind == RequestGetTile)
	}
	if !asked {
		t.Fatal("a row whose grid is not current was not asked for")
	}
}

// Across the seam: a body held while this client's stream is down may be
// behind, because nothing told it the writes made meanwhile, so a descent
// reads it again and shows the held bytes only until the answer lands.
func TestADescentReadsAHeldBodyWhileTheStreamIsDown(t *testing.T) {
	c := cache.New()
	text := &gridwellv1.Tile{Id: "t1", Kind: rpc.KindText, GridId: "g1", X: 0, Y: 0, W: 3, H: 2}
	c.PutGrid(&gridwellv1.Grid{Id: "g1"}, []*gridwellv1.Tile{text})
	c.PutFetchedContent("t1", []byte("held"), rpc.ContentBasis{}, c.AskContent("t1"))
	c.NoteStream(true)
	descend := func() []EffectKind {
		w := baseWorld(gridPane("pane1", "g1"))
		w.Door = &DoorWorld{}
		w.Current = c.Current()
		var got []EffectKind
		for _, e := range New().Do(descendGesture("pane1", text), w).Effects {
			if e.Kind == EffFetchTileContent || e.Kind == EffReadTileContent {
				got = append(got, e.Kind)
			}
		}
		return got
	}

	if got := descend(); !sameKinds(got, []EffectKind{EffFetchTileContent}) {
		t.Fatalf("with the stream open the body is %v, want only warmed", got)
	}
	c.NoteStream(false)
	if got := descend(); !sameKinds(got, []EffectKind{EffReadTileContent}) {
		t.Fatalf("with the stream down the body is %v, want read again", got)
	}
}
