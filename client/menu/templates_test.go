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

// TestTemplateCellLandsAboveEveryTile pins that a new template takes cells
// no template holds, its own height above the topmost.
func TestTemplateCellLandsAboveEveryTile(t *testing.T) {
	if x, y := TemplateCell(nil, 1); x != 0 || y != 0 {
		t.Errorf("the first template lands at (%d,%d), want the origin", x, y)
	}
	g := map[string]*gridwellv1.Tile{"a": {X: 3, Y: -2, W: 2, H: 2}, "b": {X: 0, Y: 4, W: 1, H: 1}}
	if x, y := TemplateCell(g, 3); x != 0 || y != -5 {
		t.Errorf("TemplateCell = (%d,%d), want (0,-5): three tall above y=-2", x, y)
	}
}
