package layout

import (
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func entries(keys ...string) []Entry {
	out := make([]Entry, len(keys))
	for i, k := range keys {
		out[i] = Entry{Key: k, Kind: "text", Label: k}
	}
	return out
}

func tileByKey(t *testing.T, tiles []Tile, key string) Tile {
	t.Helper()
	for _, tl := range tiles {
		if tl.Key == key {
			return tl
		}
	}
	t.Fatalf("no tile with key %q in %+v", key, tiles)
	return Tile{}
}

func TestMergeIsIdempotent(t *testing.T) {
	d := openTest(t)
	gid, err := d.ContextID("root")
	if err != nil {
		t.Fatal(err)
	}
	first, err := d.Merge(gid, entries("a", "b", "c"), true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.Merge(gid, entries("a", "b", "c"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("want 3 tiles, got %d then %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("merge not idempotent: %+v != %+v", first[i], second[i])
		}
	}
	// First-sight auto-place: listing order, first free cells.
	if a := tileByKey(t, first, "a"); a.X != 0 || a.Y != 0 {
		t.Fatalf("a at (%d,%d), want (0,0)", a.X, a.Y)
	}
	if b := tileByKey(t, first, "b"); b.X != 1 || b.Y != 0 {
		t.Fatalf("b at (%d,%d), want (1,0)", b.X, b.Y)
	}
}

func TestUserPlacementSurvivesMerges(t *testing.T) {
	d := openTest(t)
	gid, _ := d.ContextID("root")
	tiles, err := d.Merge(gid, entries("a", "b"), true)
	if err != nil {
		t.Fatal(err)
	}
	a := tileByKey(t, tiles, "a")
	// The user drags a to (5,3) and grows it 2×2.
	if err := d.Place(a.ID, 5, 3, 2, 2); err != nil {
		t.Fatal(err)
	}
	// New entries arrive; a stays put, and nothing lands INSIDE a's
	// footprint (the full-footprint occupancy rule, #265).
	tiles, err = d.Merge(gid, entries("a", "b", "c", "d"), true)
	if err != nil {
		t.Fatal(err)
	}
	a2 := tileByKey(t, tiles, "a")
	if a2.X != 5 || a2.Y != 3 || a2.W != 2 || a2.H != 2 {
		t.Fatalf("user placement lost: %+v", a2)
	}
	for _, key := range []string{"c", "d"} {
		n := tileByKey(t, tiles, key)
		inside := n.X >= 5 && n.X < 7 && n.Y >= 3 && n.Y < 5
		if inside {
			t.Fatalf("%s auto-placed inside a's footprint at (%d,%d)", key, n.X, n.Y)
		}
	}
}

func TestHintSeedsFirstSightOnly(t *testing.T) {
	d := openTest(t)
	gid, _ := d.ContextID("cal")
	ev := []Entry{{Key: "event1", Kind: "text", Label: "e", Hint: &Hint{X: 10, Y: 2, W: 2, H: 1}}}
	tiles, err := d.Merge(gid, ev, true)
	if err != nil {
		t.Fatal(err)
	}
	e := tileByKey(t, tiles, "event1")
	if e.X != 10 || e.Y != 2 || e.W != 2 {
		t.Fatalf("hint not honored at first sight: %+v", e)
	}
	// The user moves it; the plugin's hint changes; the USER wins.
	if err := d.Place(e.ID, 0, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
	ev[0].Hint = &Hint{X: 20, Y: 20, W: 1, H: 1}
	tiles, err = d.Merge(gid, ev, true)
	if err != nil {
		t.Fatal(err)
	}
	if e := tileByKey(t, tiles, "event1"); e.X != 0 || e.Y != 0 {
		t.Fatalf("a hint moved a placed tile: %+v", e)
	}
}

func TestAuthoritativeAbsenceRetiresAndRecreationMintsFresh(t *testing.T) {
	d := openTest(t)
	gid, _ := d.ContextID("root")
	tiles, _ := d.Merge(gid, entries("a", "b"), true)
	oldA := tileByKey(t, tiles, "a")

	// a vanishes from an authoritative listing: retired.
	tiles, err := d.Merge(gid, entries("b"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tiles) != 1 || tiles[0].Key != "b" {
		t.Fatalf("retired row still served: %+v", tiles)
	}
	// The retired id stays interpretable…
	_, key, tomb, err := d.TileKey(oldA.ID)
	if err != nil || key != "a" || !tomb {
		t.Fatalf("retired row lost interpretability: key=%q tomb=%v err=%v", key, tomb, err)
	}
	// …refuses mutation…
	if err := d.Place(oldA.ID, 1, 1, 1, 1); err != ErrNotFound {
		t.Fatalf("mutating a retired row: err=%v, want ErrNotFound", err)
	}
	// …and a recreated "a" is a NEW thing with a FRESH id (the legacy
	// fs identity rule: delete + recreate ≠ the same file).
	tiles, err = d.Merge(gid, entries("a", "b"), true)
	if err != nil {
		t.Fatal(err)
	}
	newA := tileByKey(t, tiles, "a")
	if newA.ID == oldA.ID {
		t.Fatal("recreated key reused the retired id")
	}
}

func TestNonAuthoritativeAbsenceKeepsTheRow(t *testing.T) {
	d := openTest(t)
	gid, _ := d.ContextID("procroot")
	tiles, _ := d.Merge(gid, entries("pid:1", "pid:2"), false)
	p2 := tileByKey(t, tiles, "pid:2")
	if err := d.Place(p2.ID, 4, 4, 1, 1); err != nil {
		t.Fatal(err)
	}
	// pid:2 unreadable this pass — absent from a NON-authoritative
	// listing. Its remembered row (and arrangement) keeps serving.
	tiles, err := d.Merge(gid, entries("pid:1"), false)
	if err != nil {
		t.Fatal(err)
	}
	kept := tileByKey(t, tiles, "pid:2")
	if kept.ID != p2.ID || kept.X != 4 || kept.Y != 4 {
		t.Fatalf("non-authoritative absence dropped state: %+v", kept)
	}
}

func TestFramingPersists(t *testing.T) {
	d := openTest(t)
	gid, _ := d.ContextID("root")
	tiles, _ := d.Merge(gid, []Entry{{Key: "dir", Kind: "well", ChildContext: "root/dir"}, {Key: "f", Kind: "text"}}, true)
	dir := tileByKey(t, tiles, "dir")
	f := tileByKey(t, tiles, "f")
	if dir.ChildGridID == 0 {
		t.Fatal("well minted no child grid")
	}
	if err := d.SetWellView(dir.ID, 3, -1, 1.5); err != nil {
		t.Fatal(err)
	}
	if err := d.SetTextView(f.ID, 0, 120, 400, 300, "rendered"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetContentZoom(f.ID, 1.25); err != nil {
		t.Fatal(err)
	}
	tiles, _ = d.Merge(gid, []Entry{{Key: "dir", Kind: "well", ChildContext: "root/dir"}, {Key: "f", Kind: "text"}}, true)
	dir2, f2 := tileByKey(t, tiles, "dir"), tileByKey(t, tiles, "f")
	if dir2.ViewX != 3 || dir2.ViewY != -1 || dir2.ViewZoom != 1.5 {
		t.Fatalf("well framing lost: %+v", dir2)
	}
	if f2.TextY != 120 || f2.TextW != 400 || f2.TextMode != "rendered" || f2.ContentZoom != 1.25 {
		t.Fatalf("text framing lost: %+v", f2)
	}
	// The child context resolves stably.
	cgid, err := d.ContextID("root/dir")
	if err != nil || cgid != dir.ChildGridID {
		t.Fatalf("child context re-minted: %d != %d (%v)", cgid, dir.ChildGridID, err)
	}
}

func TestRetireIsTheDeleteGesture(t *testing.T) {
	d := openTest(t)
	gid, _ := d.ContextID("root")
	tiles, _ := d.Merge(gid, entries("a"), true)
	a := tileByKey(t, tiles, "a")
	if err := d.Retire(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.Retire(a.ID); err != ErrNotFound {
		t.Fatalf("double retire: %v, want ErrNotFound", err)
	}
}

func TestRootView(t *testing.T) {
	d := openTest(t)
	gid, _ := d.ContextID("root")
	if _, _, _, ok, err := d.RootView(gid); err != nil || ok {
		t.Fatalf("fresh context claims a root view: ok=%v err=%v", ok, err)
	}
	if err := d.SetRootView(gid, 1.5, -2.25, 0.8); err != nil {
		t.Fatal(err)
	}
	cx, cy, zoom, ok, err := d.RootView(gid)
	if err != nil || !ok || cx != 1.5 || cy != -2.25 || zoom != 0.8 {
		t.Fatalf("root view round trip: %v %v %v ok=%v err=%v", cx, cy, zoom, ok, err)
	}
}

func TestReopenKeepsEverything(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mem.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	gid, _ := d.ContextID("root")
	tiles, _ := d.Merge(gid, entries("a"), true)
	a := tileByKey(t, tiles, "a")
	if err := d.Place(a.ID, 2, 2, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	gid2, _ := d2.ContextID("root")
	if gid2 != gid {
		t.Fatalf("context re-minted across reopen: %d != %d", gid2, gid)
	}
	tiles, err = d2.Merge(gid2, entries("a"), true)
	if err != nil {
		t.Fatal(err)
	}
	a2 := tileByKey(t, tiles, "a")
	if a2.ID != a.ID || a2.X != 2 || a2.Y != 2 {
		t.Fatalf("arrangement lost across reopen: %+v", a2)
	}
}
