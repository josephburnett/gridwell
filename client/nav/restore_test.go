package nav

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
)

// The restore paths. Every case is one address decoded against one cache
// snapshot, so what is really under test is the walk's loose-input contract
// and the token loop that replaces its blocking fetch.

func wellIn(child string) RestoreTile {
	return RestoreTile{IsWell: true, ChildGridID: child}
}

func textIn(stored string, scrollY int64) RestoreTile {
	return RestoreTile{IsContent: true, TextDocument: true,
		TextMode: stored, TextY: scrollY}
}

// restoreWorld is a one-pane world whose home is the plugin root p1/1, with
// the given grids cached. The pane sits there at (0, 0) zoom 1. Ids are
// namespace-qualified as they are on the wire: the address carries them bare
// and the machine re-qualifies with the anchor's namespace.
func restoreWorld(grids map[string]map[string]RestoreTile) World {
	w := baseWorld(gridPane("pane1", "p1/1"))
	w.Home = "p1/1"
	w.Restore = &RestoreWorld{Grids: grids, Failed: map[string]bool{},
		RootViews: map[string]Viewport{}}
	return w
}

func homeOnly() map[string]map[string]RestoreTile {
	return map[string]map[string]RestoreTile{"p1/1": {}}
}

func bootGesture(raw string) Gesture {
	return Gesture{Kind: GestureRestore, Raw: raw}
}

func TestRestoreBootPlans(t *testing.T) {
	grids := map[string]map[string]RestoreTile{
		"p1/1": {"p1/10": wellIn("p1/2"), "p1/88": textIn(rpc.TextModeRendered, 0)},
		"p1/2": {"p1/20": wellIn("p1/3"), "p1/99": textIn(rpc.TextModeRendered, 12)},
		"p1/3": {},
	}

	cases := []struct {
		name  string
		raw   string
		grids map[string]map[string]RestoreTile
		want  []EffectKind
	}{{
		name:  "no anchor and no path: home, framed as the pane already is",
		raw:   "/",
		grids: homeOnly(),
		want:  []EffectKind{EffInstallPlace, EffScheduleURLUpdate},
	}, {
		name:  "a viewport rides the root address",
		raw:   "/?x=5&y=-2&z=1.5",
		grids: homeOnly(),
		want:  []EffectKind{EffInstallPlace, EffInstallPlace, EffScheduleURLUpdate},
	}, {
		name:  "an unreadable address drops to root",
		raw:   "nonsense",
		grids: homeOnly(),
		want:  []EffectKind{EffInstallPlace, EffScheduleURLUpdate},
	}, {
		name:  "a well path lands on the grid it names",
		raw:   "/10/20",
		grids: grids,
		want: []EffectKind{EffInstallPlace, EffInstallPlace, EffFetchGrid,
			EffScheduleURLUpdate},
	}, {
		name:  "a missing middle id is skipped, not fatal",
		raw:   "/9999/10/20",
		grids: grids,
		want: []EffectKind{EffInstallPlace, EffInstallPlace, EffFetchGrid,
			EffScheduleURLUpdate},
	}, {
		name:  "a content leaf restores its body, its overlay and its surface",
		raw:   "/10/99",
		grids: grids,
		want: []EffectKind{EffInstallPlace, EffInstallPlace, EffScaleContent,
			EffAwait, EffRefreshOverlay, EffReEngage, EffFetchGrid,
			EffScheduleURLUpdate},
	}, {
		name:  "a pane-tile address is the whole place",
		raw:   "/?w=pt1",
		grids: homeOnly(),
		want:  []EffectKind{EffAwait},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := New().Do(bootGesture(c.raw), restoreWorld(c.grids))
			if !sameKinds(kinds(plan), c.want) {
				t.Fatalf("effects = %v, want %v", kinds(plan), c.want)
			}
			if plan.Next != nil {
				t.Fatalf("a boot restore asks for no continuation gesture")
			}
		})
	}

	// The loose walk in full: a bookmarked address must degrade gracefully as
	// the canvas changes underneath it, so a stale id is skipped and the ones
	// around it still resolve.
	t.Run("the skipped id leaves the rest of the path intact", func(t *testing.T) {
		plan := New().Do(bootGesture("/9999/10/20"), restoreWorld(grids))
		st := *plan.Effects[1].Stack
		got := st.Path()
		if st.Anchor() != "p1/1" || len(got) != 2 || got[0] != "p1/10" || got[1] != "p1/20" {
			t.Fatalf("place = (%q, %v), want the two live doorways", st.Anchor(), got)
		}
	})
}

func TestRestoreBootViewport(t *testing.T) {
	t.Run("the address wins", func(t *testing.T) {
		w := restoreWorld(homeOnly())
		w.Restore.RootViews["p1/1"] = Viewport{Cx: 9, Cy: 9, Zoom: 4}
		plan := New().Do(bootGesture("/?x=5&y=-2&z=1.5"), w)
		v := *plan.Effects[1].Viewport
		if v != (Viewport{Cx: 5, Cy: -2, Zoom: 1.5}) {
			t.Fatalf("viewport = %+v, want the address's", v)
		}
	})

	t.Run("a pan-only address keeps the pane's zoom", func(t *testing.T) {
		plan := New().Do(bootGesture("/?x=5&y=-2"), restoreWorld(homeOnly()))
		v := *plan.Effects[1].Viewport
		if v != (Viewport{Cx: 5, Cy: -2, Zoom: 1}) {
			t.Fatalf("viewport = %+v, want the pane's own zoom kept", v)
		}
	})

	t.Run("no address viewport: the framing the root was left at", func(t *testing.T) {
		w := restoreWorld(homeOnly())
		w.Restore.RootViews["p1/1"] = Viewport{Cx: 9, Cy: 9, Zoom: 4}
		plan := New().Do(bootGesture("/"), w)
		v := *plan.Effects[1].Viewport
		if v != (Viewport{Cx: 9, Cy: 9, Zoom: 4}) {
			t.Fatalf("viewport = %+v, want the persisted root view", v)
		}
	})

	t.Run("nothing persisted: the pane keeps what it has", func(t *testing.T) {
		plan := New().Do(bootGesture("/"), restoreWorld(homeOnly()))
		for _, e := range plan.Effects {
			if e.Kind == EffInstallPlace && e.Viewport != nil {
				t.Fatalf("re-framed a pane with nothing to restore")
			}
		}
	})
}

func TestRestoreContentLeafMode(t *testing.T) {
	cases := []struct {
		name string
		row  RestoreTile
		raw  string
		want string
	}{{
		name: "the stored mode is what the tile was left in",
		row:  textIn(rpc.TextModeRendered, 12),
		raw:  "/99",
		want: rpc.TextModeRendered,
	}, {
		name: "an address that encodes a cursor forces text mode",
		row:  textIn(rpc.TextModeRendered, 12),
		raw:  "/99?c=3&r=4",
		want: rpc.TextModeText,
	}, {
		name: "a read-only tile always restores its selectable face",
		row:  RestoreTile{IsContent: true, TextDocument: true, ReadOnly: true},
		raw:  "/99?c=3&r=4",
		want: rpc.TextModeRendered,
	}, {
		name: "a url tile carries no text mode at all",
		row:  RestoreTile{IsContent: true},
		raw:  "/99",
		want: "",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := restoreWorld(map[string]map[string]RestoreTile{"p1/1": {"p1/99": c.row}})
			plan := New().Do(bootGesture(c.raw), w)
			st := *plan.Effects[1].Stack
			if st.ContentID() != "p1/99" {
				t.Fatalf("content = %q, want the leaf", st.ContentID())
			}
			if st.TextMode != c.want {
				t.Errorf("mode = %q, want %q", st.TextMode, c.want)
			}
			if st.TextScrollY != float64(c.row.TextY) {
				t.Errorf("scroll = %v, want the tile's stored text_y", st.TextScrollY)
			}
		})
	}
}

func TestRestoreCursorLandsAfterTheBody(t *testing.T) {
	w := restoreWorld(map[string]map[string]RestoreTile{
		"p1/1": {"p1/99": textIn(rpc.TextModeText, 0)}})
	m := New()
	e := only(t, m.Do(bootGesture("/99?c=3&r=4"), w), EffAwait)
	if e.Request.Kind != RequestReadContent || e.Request.ID != "p1/99" {
		t.Fatalf("await = %+v, want the body read", e.Request)
	}
	cur := only(t, m.Resume(e.Token, Result{OK: true}, w), EffPlaceCursor)
	if cur.Col != 3 || cur.Row != 4 {
		t.Fatalf("cursor = (%d, %d), want (3, 4)", cur.Col, cur.Row)
	}
	// A body that never arrived places no cursor, and owes nothing after.
	m2 := New()
	tok := only(t, m2.Do(bootGesture("/99?c=3&r=4"), w), EffAwait).Token
	if plan := m2.Resume(tok, Result{}, w); len(plan.Effects) != 0 {
		t.Fatalf("placed a cursor with no body: %v", kinds(plan))
	}
}

func TestRestoreWalkSuspendsOnAnUncachedGrid(t *testing.T) {
	cold := map[string]map[string]RestoreTile{"p1/1": {"p1/10": wellIn("p1/2")}}
	warm := map[string]map[string]RestoreTile{
		"p1/1": {"p1/10": wellIn("p1/2")},
		"p1/2": {"p1/99": textIn(rpc.TextModeText, 0)},
	}

	m := New()
	e := only(t, m.Do(bootGesture("/10/99"), restoreWorld(cold)), EffAwait)
	if e.Request.Kind != RequestGetGrid || e.Request.ID != "p1/2" {
		t.Fatalf("await = %+v, want the missing grid", e.Request)
	}

	plan := m.Resume(e.Token, Result{OK: true}, restoreWorld(warm))
	want := []EffectKind{EffInstallPlace, EffScaleContent, EffAwait,
		EffRefreshOverlay, EffReEngage, EffFetchGrid, EffScheduleURLUpdate}
	if !sameKinds(kinds(plan), want) {
		t.Fatalf("effects = %v, want %v", kinds(plan), want)
	}
	st := *plan.Effects[0].Stack
	if st.Anchor() != "p1/1" || len(st.Path()) != 1 || st.ContentID() != "p1/99" {
		t.Fatalf("place = (%q, %v, %q)", st.Anchor(), st.Path(), st.ContentID())
	}
}

func TestRestoreWalkAsksForAGridOnlyOnce(t *testing.T) {
	cold := map[string]map[string]RestoreTile{"p1/1": {"p1/10": wellIn("p1/2")}}
	m := New()
	tok := only(t, m.Do(bootGesture("/10/99"), restoreWorld(cold)), EffAwait).Token

	// The load failed and latched nothing: the walk must not ask again, or a
	// grid that will not load is a boot that never finishes.
	plan := m.Resume(tok, Result{}, restoreWorld(cold))
	want := []EffectKind{EffInstallPlace, EffFetchGrid, EffScheduleURLUpdate}
	if !sameKinds(kinds(plan), want) {
		t.Fatalf("effects = %v, want %v", kinds(plan), want)
	}
	st := *plan.Effects[0].Stack
	if len(st.Path()) != 1 || st.ContentID() != "" {
		t.Fatalf("place = (%v, %q), want the longest prefix that resolved",
			st.Path(), st.ContentID())
	}
}

func TestRestoreWalkStopsAtALatchedGrid(t *testing.T) {
	w := restoreWorld(map[string]map[string]RestoreTile{"p1/1": {"p1/10": wellIn("p1/2")}})
	w.Restore.Failed["p1/2"] = true
	plan := New().Do(bootGesture("/10/99"), w)

	for _, e := range plan.Effects {
		if e.Kind == EffAwait {
			t.Fatalf("asked for a grid the server already refused")
		}
	}
}

func TestRestorePaneClosedMidWalk(t *testing.T) {
	cold := map[string]map[string]RestoreTile{"p1/1": {"p1/10": wellIn("p1/2")}}
	m := New()
	tok := only(t, m.Do(bootGesture("/10/99"), restoreWorld(cold)), EffAwait).Token

	gone := restoreWorld(cold)
	gone.Panes = nil
	gone.Focus = ""
	if plan := m.Resume(tok, Result{OK: true}, gone); len(plan.Effects) != 0 {
		t.Fatalf("restored into a pane that is gone: %v", kinds(plan))
	}
}

func TestRestoreFromHistory(t *testing.T) {
	m := New()
	w := restoreWorld(homeOnly())
	w.LevelDepth = 2

	first := m.Do(Gesture{Kind: GestureRestoreFromHistory, Raw: "/"}, w)
	want := []EffectKind{EffFlushDirtyText, EffFlushFraming, EffLeaveLevels}
	if !sameKinds(kinds(first), want) {
		t.Fatalf("effects = %v, want %v", kinds(first), want)
	}
	if only(t, first, EffLeaveLevels).Count != 2 {
		t.Fatalf("left the wrong number of levels")
	}
	// The URL is the restore's from the moment it is planned, which the shim
	// reaches synchronously inside the popstate callback.
	if m.URLWritable() {
		t.Fatal("a debounced write could still clobber the entry being restored")
	}
	if first.Next == nil || first.Next.Kind != GestureRestore || !first.Next.Reset {
		t.Fatalf("continuation = %+v, want the reset restore", first.Next)
	}

	// The tree the level exit restored is what the reset reads.
	second := m.Do(*first.Next, w)
	want = []EffectKind{EffCloseMenu, EffCancelTransition, EffForgetPane,
		EffInstallPlace, EffRefreshOverlay, EffInstallPlace,
		EffScheduleURLUpdate, EffWriteURLNow}
	if !sameKinds(kinds(second), want) {
		t.Fatalf("effects = %v, want %v", kinds(second), want)
	}
	// Every animation lands before the place is replaced, on every pane.
	if p := only(t, second, EffCancelTransition).PaneID; p != "" {
		t.Errorf("cancelled %q, want every pane", p)
	}
	if !m.URLWritable() {
		t.Fatal("the restore never handed the URL back")
	}
}

func TestRestoreFromHistoryEndsEvenWithNoPane(t *testing.T) {
	m := New()
	w := restoreWorld(homeOnly())
	w.Panes = nil
	w.Focus = ""
	m.Do(Gesture{Kind: GestureRestoreFromHistory, Raw: "/"}, w)
	plan := m.Do(Gesture{Kind: GestureRestore, Raw: "/", Reset: true}, w)

	if !sameKinds(kinds(plan), []EffectKind{EffWriteURLNow}) {
		t.Fatalf("effects = %v, want the address handed back", kinds(plan))
	}
	if !m.URLWritable() {
		t.Fatal("the URL stayed suppressed for the rest of the session")
	}
}

func TestRestoreFromHistorySuspendedWalkStillEnds(t *testing.T) {
	cold := map[string]map[string]RestoreTile{"p1/1": {"p1/10": wellIn("p1/2")}}
	m := New()
	w := restoreWorld(cold)
	m.Do(Gesture{Kind: GestureRestoreFromHistory, Raw: "/10/99"}, w)
	tok := only(t, m.Do(Gesture{Kind: GestureRestore, Raw: "/10/99", Reset: true}, w),
		EffAwait).Token
	if m.URLWritable() {
		t.Fatal("the URL was handed back before the walk finished")
	}

	plan := m.Resume(tok, Result{}, w)
	if only(t, plan, EffWriteURLNow).Kind != EffWriteURLNow {
		t.Fatal("the walk ended without handing the address back")
	}
	if !m.URLWritable() {
		t.Fatal("the URL stayed suppressed after the restore ended")
	}
}

func TestURLWriteBaseline(t *testing.T) {
	m := New()
	home := pane.URLPlace{PaneID: "pane1"}
	deep := pane.URLPlace{PaneID: "pane1", Path: "p1/10"}

	if m.URLWrote(home) {
		t.Error("the first write after boot must replace: boot restores a place")
	}
	if m.URLWrote(home) {
		t.Error("the same place again is framing, not navigation")
	}
	if !m.URLWrote(deep) {
		t.Error("a descent deserves a history entry")
	}

	// A popstate restore re-seeds the baseline unseen, so its final write
	// replaces even when the restore truncated the path.
	w := restoreWorld(homeOnly())
	m.Do(Gesture{Kind: GestureRestoreFromHistory, Raw: "/"}, w)
	m.Do(Gesture{Kind: GestureRestore, Raw: "/", Reset: true}, w)
	if m.URLWrote(home) {
		t.Error("a restore's own write must replace, not push")
	}
}
