package menu

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/client/cache"
)

// TestTemplatesNewestFirst pins the strip's order: newest first, by the row
// id home mints in creation order, so 10 is newer than 9, and a copy still
// pending newer than every minted row.
func TestTemplatesNewestFirst(t *testing.T) {
	c := cache.New()
	c.PutGrid(&gridwellv1.Grid{Id: "n/1"}, []*gridwellv1.Tile{
		{Id: "n/9", GridId: "n/1"}, {Id: "n/10", GridId: "n/1"}, {Id: "n/2", GridId: "n/1"}})
	c.Pend(&gridwellv1.Tile{GridId: "n/1"})
	g, _ := c.Grid("n/1")
	got := Templates(g.Drawn())
	var pending string
	for id := range g.Pending {
		pending = id
	}
	want := []string{pending, "n/10", "n/9", "n/2"}
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
	g := []*gridwellv1.Tile{{X: 3, Y: -2, W: 2, H: 2}, {X: 0, Y: 4, W: 1, H: 1}}
	if x, y := TemplateCell(g, 3); x != 0 || y != -5 {
		t.Errorf("TemplateCell = (%d,%d), want (0,-5): three tall above y=-2", x, y)
	}
}
