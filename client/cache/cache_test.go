package cache

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

func seedCache(t *testing.T) *Cache {
	t.Helper()
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{
		{ID: "100", GridID: "1", Kind: rpc.KindWell, X: 0, Y: 0, W: 1, H: 1, ChildGridID: "2"},
		{ID: "101", GridID: "1", Kind: rpc.KindText, X: 5, Y: 5, W: 1, H: 1},
	})
	return c
}

func TestPutAndGet(t *testing.T) {
	c := seedCache(t)
	g, ok := c.Grid("1")
	if !ok || g == nil {
		t.Fatal("missing grid")
	}
	if len(g.Tiles) != 2 {
		t.Errorf("nodes = %d", len(g.Tiles))
	}
	// Mutating the snapshot must not affect the cache.
	delete(g.Tiles, "100")
	if g2, _ := c.Grid("1"); len(g2.Tiles) != 2 {
		t.Errorf("snapshot wasn't deep enough")
	}
}

// TestTileContentEditDoesNotLeakToClone: two cloned text tiles have distinct
// ids, so an edit to one writes its body keyed by that id and the sibling's
// body is untouched. Tile-id keying makes this hold by construction.
func TestTileContentEditDoesNotLeakToClone(t *testing.T) {
	c := New()
	// Clones: distinct tile rows in distinct grids, both seeded with the same
	// body — but each addressed by its own tile id.
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{{ID: "10", GridID: "1", Kind: rpc.KindText}})
	c.PutGrid(rpc.Grid{ID: "2"}, []rpc.Tile{{ID: "20", GridID: "2", Kind: rpc.KindText}})
	c.PutFetchedContent("10", []byte("Hello World"), 1)
	c.PutFetchedContent("20", []byte("Hello World"), 1)

	c.PutEditedContent("10", []byte("Goodbye"))

	if b, _ := c.TileContent("10"); string(b) != "Goodbye" {
		t.Errorf("edited tile body = %q, want Goodbye", b)
	}
	if b, _ := c.TileContent("20"); string(b) != "Hello World" {
		t.Errorf("sibling body = %q, want Hello World (edit leaked)", b)
	}
}

// TestRenderedEditVisibleThroughRenderAccessor: the renderer reads a text
// tile's body through TileContent, and an edit writes through
// PutEditedContent. One store, so a keystroke is visible to the renderer at
// once.
func TestRenderedEditVisibleThroughRenderAccessor(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{{ID: "10", GridID: "1", Kind: rpc.KindText}})
	c.PutFetchedContent("10", []byte("Hello"), 1) // what the renderer reads

	c.PutEditedContent("10", []byte("Hello world")) // what an edit writes

	got, _ := c.TileContent("10")
	if string(got) != "Hello world" {
		t.Fatalf("TileContent = %q, want %q (edit invisible to the renderer)", got, "Hello world")
	}
}

func TestApplyTileChanged(t *testing.T) {
	c := seedCache(t)
	ok := c.Apply(rpc.Event{
		Kind:        rpc.EventTileChanged,
		TileChanged: &rpc.TileChanged{Tile: rpc.Tile{ID: "100", GridID: "1", Kind: rpc.KindWell, X: 9, Y: 9, W: 2, H: 2, ChildGridID: "2"}},
	})
	if !ok {
		t.Error("Apply returned false")
	}
	g, _ := c.Grid("1")
	if g.Tiles["100"].W != 2 {
		t.Errorf("tile not updated: %+v", g.Tiles["100"])
	}
}

// TestApplyStaleEchoDropped: the optimistic-echo interlock. After a local
// mutation's response lands in the cache (version N), a still-in-flight
// Subscribe echo of the previous state (N-1) is dropped — applying it would
// visibly roll the tile back and forward, mutation the user never made.
// Same-version events (framing never bumps version) and newer events still
// apply.
func TestApplyStaleEchoDropped(t *testing.T) {
	c := seedCache(t)
	// The mutation response landed: version 5.
	c.UpdateTile("1", rpc.Tile{ID: "100", GridID: "1", Kind: rpc.KindText, Version: 5, X: 1})

	// A stale echo (version 4) arrives late: dropped, no visible change.
	if c.Apply(rpc.Event{Kind: rpc.EventTileChanged,
		TileChanged: &rpc.TileChanged{Tile: rpc.Tile{ID: "100", GridID: "1", Kind: rpc.KindText, Version: 4, X: 99}}}) {
		t.Error("stale echo applied — the tile would roll back")
	}
	g, _ := c.Grid("1")
	if g.Tiles["100"].X != 1 || g.Tiles["100"].Version != 5 {
		t.Errorf("tile after stale echo = %+v, want the newer row untouched", g.Tiles["100"])
	}

	// A same-version event applies: framing writes change the framing
	// columns without a version bump, and dropping them would freeze pans.
	if !c.Apply(rpc.Event{Kind: rpc.EventTileChanged,
		TileChanged: &rpc.TileChanged{Tile: rpc.Tile{ID: "100", GridID: "1", Kind: rpc.KindText, Version: 5, X: 2}}}) {
		t.Error("same-version event dropped — framing echoes would freeze")
	}
	// And a newer one, obviously.
	if !c.Apply(rpc.Event{Kind: rpc.EventTileChanged,
		TileChanged: &rpc.TileChanged{Tile: rpc.Tile{ID: "100", GridID: "1", Kind: rpc.KindText, Version: 6, X: 3}}}) {
		t.Error("newer event dropped")
	}
	g, _ = c.Grid("1")
	if g.Tiles["100"].X != 3 {
		t.Errorf("final tile = %+v, want the newest row", g.Tiles["100"])
	}
}

func TestApplyTileRemoved(t *testing.T) {
	c := seedCache(t)
	ok := c.Apply(rpc.Event{
		Kind:        rpc.EventTileRemoved,
		TileRemoved: &rpc.TileRemoved{GridID: "1", TileID: "100"},
	})
	if !ok {
		t.Error("Apply returned false")
	}
	g, _ := c.Grid("1")
	if _, ok := g.Tiles["100"]; ok {
		t.Error("tile still present")
	}
	// Idempotent: removing again returns false (nothing changed).
	if c.Apply(rpc.Event{
		Kind:        rpc.EventTileRemoved,
		TileRemoved: &rpc.TileRemoved{GridID: "1", TileID: "100"},
	}) {
		t.Error("expected false on second remove")
	}
}

// TestTileRemovedSparesDirtyBuffer: a cross-grid move emits TileRemoved(src)
// then TileChanged(dst) for the same tile, so a dirty entry must survive
// TileRemoved — it is the only copy of the user's typing. A clean one is
// still swept.
func TestTileRemovedSparesDirtyBuffer(t *testing.T) {
	c := seedCache(t)
	c.PutFetchedContent("101", []byte("saved words"), 3)
	c.PutEditedContent("101", []byte("saved words plus unsaved typing"))

	c.Apply(rpc.Event{
		Kind:        rpc.EventTileRemoved,
		TileRemoved: &rpc.TileRemoved{GridID: "1", TileID: "101"},
	})

	b, dirty := c.DirtyContent("101")
	if !dirty || string(b) != "saved words plus unsaved typing" {
		t.Fatalf("dirty buffer after TileRemoved = (%q, %v), want the unsaved typing kept", b, dirty)
	}
	// The move's second half: the tile reappears in its destination grid.
	// The surviving entry keeps its basis, so the next flush claims it.
	c.PutGrid(rpc.Grid{ID: "2"}, nil)
	c.Apply(rpc.Event{
		Kind:        rpc.EventTileChanged,
		TileChanged: &rpc.TileChanged{Tile: rpc.Tile{ID: "101", GridID: "2", Kind: rpc.KindText, Version: 3}},
	})
	if base, ok := c.SaveBasis("101"); !ok || base != 3 {
		t.Errorf("basis after move = (%d, %v), want (3, true)", base, ok)
	}

	// A clean entry is still dropped: a delete must not strand bodies.
	c.PutFetchedContent("100", []byte("clean"), 1)
	c.Apply(rpc.Event{
		Kind:        rpc.EventTileRemoved,
		TileRemoved: &rpc.TileRemoved{GridID: "1", TileID: "100"},
	})
	if _, ok := c.TileContent("100"); ok {
		t.Error("clean body survived TileRemoved — delete should sweep it")
	}
}

func TestApplyEventForUnknownGridIgnored(t *testing.T) {
	c := seedCache(t)
	ok := c.Apply(rpc.Event{
		Kind:        rpc.EventTileChanged,
		TileChanged: &rpc.TileChanged{Tile: rpc.Tile{ID: "999", GridID: "999", Kind: rpc.KindWell, ChildGridID: "1"}},
	})
	if ok {
		t.Error("expected false for unknown grid")
	}
}

func TestTileContentPutGet(t *testing.T) {
	c := New()
	if _, ok := c.TileContent("7"); ok {
		t.Fatal("empty cache should not have content for tile 7")
	}
	c.PutFetchedContent("7", []byte("hello"), 1)
	b, ok := c.TileContent("7")
	if !ok {
		t.Fatal("tile 7 content missing after put")
	}
	if string(b) != "hello" {
		t.Errorf("content = %q, want hello", string(b))
	}
	// Content puts copy the bytes so caller mutations don't propagate.
	src := []byte("world")
	c.PutEditedContent("8", src)
	src[0] = 'X'
	got, _ := c.TileContent("8")
	if string(got) != "world" {
		t.Errorf("after mutating source, tile 8 content = %q (cache should hold its own copy)", string(got))
	}
}

func TestDropTileContent(t *testing.T) {
	c := New()
	c.PutEditedContent("7", []byte("rejected optimistic edit"))
	c.DropTileContent("7")
	if _, ok := c.TileContent("7"); ok {
		t.Fatal("content survived DropTileContent; a rejected edit would keep rendering as saved")
	}
	c.DropTileContent("absent") // no-op
}

func TestKnownGridIDs(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{})
	c.PutGrid(rpc.Grid{ID: "2"}, []rpc.Tile{})

	got := c.KnownGridIDs()
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
	have := map[string]bool{}
	for _, id := range got {
		have[id] = true
	}
	if !have["1"] || !have["2"] {
		t.Errorf("KnownGridIDs missing entries: %v", got)
	}
}

func TestUpdateTile(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "10"}, []rpc.Tile{
		{ID: "100", GridID: "10", Kind: rpc.KindURL, X: 0, Y: 0, W: 1, H: 1},
	})

	// Update the existing tile: change W.
	updated := rpc.Tile{ID: "100", GridID: "10", Kind: rpc.KindURL, X: 0, Y: 0, W: 3, H: 1}
	c.UpdateTile("10", updated)

	g, _ := c.Grid("10")
	if g.Tiles["100"].W != 3 {
		t.Errorf("UpdateTile did not change W; got %d", g.Tiles["100"].W)
	}

	// UpdateTile on an unknown grid is a no-op.
	c.UpdateTile("999", updated)

	// UpdateTile on an unknown tile id within a known grid is a no-op.
	stranger := rpc.Tile{ID: "999", GridID: "10", Kind: rpc.KindText}
	c.UpdateTile("10", stranger)
	if _, ok := g.Tiles["999"]; ok {
		t.Error("UpdateTile should not insert unknown tile ids")
	}
}

// TestRemoveTileFreesContent: a text tile's clean cached body is dropped when
// the tile is removed, or it strands in the map forever. Only clean bodies —
// a dirty buffer is the sole copy of unsaved typing and survives TileRemoved
// (TestTileRemovedSparesDirtyBuffer).
func TestRemoveTileFreesContent(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{{ID: "10", GridID: "1", Kind: rpc.KindText}})
	c.PutFetchedContent("10", []byte("Goodbye"), 2)
	if _, ok := c.TileContent("10"); !ok {
		t.Fatal("content not stored")
	}

	c.Apply(rpc.Event{Kind: rpc.EventTileRemoved, TileRemoved: &rpc.TileRemoved{GridID: "1", TileID: "10"}})
	if _, ok := c.TileContent("10"); ok {
		t.Errorf("content leaked after tile removal")
	}
}

// TestApplyBlobChangeDropsContent: a TileChanged carrying a new blob at the
// same version (a pane tile's layout never bumps version) invalidates the
// cached content bytes. Otherwise this client's preview serves the old layout
// forever, because the content fetch short-circuits on a cache hit.
func TestApplyBlobChangeDropsContent(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{
		{ID: "10", GridID: "1", Kind: rpc.KindPane, Version: 3, BlobID: 7},
	})
	c.PutFetchedContent("10", []byte(`{"v":1,"old":true}`), 3)

	changed := c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{
		Tile: rpc.Tile{ID: "10", GridID: "1", Kind: rpc.KindPane, Version: 3, BlobID: 8},
	}})
	if !changed {
		t.Fatal("same-version blob change must apply (framing writes never bump)")
	}
	if _, ok := c.TileContent("10"); ok {
		t.Fatal("stale content bytes survived a blob change — the preview would never repaint")
	}
	// Same blob again: nothing to drop, content written after the event stays.
	c.PutFetchedContent("10", []byte(`{"v":1,"new":true}`), 3)
	c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{
		Tile: rpc.Tile{ID: "10", GridID: "1", Kind: rpc.KindPane, Version: 3, BlobID: 8},
	}})
	if _, ok := c.TileContent("10"); !ok {
		t.Fatal("an unchanged blob must not drop content")
	}
}

// TestApplyTextEventSparesDirtyContent: a text tile's dirty content entry is
// the user's unsaved typing (keystrokes land there before the debounced
// save), so no arriving row — save echo or foreign edit — may blow it away;
// that would visibly revert typing. The dirty entry keeps its old save basis,
// so its eventual save claims a version the server has moved past, is
// rejected, and reconciles visibly through the conflict path. No silent
// overwrite in either direction.
func TestApplyTextEventSparesDirtyContent(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{
		{ID: "10", GridID: "1", Kind: rpc.KindText, Version: 3, BlobID: 7},
	})
	c.PutFetchedContent("10", []byte("# saved state"), 3)
	c.PutEditedContent("10", []byte("# newer unsaved keystrokes"))

	c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{
		Tile: rpc.Tile{ID: "10", GridID: "1", Kind: rpc.KindText, Version: 4, BlobID: 8},
	}})
	if b, ok := c.TileContent("10"); !ok || string(b) != "# newer unsaved keystrokes" {
		t.Fatal("dirty optimistic edit buffer was dropped by an arriving row")
	}
	if base, _ := c.SaveBasis("10"); base != 3 {
		t.Fatalf("dirty entry's save basis = %d, want the version its bytes derive from (3)", base)
	}
}

// TestApplyForeignTextEventDropsCleanContent: a foreign writer's TileChanged
// advances a text tile's row version, so a clean cached body from before that
// version is provably stale and must drop. The next render refetches and the
// foreign edit becomes visible; keeping the old bytes would leave the row
// version advancing underneath them.
func TestApplyForeignTextEventDropsCleanContent(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{
		{ID: "10", GridID: "1", Kind: rpc.KindText, Version: 3, BlobID: 7},
	})
	c.PutFetchedContent("10", []byte("# stale"), 3)

	c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{
		Tile: rpc.Tile{ID: "10", GridID: "1", Kind: rpc.KindText, Version: 4, BlobID: 8},
	}})
	if _, ok := c.TileContent("10"); ok {
		t.Fatal("clean stale body survived a foreign edit's event — the remote change would never appear")
	}

	// A same-version event (pans and scrolls never bump version) must not
	// evict the body; that would refetch content on every pan echo.
	c.PutFetchedContent("10", []byte("# current"), 4)
	c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{
		Tile: rpc.Tile{ID: "10", GridID: "1", Kind: rpc.KindText, Version: 4, BlobID: 8},
	}})
	if b, ok := c.TileContent("10"); !ok || string(b) != "# current" {
		t.Fatal("same-version (framing) event evicted the body")
	}
}

// TestCaptureEventKeepsTheBodyAndStillRenders: an automatic capture — a page
// title, a frozen jpeg, a shell's foreground command — does not bump the tile
// version, so the event it rides carries the same version the cached body
// derives from.
//
// Two things hold at once. The cached body survives, because a capture
// changed nothing about the bytes and evicting a clean one would refetch
// content on every freeze. And the capture still reaches the screen: the
// event carries the whole tile, so the row — its new name, its new preview
// blob — replaces the cached row and Apply reports a redraw.
func TestCaptureEventKeepsTheBodyAndStillRenders(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{
		{ID: "10", GridID: "1", Kind: rpc.KindText, Version: 3, BlobID: 7, AltText: "old name"},
	})
	c.PutFetchedContent("10", []byte("# the body"), 3)

	// A capture on the very tile whose body is cached: same version, new
	// name, new preview blob.
	capture := rpc.Tile{
		ID: "10", GridID: "1", Kind: rpc.KindText, Version: 3, BlobID: 7,
		AltText: "captured name", PreviewBlobID: 42,
	}
	if !c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: capture}}) {
		t.Fatal("a capture event must report a redraw — it changed what the tile looks like")
	}
	if b, ok := c.TileContent("10"); !ok || string(b) != "# the body" {
		t.Error("a capture evicted the cached body")
	}
	g, _ := c.Grid("1")
	if got := g.Tiles["10"]; got.AltText != "captured name" || got.PreviewBlobID != 42 {
		t.Errorf("the capture did not reach the cached row: %+v", got)
	}
}

// TestCaptureDuringAnEditKeepsTheKeystrokes is the same event arriving while
// the user is typing. The dirty entry — the one copy of the unsaved words —
// is untouched, and its save basis stays where it was, so the save that
// follows still claims a version the server will accept.
func TestCaptureDuringAnEditKeepsTheKeystrokes(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{
		{ID: "10", GridID: "1", Kind: rpc.KindText, Version: 3, BlobID: 7},
	})
	c.PutFetchedContent("10", []byte("# saved state"), 3)
	c.PutEditedContent("10", []byte("# words still being typed"))

	c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{
		Tile: rpc.Tile{ID: "10", GridID: "1", Kind: rpc.KindText, Version: 3, BlobID: 7, AltText: "captured"},
	}})

	data, dirty := c.DirtyContent("10")
	if !dirty || string(data) != "# words still being typed" {
		t.Fatalf("capture disturbed the unsaved edit: %q dirty=%v", data, dirty)
	}
	if base, _ := c.SaveBasis("10"); base != 3 {
		t.Errorf("save basis = %d, want 3 — a capture must not move what the edit claims", base)
	}
}

// TestPutGridReconcilesContentLikeAnEvent: a grid refetch and a Subscribe
// event are the same fact arriving on two paths, so both age cached bodies
// identically. A PutGrid that replaced rows without touching content would
// advance the version a save claims past the bytes it vouches for.
func TestPutGridReconcilesContentLikeAnEvent(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{
		{ID: "10", GridID: "1", Kind: rpc.KindText, Version: 3},
		{ID: "11", GridID: "1", Kind: rpc.KindText, Version: 3},
	})
	c.PutFetchedContent("10", []byte("# clean stale"), 3)
	c.PutFetchedContent("11", []byte("# saved"), 3)
	c.PutEditedContent("11", []byte("# dirty typing"))

	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{
		{ID: "10", GridID: "1", Kind: rpc.KindText, Version: 5},
		{ID: "11", GridID: "1", Kind: rpc.KindText, Version: 5},
	})
	if _, ok := c.TileContent("10"); ok {
		t.Fatal("clean stale body survived a refetch that advanced the row version")
	}
	if b, ok := c.TileContent("11"); !ok || string(b) != "# dirty typing" {
		t.Fatal("dirty edit buffer was dropped by a grid refetch")
	}
}

// TestFetchNeverClobbersDirtyContent closes the enqueue-to-send race: a save
// is queued with frozen bytes but claims its basis at send time. A content
// fetch completing in that window would advance the basis under the queued
// save — stale bytes going out under the current version — and overwrite
// unsaved typing on screen. A fetch never replaces a dirty entry; the entry's
// own save resolves it.
func TestFetchNeverClobbersDirtyContent(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{{ID: "10", GridID: "1", Kind: rpc.KindText, Version: 1}})
	c.PutFetchedContent("10", []byte("# v1 body"), 1)
	c.PutEditedContent("10", []byte("# v1 body + local typing")) // save queued, bytes frozen

	// A foreign edit's refetch completes mid-window with version-2 content.
	c.PutFetchedContent("10", []byte("# foreign v2 body"), 2)

	if b, _ := c.TileContent("10"); string(b) != "# v1 body + local typing" {
		t.Fatalf("fetch overwrote unsaved typing: %q", b)
	}
	if base, _ := c.SaveBasis("10"); base != 1 {
		t.Fatalf("basis = %d, want 1 — a floating basis under a queued save is the stomp re-forged", base)
	}

	// The typing's own save settles the entry clean: the response carries the
	// entry's exact bytes, since saves post DirtyContent, so a response can
	// only differ when newer typing landed mid-flight, which must survive. A
	// clean entry is replaced by a fetch — that is how foreign content becomes
	// visible.
	c.PutSavedContent("10", []byte("# v1 body + local typing"), 3)
	c.PutFetchedContent("10", []byte("# fresher"), 4)
	if b, _ := c.TileContent("10"); string(b) != "# fresher" {
		t.Fatalf("clean entry not refreshed by fetch: %q", b)
	}
}

// TestStaleFetchNeverRegressesContent: a content read that was in flight
// while the user typed and an autosave completed lands last, carrying
// pre-edit bytes read under an older version. The entry is clean the instant
// the save response settles it, so the dirty guard does not apply. Without a
// version guard the late reply rolls both the bytes and the basis backwards:
// the overlay repaints the old text, the caret jumps, and the next save
// claims a stale basis. A fetch moves the basis forward or not at all.
func TestStaleFetchNeverRegressesContent(t *testing.T) {
	c := New()
	// The fetch goes out while no entry exists; before its reply lands the
	// user types and the autosave confirms the bytes as version 3.
	c.PutEditedContent("10", []byte("# draft"))
	c.PutSavedContent("10", []byte("# draft"), 3)

	// The stale reply finally lands: pre-edit bytes, read under version 2.
	c.PutFetchedContent("10", []byte("# pre-edit"), 2)

	if b, _ := c.TileContent("10"); string(b) != "# draft" {
		t.Fatalf("stale fetch rolled content back: %q", b)
	}
	if base, _ := c.SaveBasis("10"); base != 3 {
		t.Fatalf("basis = %d, want 3 — a regressed basis manufactures a 409 on the next save", base)
	}

	// Same-version and fresher replies still apply (idempotent refresh /
	// foreign-writer visibility).
	c.PutFetchedContent("10", []byte("# same version"), 3)
	if b, _ := c.TileContent("10"); string(b) != "# same version" {
		t.Fatalf("same-version fetch refused: %q", b)
	}
	c.PutFetchedContent("10", []byte("# fresher"), 4)
	if b, _ := c.TileContent("10"); string(b) != "# fresher" {
		t.Fatalf("fresher fetch refused: %q", b)
	}
}

// TestSaveBasisFollowsBytesNotRow is the interlock itself: the version a save
// claims tracks the bytes the client has seen, never the row version foreign
// events advance. Claiming the row version would send stale bytes under the
// current version, straight through the server's concurrency check and over
// the foreign edit.
func TestSaveBasisFollowsBytesNotRow(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{
		{ID: "10", GridID: "1", Kind: rpc.KindText, Version: 3},
	})
	if _, ok := c.SaveBasis("10"); ok {
		t.Fatal("no content yet — there is no basis to claim")
	}
	c.PutFetchedContent("10", []byte("# body v3"), 3)
	if base, _ := c.SaveBasis("10"); base != 3 {
		t.Fatalf("basis after fetch = %d, want 3", base)
	}
	// Local edits ride on the fetched bytes: basis unchanged.
	c.PutEditedContent("10", []byte("# body v3 + typing"))
	if base, _ := c.SaveBasis("10"); base != 3 {
		t.Fatalf("basis after local edit = %d, want 3", base)
	}
	// A foreign event advances the row to 7; the dirty entry's basis does not
	// follow, because the client never saw version 7's bytes.
	c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{
		Tile: rpc.Tile{ID: "10", GridID: "1", Kind: rpc.KindText, Version: 7},
	}})
	if base, _ := c.SaveBasis("10"); base != 3 {
		t.Fatalf("basis after foreign event = %d, want 3 (claiming 7 would stomp the foreign edit)", base)
	}
	// A confirmed save advances it: the server accepted these bytes as v8.
	c.PutSavedContent("10", []byte("# merged"), 8)
	if base, _ := c.SaveBasis("10"); base != 8 {
		t.Fatalf("basis after save = %d, want 8", base)
	}
}

// TestSavedContentKeepsMidFlightTyping: the cache entry is the one owner of
// unsaved typing, with no DOM buffer behind it, so a save response landing
// after further keystrokes must not roll the entry back to the bytes it
// confirmed. The newer bytes stay, still dirty; only the basis advances, so
// the follow-up save chains.
func TestSavedContentKeepsMidFlightTyping(t *testing.T) {
	c := New()
	c.PutFetchedContent("10", []byte("draft"), 1)
	c.PutEditedContent("10", []byte("draft v2")) // save of "draft v2" goes out
	c.PutEditedContent("10", []byte("draft v2 plus more typing"))

	// The "draft v2" save's response lands: version 2 confirmed.
	c.PutSavedContent("10", []byte("draft v2"), 2)

	if b, _ := c.TileContent("10"); string(b) != "draft v2 plus more typing" {
		t.Fatalf("save response destroyed mid-flight typing: %q", b)
	}
	if d, ok := c.DirtyContent("10"); !ok || string(d) != "draft v2 plus more typing" {
		t.Fatalf("newer typing must stay dirty (pending its own save); got %q ok=%v", d, ok)
	}
	if base, _ := c.SaveBasis("10"); base != 2 {
		t.Fatalf("basis = %d, want 2 — the follow-up save chains from the confirmed write", base)
	}

	// Response matching the entry's bytes settles it clean.
	c.PutSavedContent("10", []byte("draft v2 plus more typing"), 3)
	if _, ok := c.DirtyContent("10"); ok {
		t.Fatal("entry must be clean once the server holds its exact bytes")
	}
}

// TestDirtyAccessors: DirtyContent answers only for entries carrying unsaved
// edits, and DirtyTileIDs enumerates exactly those — the debounced sweep's
// worklist, keyed by tile id rather than by whichever pane has focus.
func TestDirtyAccessors(t *testing.T) {
	c := New()
	c.PutFetchedContent("10", []byte("clean"), 1)
	c.PutFetchedContent("20", []byte("original"), 4)
	c.PutEditedContent("20", []byte("edited"))

	if _, ok := c.DirtyContent("10"); ok {
		t.Fatal("clean entry reported dirty")
	}
	if _, ok := c.DirtyContent("99"); ok {
		t.Fatal("absent entry reported dirty")
	}
	d, ok := c.DirtyContent("20")
	if !ok || string(d) != "edited" {
		t.Fatalf("dirty entry not returned: %q ok=%v", d, ok)
	}
	// The returned slice is a copy — mutating it must not corrupt the entry.
	d[0] = 'X'
	if b, _ := c.TileContent("20"); string(b) != "edited" {
		t.Fatalf("DirtyContent leaked the internal buffer: %q", b)
	}

	ids := c.DirtyTileIDs()
	if len(ids) != 1 || ids[0] != "20" {
		t.Fatalf("DirtyTileIDs = %v, want [20]", ids)
	}
	c.PutSavedContent("20", []byte("edited"), 5)
	if ids := c.DirtyTileIDs(); len(ids) != 0 {
		t.Fatalf("after save DirtyTileIDs = %v, want empty", ids)
	}
}

// TestGridDeclarationsAreNilSafe pins the two declaration readers on Grid.
// The nil receiver is the case the callers need: a renderer holding a grid it
// has not fetched asks anyway, and "not known" answers no rather than
// panicking or forcing every call site to spell the nil check.
func TestGridDeclarationsAreNilSafe(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1", HostContent: true, Stale: true}, nil)
	c.PutGrid(rpc.Grid{ID: "2"}, nil)

	g, ok := c.Grid("1")
	if !ok || !g.HostContent() || !g.Stale() {
		t.Errorf("declared grid: HostContent=%v Stale=%v, want both true", g.HostContent(), g.Stale())
	}
	plain, ok := c.Grid("2")
	if !ok || plain.HostContent() || plain.Stale() {
		t.Errorf("undeclared grid: HostContent=%v Stale=%v, want both false", plain.HostContent(), plain.Stale())
	}
	var missing *Grid
	if missing.HostContent() || missing.Stale() {
		t.Error("an unfetched grid declares nothing")
	}
}
