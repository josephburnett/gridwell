package wsbar

import (
	"maps"
	"slices"
	"testing"

	"github.com/josephburnett/gridwell/client/pane"
)

// A bar 400 wide: a workspace tile boundary and two chain crumbs from the left,
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
			{Workspace: true, WsLevel: 1, TileID: "ws", GridID: "g/0"},
			{Crumb: pane.Crumb{TileID: "w1", ParentAnchor: "g/1"}},
			{Crumb: pane.Crumb{TileID: "w2", ParentAnchor: "g/1", ParentPath: []string{"w1"}}},
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
		{"a left press beside the bar focuses the column above it",
			Click{Zone: ZoneBand, Button: 0}, ActionFocusColumn, -1},
		{"a right press beside the bar does nothing",
			Click{Zone: ZoneBand, Button: 2}, ActionNone, -1},
		{"a middle press beside the bar does nothing",
			Click{Zone: ZoneBand, Button: 1}, ActionNone, -1},
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
		{"a left press on a workspace tile crumb leaves its levels",
			barClick(0, 10), ActionLeaveLevels, 0},
		{"a right press on a workspace tile crumb renames the workspace",
			barClick(2, 10), ActionWorkspaceRename, 0},
		{"a left press on a chain crumb ascends to it",
			barClick(0, BoundaryW+2), ActionAscend, 1},
		{"a right press on a chain crumb does nothing",
			barClick(2, BoundaryW+2), ActionNone, -1},
		{"a middle press on a chain crumb does nothing",
			barClick(1, BoundaryW+2), ActionNone, -1},
		{"a left press beside the title, past the crumbs, zooms like the name",
			barClick(0, 190), ActionZoom, -1},
		{"a left press just past the last crumb zooms",
			barClick(0, BoundaryW+2*RowH), ActionZoom, -1},
		{"a left press just short of the slot zooms",
			barClick(0, 400-SlotW-0.5), ActionZoom, -1},
		{"a left press at the title's right edge, off the text, zooms",
			barClick(0, 300), ActionZoom, -1},
		{"a right press beside the title is swallowed, never a rename",
			barClick(2, 190), ActionNone, -1},
		{"a right press just short of the slot is swallowed",
			barClick(2, 400-SlotW-0.5), ActionNone, -1},
		{"a middle press beside the title does nothing",
			barClick(1, 190), ActionNone, -1},
		{"a left press on the last crumb's right edge still ascends",
			barClick(0, BoundaryW+2*RowH-0.5), ActionAscend, 2},
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

// The crumb standing for an ephemeral visit's row is its drag handle for
// either button, wherever it sits in the chain, a level's boundary band
// included (Joe, 2026-11-11); a release with no drag is the crumb's own
// click; every other crumb keeps its action, and without a visit the crumb
// is an ascent.
func TestRouteClickVisitDrag(t *testing.T) {
	cases := []struct {
		at                  int
		x                   float64
		bareLeft, bareRight Action
	}{
		{0, 10, ActionLeaveLevels, ActionWorkspaceRename},
		{1, BoundaryW + 2, ActionAscend, ActionNone},
		{2, BoundaryW + RowH + 2, ActionAscend, ActionNone},
	}
	for _, c := range cases {
		for _, button := range []int{0, 2} {
			in := barClick(button, c.x)
			in.Visits = map[int]string{c.at: "v"}
			bare := c.bareLeft
			if button == 2 {
				bare = c.bareRight
			}
			if got := RouteClick(in); got.Action != ActionVisitDrag || got.Segment.Index != c.at || got.Bare != bare {
				t.Fatalf("RouteClick(button %d on the visit's crumb at %d) = %+v, want its drag, bare %v", button, c.at, got, bare)
			}
		}
	}
	in := barClick(0, BoundaryW+2)
	in.Visits = map[int]string{2: "v"}
	if got := RouteClick(in); got.Action != ActionAscend || got.Segment.Index != 1 {
		t.Fatalf("RouteClick(crumb above a visit) = %+v, want ascend on 1", got)
	}
	if got := RouteClick(barClick(0, BoundaryW+RowH+2)); got.Action != ActionAscend {
		t.Fatalf("RouteClick(current crumb, no visit) = %v, want %v", got.Action, ActionAscend)
	}
	if got := RouteClick(barClick(2, BoundaryW+RowH+2)); got.Action != ActionNone {
		t.Fatalf("RouteClick(right on a crumb, no visit) = %v, want %v", got.Action, ActionNone)
	}
}

// The visits in a chain: a frame entered through an ephemeral row, wherever
// it sits, and the innermost level's boundary when its workspace tile is
// ephemeral; an outer level's boundary is never a handle (Joe, 2026-11-11).
func TestVisitsInChain(t *testing.T) {
	chain := []pane.NavCrumb{
		{CloseOnly: true},
		{Workspace: true, WsLevel: 1, TileID: "ws1"},
		{Workspace: true, WsLevel: 2, TileID: "ws2"},
		{Crumb: pane.Crumb{Level: 0, Anchor: "g/1"}},
		{Crumb: pane.Crumb{Level: 1, TileID: "d1"}},
	}
	frames := []pane.Frame{{}, {Door: "d1"}}
	eph := func(ids ...string) func(string, bool) bool {
		return func(id string, _ bool) bool { return slices.Contains(ids, id) }
	}
	cases := []struct {
		name string
		eph  func(string, bool) bool
		want map[int]string
	}{
		{"nothing ephemeral", eph(), map[int]string{}},
		{"the innermost level's tile", eph("ws2"), map[int]string{2: "ws2"}},
		{"an outer level's tile only", eph("ws1"), map[int]string{}},
		{"a door visit inside the level", eph("d1"), map[int]string{4: "d1"}},
		{"both", eph("ws2", "d1"), map[int]string{2: "ws2", 4: "d1"}},
	}
	for _, c := range cases {
		if got := Visits(chain, frames, 2, c.eph); !maps.Equal(got, c.want) {
			t.Errorf("%s: Visits = %v, want %v", c.name, got, c.want)
		}
	}
	var level []bool
	Visits(chain, frames, 2, func(id string, isLevel bool) bool { level = append(level, isLevel); return false })
	if !slices.Equal(level, []bool{true, false}) {
		t.Errorf("asked level %v, want the innermost boundary as a level, then the door as a frame", level)
	}
}

// A title that did not fit leaves no name to press, on its span or beside it.
func TestRouteClickNoTitle(t *testing.T) {
	for _, x := range []float64{210, 190} {
		in := barClick(0, x)
		in.TitleOK = false
		if got := RouteClick(in); got.Action != ActionNone {
			t.Fatalf("RouteClick(no title, x=%v) = %v, want %v", x, got.Action, ActionNone)
		}
	}
}

// With no crumbs the name's space starts at the bar's left edge.
func TestRouteClickNameAreaWithoutCrumbs(t *testing.T) {
	in := barClick(0, 2)
	in.Segments, in.Chain = nil, nil
	if got := RouteClick(in); got.Action != ActionZoom {
		t.Fatalf("RouteClick(left at x=2, no crumbs) = %v, want %v", got.Action, ActionZoom)
	}
	in.Button = 2
	if got := RouteClick(in); got.Action != ActionNone {
		t.Fatalf("RouteClick(right at x=2, no crumbs) = %v, want %v", got.Action, ActionNone)
	}
}

// The leading root crumb inside a workspace tile pops to the session, so it leaves
// levels like a boundary crumb and never ascends in the tree.
func TestRouteClickCloseOnly(t *testing.T) {
	in := barClick(0, BoundaryW+2)
	in.Chain[1].CloseOnly = true
	if got := RouteClick(in); got.Action != ActionLeaveLevels {
		t.Fatalf("RouteClick(close-only crumb) = %v, want %v", got.Action, ActionLeaveLevels)
	}
}

// Shift alone with the left button selects, as on a grid: on a crumb that
// stands for a tile it toggles that tile, never ascending or leaving a level,
// and anywhere else in the band it does nothing, so a touch Select, which
// arrives as this press, acts only where it toggles.
func TestRouteClickSelect(t *testing.T) {
	shift := func(in Click) Click { in.Shift = true; return in }
	grid := barClick(0, BoundaryW+2)
	grid.Chain = slices.Clone(grid.Chain)
	grid.Chain[1] = pane.NavCrumb{Crumb: pane.Crumb{Anchor: "g/1", ParentAnchor: "g/1"}}
	closeOnly := barClick(0, 10)
	closeOnly.Chain = slices.Clone(closeOnly.Chain)
	closeOnly.Chain[0] = pane.NavCrumb{CloseOnly: true, Crumb: pane.Crumb{Anchor: "g/0", ParentAnchor: "g/0"}}
	visit := barClick(0, BoundaryW+RowH+2)
	visit.Visits = map[int]string{2: "w2"}
	cases := []struct {
		name string
		in   Click
		want Action
		tile string // the tile the crumb's SelectTarget names, for a toggle
	}{
		{"shift on a door crumb toggles its tile", shift(barClick(0, BoundaryW+2)), ActionSelect, "w1"},
		{"shift on the current crumb toggles its tile, not the visit's drag", shift(visit), ActionSelect, "w2"},
		{"shift on a workspace tile crumb toggles the workspace tile", shift(barClick(0, 10)), ActionSelect, "ws"},
		{"shift on a grid crumb does nothing", shift(grid), ActionNone, ""},
		{"shift on the close-only root does nothing", shift(closeOnly), ActionNone, ""},
		{"shift on the title does nothing", shift(barClick(0, 210)), ActionNone, ""},
		{"shift on the slot does nothing", shift(barClick(0, 380)), ActionNone, ""},
		{"shift beside the bar does nothing", Click{Zone: ZoneBand, Shift: true}, ActionNone, ""},
		{"shift outside the band is the pane's", Click{Zone: ZoneOutside, Shift: true}, ActionPass, ""},
	}
	for _, c := range cases {
		got := RouteClick(c.in)
		tile := ""
		if got.Action == ActionSelect {
			tile, _, _, _ = c.in.Chain[got.Segment.Index].SelectTarget()
		}
		if got.Action != c.want || tile != c.tile {
			t.Errorf("%s: RouteClick = %+v (tile %q), want %v on %q", c.name, got, tile, c.want, c.tile)
		}
	}
	// Shift with ctrl or cmd is another gesture's chord, so the crumb ascends as
	// without it.
	for _, chord := range []func(*Click){func(c *Click) { c.Ctrl = true }, func(c *Click) { c.Meta = true }} {
		in := shift(barClick(0, BoundaryW+2))
		chord(&in)
		if got := RouteClick(in); got.Action != ActionAscend {
			t.Errorf("shift with a chord on a crumb: RouteClick = %v, want %v", got.Action, ActionAscend)
		}
	}
	// Shift with any other button is that button's press.
	if got := RouteClick(shift(barClick(2, 10))); got.Action != ActionWorkspaceRename {
		t.Errorf("shift + right on a workspace tile crumb = %v, want %v", got.Action, ActionWorkspaceRename)
	}
}
