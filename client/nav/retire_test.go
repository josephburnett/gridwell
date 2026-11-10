package nav

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/scratch"
)

// Ending an ephemeral visit is one gesture with two writes: the workspace tile
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
		// An ephemeral visit closes like a placed tile: its address,
		// title and preview are written before the retire.
		if cs := only(t, plan, EffCloseStream); !cs.Freeze {
			t.Fatalf("closed an ephemeral visit without its freeze")
		}
		landed := baseWorld(gridPane("pane1", "g1"))
		d := only(t, m.Resume(a.Token, Result{OK: true}, landed), EffDeleteEphemeral)
		if d.Tile.GetId() != "r1" || d.Tile.GetGridId() != "sg" {
			t.Fatalf("deleted %+v, want the visit", d)
		}
	})

	t.Run("an ephemeral shell freezes like a placed one, then deletes", func(t *testing.T) {
		m := New()
		w := ephemeralWorld(contentPane("pane1", "r1"))
		w.Leave.DescendedTile.Kind = rpc.KindShell
		plan := m.Do(ascendGesture("pane1", 1, false), w)
		if cs := only(t, plan, EffCloseStream); cs.Streams != StreamShell || !cs.Freeze {
			t.Fatalf("closed %+v, want the shell stream with its freeze", cs)
		}
		a := flushAwait(t, plan, EffInstallPlace)
		if d := only(t, m.Resume(a.Token, Result{OK: true}, baseWorld(gridPane("pane1", "g1"))), EffDeleteEphemeral); d.Tile.GetId() != "r1" {
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
		if d := only(t, m.Resume(a.Token, Result{OK: true}, landed), EffDeleteEphemeral); d.Tile.GetId() != "r1" {
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
		if d := only(t, m.Resume(a.Token, Result{OK: true}, baseWorld()), EffDeleteEphemeral); d.Tile.GetId() != "r1" {
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

}

// A template's ephemeral copy can be a door or a workspace tile (Joe
// 2026-11-09 templates): the door sits on the scratch grid, no grid of the
// pane's, so its frame carries its grid as a link's does; leaving that frame,
// or the workspace tile's level, retires the copy as a content visit is.

func ephemeralDoor() *gridwellv1.Tile {
	return &gridwellv1.Tile{Id: "d1", Kind: rpc.KindDoor, GridId: "sg", W: 1, H: 1, ChildGridId: "dc"}
}

func TestEphemeralDoorFrameCarriesItsGrid(t *testing.T) {
	w := ephemeralWorld(gridPane("pane1", "g1"))
	w.Door = &DoorWorld{}
	tr := only(t, New().Do(descendGesture("pane1", ephemeralDoor()), w), EffStartTransition)
	if child := tr.Segments[1].Place; child.GridID != "dc" || child.Door != "d1" {
		t.Fatalf("frame = %+v, want the door's grid carried", child.Frame)
	}
}

func doorVisitPane(id string) PaneView {
	p := gridPane(id, "g1")
	p.Stack.Push(pane.Frame{GridID: "dc", Door: "d1"})
	p.GridID = "dc"
	return p
}

func TestRetireEphemeralDoorOnAscent(t *testing.T) {
	t.Run("leaving the door's frame retires the copy after the layout flush", func(t *testing.T) {
		m := New()
		w := ephemeralWorld(doorVisitPane("pane1"))
		w.Leave = &LeaveWorld{DoorRow: ephemeralDoor()}
		plan := m.Do(ascendGesture("pane1", 1, false), w)
		a := flushAwait(t, plan, EffInstallPlace)
		d := only(t, m.Resume(a.Token, Result{OK: true}, baseWorld(gridPane("pane1", "g1"))), EffDeleteEphemeral)
		if d.Tile.GetId() != "d1" {
			t.Fatalf("deleted %+v, want the door copy", d.Tile)
		}
	})
	t.Run("a split still inside the door keeps it", func(t *testing.T) {
		m := New()
		w := ephemeralWorld(doorVisitPane("pane1"))
		w.Leave = &LeaveWorld{DoorRow: ephemeralDoor()}
		plan := m.Do(ascendGesture("pane1", 1, false), w)
		a := flushAwait(t, plan, EffInstallPlace)
		still := baseWorld(gridPane("pane1", "g1"), doorVisitPane("pane2"))
		if i := indexOf(m.Resume(a.Token, Result{OK: true}, still), EffDeleteEphemeral); i >= 0 {
			t.Fatal("deleted a door another pane is inside")
		}
	})
	t.Run("a placed door is not retired", func(t *testing.T) {
		w := ephemeralWorld(doorVisitPane("pane1"))
		w.Leave = &LeaveWorld{DoorRow: doorRow()}
		plan := New().Do(ascendGesture("pane1", 1, false), w)
		if indexOf(plan, EffAwait) >= 0 {
			t.Fatalf("a placed door's ascent awaits a retire: %v", kinds(plan))
		}
	})
}

func TestRetireEphemeralWorkspaceOnLeave(t *testing.T) {
	m := New()
	w := levelWorld(true)
	w.LevelRow = &gridwellv1.Tile{Id: "pt1", Kind: rpc.KindWorkspace, GridId: "sg"}
	w.LevelScratch = scratch.Grid{Cached: true, ScratchGridID: "sg"}
	plan := m.Do(leaveGesture(1), w)
	a := flushAwait(t, plan, EffPopLevel)
	if d := only(t, m.Resume(a.Token, Result{OK: true}, baseWorld(gridPane("pane1", "g1"))), EffDeleteEphemeral); d.Tile.GetId() != "pt1" {
		t.Fatalf("deleted %+v, want the workspace copy", d.Tile)
	}
	placed := levelWorld(true)
	placed.LevelRow = &gridwellv1.Tile{Id: "pt1", Kind: rpc.KindWorkspace, GridId: "g1"}
	placed.LevelScratch = w.LevelScratch
	if indexOf(New().Do(leaveGesture(1), placed), EffAwait) >= 0 {
		t.Fatal("a placed workspace tile's leave awaits a retire")
	}
}
