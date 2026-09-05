package nav

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
)

// The races. Every one of them is a continuation landing into a world that
// moved: the guard is evaluated against the fresh snapshot, so "the user went
// somewhere else" is one rule, spelled once, and every path gets it.

func TestPaneClosedBeforeTheDescentLands(t *testing.T) {
	text := rpc.Tile{ID: "t1", Kind: rpc.KindText, GridID: "g1", W: 3, H: 2}
	w := baseWorld(gridPane("pane1", "g1"))
	w.Door = &DoorWorld{}
	m := New()
	tr := only(t, m.Do(descendGesture("pane1", text), w), EffStartTransition)

	gone := baseWorld() // the pane was closed mid-flight
	if plan := m.Land(tr.Land, gone); len(plan.Effects) != 0 {
		t.Fatalf("landed onto a closed pane: %v", kinds(plan))
	}
	// The continuation is retired, not left to fire twice.
	if plan := m.Land(tr.Land, w); len(plan.Effects) != 0 {
		t.Fatalf("a retired continuation fired again: %v", kinds(plan))
	}
}

func TestPaneClosedBeforeTheAscentLands(t *testing.T) {
	w := baseWorld(wellPane("pane1"))
	w.Leave = &LeaveWorld{DoorGridID: "g1", DoorGridCached: true, DoorTile: doorRow()}
	m := New()
	tr := only(t, m.Do(ascendGesture("pane1", 1, true), w), EffStartTransition)

	if plan := m.Land(tr.Land, baseWorld()); len(plan.Effects) != 0 {
		t.Fatalf("landed onto a closed pane: %v", kinds(plan))
	}
}

func TestSecondDescentMidAnimationLandsTheFirst(t *testing.T) {
	well := rpc.Tile{ID: "w1", Kind: rpc.KindWell, GridID: "g1",
		X: 2, Y: 3, W: 4, H: 4, ChildGridID: "gc"}
	inner := rpc.Tile{ID: "w2", Kind: rpc.KindWell, GridID: "gc",
		X: 0, Y: 0, W: 2, H: 2, ChildGridID: "gcc"}
	m := New()

	w := baseWorld(gridPane("pane1", "g1"))
	w.Door = &DoorWorld{}
	m.Do(descendGesture("pane1", well), w)

	// The second descent arrives while the first is still animating: the plan
	// is the cancel alone, and the machine asks to be called again.
	mid := baseWorld(gridPane("pane1", "g1"))
	mid.Door = &DoorWorld{}
	mid.Animating = map[string]bool{"pane1": true}
	g := descendGesture("pane1", inner)
	first := m.Do(g, mid)
	if !sameKinds(kinds(first), []EffectKind{EffCancelTransition}) {
		t.Fatalf("effects = %v, want the cancel alone", kinds(first))
	}
	if first.Next == nil || first.Next.Door.ID != "w2" {
		t.Fatalf("no continuation gesture for the displaced descent: %+v", first.Next)
	}

	// The cancel landed the first descent, so the second's segments are
	// computed from the place it left — the child grid — not from the
	// outgoing animation's scratch viewport.
	landed := gridPane("pane1", "gc")
	landed.Stack = pane.NewStack("g1")
	landed.Stack.Push(pane.Frame{Door: "w1", Cx: 2, Cy: 2, Zoom: 0.5})
	landed.Cx, landed.Cy, landed.Zoom = 2, 2, 0.5
	after := baseWorld(landed)
	after.Door = &DoorWorld{}
	second := m.Do(*first.Next, after)
	if second.Next != nil {
		t.Fatalf("the descent did not settle")
	}
	tr := only(t, second, EffStartTransition)
	if tr.Segments[0].FromCx != 2 || tr.Segments[0].FromZoom != 0.5 {
		t.Fatalf("segments start at (%v, zoom %v), want the landed place",
			tr.Segments[0].FromCx, tr.Segments[0].FromZoom)
	}
	if tr.Segments[1].Place.Depth() != 3 {
		t.Fatalf("pushed onto depth %d, want the frame below the landed one",
			tr.Segments[1].Place.Depth())
	}
}

func TestSecondAscentMidAnimationLandsTheFirst(t *testing.T) {
	w := baseWorld(wellPane("pane1"))
	w.Leave = &LeaveWorld{DoorGridID: "g1", DoorGridCached: true, DoorTile: doorRow()}
	w.Animating = map[string]bool{"pane1": true}
	plan := New().Do(ascendGesture("pane1", 1, true), w)
	if !sameKinds(kinds(plan), []EffectKind{EffCancelTransition}) {
		t.Fatalf("effects = %v, want the cancel alone", kinds(plan))
	}
	if plan.Next == nil || plan.Next.N != 1 {
		t.Fatalf("no continuation gesture for the displaced ascent: %+v", plan.Next)
	}
}

func TestShellProbe(t *testing.T) {
	shell := rpc.Tile{ID: "s1", Kind: rpc.KindShell, GridID: "g1", W: 3, H: 2,
		PreviewBlobID: 7}

	probe := func(m *Machine) (Effect, World) {
		t.Helper()
		w := baseWorld(gridPane("pane1", "g1"))
		w.Door = &DoorWorld{}
		tr := only(t, m.Do(descendGesture("pane1", shell), w), EffStartTransition)
		land := m.Land(tr.Land, w)
		a := only(t, land, EffAwait)
		if a.Request.Kind != RequestProbeShell || a.Request.ID != "s1" {
			t.Fatalf("await = %+v, want a shell probe on the content id", a.Request)
		}
		// The pane is descended in the tile when the probe goes out.
		descended := baseWorld(contentPane("pane1", "s1"))
		return a, descended
	}

	t.Run("alive attaches", func(t *testing.T) {
		m := New()
		a, descended := probe(m)
		plan := m.Resume(a.Token, Result{OK: true, Alive: true}, descended)
		e := only(t, plan, EffOpenStream)
		if e.Stream != StreamShell || e.TileID != "s1" {
			t.Fatalf("opened %+v, want the shell on s1", e)
		}
	})

	t.Run("dead stays frozen", func(t *testing.T) {
		m := New()
		a, descended := probe(m)
		if plan := m.Resume(a.Token, Result{OK: true}, descended); len(plan.Effects) != 0 {
			t.Fatalf("a dead session opened anyway: %v", kinds(plan))
		}
	})

	t.Run("moved on mid probe", func(t *testing.T) {
		m := New()
		a, _ := probe(m)
		// The user descended somewhere else while the probe was in flight.
		elsewhere := baseWorld(contentPane("pane1", "other"))
		if plan := m.Resume(a.Token, Result{OK: true, Alive: true}, elsewhere); len(plan.Effects) != 0 {
			t.Fatalf("opened a shell in a pane that moved on: %v", kinds(plan))
		}
		// And the continuation is gone, so a late second answer does nothing.
		back := baseWorld(contentPane("pane1", "s1"))
		if plan := m.Resume(a.Token, Result{OK: true, Alive: true}, back); len(plan.Effects) != 0 {
			t.Fatalf("a retired continuation fired again: %v", kinds(plan))
		}
	})

	t.Run("pane closed mid probe", func(t *testing.T) {
		m := New()
		a, _ := probe(m)
		if plan := m.Resume(a.Token, Result{OK: true, Alive: true}, baseWorld()); len(plan.Effects) != 0 {
			t.Fatalf("opened a shell in a closed pane: %v", kinds(plan))
		}
	})
}

func TestUnknownTokenPlansNothing(t *testing.T) {
	m := New()
	w := baseWorld(gridPane("pane1", "g1"))
	if plan := m.Land(Token(42), w); len(plan.Effects) != 0 {
		t.Fatalf("an unknown token landed: %v", kinds(plan))
	}
	if plan := m.Resume(Token(42), Result{OK: true}, w); len(plan.Effects) != 0 {
		t.Fatalf("an unknown token resumed: %v", kinds(plan))
	}
}

func TestForgetRetiresAPaneContinuations(t *testing.T) {
	text := rpc.Tile{ID: "t1", Kind: rpc.KindText, GridID: "g1", W: 3, H: 2}
	w := baseWorld(gridPane("pane1", "g1"), gridPane("pane2", "g1"))
	w.Door = &DoorWorld{}
	m := New()
	one := only(t, m.Do(descendGesture("pane1", text), w), EffStartTransition)
	two := only(t, m.Do(descendGesture("pane2", text), w), EffStartTransition)

	// A pane that is going away has its transition dropped, not landed, so
	// nothing else would ever retire what it was waiting on.
	m.Forget("pane1")
	if plan := m.Land(one.Land, w); len(plan.Effects) != 0 {
		t.Fatalf("a forgotten pane's continuation fired: %v", kinds(plan))
	}
	if plan := m.Land(two.Land, w); len(plan.Effects) == 0 {
		t.Fatalf("forgetting one pane retired another's continuation")
	}
}
