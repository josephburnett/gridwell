package menu

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// TestTemplatesNewestFirst pins the strip's order: newest first, by the row
// id home mints in creation order, so 10 is newer than 9.
func TestTemplatesNewestFirst(t *testing.T) {
	g := map[string]*gridwellv1.Tile{
		"n/9":  {Id: "n/9"},
		"n/10": {Id: "n/10"},
		"n/2":  {Id: "n/2"},
	}
	got := Templates(g)
	want := []string{"n/10", "n/9", "n/2"}
	if len(got) != len(want) {
		t.Fatalf("Templates = %d rows, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].Id != id {
			t.Errorf("Templates[%d] = %s, want %s", i, got[i].Id, id)
		}
	}
	if len(Templates(nil)) != 0 {
		t.Error("an unread grid has templates")
	}
}

// TestFitKeepsTheShape pins that a 2x1 tile fills its slot's width at half
// its height, centered, and a 1x1 fills the slot.
func TestFitKeepsTheShape(t *testing.T) {
	slot := Rect{X: 10, Y: 20, W: 48, H: 48}
	cell, at := Fit(slot, 2, 1)
	if cell != 24 || at != (Rect{X: 10, Y: 32, W: 48, H: 24}) {
		t.Errorf("Fit(2x1) = %v, %+v", cell, at)
	}
	if cell, at := Fit(slot, 0, 0); cell != 48 || at != slot {
		t.Errorf("Fit(unsized) = %v, %+v, want the slot", cell, at)
	}
}
