package nav

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/scratch"
)

// The go-live arm of the restore paths, and the stale-path heal that runs
// ahead of it. Both are token loops over reads the user can outrun, so every
// case here is really about which world the answer lands in.

func reEngageGesture(paneID, tileID string) Gesture {
	return Gesture{Kind: GestureReEngage, PaneID: paneID, TileID: tileID}
}

// engagedPane is a pane restored into url tile r1, whose grid g1 is cached
// and is not a scratch grid.
func engagedPane(id string) PaneView {
	p := contentPane(id, "r1")
	p.Scratch = scratch.Grid{Cached: true, ScratchGridID: "sg"}
	return p
}

func urlRowIn(gridID string) *rpc.Tile {
	return &rpc.Tile{ID: "r1", Kind: rpc.KindURL, GridID: gridID,
		X: 2, Y: 2, W: 3, H: 2, URLString: "https://example.test/"}
}

func TestReEngageReadsTheRowFirst(t *testing.T) {
	m := New()
	w := baseWorld(engagedPane("pane1"))
	plan := m.Do(reEngageGesture("pane1", "r1"), w)

	e := only(t, plan, EffAwait)
	if e.Request.Kind != RequestGetTile || e.Request.ID != "r1" {
		t.Fatalf("await = %+v, want GetTile r1", e.Request)
	}
	if len(plan.Effects) != 1 {
		t.Fatalf("effects = %v, want the read alone", kinds(plan))
	}
}

func TestReEngageAfterTheRowLands(t *testing.T) {
	row := urlRowIn("g1") // the pane's own grid: nothing to heal

	cases := []struct {
		name   string
		result Result
		world  func() World
		want   []EffectKind
	}{{
		name:   "the row resolves where the pane says it is: go live",
		result: Result{OK: true, Tile: row},
		world:  func() World { return baseWorld(engagedPane("pane1")) },
		want:   []EffectKind{EffOpenStream},
	}, {
		name:   "the reference no longer resolves: stay frozen",
		result: Result{},
		world:  func() World { return baseWorld(engagedPane("pane1")) },
		want:   nil,
	}, {
		name:   "the user moved on while the row was in flight",
		result: Result{OK: true, Tile: row},
		world:  func() World { return baseWorld(gridPane("pane1", "g1")) },
		want:   nil,
	}, {
		name:   "the pane closed while the row was in flight",
		result: Result{OK: true, Tile: row},
		world:  func() World { return baseWorld() },
		want:   nil,
	}, {
		name:   "an ephemeral visit is off its grid by design, not stale",
		result: Result{OK: true, Tile: urlRowIn("sg")},
		world:  func() World { return baseWorld(engagedPane("pane1")) },
		want:   []EffectKind{EffOpenStream},
	}, {
		name:   "a grid that cannot say ephemeral yet is not healed either",
		result: Result{OK: true, Tile: urlRowIn("elsewhere")},
		world: func() World {
			p := engagedPane("pane1")
			p.Scratch = scratch.Grid{}
			return baseWorld(p)
		},
		want: []EffectKind{EffOpenStream},
	}, {
		name:   "the tile moved: locate it before engaging",
		result: Result{OK: true, Tile: urlRowIn("elsewhere")},
		world:  func() World { return baseWorld(engagedPane("pane1")) },
		want:   []EffectKind{EffAwait},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			tok := only(t, m.Do(reEngageGesture("pane1", "r1"),
				baseWorld(engagedPane("pane1"))), EffAwait).Token
			plan := m.Resume(tok, c.result, c.world())
			if !sameKinds(kinds(plan), c.want) {
				t.Fatalf("effects = %v, want %v", kinds(plan), c.want)
			}
		})
	}
}

// healAwait drives a re-engagement to the point where the locate is
// outstanding, and returns its token.
func healAwait(t *testing.T, m *Machine, moved *rpc.Tile) Token {
	t.Helper()
	w := baseWorld(engagedPane("pane1"))
	tok := only(t, m.Do(reEngageGesture("pane1", "r1"), w), EffAwait).Token
	e := only(t, m.Resume(tok, Result{OK: true, Tile: moved}, w), EffAwait)
	if e.Request.Kind != RequestSearch || e.Request.Query != "id:r1" ||
		e.Request.Scope != "r1" || e.Request.Limit != 1 {
		t.Fatalf("locate = %+v, want the scoped id: search", e.Request)
	}
	return e.Token
}

func TestHealedPlans(t *testing.T) {
	moved := urlRowIn("elsewhere")

	t.Run("the locate answers: re-anchor, then engage", func(t *testing.T) {
		m := New()
		tok := healAwait(t, m, moved)
		plan := m.Resume(tok, Result{OK: true, Wells: []rpc.Tile{
			{ID: "w9", GridID: "root9"}, {ID: "w8", GridID: "mid"},
		}}, baseWorld(engagedPane("pane1")))

		want := []EffectKind{EffInstallPlace, EffFetchGrid, EffScheduleURLUpdate,
			EffOpenStream}
		if !sameKinds(kinds(plan), want) {
			t.Fatalf("effects = %v, want %v", kinds(plan), want)
		}
		st := *only(t, plan, EffInstallPlace).Stack
		if st.Anchor() != "root9" {
			t.Errorf("anchor = %q, want the owning root", st.Anchor())
		}
		if got := st.Path(); len(got) != 2 || got[0] != "w9" || got[1] != "w8" {
			t.Errorf("path = %v, want the fresh well chain", got)
		}
		if st.ContentID() != "r1" {
			t.Errorf("content = %q, want the tile still descended", st.ContentID())
		}
		// Centred on the tile in its new grid, so ascending out of the
		// descent lands looking at it.
		if st.Cx != 3.5 || st.Cy != 3 {
			t.Errorf("viewport = (%v, %v), want the tile's centre", st.Cx, st.Cy)
		}
		if g := only(t, plan, EffFetchGrid).GridID; g != "elsewhere" {
			t.Errorf("fetched %q, want the grid the tile now lives in", g)
		}
	})

	t.Run("a tile at a root has no well chain", func(t *testing.T) {
		m := New()
		tok := healAwait(t, m, moved)
		plan := m.Resume(tok, Result{OK: true}, baseWorld(engagedPane("pane1")))
		st := *only(t, plan, EffInstallPlace).Stack
		if st.Anchor() != "elsewhere" || len(st.Path()) != 0 {
			t.Fatalf("place = (%q, %v), want the tile's own grid", st.Anchor(), st.Path())
		}
	})

	t.Run("an unsearchable tile keeps its place and still engages", func(t *testing.T) {
		m := New()
		tok := healAwait(t, m, moved)
		plan := m.Resume(tok, Result{}, baseWorld(engagedPane("pane1")))
		if !sameKinds(kinds(plan), []EffectKind{EffOpenStream}) {
			t.Fatalf("effects = %v, want the engagement alone", kinds(plan))
		}
	})

	t.Run("the user moved on while the locate was in flight", func(t *testing.T) {
		m := New()
		tok := healAwait(t, m, moved)
		plan := m.Resume(tok, Result{OK: true, Wells: []rpc.Tile{{ID: "w9", GridID: "root9"}}},
			baseWorld(gridPane("pane1", "g1")))
		if len(plan.Effects) != 0 {
			t.Fatalf("re-anchored a pane that moved on: %v", kinds(plan))
		}
	})
}
