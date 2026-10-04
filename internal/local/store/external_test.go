package store

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// The externals' memory engine: the join writes nothing, the user's placement
// wins, hints seed first sight only, authoritative absence retires and
// recreation mints fresh, and everything survives reopen.

func openExt(t *testing.T) (*Store, *Namespace) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, st.Namespace("plug1")
}

func textEntries(keys ...string) []*pluginv1.Entry {
	out := make([]*pluginv1.Entry, len(keys))
	for i, k := range keys {
		out[i] = &pluginv1.Entry{Key: k, Kind: "text", Label: k}
	}
	return out
}

// sameExt compares what the join decides about an entry: identity, placement
// and the content facts the listing supplied. A row read back from the store
// carries more columns than a derived placement does, so the wire record is
// not compared whole.
func sameExt(a, b ExtTile) bool {
	return a.ID == b.ID && a.Key == b.Key && a.ChildGridID == b.ChildGridID &&
		a.X == b.X && a.Y == b.Y && a.W == b.W && a.H == b.H &&
		a.Kind == b.Kind && a.AltText == b.AltText
}

// mintAll sweeps, joins, and mints every entry the join derived. Nothing in
// the product does this, a row costing a durable touch, but a test about
// placement or retirement wants rows for every key, at exactly the placements
// the join derived.
func mintAll(t *testing.T, d *Namespace, gid int64, entries []*pluginv1.Entry, authoritative bool) []ExtTile {
	t.Helper()
	if authoritative {
		present := map[string]bool{}
		for _, e := range entries {
			present[e.Key] = true
		}
		if err := d.Sweep(gid, present); err != nil {
			t.Fatal(err)
		}
	}
	tiles, err := d.Overlay(gid, entries)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]*pluginv1.Entry{}
	for _, e := range entries {
		byKey[e.Key] = e
	}
	for i, tl := range tiles {
		if tl.ID != 0 {
			continue
		}
		e := byKey[tl.Key]
		var child int64
		if e.ChildContext != "" {
			if child, err = d.ContextID(e.ChildContext); err != nil {
				t.Fatal(err)
			}
		}
		id, err := d.Mint(gid, e, child, tl.X, tl.Y, tl.W, tl.H)
		if err != nil {
			t.Fatal(err)
		}
		tiles[i].ID, tiles[i].ChildGridID = id, child
	}
	return tiles
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
	first := mintAll(t, d, gid, textEntries("a", "b", "c"), true)
	second := mintAll(t, d, gid, textEntries("a", "b", "c"), true)
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("want 3 tiles, got %d then %d", len(first), len(second))
	}
	for i := range first {
		if !sameExt(first[i], second[i]) {
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
	mintAll(t, d, gid, textEntries("a"), true)
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
	tiles := mintAll(t, d, gid, textEntries("a", "b"), true)
	a := extByKey(t, tiles, "a")
	if err := d.Place(a.ID, 5, 3, 2, 2); err != nil {
		t.Fatal(err)
	}
	tiles = mintAll(t, d, gid, textEntries("a", "b", "c", "d"), true)
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
	ev := []*pluginv1.Entry{{Key: "event1", Kind: "text", Label: "e", PlacementHint: &pluginv1.PlacementHint{X: 10, Y: 2, W: 2, H: 1}}}
	tiles := mintAll(t, d, gid, ev, true)
	e := extByKey(t, tiles, "event1")
	if e.X != 10 || e.Y != 2 || e.W != 2 {
		t.Fatalf("hint not honored at first sight: %+v", e)
	}
	if err := d.Place(e.ID, 0, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
	ev[0].PlacementHint = &pluginv1.PlacementHint{X: 20, Y: 20, W: 1, H: 1}
	tiles = mintAll(t, d, gid, ev, true)
	if e := extByKey(t, tiles, "event1"); e.X != 0 || e.Y != 0 {
		t.Fatalf("a hint moved a placed tile: %+v", e)
	}
}

func TestExtAuthoritativeAbsenceRetiresAndRecreationMintsFresh(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("root")
	tiles := mintAll(t, d, gid, textEntries("a", "b"), true)
	oldA := extByKey(t, tiles, "a")
	tiles = mintAll(t, d, gid, textEntries("b"), true)
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
	tiles = mintAll(t, d, gid, textEntries("a", "b"), true)
	if newA := extByKey(t, tiles, "a"); newA.ID == oldA.ID {
		t.Fatal("recreated key reused the retired id")
	}
}

func TestExtNonAuthoritativeAbsenceKeepsTheRow(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("procroot")
	tiles := mintAll(t, d, gid, textEntries("pid:1", "pid:2"), false)
	p2 := extByKey(t, tiles, "pid:2")
	if err := d.Place(p2.ID, 4, 4, 1, 1); err != nil {
		t.Fatal(err)
	}
	tiles = mintAll(t, d, gid, textEntries("pid:1"), false)
	if kept := extByKey(t, tiles, "pid:2"); kept.ID != p2.ID || kept.X != 4 || kept.Y != 4 {
		t.Fatalf("non-authoritative absence dropped state: %+v", kept)
	}
}

func TestExtFramingPersistsAndFactsRefresh(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("root")
	listing := []*pluginv1.Entry{{Key: "dir", Kind: "well", Label: "dir", ChildContext: "root/dir"}, {Key: "f", Kind: "text", Label: "f"}, {Key: "u", Kind: "url", Label: "u", UrlString: "https://a"}}
	tiles := mintAll(t, d, gid, listing, true)
	dir, f := extByKey(t, tiles, "dir"), extByKey(t, tiles, "f")
	if dir.ChildGridID == 0 {
		t.Fatal("well minted no child grid")
	}
	if err := d.SetFraming(dir.ID, 0, mkFraming(3, -1, 1.5)); err != nil {
		t.Fatal(err)
	}
	if err := d.SetTextView(f.ID, 0, 120, 400, 300, "rendered"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetContentZoom(f.ID, zoomOf(1.25)); err != nil {
		t.Fatal(err)
	}
	listing[1].Label = "f renamed"
	tiles = mintAll(t, d, gid, listing, true)
	dir2, f2 := extByKey(t, tiles, "dir"), extByKey(t, tiles, "f")
	if dir2.ViewCx != 3 || dir2.ViewCy != -1 || dir2.ViewZoom != 1.5 {
		t.Fatalf("well framing lost: %+v", dir2)
	}
	if f2.TextY != 120 || f2.TextW != 400 || f2.TextMode != "rendered" || f2.ContentZoom != 1.25 || f2.AltText != "f renamed" {
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
	tiles := mintAll(t, d, gid, textEntries("a"), true)
	a := extByKey(t, tiles, "a")
	if err := d.Retire(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.Retire(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double retire: %v, want ErrNotFound", err)
	}
}

func TestExtRootFraming(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("root")
	if v, err := d.RootFraming(gid); err != nil || !v.SameAs(rpc.View{}) {
		t.Fatalf("fresh context claims a root view: %+v err=%v", v, err)
	}
	if err := d.SetFraming(0, gid, mkFraming(1.5, -2.25, 0.8)); err != nil {
		t.Fatal(err)
	}
	v, err := d.RootFraming(gid)
	if f, ok := v.Framing(); err != nil || !ok || f.Cx() != 1.5 || f.Cy() != -2.25 || f.Zoom() != 0.8 {
		t.Fatalf("root framing round trip: %+v err=%v", v, err)
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
	tiles := mintAll(t, d, gid, textEntries("a"), true)
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
	tiles = mintAll(t, d2, gid2, textEntries("a"), true)
	if a2 := extByKey(t, tiles, "a"); a2.ID != a.ID || a2.X != 2 || a2.Y != 2 {
		t.Fatalf("arrangement lost across reopen: %+v", a2)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// rowCount is every tile row in the file, whatever the namespace: the number
// a listing must not move.
func rowCount(t *testing.T, st *Store) int {
	t.Helper()
	var n int
	if err := st.SQL().QueryRow(`SELECT count(*) FROM tiles`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The join is a read. Listing a hundred entries a hundred times mints
// nothing, and the placements it derives are exactly the ones Mint stores, so
// the tile a user finally touches does not move under the touch.
func TestExtOverlayWritesNothingAndMintKeepsThePlacement(t *testing.T) {
	st, d := openExt(t)
	gid, _ := d.ContextID("root")
	keys := make([]string, 100)
	for i := range keys {
		keys[i] = "f" + itoa(int64(i))
	}
	entries := textEntries(keys...)
	before := rowCount(t, st)
	var first []ExtTile
	for pass := range 3 {
		tiles, err := d.Overlay(gid, entries)
		if err != nil {
			t.Fatal(err)
		}
		if len(tiles) != 100 {
			t.Fatalf("pass %d joined %d entries, want 100", pass, len(tiles))
		}
		for _, tl := range tiles {
			if tl.ID != 0 {
				t.Fatalf("pass %d: entry %q was minted by a listing (id %d)", pass, tl.Key, tl.ID)
			}
		}
		if pass == 0 {
			first = tiles
			continue
		}
		for i := range tiles {
			if !sameExt(tiles[i], first[i]) {
				t.Fatalf("pass %d derived a different answer than pass 0: %v vs %v", pass, tiles[i], first[i])
			}
		}
	}
	if got := rowCount(t, st); got != before {
		t.Fatalf("listing wrote %d rows; the join must write none", got-before)
	}
	// One durable touch: the row lands where the join was already answering.
	want := extByKey(t, first, "f42")
	id, err := d.Mint(gid, &pluginv1.Entry{Key: "f42", Kind: "text", Label: "f42"}, 0, want.X, want.Y, want.W, want.H)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowCount(t, st); got != before+1 {
		t.Fatalf("mint wrote %d rows, want 1", got-before)
	}
	after, err := d.Overlay(gid, entries)
	if err != nil {
		t.Fatal(err)
	}
	got := extByKey(t, after, "f42")
	if got.ID != id || got.X != want.X || got.Y != want.Y {
		t.Fatalf("minting moved the tile: derived %+v, minted %+v", want, got)
	}
	for _, tl := range after {
		if tl.Key != "f42" && tl.ID != 0 {
			t.Fatalf("minting one entry minted %q too", tl.Key)
		}
	}
	// Idempotent: a second mint of the same key is the same row.
	again, err := d.Mint(gid, &pluginv1.Entry{Key: "f42", Kind: "text", Label: "f42"}, 0, 9, 9, 1, 1)
	if err != nil || again != id {
		t.Fatalf("second mint = %d (%v), want the first row %d", again, err, id)
	}
}

// Dragging one tile must not rearrange the room: a minted entry keeps its slot
// in the derived flow and overrides it, so its neighbours' cells are the same
// before and after.
func TestMintingOneEntryLeavesItsNeighboursWhereTheyWere(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("root")
	entries := textEntries("a", "b", "c", "d", "e")
	before, err := d.Overlay(gid, entries)
	if err != nil {
		t.Fatal(err)
	}
	// The user drags "b" well clear of the flow.
	b := extByKey(t, before, "b")
	if _, err := d.Mint(gid, &pluginv1.Entry{Key: "b", Kind: "text", Label: "b"}, 0, b.X, b.Y, b.W, b.H); err != nil {
		t.Fatal(err)
	}
	id, _, err := d.LiveTileID(gid, "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Place(id, 3, 4, 1, 1); err != nil {
		t.Fatal(err)
	}
	after, err := d.Overlay(gid, entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "c", "d", "e"} {
		was, now := extByKey(t, before, key), extByKey(t, after, key)
		if was.X != now.X || was.Y != now.Y {
			t.Fatalf("%q moved because a neighbour was dragged: (%d,%d) → (%d,%d)",
				key, was.X, was.Y, now.X, now.Y)
		}
	}
	if got := extByKey(t, after, "b"); got.X != 3 || got.Y != 4 {
		t.Fatalf("the dragged tile is at (%d,%d), want (3,4)", got.X, got.Y)
	}
}

// A touch mints the row where the entry already stood, so the entries after
// it in the flow must not step past it: an entry's own row is its slot, not
// an obstacle.
func TestMintingInPlaceLeavesItsNeighboursWhereTheyWere(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("root")
	entries := textEntries("a", "b", "c", "d", "e")
	before, err := d.Overlay(gid, entries)
	if err != nil {
		t.Fatal(err)
	}
	b := extByKey(t, before, "b")
	if _, err := d.Mint(gid, &pluginv1.Entry{Key: "b", Kind: "text", Label: "b"}, 0, b.X, b.Y, b.W, b.H); err != nil {
		t.Fatal(err)
	}
	after, err := d.Overlay(gid, entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b", "c", "d", "e"} {
		was, now := extByKey(t, before, key), extByKey(t, after, key)
		if was.X != now.X || was.Y != now.Y {
			t.Fatalf("%q moved because a neighbour was touched in place: (%d,%d) → (%d,%d)",
				key, was.X, was.Y, now.X, now.Y)
		}
	}
}

func hinted(key string, x, y, w, h int64) *pluginv1.Entry {
	return &pluginv1.Entry{Key: key, Kind: "text", Label: key, PlacementHint: &pluginv1.PlacementHint{X: x, Y: y, W: w, H: h}}
}

// overlapping names the first two tiles that share a cell, or "".
func overlapping(tiles []ExtTile) string {
	owner := map[[2]int64]string{}
	for _, tl := range tiles {
		for dx := range max(tl.W, 1) {
			for dy := range max(tl.H, 1) {
				c := [2]int64{tl.X + dx, tl.Y + dy}
				if o, ok := owner[c]; ok {
					return o + " and " + tl.Key
				}
				owner[c] = tl.Key
			}
		}
	}
	return ""
}

// A hint is a preference: a hinted rect that meets a stored row moves to the
// first free rect of its size below the hint, instead of landing on the row.
func TestHintOntoAStoredRowStacksBelowIt(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("week")
	mintAll(t, d, gid, []*pluginv1.Entry{hinted("old", 0, 0, 2, 1)}, false)
	tiles, err := d.Overlay(gid, []*pluginv1.Entry{hinted("new", 0, 0, 2, 1), hinted("old", 0, 1, 2, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if o := overlapping(tiles); o != "" {
		t.Fatalf("%s overlap: %+v", o, tiles)
	}
	if n := extByKey(t, tiles, "new"); n.X != 0 || n.Y != 1 || n.W != 2 {
		t.Fatalf("new = %+v, want the first free 2×1 below the hint, (0,1)", n)
	}
	if o := extByKey(t, tiles, "old"); o.X != 0 || o.Y != 0 {
		t.Fatalf("the stored row moved: %+v", o)
	}
}

func TestTwoEntriesWithOneHintGetTwoRects(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("week")
	tiles, err := d.Overlay(gid, []*pluginv1.Entry{hinted("a", 3, 2, 1, 1), hinted("b", 3, 2, 1, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if o := overlapping(tiles); o != "" {
		t.Fatalf("%s overlap: %+v", o, tiles)
	}
	if a := extByKey(t, tiles, "a"); a.X != 3 || a.Y != 2 {
		t.Fatalf("a = %+v, want its hint", a)
	}
	if b := extByKey(t, tiles, "b"); b.X != 3 || b.Y != 3 {
		t.Fatalf("b = %+v, want (3,3), below a", b)
	}
}

// The reported shape: rows minted one cell wide, then hints two cells wide. A
// 2-wide hint whose second cell is a stored row stacks below it.
func TestTwoWideHintBesideAOneWideRowStacksBelowIt(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("week")
	mintAll(t, d, gid, []*pluginv1.Entry{hinted("tue", 1, 0, 1, 1)}, false)
	tiles, err := d.Overlay(gid, []*pluginv1.Entry{hinted("mon", 0, 0, 2, 1), hinted("tue", 2, 0, 2, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if o := overlapping(tiles); o != "" {
		t.Fatalf("%s overlap: %+v", o, tiles)
	}
	if m := extByKey(t, tiles, "mon"); m.X != 0 || m.Y != 1 || m.W != 2 {
		t.Fatalf("mon = %+v, want (0,1) 2 wide", m)
	}
}

func TestUnhintedEntriesStillFlowFromTheCursor(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.ContextID("mixed")
	entries := append(textEntries("a"), hinted("h", 0, 0, 1, 1), hinted("h2", 0, 0, 1, 1))
	entries = append(entries, textEntries("b")...)
	tiles, err := d.Overlay(gid, entries)
	if err != nil {
		t.Fatal(err)
	}
	if o := overlapping(tiles); o != "" {
		t.Fatalf("%s overlap: %+v", o, tiles)
	}
	want := map[string][2]int64{"a": {0, 0}, "h": {0, 1}, "h2": {0, 2}, "b": {1, 0}}
	for k, xy := range want {
		if tl := extByKey(t, tiles, k); tl.X != xy[0] || tl.Y != xy[1] {
			t.Fatalf("%s = (%d,%d), want %v", k, tl.X, tl.Y, xy)
		}
	}
}
