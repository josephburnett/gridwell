package nav

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/scratch"
)

// Ending an ephemeral visit is one gesture with two writes: the pane-tile
// layout that stops naming the visit, and the delete. The delete waits on the
// layout write's verdict, so the node never holds a layout naming a deleted
// tile.

func ephemeralRow() *gridwellv1.Tile {
	return &gridwellv1.Tile{Id: "r1", Kind: rpc.KindURL, GridId: "sg", X: 2, Y: 2, W: 3, H: 2}
}

func ephemeralWorld(panes ...PaneView) World {
	w := baseWorld(panes...)
	for i := range w.Panes {
		w.Panes[i].Scratch = scratch.Grid{Cached: true, ScratchGridID: "sg"}
	}
	w.Leave = &LeaveWorld{DescendedTile: ephemeralRow()}
	return w
}

func indexOf(p Plan, k EffectKind) int {
	for i, e := range p.Effects {
		if e.Kind == k {
			return i
		}
	}
	return -1
}

// flushAwait is the plan's one layout-flush await, after the pane's place
// stopped naming the visit.
func flushAwait(t *testing.T, p Plan, after EffectKind) Effect {
	t.Helper()
	if i := indexOf(p, EffDeleteEphemeral); i >= 0 {
		t.Fatalf("deleted beside the layout write, not after its verdict: %v", kinds(p))
	}
	a := only(t, p, EffAwait)
	if a.Request.Kind != RequestFlushLayout {
		t.Fatalf("awaited %+v, want the layout flush", a.Request)
	}
	if indexOf(p, EffAwait) < indexOf(p, after) {
		t.Fatalf("flushed the layout before the place changed: %v", kinds(p))
	}
	return a
}

func TestRetireVisitAfterTheLayoutIsHeld(t *testing.T) {
	t.Run("an instant ascent flushes the landed layout, then deletes", func(t *testing.T) {
		m := New()
		w := ephemeralWorld(contentPane("pane1", "r1"))
		plan := m.Do(ascendGesture("pane1", 1, false), w)
		a := flushAwait(t, plan, EffInstallPlace)
		if cs := only(t, plan, EffCloseStream); cs.Freeze {
			t.Fatalf("froze a row about to die")
		}
		landed := baseWorld(gridPane("pane1", "g1"))
		d := only(t, m.Resume(a.Token, Result{OK: true}, landed), EffDeleteEphemeral)
		if d.TileID != "r1" || d.GridID != "sg" {
			t.Fatalf("deleted %+v, want the visit", d)
		}
	})

	t.Run("an animated ascent flushes on landing", func(t *testing.T) {
		m := New()
		plan := m.Do(ascendGesture("pane1", 1, true), ephemeralWorld(contentPane("pane1", "r1")))
		if indexOf(plan, EffDeleteEphemeral) >= 0 || indexOf(plan, EffAwait) >= 0 {
			t.Fatalf("retired the visit while the pane still shows it: %v", kinds(plan))
		}
		landed := baseWorld(gridPane("pane1", "g1"))
		land := m.Land(only(t, plan, EffStartTransition).Land, landed)
		a := flushAwait(t, land, EffFetchGrid)
		if d := only(t, m.Resume(a.Token, Result{OK: true}, landed), EffDeleteEphemeral); d.TileID != "r1" {
			t.Fatalf("deleted %+v, want the visit", d)
		}
	})

	t.Run("a layout the node did not take keeps the visit", func(t *testing.T) {
		m := New()
		a := flushAwait(t, m.Do(ascendGesture("pane1", 1, false),
			ephemeralWorld(contentPane("pane1", "r1"))), EffInstallPlace)
		if plan := m.Resume(a.Token, Result{}, baseWorld(gridPane("pane1", "g1"))); indexOf(plan, EffDeleteEphemeral) >= 0 {
			t.Fatalf("deleted a tile the stored layout still names: %v", kinds(plan))
		}
	})

	t.Run("a pane showing the visit again by the verdict keeps it", func(t *testing.T) {
		m := New()
		a := flushAwait(t, m.Do(ascendGesture("pane1", 1, false),
			ephemeralWorld(contentPane("pane1", "r1"))), EffInstallPlace)
		back := baseWorld(contentPane("pane1", "r1"))
		if plan := m.Resume(a.Token, Result{OK: true}, back); indexOf(plan, EffDeleteEphemeral) >= 0 {
			t.Fatalf("deleted a visit a pane shows: %v", kinds(plan))
		}
	})

	t.Run("the ascending pane closing before the verdict still deletes", func(t *testing.T) {
		m := New()
		a := flushAwait(t, m.Do(ascendGesture("pane1", 1, false),
			ephemeralWorld(contentPane("pane1", "r1"))), EffInstallPlace)
		m.Forget("pane1")
		if d := only(t, m.Resume(a.Token, Result{OK: true}, baseWorld()), EffDeleteEphemeral); d.TileID != "r1" {
			t.Fatalf("deleted %+v, want the visit", d)
		}
	})

	t.Run("a split sibling still showing the visit plans no retire", func(t *testing.T) {
		plan := New().Do(ascendGesture("pane1", 1, false),
			ephemeralWorld(contentPane("pane1", "r1"), contentPane("pane2", "r1")))
		if indexOf(plan, EffAwait) >= 0 || indexOf(plan, EffDeleteEphemeral) >= 0 {
			t.Fatalf("retired a visit a sibling shows: %v", kinds(plan))
		}
	})

	t.Run("a promote flushes after the relocate, then deletes", func(t *testing.T) {
		m := New()
		created := &gridwellv1.Tile{Id: "n1", Kind: rpc.KindURL, GridId: "g2", X: 4, Y: 5, W: 1, H: 1}
		w := baseWorld(visitPane("pane1"), gridPane("pane2", "g2"))
		w.Promote = &PromoteWorld{OldTile: &gridwellv1.Tile{Id: "v1", Kind: rpc.KindURL, GridId: "sg"}}
		a := flushAwait(t, m.Do(promoteGesture(created), w), EffRelocatePane)
		after := baseWorld(contentPane("pane1", "n1"), gridPane("pane2", "g2"))
		if d := only(t, m.Resume(a.Token, Result{OK: true}, after), EffDeleteEphemeral); d.TileID != "v1" || d.GridID != "sg" {
			t.Fatalf("deleted %+v, want the visit", d)
		}
	})
}
