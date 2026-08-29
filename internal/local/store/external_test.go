package store

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"
)

// The externals' memory engine (external.go), ported from the retired
// internal/layout with its contract intact: idempotent merge, the user's
// placement wins, hints seed first sight only, authoritative absence
// retires and recreation mints fresh, framing persists, retire is the
// delete gesture, root views round-trip, and everything survives reopen.

func openExt(t *testing.T) (*Store, *Namespace) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, st.Namespace("plug1")
}

func textEntries(keys ...string) []Entry {
	out := make([]Entry, len(keys))
	for i, k := range keys {
		out[i] = Entry{Key: k, Kind: "text", Label: k}
	}
	return out
}

func extByKey(t *testing.T, tiles []ExtTile, key string) ExtTile {
	t.Helper()
	for _, tl := range tiles {
		if tl.Key == key {
			return tl
		}
	}
	t.Fatalf("no tile with key %q in %+v", key, tiles)
	return ExtTile{}
}

func TestExtMergeIsIdempotent(t *testing.T) {
	_, d := openExt(t)
	gid, err := d.ContextID("root")
	if err != nil {
		t.Fatal(err)
	}
	first, err := d.Merge(gid, textEntries("a", "b", "c"), true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.Merge(gid, textEntries("a", "b", "c"), true)
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
	if a := extByKey(t, first, "a"); a.X != 0 || a.Y != 0 {
		t.Fatalf("a at (%d,%d), want (0,0)", a.X, a.Y)
	}
	if b := extByKey(t, first, "b"); b.X != 1 || b.Y != 0 {
		t.Fatalf("b at (%d,%d), want (1,0)", b.X, b.Y)
	}
}

// Externals share the tables with home but never its reads: a plugin's
// context grid is invisible through the home door, and home's grid holds
// no plugin rows.
func TestExtRowsAreInvisibleToHome(t *testing.T) {
	st, d := openExt(t)
	gid, _ := d.ContextID("root")
	if _, err := d.Merge(gid, textEntries("a"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetGrid(t.Context(), itoa(gid)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("home GetGrid of a plugin context = %v, want ErrNotFound", err)
	}
	root, _ := st.RootGridID(t.Context())
	g, err := st.GetGrid(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range g.Tiles {
		if tl.AltText == "a" {
			t.Fatal("a plugin row leaked into home's root grid")
		}
	}
}

func TestExtUserPlacementSurvivesMerges(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("root")
	tiles, err := d.Merge(gid, textEntries("a", "b"), true)
	if err != nil {
		t.Fatal(err)
	}
	a := extByKey(t, tiles, "a")
	if err := d.Place(a.ID, 5, 3, 2, 2); err != nil {
		t.Fatal(err)
	}
	tiles, err = d.Merge(gid, textEntries("a", "b", "c", "d"), true)
	if err != nil {
		t.Fatal(err)
	}
	a2 := extByKey(t, tiles, "a")
	if a2.X != 5 || a2.Y != 3 || a2.W != 2 || a2.H != 2 {
		t.Fatalf("user placement lost: %+v", a2)
	}
	for _, key := range []string{"c", "d"} {
		n := extByKey(t, tiles, key)
		if n.X >= 5 && n.X < 7 && n.Y >= 3 && n.Y < 5 {
			t.Fatalf("%s auto-placed inside a's footprint at (%d,%d)", key, n.X, n.Y)
		}
	}
}

func TestExtHintSeedsFirstSightOnly(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("cal")
	ev := []Entry{{Key: "event1", Kind: "text", Label: "e", Hint: &Hint{X: 10, Y: 2, W: 2, H: 1}}}
	tiles, err := d.Merge(gid, ev, true)
	if err != nil {
		t.Fatal(err)
	}
	e := extByKey(t, tiles, "event1")
	if e.X != 10 || e.Y != 2 || e.W != 2 {
		t.Fatalf("hint not honored at first sight: %+v", e)
	}
	if err := d.Place(e.ID, 0, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
	ev[0].Hint = &Hint{X: 20, Y: 20, W: 1, H: 1}
	tiles, err = d.Merge(gid, ev, true)
	if err != nil {
		t.Fatal(err)
	}
	if e := extByKey(t, tiles, "event1"); e.X != 0 || e.Y != 0 {
		t.Fatalf("a hint moved a placed tile: %+v", e)
	}
}

func TestExtAuthoritativeAbsenceRetiresAndRecreationMintsFresh(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("root")
	tiles, _ := d.Merge(gid, textEntries("a", "b"), true)
	oldA := extByKey(t, tiles, "a")
	tiles, err := d.Merge(gid, textEntries("b"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tiles) != 1 || tiles[0].Key != "b" {
		t.Fatalf("retired row still served: %+v", tiles)
	}
	_, key, tomb, err := d.TileKey(oldA.ID)
	if err != nil || key != "a" || !tomb {
		t.Fatalf("retired row lost interpretability: key=%q tomb=%v err=%v", key, tomb, err)
	}
	if err := d.Place(oldA.ID, 1, 1, 1, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mutating a retired row: err=%v, want ErrNotFound", err)
	}
	tiles, err = d.Merge(gid, textEntries("a", "b"), true)
	if err != nil {
		t.Fatal(err)
	}
	if newA := extByKey(t, tiles, "a"); newA.ID == oldA.ID {
		t.Fatal("recreated key reused the retired id")
	}
	if retired, _ := d.RetiredKeys(gid); !retired["a"] {
		t.Fatal("the retired key must stay listed as retired")
	}
}

func TestExtNonAuthoritativeAbsenceKeepsTheRow(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("procroot")
	tiles, _ := d.Merge(gid, textEntries("pid:1", "pid:2"), false)
	p2 := extByKey(t, tiles, "pid:2")
	if err := d.Place(p2.ID, 4, 4, 1, 1); err != nil {
		t.Fatal(err)
	}
	tiles, err := d.Merge(gid, textEntries("pid:1"), false)
	if err != nil {
		t.Fatal(err)
	}
	if kept := extByKey(t, tiles, "pid:2"); kept.ID != p2.ID || kept.X != 4 || kept.Y != 4 {
		t.Fatalf("non-authoritative absence dropped state: %+v", kept)
	}
}

func TestExtFramingPersistsAndFactsRefresh(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("root")
	listing := []Entry{{Key: "dir", Kind: "well", Label: "dir", ChildContext: "root/dir"}, {Key: "f", Kind: "text", Label: "f"}, {Key: "u", Kind: "url", Label: "u", URL: "https://a"}}
	tiles, err := d.Merge(gid, listing, true)
	if err != nil {
		t.Fatal(err)
	}
	dir, f := extByKey(t, tiles, "dir"), extByKey(t, tiles, "f")
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
	listing[1].Label = "f renamed"
	tiles, _ = d.Merge(gid, listing, true)
	dir2, f2 := extByKey(t, tiles, "dir"), extByKey(t, tiles, "f")
	if dir2.ViewX != 3 || dir2.ViewY != -1 || dir2.ViewZoom != 1.5 {
		t.Fatalf("well framing lost: %+v", dir2)
	}
	if f2.TextY != 120 || f2.TextW != 400 || f2.TextMode != "rendered" || f2.ContentZoom != 1.25 || f2.Label != "f renamed" {
		t.Fatalf("text framing/facts lost: %+v", f2)
	}
	if cgid, err := d.ContextID("root/dir"); err != nil || cgid != dir.ChildGridID {
		t.Fatalf("child context re-minted: %d != %d (%v)", cgid, dir.ChildGridID, err)
	}
	if key, err := d.ContextKey(dir.ChildGridID); err != nil || key != "root/dir" {
		t.Fatalf("ContextKey = %q (%v)", key, err)
	}
}

func TestExtRetireIsTheDeleteGesture(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("root")
	tiles, _ := d.Merge(gid, textEntries("a"), true)
	a := extByKey(t, tiles, "a")
	if err := d.Retire(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.Retire(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double retire: %v, want ErrNotFound", err)
	}
}

func TestExtRootViewAndListings(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("root")
	if _, _, _, ok, err := d.RootView(gid); err != nil || ok {
		t.Fatalf("fresh context claims a root view: ok=%v err=%v", ok, err)
	}
	if err := d.SetRootView(gid, 1.5, -2.25, 0.8); err != nil {
		t.Fatal(err)
	}
	if cx, cy, zoom, ok, err := d.RootView(gid); err != nil || !ok || cx != 1.5 || cy != -2.25 || zoom != 0.8 {
		t.Fatalf("root view round trip: %v %v %v ok=%v err=%v", cx, cy, zoom, ok, err)
	}
	if _, _, ok, err := d.CachedListing(gid); err != nil || ok {
		t.Fatalf("fresh listing: ok=%v err=%v", ok, err)
	}
	if err := d.CacheListing(gid, []byte{1, 2}, true); err != nil {
		t.Fatal(err)
	}
	if blob, auth, ok, err := d.CachedListing(gid); err != nil || !ok || !auth || len(blob) != 2 {
		t.Fatalf("listing round trip: %v %v %v %v", blob, auth, ok, err)
	}
}

func TestExtReopenKeepsEverything(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gridwell.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d := st.Namespace("plug1")
	gid, _ := d.ContextID("root")
	tiles, _ := d.Merge(gid, textEntries("a"), true)
	a := extByKey(t, tiles, "a")
	if err := d.Place(a.ID, 2, 2, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	d2 := st2.Namespace("plug1")
	gid2, _ := d2.ContextID("root")
	if gid2 != gid {
		t.Fatalf("context re-minted across reopen: %d != %d", gid2, gid)
	}
	tiles, err = d2.Merge(gid2, textEntries("a"), true)
	if err != nil {
		t.Fatal(err)
	}
	if a2 := extByKey(t, tiles, "a"); a2.ID != a.ID || a2.X != 2 || a2.Y != 2 {
		t.Fatalf("arrangement lost across reopen: %+v", a2)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
