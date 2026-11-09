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
