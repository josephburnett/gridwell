package wsbar

import (
	"testing"

	"github.com/josephburnett/gridwell/client/pane"
)

// A bar 400 wide: a pane-tile boundary and two chain crumbs from the left,
// the slot at the right end, and the title between them.
func barClick(button int, x float64) Click {
	return Click{
		Button: button, X: x, Zone: ZoneBar, BarW: 400,
		Segments: []Segment{
			{Index: 0, X: 0, W: BoundaryW},
			{Index: 1, X: BoundaryW, W: RowH},
			{Index: 2, X: BoundaryW + RowH, W: RowH},
		},
		Chain: []pane.NavCrumb{
			{PaneTile: true, WsLevel: 1},
			{Crumb: pane.Crumb{TileID: "w1"}},
			{Crumb: pane.Crumb{TileID: "w2"}},
		},
		TitleX: 200, TitleW: 80, TitleOK: true,
	}
}

func TestRouteClick(t *testing.T) {
	cases := []struct {
		name  string
		in    Click
		want  Action
		index int // the segment the action names; -1 for none
	}{
		{"a press outside the band belongs to a pane",
			Click{Zone: ZoneOutside}, ActionPass, -1},
		{"a press beside the bar is swallowed",
			Click{Zone: ZoneBand}, ActionNone, -1},
		{"a left press on the slot runs the slot",
			barClick(0, 380), ActionSlot, -1},
		{"a middle press on the slot still runs the slot, which ignores it",
			barClick(1, 380), ActionSlot, -1},
		{"a right press on a slot with no menu does nothing",
			barClick(2, 380), ActionNone, -1},
		{"a left press on the title zooms the pane",
			barClick(0, 210), ActionZoom, -1},
		{"a middle press on the title does nothing",
			barClick(1, 210), ActionNone, -1},
		{"a right press on the title renames it",
			barClick(2, 210), ActionRename, -1},
		{"a left press on a pane-tile crumb leaves its levels",
			barClick(0, 10), ActionLeaveLevels, 0},
		{"a right press on a pane-tile crumb renames the workspace",
			barClick(2, 10), ActionWorkspaceRename, 0},
		{"a left press on a chain crumb ascends to it",
			barClick(0, BoundaryW+2), ActionAscend, 1},
		{"a right press on a chain crumb does nothing",
			barClick(2, BoundaryW+2), ActionNone, -1},
		{"a middle press on a chain crumb does nothing",
			barClick(1, BoundaryW+2), ActionNone, -1},
		{"a left press on empty band space is swallowed",
			barClick(0, 190), ActionNone, -1},
		{"a right press on empty band space is swallowed",
			barClick(2, 190), ActionNone, -1},
	}
	for _, c := range cases {
		got := RouteClick(c.in)
		wantSeg := Segment{}
		if c.index >= 0 {
			wantSeg = c.in.Segments[c.index]
		}
		if got.Action != c.want || got.Segment != wantSeg {
			t.Errorf("%s: RouteClick = %+v, want %v on %+v", c.name, got, c.want, wantSeg)
		}
	}
}

// The slot is the bar's right end whatever the chain does, so the circle's
// menu is reachable there and nowhere else, and only on the right button.
func TestRouteClickSlotMenu(t *testing.T) {
	in := barClick(2, 380)
	in.SlotMenu = true
	if got := RouteClick(in); got.Action != ActionSlotMenu {
		t.Fatalf("RouteClick(right on a slot with a menu) = %v, want %v", got.Action, ActionSlotMenu)
	}
	in = barClick(0, 380)
	in.SlotMenu = true
	if got := RouteClick(in); got.Action != ActionSlot {
		t.Fatalf("RouteClick(left on a live url slot) = %v, want %v", got.Action, ActionSlot)
	}
}

// Only the current crumb of an ephemeral visit is a drag handle; the crumbs
// above it stay ascents, and without the visit it is an ascent too.
func TestRouteClickPromote(t *testing.T) {
	in := barClick(0, BoundaryW+RowH+2)
	in.Promote = true
	if got := RouteClick(in); got.Action != ActionPromote || got.Segment.Index != 2 {
		t.Fatalf("RouteClick(current crumb of a visit) = %+v, want promote on 2", got)
	}
	in = barClick(0, BoundaryW+2)
	in.Promote = true
	if got := RouteClick(in); got.Action != ActionAscend || got.Segment.Index != 1 {
		t.Fatalf("RouteClick(crumb above a visit) = %+v, want ascend on 1", got)
	}
	if got := RouteClick(barClick(0, BoundaryW+RowH+2)); got.Action != ActionAscend {
		t.Fatalf("RouteClick(current crumb, no visit) = %v, want %v", got.Action, ActionAscend)
	}
}

// A title that did not fit leaves its span to the crumbs beneath it rather
// than eating presses at the bar's center.
func TestRouteClickNoTitle(t *testing.T) {
	in := barClick(0, 210)
	in.TitleOK = false
	if got := RouteClick(in); got.Action != ActionNone {
		t.Fatalf("RouteClick(no title) = %v, want %v", got.Action, ActionNone)
	}
}

// The leading root crumb inside a pane tile pops to the session, so it leaves
// levels like a boundary crumb and never ascends in the tree.
func TestRouteClickCloseOnly(t *testing.T) {
	in := barClick(0, BoundaryW+2)
	in.Chain[1].CloseOnly = true
	if got := RouteClick(in); got.Action != ActionLeaveLevels {
		t.Fatalf("RouteClick(close-only crumb) = %v, want %v", got.Action, ActionLeaveLevels)
	}
}
