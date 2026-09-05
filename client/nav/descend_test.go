package nav

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
)

// gridPane is a pane sitting at the root of gridID, 800×600, at zoom 1.
func gridPane(id, gridID string) PaneView {
	return PaneView{
		ID: id, Stack: pane.NewStack(gridID),
		Cx: 0, Cy: 0, Zoom: 1,
		Rect: pane.Rect{W: 800, H: 600}, OnScreen: true, GridID: gridID,
	}
}

// baseWorld is a one-pane world with the renderer's constants bound and every
// capability on: the shape every descent and ascent case starts from.
func baseWorld(panes ...PaneView) World {
	w := World{
		Panes:           panes,
		CellPx:          64,
		TransitionMs:    350,
		ZoomDistFactor:  4,
		TextSideInset:   6,
		Animating:       map[string]bool{},
		ShellAlive:      map[string]bool{},
		ShellAliveKnown: map[string]bool{},
		Caps:            caps.Caps{LiveURL: true, LiveShell: true, Shells: true},
	}
	if len(panes) > 0 {
		w.Focus = panes[0].ID
	}
	return w
}

// kinds is the plan's effect sequence: what a table test asserts on.
func kinds(p Plan) []EffectKind {
	out := make([]EffectKind, 0, len(p.Effects))
	for _, e := range p.Effects {
		out = append(out, e.Kind)
	}
	return out
}

func sameKinds(a, b []EffectKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// only returns the single effect of kind k, failing when there is not exactly
// one.
func only(t *testing.T, p Plan, k EffectKind) Effect {
	t.Helper()
	var found []Effect
	for _, e := range p.Effects {
		if e.Kind == k {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one effect of kind %d, got %d in %v", k, len(found), kinds(p))
	}
	return found[0]
}

func descendGesture(paneID string, door rpc.Tile) Gesture {
	return Gesture{Kind: GestureDescend, PaneID: paneID, Door: door}
}

func TestDescendPlans(t *testing.T) {
	well := rpc.Tile{ID: "w1", Kind: rpc.KindWell, GridID: "g1",
		X: 2, Y: 3, W: 4, H: 4, ChildGridID: "g2"}
	link := rpc.Tile{ID: "u1/l1", Kind: rpc.KindWell, GridID: "g1",
		X: 1, Y: 1, W: 2, H: 2, ChildGridID: "u2/root", Reference: true}
	text := rpc.Tile{ID: "t1", Kind: rpc.KindText, GridID: "g1", X: 0, Y: 0, W: 3, H: 2}
	url := rpc.Tile{ID: "r1", Kind: rpc.KindURL, GridID: "g1", X: 0, Y: 0, W: 3, H: 2,
		URLString: "https://example.test/"}
	shell := rpc.Tile{ID: "s1", Kind: rpc.KindShell, GridID: "g1", X: 0, Y: 0, W: 3, H: 2}
	page := rpc.Tile{ID: "p1t", Kind: rpc.KindText, GridID: "g1", X: 0, Y: 0, W: 3, H: 2,
		ServesPage: true}
	wsTile := rpc.Tile{ID: "pt1", Kind: rpc.KindPane, GridID: "g1", X: 0, Y: 0, W: 2, H: 2}

	cases := []struct {
		name string
		door rpc.Tile
		dw   DoorWorld
		want []EffectKind
	}{{
		name: "well",
		door: well,
		want: []EffectKind{EffCancelTransition, EffFlushFraming, EffFetchGrid,
			EffStartTransition},
	}, {
		name: "link tile",
		door: link,
		dw:   DoorWorld{IsLink: true},
		want: []EffectKind{EffCancelTransition, EffFlushFraming, EffCloseMenu,
			EffFetchGrid, EffStartTransition},
	}, {
		name: "content text",
		door: text,
		want: []EffectKind{EffCancelTransition, EffFlushFraming,
			EffFetchTileContent, EffStartTransition},
	}, {
		name: "content read only drops then fetches",
		door: text,
		dw:   DoorWorld{ReadOnly: true},
		want: []EffectKind{EffCancelTransition, EffFlushFraming,
			EffDropTileContent, EffFetchGrid, EffFetchTileContent,
			EffStartTransition},
	}, {
		name: "content url has no blob",
		door: url,
		want: []EffectKind{EffCancelTransition, EffFlushFraming, EffStartTransition},
	}, {
		name: "content shell has no blob",
		door: shell,
		want: []EffectKind{EffCancelTransition, EffFlushFraming, EffStartTransition},
	}, {
		name: "page tile descends to the page, not the body",
		door: page,
		want: []EffectKind{EffCancelTransition, EffFlushFraming, EffStartTransition},
	}, {
		name: "dead link plans nothing",
		door: link,
		dw:   DoorWorld{IsLink: true, DeadLink: true},
		want: nil,
	}, {
		name: "workspace kind enters a level",
		door: wsTile,
		want: []EffectKind{EffCancelTransition, EffFlushFraming, EffEnterLevel},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := baseWorld(gridPane("pane1", "g1"))
			dw := c.dw
			w.Door = &dw
			plan := New().Do(descendGesture("pane1", c.door), w)
			if !sameKinds(kinds(plan), c.want) {
				t.Fatalf("effects = %v, want %v", kinds(plan), c.want)
			}
			if plan.Next != nil {
				t.Fatalf("unexpected continuation gesture")
			}
		})
	}
}

func TestDescendWellPushesGridFrame(t *testing.T) {
	well := rpc.Tile{ID: "w1", Kind: rpc.KindWell, GridID: "g1",
		X: 2, Y: 3, W: 4, H: 4, ChildGridID: "g2"}
	w := baseWorld(gridPane("pane1", "g1"))
	w.Door = &DoorWorld{}
	plan := New().Do(descendGesture("pane1", well), w)

	if f := only(t, plan, EffFetchGrid); f.GridID != "g2" {
		t.Fatalf("fetched %q, want the child grid", f.GridID)
	}
	tr := only(t, plan, EffStartTransition)
	if len(tr.Segments) != 2 {
		t.Fatalf("segments = %d, want 2 (parent zoom, child ease-out)", len(tr.Segments))
	}
	// The parent segment plays where the pane already is; the child segment in
	// the pushed frame.
	if tr.Segments[0].Place.Depth() != 1 {
		t.Fatalf("parent segment depth = %d, want 1", tr.Segments[0].Place.Depth())
	}
	child := tr.Segments[1].Place
	if child.Depth() != 2 || child.Door != "w1" {
		t.Fatalf("child frame = %+v, want a pushed frame through w1", child.Frame)
	}
	// A plain well derives its grid by walking the parent; only a crossing is
	// authoritative about the target id.
	if child.GridID != "" {
		t.Fatalf("plain well frame carries GridID %q, want it derived", child.GridID)
	}
	if got := tr.Segments[0].DurationMs + tr.Segments[1].DurationMs; got != 350 {
		t.Fatalf("total duration = %v, want the transition budget", got)
	}
}

func TestDescendLinkCarriesTargetGridAndCapturesMenu(t *testing.T) {
	link := rpc.Tile{ID: "u1/l1", Kind: rpc.KindWell, GridID: "g1",
		X: 1, Y: 1, W: 2, H: 2, ChildGridID: "u2/root", Reference: true}
	w := baseWorld(gridPane("pane1", "g1"))
	w.Door = &DoorWorld{IsLink: true}
	w.MenuOpenOn = "pane1"

	tr := only(t, New().Do(descendGesture("pane1", link), w), EffStartTransition)
	child := tr.Segments[1].Place
	if child.GridID != "u2/root" {
		t.Fatalf("crossing frame GridID = %q, want the target grid id", child.GridID)
	}
	if !tr.Segments[0].Place.MenuOpen {
		t.Fatalf("the + menu was not captured onto the frame being left")
	}
	if !child.Frames()[0].MenuOpen {
		t.Fatalf("the captured menu did not travel below the pushed frame")
	}
	// The footprint re-centre: a synthetic + menu well rounds its position, so
	// the parent zoom targets the exact footprint centre.
	if tr.Segments[0].ToCx != 2 || tr.Segments[0].ToCy != 2 {
		t.Fatalf("parent segment ends at (%v,%v), want the footprint centre (2,2)",
			tr.Segments[0].ToCx, tr.Segments[0].ToCy)
	}
}

func TestDescendLinkLeavesAnUnopenedMenuAlone(t *testing.T) {
	link := rpc.Tile{ID: "u1/l1", Kind: rpc.KindWell, GridID: "g1",
		X: 1, Y: 1, W: 2, H: 2, ChildGridID: "u2/root", Reference: true}
	w := baseWorld(gridPane("pane1", "g1"))
	w.Door = &DoorWorld{IsLink: true}
	w.MenuOpenOn = "other"

	tr := only(t, New().Do(descendGesture("pane1", link), w), EffStartTransition)
	if tr.Segments[0].Place.MenuOpen {
		t.Fatalf("a menu open on another pane was captured onto this one")
	}
}

func TestDescendLinkWithNoChildGrid(t *testing.T) {
	link := rpc.Tile{ID: "u1/l1", Kind: rpc.KindWell, GridID: "g1",
		W: 2, H: 2, Reference: true, AltText: "files"}

	t.Run("health notice", func(t *testing.T) {
		w := baseWorld(gridPane("pane1", "g1"))
		w.Door = &DoorWorld{IsLink: true, Health: &Notice{
			Severity: errsurface.Error, Source: "launcher:u1", Message: "files: boom"}}
		plan := New().Do(descendGesture("pane1", link), w)
		r := only(t, plan, EffReport)
		if r.Severity != errsurface.Error || r.Source != "launcher:u1" || r.Message != "files: boom" {
			t.Fatalf("report = %+v, want pluginhealth's wording verbatim", r)
		}
		if len(plan.Effects) != 3 {
			t.Fatalf("effects = %v, want cancel, flush, report", kinds(plan))
		}
	})

	t.Run("no notice", func(t *testing.T) {
		w := baseWorld(gridPane("pane1", "g1"))
		w.Door = &DoorWorld{IsLink: true}
		r := only(t, New().Do(descendGesture("pane1", link), w), EffReport)
		if r.Severity != errsurface.Info || r.Source != "descend" ||
			r.Message != "nothing to descend into: files" {
			t.Fatalf("report = %+v, want the generic descend notice", r)
		}
	})
}

func TestDescendContentPushesContentFrame(t *testing.T) {
	text := rpc.Tile{ID: "t1", Kind: rpc.KindText, GridID: "g1",
		X: 5, Y: 7, W: 3, H: 2, TextX: 11, TextY: 13, TextMode: rpc.TextModeText}
	w := baseWorld(gridPane("pane1", "g1"))
	w.Door = &DoorWorld{}
	m := New()
	plan := m.Do(descendGesture("pane1", text), w)

	tr := only(t, plan, EffStartTransition)
	if len(tr.Segments) != 1 {
		t.Fatalf("segments = %d, want one combined pan+zoom", len(tr.Segments))
	}
	if tr.Land == 0 {
		t.Fatalf("the content descent carries no landing continuation")
	}
	// Nothing is pushed until the landing: the animation plays in the grid.
	if tr.Segments[0].Place.Depth() != 1 || tr.Segments[0].Place.Content {
		t.Fatalf("animation plays in %+v, want the grid it is leaving", tr.Segments[0].Place.Frame)
	}
	if tr.Segments[0].ToCx != 6.5 || tr.Segments[0].ToCy != 8 {
		t.Fatalf("pan target = (%v,%v), want the footprint centre",
			tr.Segments[0].ToCx, tr.Segments[0].ToCy)
	}

	land := m.Land(tr.Land, w)
	want := []EffectKind{EffInstallPlace, EffScaleContent, EffRefreshOverlay,
		EffScheduleURLUpdate}
	if !sameKinds(kinds(land), want) {
		t.Fatalf("landing = %v, want %v", kinds(land), want)
	}
	st := only(t, land, EffInstallPlace).Stack
	if !st.Content || st.Door != "t1" || st.Depth() != 2 {
		t.Fatalf("landed place = %+v, want a content frame on t1", st.Frame)
	}
	if st.TextMode != rpc.TextModeText {
		t.Fatalf("text mode = %q, want the stored mode", st.TextMode)
	}
	if st.TextScrollX != 11 || st.TextScrollY != 13 {
		t.Fatalf("scroll = (%v,%v), want the stored framed window",
			st.TextScrollX, st.TextScrollY)
	}
}

func TestDescendContentOverContentAnimatesInTheGridBehind(t *testing.T) {
	p := gridPane("pane1", "g1")
	p.Stack.Push(pane.ContentFrame("t0", pane.Footprint{W: 2, H: 2}, 3, rpc.TextModeText, 0, 0))
	url := rpc.Tile{ID: "r1", Kind: rpc.KindURL, GridID: "g1", W: 3, H: 2}
	w := baseWorld(p)
	w.Door = &DoorWorld{}
	plan := New().Do(descendGesture("pane1", url), w)

	tr := only(t, plan, EffStartTransition)
	if tr.Segments[0].Place.Depth() != 1 || tr.Segments[0].Place.Content {
		t.Fatalf("stacked visit animates in %+v, want the grid behind the content",
			tr.Segments[0].Place.Frame)
	}
	// The outgoing content's overlay goes now; the animation plays over the
	// grid behind it.
	last := plan.Effects[len(plan.Effects)-1]
	if last.Kind != EffRefreshOverlay {
		t.Fatalf("last effect = %v, want the overlay refresh", kinds(plan))
	}
}

func TestDescendLandGoesLive(t *testing.T) {
	cases := []struct {
		name  string
		tile  rpc.Tile
		caps  caps.Caps
		alive map[string]bool
		known map[string]bool
		want  []EffectKind
	}{{
		name: "url opens the native view",
		tile: rpc.Tile{ID: "r1", Kind: rpc.KindURL, GridID: "g1", W: 2, H: 2},
		caps: caps.Caps{LiveURL: true, LiveShell: true},
		want: []EffectKind{EffInstallPlace, EffScaleContent, EffRefreshOverlay,
			EffOpenStream, EffScheduleURLUpdate},
	}, {
		name: "a browser host stays frozen",
		tile: rpc.Tile{ID: "r1", Kind: rpc.KindURL, GridID: "g1", W: 2, H: 2},
		caps: caps.Caps{LiveShell: true},
		want: []EffectKind{EffInstallPlace, EffScaleContent, EffRefreshOverlay,
			EffScheduleURLUpdate},
	}, {
		name: "a frozen url stays frozen",
		tile: rpc.Tile{ID: "r1", Kind: rpc.KindURL, GridID: "g1", W: 2, H: 2, URLFrozen: true},
		caps: caps.Caps{LiveURL: true, LiveShell: true},
		want: []EffectKind{EffInstallPlace, EffScaleContent, EffRefreshOverlay,
			EffScheduleURLUpdate},
	}, {
		name: "a fresh shell creates",
		tile: rpc.Tile{ID: "s1", Kind: rpc.KindShell, GridID: "g1", W: 2, H: 2},
		caps: caps.Caps{LiveURL: true, LiveShell: true},
		want: []EffectKind{EffInstallPlace, EffScaleContent, EffRefreshOverlay,
			EffOpenStream, EffScheduleURLUpdate},
	}, {
		name:  "a shell with an unknown session probes first",
		tile:  rpc.Tile{ID: "s1", Kind: rpc.KindShell, GridID: "g1", W: 2, H: 2, PreviewBlobID: 9},
		caps:  caps.Caps{LiveURL: true, LiveShell: true},
		want:  []EffectKind{EffInstallPlace, EffScaleContent, EffRefreshOverlay, EffAwait, EffScheduleURLUpdate},
		alive: map[string]bool{},
		known: map[string]bool{},
	}, {
		name:  "a shell known dead stays frozen",
		tile:  rpc.Tile{ID: "s1", Kind: rpc.KindShell, GridID: "g1", W: 2, H: 2, PreviewBlobID: 9},
		caps:  caps.Caps{LiveURL: true, LiveShell: true},
		alive: map[string]bool{"s1": false},
		known: map[string]bool{"s1": true},
		want: []EffectKind{EffInstallPlace, EffScaleContent, EffRefreshOverlay,
			EffScheduleURLUpdate},
	}, {
		name: "text stays frozen",
		tile: rpc.Tile{ID: "t1", Kind: rpc.KindText, GridID: "g1", W: 2, H: 2},
		caps: caps.Caps{LiveURL: true, LiveShell: true},
		want: []EffectKind{EffInstallPlace, EffScaleContent, EffRefreshOverlay,
			EffScheduleURLUpdate},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := baseWorld(gridPane("pane1", "g1"))
			w.Door = &DoorWorld{}
			w.Caps = c.caps
			if c.alive != nil {
				w.ShellAlive, w.ShellAliveKnown = c.alive, c.known
			}
			m := New()
			tr := only(t, m.Do(descendGesture("pane1", c.tile), w), EffStartTransition)
			land := m.Land(tr.Land, w)
			if !sameKinds(kinds(land), c.want) {
				t.Fatalf("landing = %v, want %v", kinds(land), c.want)
			}
		})
	}
}
