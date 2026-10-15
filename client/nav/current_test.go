package nav

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
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
	well := &gridwellv1.Tile{Id: "w1", Kind: rpc.KindWell, GridId: "g1",
		X: 2, Y: 3, W: 4, H: 4, ChildGridId: "g2"}
	text := &gridwellv1.Tile{Id: "t1", Kind: rpc.KindText, GridId: "g1", X: 0, Y: 0, W: 3, H: 2}

	cases := []struct {
		name string
		grid string // the grid the plan would ask for
		plan func(t *testing.T, current map[string]bool) Plan
	}{{
		name: "descending into a well",
		grid: "g2",
		plan: func(t *testing.T, current map[string]bool) Plan {
			w := baseWorld(gridPane("pane1", "g1"))
			w.Door = &DoorWorld{}
			w.Current = current
			return New().Do(descendGesture("pane1", well), w)
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
			w := baseWorld(wellPane("pane1"))
			w.Leave = &LeaveWorld{DoorGridID: "g1", DoorGridCached: true,
				DoorTile: doorRow(), LandingGridID: "g1"}
			w.Current = current
			return New().Do(ascendGesture("pane1", 1, false), w)
		},
	}, {
		name: "an animated ascent's landing",
		grid: "g1",
		plan: func(t *testing.T, current map[string]bool) Plan {
			w := baseWorld(wellPane("pane1"))
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
		name: "the post-reload re-centre on a pane tile",
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
			return m.Resume(tok, Result{OK: true, Wells: []*gridwellv1.Tile{
				{Id: "w9", GridId: "root9"}}}, w)
		},
	}, {
		name: "a restore onto a grid it did not have to read",
		grid: "p1/3",
		plan: func(t *testing.T, current map[string]bool) Plan {
			w := restoreWorld(map[string]map[string]RestoreTile{
				"p1/1": {"p1/10": wellIn("p1/2")},
				"p1/2": {"p1/20": wellIn("p1/3")},
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
