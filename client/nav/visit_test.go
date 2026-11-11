package nav

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/scratch"
)

// An ephemeral visit's crumb drags as the tile it is (CLAUDE.md, 2026-11-10):
// once its move lands, the visiting pane's place is re-rooted under the
// destination pane's, the frames from the row's up as they were, and nothing
// is retired, since the row is not ephemeral any more.

func visitMoved(row *gridwellv1.Tile) Gesture {
	return Gesture{Kind: GestureVisitMoved, PaneID: "pane1", DestPaneID: "pane2", Door: row}
}

func movedWorld(panes ...PaneView) World {
	w := baseWorld(panes...)
	for i := range w.Panes {
		w.Panes[i].Scratch = scratch.Grid{Cached: true, ScratchGridID: "sg"}
	}
	w.Current = map[string]bool{"g2": true, "dc": true}
	return w
}

func installed(t *testing.T, p Plan) pane.Stack {
	t.Helper()
	e := only(t, p, EffInstallPlace)
	if e.PaneID != "pane1" || e.Stack == nil {
		t.Fatalf("installed %+v, want the visiting pane's place", e)
	}
	return *e.Stack
}

func noRetire(t *testing.T, p Plan) {
	t.Helper()
	for _, k := range kinds(p) {
		if k == EffAwait || k == EffDeleteEphemeral {
			t.Fatalf("retired the moved row: %v", kinds(p))
		}
	}
}

func TestVisitMoved(t *testing.T) {
	dest := gridPane("pane2", "g2")
	dest.Stack.SetView(10, 20, 2)

	for _, kind := range []string{rpc.KindText, rpc.KindURL, rpc.KindShell} {
		t.Run("a "+kind+" visit", func(t *testing.T) {
			p := contentPane("pane1", "v1")
			p.Stack.TextScrollY = 40
			row := &gridwellv1.Tile{Id: "v1", Kind: kind, GridId: "g2", X: 4, Y: 5, W: 1, H: 1}
			plan := New().Do(visitMoved(row), movedWorld(p, dest))
			// No close: the live surface is on the same row, so it stays,
			// and the re-engage keeps it (pane.TakeOver).
			want := []EffectKind{EffInstallPlace, EffScaleContent, EffRefreshOverlay,
				EffReEngage, EffScheduleURLUpdate}
			if !sameKinds(kinds(plan), want) {
				t.Fatalf("effects = %v, want %v", kinds(plan), want)
			}
			st := installed(t, plan)
			if st.Anchor() != "g2" || st.ContentID() != "v1" || st.TextScrollY != 40 {
				t.Fatalf("place = %+v, want the same content frame on the destination's grid", st.Frame)
			}
			if !centredOn(st.View, 4.5, 5.5) {
				t.Fatalf("view = %+v, want the descent onto the new footprint", st.View)
			}
			if !st.Pop() || !centredOn(st.View, 10, 20) {
				t.Fatalf("ascent lands on %+v, want where the destination stood", st.View)
			}
			if e := only(t, plan, EffReEngage); e.TileID != "v1" {
				t.Fatalf("re-engaged %+v, want the moved row", e)
			}
			noRetire(t, plan)
		})
	}

	t.Run("a door visit keeps its grid and framing", func(t *testing.T) {
		p := doorVisitPane("pane1")
		p.Stack.SetView(3, 4, 0.5)
		row := &gridwellv1.Tile{Id: "d1", Kind: rpc.KindDoor, GridId: "g2", X: 4, Y: 5, W: 1, H: 1, ChildGridId: "dc"}
		plan := New().Do(visitMoved(row), movedWorld(p, dest))
		if !sameKinds(kinds(plan), []EffectKind{EffInstallPlace, EffScheduleURLUpdate}) {
			t.Fatalf("effects = %v, want the re-root alone", kinds(plan))
		}
		st := installed(t, plan)
		if st.Frames()[0].GridID != "g2" || st.Depth() != 2 || st.Door != "d1" || st.GridID != "dc" || !centredOn(st.View, 3, 4) {
			t.Fatalf("place = %+v, want the door frame as it was on the destination's grid", st.Frame)
		}
		noRetire(t, plan)
	})

	t.Run("a door visit held at depth", func(t *testing.T) {
		p := doorVisitPane("pane1")
		p.Stack.Push(pane.Frame{Door: "s1"})
		p.Stack.SetView(7, 8, 1)
		row := &gridwellv1.Tile{Id: "d1", Kind: rpc.KindDoor, GridId: "g2", X: 4, Y: 5, W: 1, H: 1, ChildGridId: "dc"}
		st := installed(t, New().Do(visitMoved(row), movedWorld(p, dest)))
		if st.Depth() != 3 || st.Door != "s1" || !centredOn(st.View, 7, 8) {
			t.Fatalf("place = %+v, want the door inside the visit on top", st.Crumbs())
		}
	})

	t.Run("dropped through an open door", func(t *testing.T) {
		row := &gridwellv1.Tile{Id: "v1", Kind: rpc.KindText, GridId: "gk", X: 1, Y: 1, W: 1, H: 1}
		g := visitMoved(row)
		g.TileID = "k1"
		st := installed(t, New().Do(g, movedWorld(contentPane("pane1", "v1"), dest)))
		if st.Depth() != 3 || st.Path()[0] != "k1" || st.ContentID() != "v1" {
			t.Fatalf("place = %+v, want the open door under the visit", st.Crumbs())
		}
	})

	t.Run("a sibling pane on the visit keeps its place", func(t *testing.T) {
		row := &gridwellv1.Tile{Id: "v1", Kind: rpc.KindText, GridId: "g2", X: 4, Y: 5, W: 1, H: 1}
		plan := New().Do(visitMoved(row), movedWorld(contentPane("pane1", "v1"), dest, contentPane("pane3", "v1")))
		for _, e := range plan.Effects {
			if e.PaneID == "pane3" {
				t.Fatalf("touched the sibling: %+v", e)
			}
		}
		noRetire(t, plan)
	})

	t.Run("an origin pane that moved on leaves the row where it landed", func(t *testing.T) {
		row := &gridwellv1.Tile{Id: "v1", Kind: rpc.KindText, GridId: "g2", X: 4, Y: 5, W: 1, H: 1}
		if plan := New().Do(visitMoved(row), movedWorld(contentPane("pane1", "other"), dest)); len(plan.Effects) != 0 {
			t.Fatalf("re-rooted a pane that moved on: %v", kinds(plan))
		}
	})

	t.Run("a destination pane that closed plans nothing", func(t *testing.T) {
		row := &gridwellv1.Tile{Id: "v1", Kind: rpc.KindText, GridId: "g2", X: 4, Y: 5, W: 1, H: 1}
		if plan := New().Do(visitMoved(row), movedWorld(contentPane("pane1", "v1"))); len(plan.Effects) != 0 {
			t.Fatalf("re-rooted onto a pane that is gone: %v", kinds(plan))
		}
	})
}

// A sibling pane left on the moved row ascends out of it as out of any tile:
// its row's grid is not the scratch grid, so nothing is retired.
func TestASiblingLeavingTheMovedRowRetiresNothing(t *testing.T) {
	w := ephemeralWorld(contentPane("pane1", "r1"))
	w.Leave = &LeaveWorld{DescendedTile: &gridwellv1.Tile{Id: "r1", Kind: rpc.KindText, GridId: "g2", W: 1, H: 1}}
	noRetire(t, New().Do(ascendGesture("pane1", 1, false), w))
}

// A workspace tile's visit moved off its boundary crumb (Joe, 2026-11-11):
// the panes are the level's own, so no place changes; the level follows the
// row to its grid, and leaving it lands where it did and retires nothing.
func TestWorkspaceVisitMoved(t *testing.T) {
	for _, outer := range []bool{true, false} {
		w := levelWorld(outer)
		w.LevelTop.GridID = "sg"
		w.LevelScratch = scratch.Grid{Cached: true, ScratchGridID: "sg"}
		row := &gridwellv1.Tile{Id: "pt1", Kind: rpc.KindWorkspace, GridId: "g2", X: 3, Y: 4, W: 1, H: 1}
		plan := New().Do(Gesture{Kind: GestureVisitMoved, PaneID: "pane1", DestPaneID: "pane1", Door: row}, w)
		if !sameKinds(kinds(plan), []EffectKind{EffLevelMoved}) {
			t.Fatalf("outer %v: the move planned %v, want only the level's grid", outer, kinds(plan))
		}
		if e := plan.Effects[0]; e.TileID != "pt1" || e.GridID != "g2" {
			t.Fatalf("outer %v: %+v, want pt1 onto g2", outer, e)
		}

		// The shim performs it; the leave reads the level and the row after.
		w.LevelTop.GridID, w.LevelRow = "g2", row
		leave := New().Do(leaveGesture(1), w)
		noRetire(t, leave)
		pop := only(t, leave, EffPopLevel)
		if outer && pop.GridID != "" || !outer && pop.GridID != "g2" {
			t.Fatalf("outer %v: the leave landed on %q", outer, pop.GridID)
		}
	}
}
