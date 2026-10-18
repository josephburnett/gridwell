package nav

import (
	"testing"

	"github.com/josephburnett/gridwell/client/pane"
)

// A workspace tile's layout is the user's bytes: the level remembers the version
// its tree derives from, and a refused write puts the arrangement on disk in
// place of the open tree, whole.

func TestLevelInstallCarriesTheVersionItsTreeDerivesFrom(t *testing.T) {
	m := New()
	row := arranged(workspace())
	row.Version = 4
	anim, fetch, w := enter(t, m, row)
	m.Land(anim.Land, w)
	plan := m.Resume(fetch.Token, Result{OK: true, Tile: row}, w)
	body := only(t, plan, EffAwait)
	plan = m.Resume(body.Token, Result{OK: true, Data: layoutBytes(t), Version: 5}, w)
	if e := only(t, plan, EffInstallLevel); e.Level.Basis != 5 {
		t.Fatalf("basis = %d, want 5, the version the bytes were read at", e.Level.Basis)
	}
}

func TestACaptureClaimsTheRowsVersion(t *testing.T) {
	m := New()
	row := workspace()
	row.Version = 3
	anim, fetch, w := enter(t, m, row)
	m.Land(anim.Land, w)
	plan := m.Resume(fetch.Token, Result{OK: true, Tile: row}, w)
	if e := only(t, plan, EffInstallLevel); e.Level.Basis != 3 {
		t.Fatalf("basis = %d, want the row's 3", e.Level.Basis)
	}
}

func openLevelWorld(tileID string, depth int) World {
	w := baseWorld(gridPane("w1:p1", "g2"))
	w.LevelDepth = depth
	w.LevelTop = &pane.Level{TileID: tileID, GridID: "g1", Basis: 4}
	return w
}

func TestReconcileReplacesTheTreeWithTheArrangementOnDisk(t *testing.T) {
	m := New()
	w := openLevelWorld("pt1", 1)
	plan := m.Do(Gesture{Kind: GestureReconcileLevel, TileID: "pt1"}, w)
	await := only(t, plan, EffAwait)
	if await.Request.Kind != RequestReadLayout || await.Request.ID != "pt1" {
		t.Fatalf("await = %+v, want the layout on disk", await.Request)
	}
	disk := layoutBytes(t)
	plan = m.Resume(await.Token, Result{OK: true, Data: disk, Version: 6}, w)
	want := []EffectKind{EffCancelTransition, EffCloseMenu, EffFlushDroppedSubtree,
		EffReplaceLevel, EffRefreshOverlay, EffScheduleURLUpdate}
	if !sameKinds(kinds(plan), want) {
		t.Fatalf("effects = %v, want %v: no layout flush, the refused bytes stay unsent", kinds(plan), want)
	}
	e := only(t, plan, EffReplaceLevel)
	if e.Tree == nil || e.Level.Basis != 6 || e.Level.ReadOnly || string(e.Baseline) != string(disk) {
		t.Fatalf("replace = %+v, want the disk tree at version 6 as the baseline", e)
	}
	if got := e.Tree.FocusedPane().ID; got[:3] != "w1:" {
		t.Fatalf("pane id %q, want the open level's prefix", got)
	}
	if plan.Next == nil || plan.Next.Kind != GestureEngageLeaves {
		t.Fatalf("next = %+v, want the new leaves engaged", plan.Next)
	}
}

func TestReconcileIsDroppedOnceTheLevelIsLeft(t *testing.T) {
	m := New()
	if plan := m.Do(Gesture{Kind: GestureReconcileLevel, TileID: "pt1"}, baseWorld(gridPane("p1", "g1"))); len(plan.Effects) != 0 {
		t.Fatalf("reconciled with no level open: %v", kinds(plan))
	}
	w := openLevelWorld("pt1", 1)
	plan := m.Do(Gesture{Kind: GestureReconcileLevel, TileID: "pt1"}, w)
	await := only(t, plan, EffAwait)
	left := baseWorld(gridPane("p1", "g1"))
	if plan := m.Resume(await.Token, Result{OK: true, Data: layoutBytes(t), Version: 6}, left); len(plan.Effects) != 0 {
		t.Fatalf("replaced a tree after the level was left: %v", kinds(plan))
	}
}

func TestReconcileOfAnUnreadableBlobLatchesReadOnly(t *testing.T) {
	m := New()
	w := openLevelWorld("pt1", 1)
	await := only(t, m.Do(Gesture{Kind: GestureReconcileLevel, TileID: "pt1"}, w), EffAwait)
	plan := m.Resume(await.Token, Result{OK: true, Data: []byte("not a layout"), Version: 6}, w)
	e := only(t, plan, EffReplaceLevel)
	if e.Tree != nil || !e.Level.ReadOnly {
		t.Fatalf("replace = %+v, want the tree kept and the level read-only", e)
	}
}
