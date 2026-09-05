package nav

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
)

// The second axis: the pane-tile descent's two arms, the install they join
// into, and the way back out.

// paneTile is a never-arranged pane tile in g1; arranged() gives it a blob.
func paneTile() rpc.Tile {
	return rpc.Tile{ID: "pt1", Kind: rpc.KindPane, GridID: "g1",
		X: 2, Y: 3, W: 4, H: 4, AltText: "plan"}
}

func arranged(t rpc.Tile) rpc.Tile {
	t.BlobID = 7
	return t
}

// layoutBytes is a blob the codec can read back: one pane at g2.
func layoutBytes(t *testing.T) []byte {
	t.Helper()
	data, _, err := pane.EncodeLayout(pane.TreeAtPlace("w1:", "g2", nil, 1, 2, 1), nil)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return data
}

func enterGesture(paneID string, door rpc.Tile) Gesture {
	return Gesture{Kind: GestureEnterLevel, PaneID: paneID, Door: door}
}

// enter plans a descent into door and returns the two arms.
func enter(t *testing.T, m *Machine, door rpc.Tile) (anim, fetch Effect, w World) {
	t.Helper()
	w = baseWorld(gridPane("pane1", "g1"))
	plan := m.Do(enterGesture("pane1", door), w)
	if !sameKinds(kinds(plan), []EffectKind{EffStartTransition, EffAwait}) {
		t.Fatalf("effects = %v, want the animation and the fetch", kinds(plan))
	}
	return plan.Effects[0], plan.Effects[1], w
}

// installKinds is the swap, in order.
var installKinds = []EffectKind{EffInstallPlace, EffCancelTransition, EffCloseMenu,
	EffFlushLayout, EffInstallLevel, EffRefreshOverlay, EffScheduleURLUpdate}

func TestEnterLevelAnimations(t *testing.T) {
	t.Run("a never-arranged tile expands rather than zooming", func(t *testing.T) {
		anim, _, _ := enter(t, New(), paneTile())
		if !anim.Expand {
			t.Fatalf("the first descent did not ask for the capture animation")
		}
		s := anim.Segments[0]
		if s.ToCx != s.FromCx || s.ToZoom != s.FromZoom {
			t.Fatalf("the capture animation moved the content: %+v", s)
		}
	})

	t.Run("an arranged tile zooms into its footprint", func(t *testing.T) {
		anim, _, _ := enter(t, New(), arranged(paneTile()))
		if anim.Expand {
			t.Fatalf("an arranged tile asked for the capture animation")
		}
		s := anim.Segments[0]
		if s.ToCx != 4 || s.ToCy != 5 || s.ToZoom <= s.FromZoom {
			t.Fatalf("segment = %+v, want a zoom onto the tile centre", s)
		}
	})
}

func TestEnterLevelArmsJoin(t *testing.T) {
	row := arranged(paneTile())
	blob := layoutBytes(t)

	// The fetch arm, run to the end. Returns the plan of its last step.
	feed := func(t *testing.T, m *Machine, fetch Effect, w World) Plan {
		t.Helper()
		if fetch.Request.Kind != RequestGetTile || fetch.Request.ID != "pt1" {
			t.Fatalf("await = %+v, want the tile row", fetch.Request)
		}
		plan := m.Resume(fetch.Token, Result{OK: true, Tile: &row}, w)
		body := only(t, plan, EffAwait)
		if body.Request.Kind != RequestReadLayout || body.Request.ID != "pt1" {
			t.Fatalf("await = %+v, want the layout blob", body.Request)
		}
		return m.Resume(body.Token, Result{OK: true, Data: blob}, w)
	}

	t.Run("the animation finishes first", func(t *testing.T) {
		m := New()
		anim, fetch, w := enter(t, m, row)
		if plan := m.Land(anim.Land, w); len(plan.Effects) != 0 {
			t.Fatalf("installed before the layout arrived: %v", kinds(plan))
		}
		plan := feed(t, m, fetch, w)
		if !sameKinds(kinds(plan), installKinds) {
			t.Fatalf("effects = %v, want the swap", kinds(plan))
		}
		e := only(t, plan, EffInstallLevel)
		if e.Level.TileID != "pt1" || e.Level.GridID != "g1" || e.Level.Name != "plan" {
			t.Fatalf("level = %+v, want it named off the row the descent read", e.Level)
		}
		if !e.KeepOuter || e.Level.ReadOnly || e.Capture || e.Tree == nil {
			t.Fatalf("effect = %+v, want the decoded tree with the outer level kept", e)
		}
		if string(e.Baseline) != string(blob) {
			t.Fatalf("baseline is not the bytes that were read")
		}
	})

	t.Run("the fetch finishes first", func(t *testing.T) {
		m := New()
		anim, fetch, w := enter(t, m, row)
		if plan := feed(t, m, fetch, w); len(plan.Effects) != 0 {
			t.Fatalf("installed before the animation landed: %v", kinds(plan))
		}
		if plan := m.Land(anim.Land, w); !sameKinds(kinds(plan), installKinds) {
			t.Fatalf("effects = %v, want the swap", kinds(plan))
		}
	})

	t.Run("a failed fetch restores the origin viewport, and only then", func(t *testing.T) {
		m := New()
		anim, fetch, w := enter(t, m, row)
		plan := m.Resume(fetch.Token, Result{Err: "no route"}, w)
		if !sameKinds(kinds(plan), []EffectKind{EffReport}) {
			t.Fatalf("effects = %v, want the notice alone while the zoom runs", kinds(plan))
		}
		plan = m.Land(anim.Land, w)
		if !sameKinds(kinds(plan), []EffectKind{EffInstallPlace}) {
			t.Fatalf("effects = %v, want the origin place back and no swap", kinds(plan))
		}
		if e := only(t, plan, EffInstallPlace); e.Stack.Anchor() != "g1" || e.Stack.Depth() != 1 {
			t.Fatalf("restored %+v, want the place the descent started from", e.Stack)
		}
	})

	t.Run("a superseded descent installs nothing", func(t *testing.T) {
		m := New()
		anim, fetch, w := enter(t, m, row)
		// A second descent in the same pane supersedes the first.
		_, _, _ = enter(t, m, row)
		if plan := m.Land(anim.Land, w); len(plan.Effects) != 0 {
			t.Fatalf("a superseded animation arm acted: %v", kinds(plan))
		}
		if plan := feed(t, m, fetch, w); len(plan.Effects) != 0 {
			t.Fatalf("a superseded fetch arm installed: %v", kinds(plan))
		}
	})

	t.Run("a closed pane takes its barrier with it", func(t *testing.T) {
		m := New()
		anim, fetch, w := enter(t, m, row)
		// The pane's transition is dropped rather than landed, so its arm never
		// arrives: nothing may be left waiting on it.
		m.Forget("pane1")
		if plan := m.Resume(fetch.Token, Result{OK: true, Tile: &row}, w); len(plan.Effects) != 0 {
			t.Fatalf("a forgotten pane's fetch arm acted: %v", kinds(plan))
		}
		if m.LevelPending() {
			t.Fatalf("a level is still pending after its pane went away")
		}
		if plan := m.Land(anim.Land, w); len(plan.Effects) != 0 {
			t.Fatalf("a forgotten pane's animation arm acted: %v", kinds(plan))
		}
	})
}

func TestEnterLevelRows(t *testing.T) {
	t.Run("a pane link opens its target's arrangement", func(t *testing.T) {
		m := New()
		_, fetch, w := enter(t, m, paneTile())
		link := paneTile()
		link.LinkTargetID = "u2/pt9"
		plan := m.Resume(fetch.Token, Result{OK: true, Tile: &link}, w)
		e := only(t, plan, EffAwait)
		if e.Request.Kind != RequestGetTile || e.Request.ID != "u2/pt9" {
			t.Fatalf("await = %+v, want the link's target row", e.Request)
		}
		// And the target is taken as read: a link to a link stops there.
		target := link
		target.ID = "u2/pt9"
		plan = m.Resume(e.Token, Result{OK: true, Tile: &target}, w)
		if len(plan.Effects) != 0 {
			t.Fatalf("followed a second hop: %v", kinds(plan))
		}
	})

	t.Run("a never-arranged tile captures the window at the swap", func(t *testing.T) {
		m := New()
		anim, fetch, w := enter(t, m, paneTile())
		row := paneTile()
		if plan := m.Resume(fetch.Token, Result{OK: true, Tile: &row}, w); len(plan.Effects) != 0 {
			t.Fatalf("a never-arranged tile read a blob: %v", kinds(plan))
		}
		plan := m.Land(anim.Land, w)
		e := only(t, plan, EffInstallLevel)
		if !e.Capture || e.Tree != nil {
			t.Fatalf("effect = %+v, want the capture, which only the shim can take", e)
		}
		if e.IDPrefix != "w1:" {
			t.Fatalf("id prefix = %q, want the level's own namespace", e.IDPrefix)
		}
		if len(e.Baseline) != 0 {
			t.Fatalf("a never-arranged tile has no baseline to diff against")
		}
	})

	t.Run("an unreadable blob opens read-only", func(t *testing.T) {
		m := New()
		anim, fetch, w := enter(t, m, arranged(paneTile()))
		row := arranged(paneTile())
		body := only(t, m.Resume(fetch.Token, Result{OK: true, Tile: &row}, w), EffAwait)
		plan := m.Resume(body.Token, Result{OK: true, Data: []byte("{}")}, w)
		if !sameKinds(kinds(plan), []EffectKind{EffReport}) {
			t.Fatalf("effects = %v, want the notice while the zoom runs", kinds(plan))
		}
		e := only(t, m.Land(anim.Land, w), EffInstallLevel)
		if !e.Level.ReadOnly || e.Tree == nil {
			t.Fatalf("effect = %+v, want the default tree, latched read-only", e)
		}
		// The pane sits where it was: an unreadable level must not move you.
		if p := e.Tree.FocusedPane(); p == nil || p.Anchor() != "g1" {
			t.Fatalf("the fallback tree did not land on the origin pane's place")
		}
	})

	t.Run("a nested level namespaces its pane ids", func(t *testing.T) {
		m := New()
		w := baseWorld(gridPane("pane1", "g1"))
		w.LevelDepth = 1
		plan := m.Do(enterGesture("pane1", paneTile()), w)
		fetch := plan.Effects[1]
		row := paneTile()
		m.Resume(fetch.Token, Result{OK: true, Tile: &row}, w)
		e := only(t, m.Land(plan.Effects[0].Land, w), EffInstallLevel)
		if e.IDPrefix != "w2:" {
			t.Fatalf("id prefix = %q, want the second level's", e.IDPrefix)
		}
	})
}

func TestBootLevel(t *testing.T) {
	bootW := func() World {
		w := baseWorld(gridPane("pane1", "g1"))
		w.Home = "g1"
		w.Restore = &RestoreWorld{}
		return w
	}
	boot := func(t *testing.T, m *Machine) (Effect, World) {
		t.Helper()
		w := bootW()
		plan := m.Do(Gesture{Kind: GestureRestore, Raw: "/?w=pt1"}, w)
		return only(t, plan, EffAwait), w
	}

	t.Run("a reload opens the level with no outer tree", func(t *testing.T) {
		m := New()
		fetch, w := boot(t, m)
		row := arranged(paneTile())
		body := only(t, m.Resume(fetch.Token, Result{OK: true, Tile: &row}, w), EffAwait)
		plan := m.Resume(body.Token, Result{OK: true, Data: layoutBytes(t)}, w)
		// No barrier, so no origin place to put back: the swap alone.
		if !sameKinds(kinds(plan), installKinds[1:]) {
			t.Fatalf("effects = %v, want the swap with nothing to restore", kinds(plan))
		}
		e := only(t, plan, EffInstallLevel)
		if e.KeepOuter || e.Level.OriginPane != "" {
			t.Fatalf("effect = %+v, want no parked tree: nesting is session-only", e)
		}
	})

	t.Run("a never-arranged tile opens on its own grid", func(t *testing.T) {
		m := New()
		fetch, w := boot(t, m)
		row := paneTile()
		e := only(t, m.Resume(fetch.Token, Result{OK: true, Tile: &row}, w), EffInstallLevel)
		if e.Capture {
			t.Fatalf("a boot restore captured a window it never had")
		}
		p := e.Tree.FocusedPane()
		if p == nil || p.Anchor() != "g1" || p.Cx != 4 || p.Cy != 5 {
			t.Fatalf("landed at %+v, want the pane tile's grid, centred on the tile", p)
		}
	})

	t.Run("?w= naming something else says so", func(t *testing.T) {
		m := New()
		fetch, w := boot(t, m)
		row := rpc.Tile{ID: "pt1", Kind: rpc.KindText, GridID: "g1"}
		plan := m.Resume(fetch.Token, Result{OK: true, Tile: &row}, w)
		if !sameKinds(kinds(plan), []EffectKind{EffReport}) {
			t.Fatalf("effects = %v, want the notice alone", kinds(plan))
		}
	})
}

// levelWorld is the window inside one pane tile, with `outer` saying whether
// that level parked a tree.
func levelWorld(outer bool) World {
	w := baseWorld(gridPane("pane1", "g1"))
	lvl := &pane.Level{OriginPane: "pane1", TileID: "pt1", GridID: "g1"}
	if outer {
		lvl.OuterTree = pane.NewTree()
	}
	w.LevelDepth = 1
	w.LevelTop = lvl
	w.Home = "home"
	return w
}

func leaveGesture(count int) Gesture {
	return Gesture{Kind: GestureLeaveLevels, Count: count}
}

func TestLeaveLevels(t *testing.T) {
	t.Run("leaving nothing does nothing", func(t *testing.T) {
		if plan := New().Do(leaveGesture(0), levelWorld(true)); len(plan.Effects) != 0 {
			t.Fatalf("a crumb click on the level you are in acted: %v", kinds(plan))
		}
	})

	t.Run("a parked tree comes back and animates", func(t *testing.T) {
		m := New()
		plan := m.Do(leaveGesture(1), levelWorld(true))
		if !sameKinds(kinds(plan), []EffectKind{EffFlushLayout, EffCancelTransition,
			EffCloseMenu, EffFlushDroppedSubtree, EffPopLevel}) {
			t.Fatalf("effects = %v, want the hop", kinds(plan))
		}
		if e := only(t, plan, EffPopLevel); e.GridID != "" || e.OriginPane != "pane1" {
			t.Fatalf("pop = %+v, want the parked tree's own landing", e)
		}
		if plan.Next == nil || !plan.Next.Outer || !plan.Next.Animate {
			t.Fatalf("no animated landing asked for: %+v", plan.Next)
		}
		// The landing reads the tree the pop installed.
		after := baseWorld(gridPane("pane1", "g1"))
		after.Level = &LevelWorld{Tile: &rpc.Tile{ID: "pt1", GridID: "g1", X: 2, Y: 3, W: 4, H: 4}}
		land := m.Do(*plan.Next, after)
		if !sameKinds(kinds(land), []EffectKind{EffStartTransition, EffRefreshOverlay,
			EffScheduleURLUpdate}) {
			t.Fatalf("effects = %v, want the zoom out and the tail", kinds(land))
		}
		s := only(t, land, EffStartTransition).Segments[0]
		if s.FromCx != 4 || s.FromCy != 5 || s.ToCx != 0 || s.ToZoom != 1 {
			t.Fatalf("segment = %+v, want the tile's footprint back to the pane's viewport", s)
		}
	})

	t.Run("an uncached row lands instantly", func(t *testing.T) {
		m := New()
		plan := m.Do(leaveGesture(1), levelWorld(true))
		after := baseWorld(gridPane("pane1", "g1"))
		after.Level = &LevelWorld{}
		land := m.Do(*plan.Next, after)
		if !sameKinds(kinds(land), []EffectKind{EffRefreshOverlay, EffScheduleURLUpdate}) {
			t.Fatalf("effects = %v, want the tail alone", kinds(land))
		}
	})

	t.Run("the outer leaves re-engage", func(t *testing.T) {
		m := New()
		plan := m.Do(leaveGesture(1), levelWorld(true))
		after := baseWorld(contentPane("pane1", "t1"), gridPane("pane2", "g1"))
		after.Level = &LevelWorld{}
		e := only(t, m.Do(*plan.Next, after), EffReEngage)
		if e.PaneID != "pane1" || e.TileID != "t1" {
			t.Fatalf("re-engaged %+v, want the pane sitting in a content descent", e)
		}
	})

	t.Run("a level that parked no tree falls back and re-centres", func(t *testing.T) {
		m := New()
		plan := m.Do(leaveGesture(1), levelWorld(false))
		if !sameKinds(kinds(plan), []EffectKind{EffFlushLayout, EffCancelTransition,
			EffCloseMenu, EffFlushDroppedSubtree, EffPopLevel, EffFetchGrid}) {
			t.Fatalf("effects = %v, want the hop with a landing grid", kinds(plan))
		}
		if e := only(t, plan, EffPopLevel); e.GridID != "g1" {
			t.Fatalf("pop = %+v, want the pane tile's own grid", e)
		}
		after := baseWorld(gridPane("pane1", "g1"))
		land := m.Do(*plan.Next, after)
		aw := only(t, land, EffAwait)
		if aw.Request.Kind != RequestGetTile || aw.Request.ID != "pt1" {
			t.Fatalf("await = %+v, want the pane tile's row", aw.Request)
		}
		row := rpc.Tile{ID: "pt1", GridID: "g9", X: 2, Y: 3, W: 4, H: 4}
		plan = m.Resume(aw.Token, Result{OK: true, Tile: &row}, after)
		if !sameKinds(kinds(plan), []EffectKind{EffInstallPlace, EffFetchGrid,
			EffScheduleURLUpdate}) {
			t.Fatalf("effects = %v, want the re-centre", kinds(plan))
		}
		e := only(t, plan, EffInstallPlace)
		if e.Stack.Anchor() != "g9" || e.Stack.Cx != 4 || e.Stack.Cy != 5 {
			t.Fatalf("re-centred on %+v, want the tile in the grid it lives in", e.Stack)
		}
	})

	t.Run("the re-centre is skipped once the user has moved", func(t *testing.T) {
		m := New()
		plan := m.Do(leaveGesture(1), levelWorld(false))
		after := baseWorld(gridPane("pane1", "g1"))
		aw := only(t, m.Do(*plan.Next, after), EffAwait)
		// The user descended while the row was in flight: they win.
		moved := baseWorld(wellPane("pane1"))
		row := rpc.Tile{ID: "pt1", GridID: "g9", X: 2, Y: 3, W: 4, H: 4}
		if plan := m.Resume(aw.Token, Result{OK: true, Tile: &row}, moved); len(plan.Effects) != 0 {
			t.Fatalf("re-centred a pane the user had already moved: %v", kinds(plan))
		}
	})

	t.Run("only the last hop animates", func(t *testing.T) {
		m := New()
		w := levelWorld(true)
		w.LevelDepth = 2
		plan := m.Do(leaveGesture(2), w)
		if plan.Next == nil || plan.Next.Animate || plan.Next.Count != 1 {
			t.Fatalf("first hop landing = %+v, want an instant one with a hop owed", plan.Next)
		}
		after := baseWorld(gridPane("pane1", "g1"))
		after.Level = &LevelWorld{}
		land := m.Do(*plan.Next, after)
		if len(land.Effects) != 0 {
			t.Fatalf("the tail ran before the last hop: %v", kinds(land))
		}
		if land.Next == nil || land.Next.Kind != GestureLeaveLevels || land.Next.Count != 1 {
			t.Fatalf("next = %+v, want the remaining hop", land.Next)
		}
	})
}

func TestFollowLinkTarget(t *testing.T) {
	link := rpc.Tile{ID: "r1", Kind: rpc.KindURL, GridID: "g1", LinkTargetID: "u2/r9"}
	target := rpc.Tile{ID: "u2/r9", Kind: rpc.KindURL, GridID: "u2/1",
		URLString: "https://example.test/"}
	descended := baseWorld(contentPane("pane1", "r1"))

	t.Run("the view places on the row that owns the content", func(t *testing.T) {
		m := New()
		aw := only(t, m.Do(Gesture{Kind: GestureFollowLink, PaneID: "pane1", Door: link},
			descended), EffAwait)
		if aw.Request.ID != "u2/r9" {
			t.Fatalf("await = %+v, want the link's target", aw.Request)
		}
		e := only(t, m.Resume(aw.Token, Result{OK: true, Tile: &target}, descended), EffPlaceURLView)
		if e.PaneID != "pane1" || e.Tile.URLString != target.URLString {
			t.Fatalf("placed %+v, want the target row by value", e)
		}
	})

	t.Run("a pane that moved on gets no view", func(t *testing.T) {
		m := New()
		aw := only(t, m.Do(Gesture{Kind: GestureFollowLink, PaneID: "pane1", Door: link},
			descended), EffAwait)
		elsewhere := baseWorld(contentPane("pane1", "other"))
		if plan := m.Resume(aw.Token, Result{OK: true, Tile: &target}, elsewhere); len(plan.Effects) != 0 {
			t.Fatalf("placed a view over a pane that moved on: %v", kinds(plan))
		}
	})
}
