package store

import (
	"errors"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
)

// A row the plugin moved to another grid keeps its id under its new key and
// leaves the old grid's selection, and a stale claim moves nothing.
func TestExtRekeyKeepsTheIDAndLeavesTheSelection(t *testing.T) {
	_, d := openExt(t)
	from, _ := d.GridOf("src")
	to, _ := d.GridOf("dst")
	id, err := d.Mint(from, &pluginv1.Entry{Key: "src/a", Kind: "text", Label: "a"}, 0, 1, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetSelection(from, SelectOp{Add: []int64{id}}); err != nil {
		t.Fatal(err)
	}
	stale := &gridwellv1.Placement{X: 9, Y: 9, W: 1, H: 1}
	if _, err := d.Move(id, to, "dst/a", 2, 2, 1, 1, stale); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale claim: err = %v, want a conflict", err)
	}
	if changed, err := d.Move(id, to, "dst/a", 2, 2, 1, 1, &gridwellv1.Placement{X: 1, Y: 1, W: 1, H: 1}); err != nil || !changed {
		t.Fatalf("Move: changed %v, err %v", changed, err)
	}
	if got, ok, err := d.LiveTileID(to, "dst/a"); err != nil || !ok || got != id {
		t.Fatalf("the moved row is %d (%v, %v), want %d", got, ok, err, id)
	}
	if _, ok, _ := d.LiveTileID(from, "src/a"); ok {
		t.Fatal("the old grid still names the moved row")
	}
	if sel, err := d.Selection(from); err != nil || len(sel) != 0 {
		t.Fatalf("old grid's selection = %v (%v), want empty", sel, err)
	}
	tiles := mintAll(t, d, to, textEntries("dst/a"), true)
	if r := extByKey(t, tiles, "dst/a"); r.ID != id || r.X != 2 || r.Y != 2 {
		t.Fatalf("listed after the move: %+v, want row %d at 2,2", r, id)
	}
}
