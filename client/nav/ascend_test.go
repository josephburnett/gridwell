package nav

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/scratch"
)

// wellPane is a pane that descended from g1 through the doorway w1, sitting
// in the child grid at (5, 6) zoom 2.
func wellPane(id string) PaneView {
	p := gridPane(id, "g1")
	p.Stack.Push(pane.Frame{Door: "w1", Cx: 5, Cy: 6, Zoom: 2})
	p.Cx, p.Cy, p.Zoom = 5, 6, 2
	return p
}

// contentPane is a pane descended from g1 into the content tile tileID.
func contentPane(id, tileID string) PaneView {
	p := gridPane(id, "g1")
	p.Stack.Push(pane.ContentFrame(tileID, pane.Footprint{X: 2, Y: 2, W: 3, H: 2},
		4, rpc.TextModeText, 0, 0))
	p.Cx, p.Cy, p.Zoom = 3.5, 3, 4
	return p
}

func doorRow() *rpc.Tile {
	return &rpc.Tile{ID: "w1", Kind: rpc.KindWell, GridID: "g1",
		X: 2, Y: 3, W: 4, H: 4, ChildGridID: "gc"}
}

func ascendGesture(paneID string, n int, animate bool) Gesture {
	return Gesture{Kind: GestureAscend, PaneID: paneID, N: n, Animate: animate}
}

func TestAscendPlans(t *testing.T) {
	textRow := &rpc.Tile{ID: "t1", Kind: rpc.KindText, GridID: "g1", X: 2, Y: 2, W: 3, H: 2}
	urlRow := &rpc.Tile{ID: "r1", Kind: rpc.KindURL, GridID: "sg", X: 2, Y: 2, W: 3, H: 2}

	cases := []struct {
		name    string
		pane    PaneView
		leave   LeaveWorld
		scratch scratch.Grid
		animate bool
		want    []EffectKind
	}{{
		name:    "depth 1 is a no-op",
		pane:    gridPane("pane1", "g1"),
		animate: true,
		want:    []EffectKind{EffCancelTransition, EffRefreshOverlay, EffScheduleURLUpdate},
	}, {
		name:    "the doorway's grid is not cached: fetch it and land instantly",
		pane:    wellPane("pane1"),
		leave:   LeaveWorld{DoorGridID: "g1"},
		animate: true,
		want: []EffectKind{EffCancelTransition, EffFetchGrid, EffPersistFraming,
			EffInstallPlace, EffClearSelection, EffFetchGrid,
			EffRefreshOverlay, EffScheduleURLUpdate},
	}, {
		name:    "a + menu portal has no doorway row: the root grid owns the framing",
		pane:    wellPane("pane1"),
		leave:   LeaveWorld{DoorGridID: "g1", DoorGridCached: true},
		animate: true,
		want: []EffectKind{EffCancelTransition, EffPersistFraming,
			EffInstallPlace, EffClearSelection, EffFetchGrid,
			EffRefreshOverlay, EffScheduleURLUpdate},
	}, {
		name:    "animated out of a child grid",
		pane:    wellPane("pane1"),
		leave:   LeaveWorld{DoorGridID: "g1", DoorGridCached: true, DoorTile: doorRow()},
		animate: true,
		want: []EffectKind{EffCancelTransition, EffPersistFraming,
			EffStartTransition, EffRefreshOverlay, EffScheduleURLUpdate},
	}, {
		name:    "instant out of a child grid",
		pane:    wellPane("pane1"),
		leave:   LeaveWorld{DoorGridID: "g1", DoorGridCached: true, DoorTile: doorRow()},
		animate: false,
		want: []EffectKind{EffCancelTransition, EffPersistFraming,
			EffInstallPlace, EffClearSelection, EffFetchGrid,
			EffRefreshOverlay, EffScheduleURLUpdate},
	}, {
		name:    "animated out of content",
		pane:    contentPane("pane1", "t1"),
		leave:   LeaveWorld{DescendedTile: textRow},
		animate: true,
		want: []EffectKind{EffCancelTransition, EffSaveText,
			EffStartTransition, EffRefreshOverlay, EffScheduleURLUpdate},
	}, {
		name:    "the content row vanished: close both streams and land instantly",
		pane:    contentPane("pane1", "t1"),
		leave:   LeaveWorld{},
		animate: true,
		want: []EffectKind{EffCancelTransition, EffCloseStream,
			EffInstallPlace, EffClearSelection, EffFetchGrid,
			EffRefreshOverlay, EffScheduleURLUpdate},
	}, {
		name:    "leaving an ephemeral visit deletes it without freezing",
		pane:    contentPane("pane1", "r1"),
		leave:   LeaveWorld{DescendedTile: urlRow},
		scratch: scratch.Grid{Cached: true, ScratchGridID: "sg"},
		animate: true,
		want: []EffectKind{EffCancelTransition, EffSaveText, EffCloseStream,
			EffDeleteEphemeral, EffStartTransition,
			EffRefreshOverlay, EffScheduleURLUpdate},
	}, {
		name:    "an unloaded grid cannot say ephemeral: freeze, do not delete",
		pane:    contentPane("pane1", "r1"),
		leave:   LeaveWorld{DescendedTile: urlRow},
		scratch: scratch.Grid{},
		animate: true,
		want: []EffectKind{EffCancelTransition, EffSaveText, EffCloseStream,
			EffStartTransition, EffRefreshOverlay, EffScheduleURLUpdate},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := baseWorld(c.pane)
			w.Panes[0].Scratch = c.scratch
			lw := c.leave
			w.Leave = &lw
			plan := New().Do(ascendGesture("pane1", 1, c.animate), w)
			if !sameKinds(kinds(plan), c.want) {
				t.Fatalf("effects = %v, want %v", kinds(plan), c.want)
			}
			if plan.Next != nil {
				t.Fatalf("unexpected continuation gesture")
			}
		})
	}
}

func TestAscendEphemeralWithASplitSiblingDoesNotDelete(t *testing.T) {
	urlRow := &rpc.Tile{ID: "r1", Kind: rpc.KindURL, GridID: "sg", X: 2, Y: 2, W: 3, H: 2}
	w := baseWorld(contentPane("pane1", "r1"), contentPane("pane2", "r1"))
	w.Leave = &LeaveWorld{DescendedTile: urlRow}
	for i := range w.Panes {
		w.Panes[i].Scratch = scratch.Grid{Cached: true, ScratchGridID: "sg"}
	}
	plan := New().Do(ascendGesture("pane1", 1, true), w)

	for _, e := range plan.Effects {
		if e.Kind == EffDeleteEphemeral {
			t.Fatalf("deleted a visit a split sibling still shows")
		}
	}
	cs := only(t, plan, EffCloseStream)
	if !cs.Freeze {
		t.Fatalf("close froze nothing; a visit that survives keeps its preview")
	}
}

func TestAscendOutOfARootGridWithNoDoorway(t *testing.T) {
	p := gridPane("pane1", "g1")
	p.Stack = pane.StackOf([]pane.Frame{{GridID: "g1", Zoom: 1}, {GridID: "g2", Zoom: 1}})
	w := baseWorld(p)
	w.Leave = &LeaveWorld{}
	plan := New().Do(ascendGesture("pane1", 1, true), w)

	pf := only(t, plan, EffPersistFraming)
	if pf.Door {
		t.Fatalf("a root grid with no doorway persisted onto a doorway")
	}
	if pf.Owner.TileID != "" {
		t.Fatalf("framing owner = %+v, want the grid row", pf.Owner)
	}
	// Nothing to zoom out of: the landing is instant.
	if _, ok := findKind(plan, EffStartTransition); ok {
		t.Fatalf("animated an ascent with no doorway footprint")
	}
}

func TestAscendAnimatesFromTheJustPersistedFraming(t *testing.T) {
	// The doorway row carries a stale view; the writeback the plan emits
	// replaces it with where the user actually is, and the ascent calibrates
	// against THAT, so the frame swap does not snap back to the stored origin.
	door := doorRow()
	door.ViewCx, door.ViewCy, door.ViewZoom = 99, 99, 0.5
	w := baseWorld(wellPane("pane1"))
	w.Leave = &LeaveWorld{DoorGridID: "g1", DoorGridCached: true, DoorTile: door}
	plan := New().Do(ascendGesture("pane1", 1, true), w)

	tr := only(t, plan, EffStartTransition)
	if len(tr.Segments) != 2 {
		t.Fatalf("segments = %d, want the child leg and the parent leg", len(tr.Segments))
	}
	if tr.TraceTileID != "w1" {
		t.Fatalf("trace = %q, want the doorway just left", tr.TraceTileID)
	}
	child := tr.Segments[0]
	if child.ToCx != 5 || child.ToCy != 6 || child.ToZoom != 2 {
		t.Fatalf("child leg ends at (%v,%v,%v), want the pane's own place — the "+
			"framing was just written from it", child.ToCx, child.ToCy, child.ToZoom)
	}
	// The stored row was never read: had it been, the child leg would aim at
	// (99, 99).
	if child.ToCx == 99 {
		t.Fatalf("the ascent calibrated against the stale stored view")
	}
	if got := child.DurationMs + tr.Segments[1].DurationMs; got != 350 {
		t.Fatalf("total duration = %v, want the transition budget", got)
	}
}

func TestAscendLandsOnTheFrameItReached(t *testing.T) {
	w := baseWorld(wellPane("pane1"))
	w.Leave = &LeaveWorld{DoorGridID: "g1", DoorGridCached: true, DoorTile: doorRow()}
	m := New()
	tr := only(t, m.Do(ascendGesture("pane1", 1, true), w), EffStartTransition)
	if tr.Land == 0 {
		t.Fatalf("the animated ascent carries no landing continuation")
	}
	// The segments installed the landing place, so the land step reads it
	// fresh rather than trusting a gesture-time copy.
	landed := baseWorld(gridPane("pane1", "g1"))
	if got := kinds(m.Land(tr.Land, landed)); !sameKinds(got, []EffectKind{EffFetchGrid}) {
		t.Fatalf("landing = %v, want the grid fetch", got)
	}
}

func TestAscendLandingBackOnContentReEngages(t *testing.T) {
	w := baseWorld(contentPane("pane1", "t1"))
	w.Leave = &LeaveWorld{DescendedTile: &rpc.Tile{ID: "t1", Kind: rpc.KindText,
		GridID: "g1", X: 2, Y: 2, W: 3, H: 2}}
	m := New()
	tr := only(t, m.Do(ascendGesture("pane1", 1, true), w), EffStartTransition)

	// The pane landed back onto a content frame stacked below (a url opened
	// from a shell, then left).
	landed := baseWorld(contentPane("pane1", "t0"))
	want := []EffectKind{EffScaleContent, EffRefreshOverlay, EffReEngage, EffFetchGrid}
	if got := kinds(m.Land(tr.Land, landed)); !sameKinds(got, want) {
		t.Fatalf("landing = %v, want %v", got, want)
	}
}

func TestAscendRestoresTheMenuItWasLeftWith(t *testing.T) {
	p := gridPane("pane1", "g1")
	p.Stack.MenuOpen = true
	p.Stack.Push(pane.Frame{Door: "w1", Cx: 5, Cy: 6, Zoom: 2})
	p.Cx, p.Cy, p.Zoom = 5, 6, 2
	w := baseWorld(p)
	w.Leave = &LeaveWorld{DoorGridID: "g1", DoorGridCached: true}
	plan := New().Do(ascendGesture("pane1", 1, true), w)
	if _, ok := findKind(plan, EffOpenMenu); !ok {
		t.Fatalf("effects = %v, want the + menu reopened", kinds(plan))
	}
}

func TestAscendMultiHopOnlyAnimatesTheLast(t *testing.T) {
	p := gridPane("pane1", "g1")
	p.Stack.Push(pane.Frame{Door: "w1", Cx: 1, Cy: 1, Zoom: 1})
	p.Stack.Push(pane.Frame{Door: "w2", Cx: 5, Cy: 6, Zoom: 2})
	p.Cx, p.Cy, p.Zoom = 5, 6, 2
	w := baseWorld(p)
	w.Leave = &LeaveWorld{DoorGridID: "g1", DoorGridCached: true,
		DoorTile: &rpc.Tile{ID: "w2", Kind: rpc.KindWell, GridID: "gc", W: 4, H: 4}}
	m := New()

	first := m.Do(ascendGesture("pane1", 2, true), w)
	if _, ok := findKind(first, EffStartTransition); ok {
		t.Fatalf("the first of two hops animated: %v", kinds(first))
	}
	if first.Next == nil || first.Next.N != 1 {
		t.Fatalf("no continuation gesture for the remaining hop: %+v", first.Next)
	}
	if _, ok := findKind(first, EffRefreshOverlay); ok {
		t.Fatalf("the tail ran before the last hop: %v", kinds(first))
	}

	// The second hop reads the place the first landed on.
	landed := gridPane("pane1", "g1")
	landed.Stack.Push(pane.Frame{Door: "w1", Cx: 1, Cy: 1, Zoom: 1})
	landed.Cx, landed.Cy, landed.Zoom = 1, 1, 1
	w2 := baseWorld(landed)
	w2.Leave = &LeaveWorld{DoorGridID: "g1", DoorGridCached: true, DoorTile: doorRow()}
	second := m.Do(*first.Next, w2)
	if _, ok := findKind(second, EffStartTransition); !ok {
		t.Fatalf("the last hop did not animate: %v", kinds(second))
	}
	if second.Next != nil {
		t.Fatalf("the ascent did not stop")
	}
}

func TestAscendLandingViewport(t *testing.T) {
	// A frame restored from a URL carries no viewport; the ascent onto it
	// falls back to the grid's persisted framing rather than an origin.
	p := gridPane("pane1", "g1")
	p.Stack = pane.StackAt("g1", []string{"w1"}, "")
	p.Cx, p.Cy, p.Zoom = 5, 6, 2
	w := baseWorld(p)
	w.Leave = &LeaveWorld{DoorGridID: "g1", DoorGridCached: true,
		LandingView: &Viewport{Cx: 8, Cy: 9, Zoom: 1.5}}
	vp := only(t, New().Do(ascendGesture("pane1", 1, true), w), EffInstallPlace).Viewport
	if vp == nil || *vp != (Viewport{Cx: 8, Cy: 9, Zoom: 1.5}) {
		t.Fatalf("landed at %+v, want the persisted framing", vp)
	}

	// With nothing persisted either, a grid landing goes to the origin...
	w.Leave = &LeaveWorld{DoorGridID: "g1", DoorGridCached: true}
	vp = only(t, New().Do(ascendGesture("pane1", 1, true), w), EffInstallPlace).Viewport
	if vp == nil || *vp != (Viewport{Zoom: 1}) {
		t.Fatalf("landed at %+v, want the origin", vp)
	}

	// ...but leaving a content descent keeps the viewport, which is already in
	// the landing grid's coordinates.
	cp := contentPane("pane1", "t1")
	cp.Stack = pane.StackAt("g1", nil, "t1")
	w = baseWorld(cp)
	w.Leave = &LeaveWorld{}
	vp = only(t, New().Do(ascendGesture("pane1", 1, true), w), EffInstallPlace).Viewport
	if vp != nil {
		t.Fatalf("moved the viewport out of a content descent: %+v", vp)
	}
}

// findKind reports the first effect of kind k.
func findKind(p Plan, k EffectKind) (Effect, bool) {
	for _, e := range p.Effects {
		if e.Kind == k {
			return e, true
		}
	}
	return Effect{}, false
}
