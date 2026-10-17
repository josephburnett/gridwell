package gesture

import (
	"slices"
	"testing"
)

// Shift is the select key and the only one: shift + ctrl or shift + cmd is the
// split chord's, and a content descent has no grid to select in.
func TestClassifySelect(t *testing.T) {
	tests := []struct {
		name string
		in   SelectInput
		want SelectPress
	}{
		{"plain press on a tile", SelectInput{GridPlace: true, OverTile: true}, SelectNone},
		{"plain press on the background pans", SelectInput{GridPlace: true}, SelectNone},
		{"shift on a tile toggles it", SelectInput{Shift: true, GridPlace: true, OverTile: true}, SelectToggle},
		{"shift on the background sweeps a band", SelectInput{Shift: true, GridPlace: true}, SelectBand},
		{"ctrl alone is the split, never a select", SelectInput{Ctrl: true, GridPlace: true, OverTile: true}, SelectNone},
		{"cmd alone is no alias", SelectInput{Meta: true, GridPlace: true, OverTile: true}, SelectNone},
		{"shift + ctrl is not a select", SelectInput{Shift: true, Ctrl: true, GridPlace: true, OverTile: true}, SelectNone},
		{"shift + cmd is not a select", SelectInput{Shift: true, Meta: true, GridPlace: true}, SelectNone},
		{"a content descent has no grid", SelectInput{Shift: true, OverTile: true}, SelectNone},
	}
	for _, c := range tests {
		if got := ClassifySelect(c.in); got != c.want {
			t.Errorf("%s: ClassifySelect(%+v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// A shift-tap names one tile: it adds the tile when the shown set lacks it
// and removes it when the set holds it, and touches nothing else.
func TestToggleNamesOneTile(t *testing.T) {
	if got := Toggle(nil, "n/4"); !slices.Equal(got.Add, []string{"n/4"}) || got.Remove != nil {
		t.Errorf("toggle into empty = %+v", got)
	}
	if got := Toggle([]string{"n/5", "n/4"}, "n/4"); got.Add != nil || !slices.Equal(got.Remove, []string{"n/4"}) {
		t.Errorf("toggle out = %+v", got)
	}
	if got := Toggle([]string{"n/5"}, "n/3").Apply([]string{"n/5"}); !slices.Equal(got, []string{"n/3", "n/5"}) {
		t.Errorf("toggle in, applied = %v", got)
	}
}

// Esc removes the set the pane shows, and nothing it does not show.
func TestClearRemovesTheShownSet(t *testing.T) {
	got := Clear([]string{"n/5", "n/4"})
	if got.Add != nil || !slices.Equal(got.Remove, []string{"n/4", "n/5"}) {
		t.Errorf("Clear = %+v", got)
	}
	if !Clear(nil).Empty() {
		t.Error("clearing nothing named something")
	}
	// Another writer's tile, selected after this pane last drew, stays.
	if got := Clear([]string{"n/4"}).Apply([]string{"n/4", "n/9"}); !slices.Equal(got, []string{"n/9"}) {
		t.Errorf("Clear applied over a newer set = %v, want [n/9]", got)
	}
}

// The band is cell-snapped, spans both ends whichever way the drag went, and
// takes a tile only when every cell of it is inside: touching is not enough.
func TestSweepTakesOnlyTilesWhollyInside(t *testing.T) {
	b := BandOf(3, 2, 0, 0) // dragged up and left: cells 0..3 by 0..2
	if b != (Band{X: 0, Y: 0, W: 4, H: 3}) {
		t.Fatalf("BandOf = %+v", b)
	}
	tiles := []Placed{
		{ID: "in", X: 1, Y: 1, W: 1, H: 1},
		{ID: "edge", X: 0, Y: 0, W: 4, H: 3},   // exactly the band
		{ID: "touch", X: 3, Y: 1, W: 2, H: 1},  // one cell in, one out
		{ID: "below", X: 1, Y: 3, W: 1, H: 1},  // adjacent, outside
		{ID: "over", X: -1, Y: -1, W: 6, H: 5}, // covers the band
		{ID: "corner", X: 3, Y: 2, W: 1, H: 2}, // reaches past the bottom
	}
	got := Sweep([]string{"kept"}, b, tiles)
	if want := []string{"edge", "in"}; !slices.Equal(got.Add, want) || got.Remove != nil {
		t.Errorf("Sweep = %+v, want it to add %v and remove nothing", got, want)
	}
	if got := Sweep([]string{"in"}, b, tiles[:1]); !got.Empty() {
		t.Errorf("a sweep over a selected tile named it again: %+v", got)
	}
	one := BandOf(5, 5, 5, 5)
	if one != (Band{X: 5, Y: 5, W: 1, H: 1}) || !one.Holds(5, 5, 1, 1) || one.Holds(5, 5, 2, 1) {
		t.Errorf("a one-cell band = %+v", one)
	}
}

// A select adds only tiles on the grid now; a remove needs no such check.
func TestOnGridDropsAddsThatLeft(t *testing.T) {
	here := map[string]bool{"a": true}
	got := Select{Add: []string{"a", "gone"}, Remove: []string{"x"}}.OnGrid(func(id string) bool { return here[id] })
	if !slices.Equal(got.Add, []string{"a"}) || !slices.Equal(got.Remove, []string{"x"}) {
		t.Errorf("OnGrid = %+v", got)
	}
}
