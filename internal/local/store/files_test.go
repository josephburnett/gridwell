package store

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// The files' rows engine: the join writes nothing, the user's placement
// wins, authoritative absence retires and recreation mints fresh, and
// everything survives reopen.

func openExt(t *testing.T) (*Store, *FileRows) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, st.Files()
}

func textEntries(keys ...string) []Entry {
	out := make([]Entry, len(keys))
	for i, k := range keys {
		out[i] = Entry{Key: k, Kind: "text", Label: k}
	}
	return out
}

// sameExt compares what the join decides about an entry: identity, placement
// and the content facts the listing supplied. A row read back from the store
// carries more columns than a derived placement does, so the wire record is
// not compared whole.
func sameExt(a, b FileTile) bool {
	return a.ID == b.ID && a.Key == b.Key && a.ChildGridID == b.ChildGridID &&
		a.X == b.X && a.Y == b.Y && a.W == b.W && a.H == b.H &&
		a.Kind == b.Kind && a.AltText == b.AltText
}

// mintAll sweeps, joins, and mints every entry the join derived. Nothing in
// the product does this, a row costing a durable touch, but a test about
// placement or retirement wants rows for every key, at exactly the placements
// the join derived.
func mintAll(t *testing.T, d *FileRows, gid int64, entries []Entry, authoritative bool) []FileTile {
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
	byKey := map[string]Entry{}
	for _, e := range entries {
		byKey[e.Key] = e
	}
	for i, tl := range tiles {
		if tl.ID != 0 {
			continue
		}
		e := byKey[tl.Key]
		var child int64
		if e.Kind == "door" {
			if child, err = d.GridOf("root/" + e.Key); err != nil {
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

func fileByKey(t *testing.T, tiles []FileTile, key string) FileTile {
	t.Helper()
	for _, tl := range tiles {
		if tl.Key == key {
			return tl
		}
	}
	t.Fatalf("no tile with key %q in %+v", key, tiles)
	return FileTile{}
}

func TestExtMergeIsIdempotent(t *testing.T) {
	_, d := openExt(t)
	gid, err := d.GridOf("root")
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
	if a := fileByKey(t, first, "a"); a.X != 0 || a.Y != 0 {
		t.Fatalf("a at (%d,%d), want (0,0)", a.X, a.Y)
	}
	if b := fileByKey(t, first, "b"); b.X != 1 || b.Y != 0 {
		t.Fatalf("b at (%d,%d), want (1,0)", b.X, b.Y)
	}
}

// The files' rows share the tables with home but never its reads: a file's
// grid is invisible through home's reads, and home's grid holds
// no file rows.
func TestExtRowsAreInvisibleToHome(t *testing.T) {
	st, d := openExt(t)
	gid, _ := d.GridOf("root")
	mintAll(t, d, gid, textEntries("a"), true)
	if _, err := st.GetGrid(t.Context(), itoa(gid)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("home GetGrid of a directory grid = %v, want ErrNotFound", err)
	}
	root, _ := st.RootGridID(t.Context())
	g, err := st.GetGrid(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range g.Tiles {
		if tl.AltText == "a" {
			t.Fatal("a file row leaked into home's root grid")
		}
	}
}

func TestExtUserPlacementSurvivesMerges(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.GridOf("root")
	tiles := mintAll(t, d, gid, textEntries("a", "b"), true)
	a := fileByKey(t, tiles, "a")
	if _, err := d.Move(a.ID, 0, "", 5, 3, 2, 2, nil); err != nil {
		t.Fatal(err)
	}
	tiles = mintAll(t, d, gid, textEntries("a", "b", "c", "d"), true)
	a2 := fileByKey(t, tiles, "a")
	if a2.X != 5 || a2.Y != 3 || a2.W != 2 || a2.H != 2 {
		t.Fatalf("user placement lost: %+v", a2)
	}
	for _, key := range []string{"c", "d"} {
		n := fileByKey(t, tiles, key)
		if n.X >= 5 && n.X < 7 && n.Y >= 3 && n.Y < 5 {
			t.Fatalf("%s landed inside a's footprint at (%d,%d)", key, n.X, n.Y)
		}
	}
}

func TestExtAuthoritativeAbsenceRetiresAndRecreationMintsFresh(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.GridOf("root")
	tiles := mintAll(t, d, gid, textEntries("a", "b"), true)
	oldA := fileByKey(t, tiles, "a")
	tiles = mintAll(t, d, gid, textEntries("b"), true)
	if len(tiles) != 1 || tiles[0].Key != "b" {
		t.Fatalf("retired row still served: %+v", tiles)
	}
	_, key, tomb, err := d.TileKey(oldA.ID)
	if err != nil || key != "a" || !tomb {
		t.Fatalf("retired row lost interpretability: key=%q tomb=%v err=%v", key, tomb, err)
	}
	if _, err := d.Move(oldA.ID, 0, "", 1, 1, 1, 1, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mutating a retired row: err=%v, want ErrNotFound", err)
	}
	tiles = mintAll(t, d, gid, textEntries("a", "b"), true)
	if newA := fileByKey(t, tiles, "a"); newA.ID == oldA.ID {
		t.Fatal("recreated key reused the retired id")
	}
}

func TestExtNonAuthoritativeAbsenceKeepsTheRow(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.GridOf("procroot")
	tiles := mintAll(t, d, gid, textEntries("pid:1", "pid:2"), false)
	p2 := fileByKey(t, tiles, "pid:2")
	if _, err := d.Move(p2.ID, 0, "", 4, 4, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	tiles = mintAll(t, d, gid, textEntries("pid:1"), false)
	if kept := fileByKey(t, tiles, "pid:2"); kept.ID != p2.ID || kept.X != 4 || kept.Y != 4 {
		t.Fatalf("non-authoritative absence dropped state: %+v", kept)
	}
}

func TestExtFramingPersistsAndFactsRefresh(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.GridOf("root")
	listing := []Entry{{Key: "dir", Kind: "door", Label: "dir"}, {Key: "f", Kind: "text", Label: "f"}, {Key: "u", Kind: "url", Label: "u", URL: "https://a"}}
	tiles := mintAll(t, d, gid, listing, true)
	dir, f := fileByKey(t, tiles, "dir"), fileByKey(t, tiles, "f")
	if dir.ChildGridID == 0 {
		t.Fatal("door minted no child grid")
	}
	if _, err := d.SetFraming(dir.ID, 0, mkFraming(3, -1, 1.5)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetTextScroll(f.ID, 0, 120, 400, 300, "rendered"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetContentZoom(f.ID, zoomOf(1.25)); err != nil {
		t.Fatal(err)
	}
	listing[1].Label = "f renamed"
	tiles = mintAll(t, d, gid, listing, true)
	dir2, f2 := fileByKey(t, tiles, "dir"), fileByKey(t, tiles, "f")
	if dir2.ViewCx != 3 || dir2.ViewCy != -1 || dir2.ViewZoom != 1.5 {
		t.Fatalf("door framing lost: %+v", dir2)
	}
	if f2.TextY != 120 || f2.TextW != 400 || f2.TextMode != "rendered" || f2.ContentZoom != 1.25 || f2.AltText != "f renamed" {
		t.Fatalf("text framing/facts lost: %+v", f2)
	}
	if cgid, err := d.GridOf("root/dir"); err != nil || cgid != dir.ChildGridID {
		t.Fatalf("child grid re-minted: %d != %d (%v)", cgid, dir.ChildGridID, err)
	}
	if key, err := d.GridKey(dir.ChildGridID); err != nil || key != "root/dir" {
		t.Fatalf("GridKey = %q (%v)", key, err)
	}
}

func TestExtRetireIsTheDeleteGesture(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.GridOf("root")
	tiles := mintAll(t, d, gid, textEntries("a"), true)
	a := fileByKey(t, tiles, "a")
	if err := d.Retire(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.Retire(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double retire: %v, want ErrNotFound", err)
	}
}

func TestExtRootFraming(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.GridOf("root")
	if v, err := d.RootFraming(gid); err != nil || !v.SameAs(rpc.View{}) {
		t.Fatalf("fresh grid claims a root view: %+v err=%v", v, err)
	}
	if _, err := d.SetFraming(0, gid, mkFraming(1.5, -2.25, 0.8)); err != nil {
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
	d := st.Files()
	gid, _ := d.GridOf("root")
	tiles := mintAll(t, d, gid, textEntries("a"), true)
	a := fileByKey(t, tiles, "a")
	if _, err := d.Move(a.ID, 0, "", 2, 2, 1, 1, nil); err != nil {
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
	d2 := st2.Files()
	gid2, _ := d2.GridOf("root")
	if gid2 != gid {
		t.Fatalf("grid re-minted across reopen: %d != %d", gid2, gid)
	}
	tiles = mintAll(t, d2, gid2, textEntries("a"), true)
	if a2 := fileByKey(t, tiles, "a"); a2.ID != a.ID || a2.X != 2 || a2.Y != 2 {
		t.Fatalf("arrangement lost across reopen: %+v", a2)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// rowCount is every tile row in the file, whatever the namespace: the number
// a listing must not move.
func rowCount(t *testing.T, st *Store) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM tiles`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The join is a read. Listing a hundred entries a hundred times mints
// nothing, and the placements it derives are exactly the ones Mint stores, so
// the tile a user finally touches does not move under the touch.
func TestExtOverlayWritesNothingAndMintKeepsThePlacement(t *testing.T) {
	st, d := openExt(t)
	gid, _ := d.GridOf("root")
	keys := make([]string, 100)
	for i := range keys {
		keys[i] = "f" + itoa(int64(i))
	}
	entries := textEntries(keys...)
	before := rowCount(t, st)
	var first []FileTile
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
	want := fileByKey(t, first, "f42")
	id, err := d.Mint(gid, Entry{Key: "f42", Kind: "text", Label: "f42"}, 0, want.X, want.Y, want.W, want.H)
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
	got := fileByKey(t, after, "f42")
	if got.ID != id || got.X != want.X || got.Y != want.Y {
		t.Fatalf("minting moved the tile: derived %+v, minted %+v", want, got)
	}
	for _, tl := range after {
		if tl.Key != "f42" && tl.ID != 0 {
			t.Fatalf("minting one entry minted %q too", tl.Key)
		}
	}
	// Idempotent: a second mint of the same key is the same row.
	again, err := d.Mint(gid, Entry{Key: "f42", Kind: "text", Label: "f42"}, 0, 9, 9, 1, 1)
	if err != nil || again != id {
		t.Fatalf("second mint = %d (%v), want the first row %d", again, err, id)
	}
}

// Dragging one tile must not rearrange the grid: a minted entry keeps its slot
// in the derived flow and overrides it, so its neighbours' cells are the same
// before and after.
func TestMintingOneEntryLeavesItsNeighboursWhereTheyWere(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.GridOf("root")
	entries := textEntries("a", "b", "c", "d", "e")
	before, err := d.Overlay(gid, entries)
	if err != nil {
		t.Fatal(err)
	}
	// The user drags "b" door clear of the flow.
	b := fileByKey(t, before, "b")
	if _, err := d.Mint(gid, Entry{Key: "b", Kind: "text", Label: "b"}, 0, b.X, b.Y, b.W, b.H); err != nil {
		t.Fatal(err)
	}
	id, _, err := d.LiveTileID(gid, "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Move(id, 0, "", 3, 4, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	after, err := d.Overlay(gid, entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "c", "d", "e"} {
		was, now := fileByKey(t, before, key), fileByKey(t, after, key)
		if was.X != now.X || was.Y != now.Y {
			t.Fatalf("%q moved because a neighbour was dragged: (%d,%d) → (%d,%d)",
				key, was.X, was.Y, now.X, now.Y)
		}
	}
	if got := fileByKey(t, after, "b"); got.X != 3 || got.Y != 4 {
		t.Fatalf("the dragged tile is at (%d,%d), want (3,4)", got.X, got.Y)
	}
}

// A touch mints the row where the entry already stood, so the entries after
// it in the flow must not step past it: an entry's own row is its slot, not
// an obstacle.
func TestMintingInPlaceLeavesItsNeighboursWhereTheyWere(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.GridOf("root")
	entries := textEntries("a", "b", "c", "d", "e")
	before, err := d.Overlay(gid, entries)
	if err != nil {
		t.Fatal(err)
	}
	b := fileByKey(t, before, "b")
	if _, err := d.Mint(gid, Entry{Key: "b", Kind: "text", Label: "b"}, 0, b.X, b.Y, b.W, b.H); err != nil {
		t.Fatal(err)
	}
	after, err := d.Overlay(gid, entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b", "c", "d", "e"} {
		was, now := fileByKey(t, before, key), fileByKey(t, after, key)
		if was.X != now.X || was.Y != now.Y {
			t.Fatalf("%q moved because a neighbour was touched in place: (%d,%d) → (%d,%d)",
				key, was.X, was.Y, now.X, now.Y)
		}
	}
}

// A typed name is the user's, on a file's row as on home's: the listing's
// label is the name only until the user renames, and every later listing,
// the join and the stored snapshot alike, keeps what the user typed.
func TestExtTypedNameOutlivesTheSourcesLabel(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.GridOf("root")
	listing := []Entry{{Key: "u", Kind: "url", Label: "u", URL: "https://a"}, {Key: "f", Kind: "text", Label: "f"}}
	tiles := mintAll(t, d, gid, listing, true)
	u, f := fileByKey(t, tiles, "u"), fileByKey(t, tiles, "f")

	changed, err := d.Rename(u.ID, 0, "mine")
	if err != nil || !changed {
		t.Fatalf("rename: changed=%v err=%v", changed, err)
	}
	if _, err := d.Rename(u.ID, 0, "again"); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("a rename claiming the version before the first: got %v, want the conflict", err)
	}
	if changed, err := d.Rename(u.ID, 1, "mine"); err != nil || changed {
		t.Fatalf("the same name again: changed=%v err=%v, want nothing written", changed, err)
	}
	if _, err := d.Rename(f.ID, 0, "nope"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("text rename: got %v, want ErrInvalidArgument", err)
	}

	listing[0].Label, listing[1].Label = "u retitled", "f retitled"
	if err := d.Refresh(gid, listing); err != nil {
		t.Fatal(err)
	}
	stored, err := d.Overlay(gid, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileByKey(t, stored, "u"); got.AltText != "mine" || got.Version != 1 {
		t.Fatalf("stored snapshot = %q at version %d, want the typed name at version 1", got.AltText, got.Version)
	}
	if got := fileByKey(t, stored, "f"); got.AltText != "f retitled" {
		t.Fatalf("an unrenamed row's snapshot = %q, want the source's new label", got.AltText)
	}
	joined, err := d.Overlay(gid, listing)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileByKey(t, joined, "u"); got.AltText != "mine" {
		t.Fatalf("join = %q, want the typed name over the source's label", got.AltText)
	}
}

// A landed address is the user's, as a typed name is: the listing's address
// is the url's only until a navigation lands elsewhere, and every later
// listing, the join and the stored snapshot alike, keeps where the user went.
func TestExtLandedAddressOutlivesTheListing(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.GridOf("root")
	listing := []Entry{
		{Key: "u", Kind: "url", Label: "u", URL: "https://a.test/"},
		{Key: "p", Kind: "url", Label: "p", ServesPage: true},
		{Key: "f", Kind: "text", Label: "f"},
	}
	tiles := mintAll(t, d, gid, listing, true)
	u := fileByKey(t, tiles, "u")

	if changed, err := d.SetAddress(u.ID, 0, "https://a.test/"); err != nil || changed {
		t.Fatalf("landing where the row points: changed=%v err=%v, want nothing written", changed, err)
	}
	if changed, err := d.SetAddress(u.ID, 0, " https://b.test/p "); err != nil || !changed {
		t.Fatalf("landing: changed=%v err=%v", changed, err)
	}
	if _, err := d.SetAddress(u.ID, 0, "https://c.test/"); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("a landing claiming the version before the first: got %v, want the conflict", err)
	}
	if _, err := d.SetAddress(u.ID, 1, "about:blank"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("a non-http address: got %v, want ErrInvalidArgument", err)
	}
	for _, k := range []string{"p", "f"} {
		if _, err := d.SetAddress(fileByKey(t, tiles, k).ID, 0, "https://b.test/"); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("%s has no address of its own: got %v, want ErrInvalidArgument", k, err)
		}
	}

	listing[0].URL = "https://a.test/moved"
	if err := d.Refresh(gid, listing); err != nil {
		t.Fatal(err)
	}
	stored, err := d.Overlay(gid, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileByKey(t, stored, "u"); got.UrlString != "https://b.test/p" || got.Version != 1 || !got.URLUser {
		t.Fatalf("stored snapshot = %q at version %d latched %v, want the landed address at version 1", got.UrlString, got.Version, got.URLUser)
	}
	joined, err := d.Overlay(gid, listing)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileByKey(t, joined, "u"); got.UrlString != "https://b.test/p" {
		t.Fatalf("join = %q, want the landed address over the listing's", got.UrlString)
	}
	listing[0].LinkTarget = rpc.KeyTileID("root" + "/" + "f")
	if err := d.Refresh(gid, listing); err != nil {
		t.Fatal(err)
	}
	listing[0].LinkTarget = ""
	if err := d.Refresh(gid, listing); err != nil {
		t.Fatal(err)
	}
	if joined, err = d.Overlay(gid, listing); err != nil {
		t.Fatal(err)
	}
	if got := fileByKey(t, joined, "u"); got.UrlString != "https://a.test/moved" || got.URLUser {
		t.Fatalf("after a link and back = %q latched %v, want the listing's address: a link holds none", got.UrlString, got.URLUser)
	}
}

// A door row whose entry becomes a link, a symlink to a directory whose
// target is gone, gives up its child grid with its kind, so the refresh
// lands and the grid still lists; a link row has no child grid of its own,
// and one that becomes a door again takes its entry's.
func TestExtDoorRowBecomingALinkGivesUpItsChildGrid(t *testing.T) {
	_, d := openExt(t)
	gid, _ := d.GridOf("root")
	child, _ := d.GridOf("hall")
	door := Entry{Key: "h", Kind: "door", Label: "h"}
	if _, err := d.Mint(gid, door, child, 0, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
	link := []Entry{{Key: "h", Kind: "text", Label: "h", LinkTarget: rpc.KeyTileID("/gone")}}
	if err := d.Refresh(gid, link); err != nil {
		t.Fatalf("refresh to a link: %v", err)
	}
	got, err := d.Overlay(gid, link)
	if err != nil {
		t.Fatal(err)
	}
	if h := fileByKey(t, got, "h"); h.Kind != "text" || h.LinkTargetId != rpc.KeyTileID("/gone") || h.ChildGridID != 0 {
		t.Fatalf("row = kind %q link %q child %d, want a text link with no child grid", h.Kind, h.LinkTargetId, h.ChildGridID)
	}
	door.Child = "hall"
	if err := d.Refresh(gid, []Entry{door}); err != nil {
		t.Fatalf("refresh back to a door: %v", err)
	}
	if got, err = d.Overlay(gid, []Entry{door}); err != nil {
		t.Fatal(err)
	}
	if h := fileByKey(t, got, "h"); h.Kind != "door" || h.LinkTargetId != "" || h.ChildGridID != child {
		t.Fatalf("row = kind %q link %q child %d, want the door onto grid %d", h.Kind, h.LinkTargetId, h.ChildGridID, child)
	}
}
