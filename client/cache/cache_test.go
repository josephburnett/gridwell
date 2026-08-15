package cache

import (
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
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

// TestTileContentEditDoesNotLeakToClone is the regression for the cross-clone
// content leak: two cloned text tiles have distinct ids. An edit to one writes
// its body keyed by that id, so the sibling's body is untouched (the "edit one
// clone, the other changed too" bug). Tile-id keying makes this hold by
// construction — the blob-id store that once needed careful repointing is gone.
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

// TestRenderedEditVisibleThroughRenderAccessor reproduces the rendered-mode
// "typing does nothing" bug: the renderer reads a text tile's body via
// TileContent (tileBody -> TileContent, the tile-id content store), but the
// edit path wrote through OptimisticEdit (the blob-id store). The two stores
// disagree, so edits never appear. The fix routes edits through the same store
// the renderer reads.
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

// TestApplyStaleEchoDropped: the optimistic-echo interlock (I11 residual,
// issue #5). After a local mutation's response lands in the cache (version
// N), a still-in-flight Subscribe echo of the PREVIOUS state (N-1) must be
// dropped — applying it would visibly roll the tile back and forward, i.e.
// mutation the user never made. Same-version events (framing — never bumps
// version) and newer events still apply.
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

	// A SAME-version event applies: framing writes change view_* without a
	// version bump, and dropping them would freeze pans.
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

// TestTileRemovedSparesDirtyBuffer reproduces the move-eats-typing bug
// (2026-08-14 transport-loss audit, #5): a cross-grid MOVE emits
// TileRemoved(src) then TileChanged(dst) for the SAME tile, and Apply used
// to delete the content entry unconditionally — the unsaved keystrokes
// vanished and the textarea repainted from stale server bytes. A dirty
// entry must survive TileRemoved (it is the only copy of the user's
// typing); a clean one is still swept.
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

	// A CLEAN entry is still dropped — a delete must not strand bodies.
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

// TestRemoveTileFreesContent is the regression for a body leak: a text
// tile's CLEAN cached body must be dropped when the tile is removed
// (dragged onto the + trashcan) — otherwise it strands in the map forever.
// Only clean bodies: a DIRTY buffer is the sole copy of unsaved typing and
// survives TileRemoved (TestTileRemovedSparesDirtyBuffer, 2026-08-14).
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

// TestApplyBlobChangeDropsContent: a TileChanged carrying a NEW blob at the
// SAME version (a framing-class blob write — a pane tile's layout never bumps
// version) must invalidate the cached content bytes, or this client's preview
// serves the OLD layout forever (fetchTileContent short-circuits on a cache
// hit, so nothing would ever refetch).
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

// TestApplyTextEventSparesDirtyContent: a text tile's DIRTY content entry is
// the user's unsaved typing (rendered-mode keystrokes land there before the
// debounced UpdateText), so no arriving row — save echo or foreign edit —
// may blow it away; that would visibly revert typing. The dirty entry keeps
// its old save basis instead, so its eventual save claims a version the
// server has moved past, is rejected, and reconciles VISIBLY through the
// conflict path — never a silent overwrite in either direction.
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

// TestApplyForeignTextEventDropsCleanContent is the regression for the
// remote-staleness half of the stomp bug (the "today" tile): a foreign
// writer's TileChanged advances a text tile's row version, and a CLEAN cached
// body from before that version is now provably stale — it must drop so the
// next render refetches and the foreign edit becomes visible. Before this
// rule the cache kept the old bytes forever (fetchTileContent short-circuits
// on a hit), so the remote edit never appeared on screen while the row
// version silently advanced underneath — arming the stomp.
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

	// A SAME-version event (framing — pans/scrolls never bump version) must
	// not evict the body; that would refetch content on every pan echo.
	c.PutFetchedContent("10", []byte("# current"), 4)
	c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{
		Tile: rpc.Tile{ID: "10", GridID: "1", Kind: rpc.KindText, Version: 4, BlobID: 8},
	}})
	if b, ok := c.TileContent("10"); !ok || string(b) != "# current" {
		t.Fatal("same-version (framing) event evicted the body")
	}
}

// TestPutGridReconcilesContentLikeAnEvent: a grid REFETCH and a Subscribe
// event are the same fact arriving on two paths; both must age cached bodies
// identically. Before this, PutGrid replaced rows (advancing the version a
// save would claim) without touching content — a refetch could arm the stomp
// even with the event path fixed.
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
// is queued with FROZEN bytes but claims its basis at SEND time (issue #140's
// chaining). If a content fetch completing in that window could replace the
// dirty entry, it would advance the basis under the queued save — stale bytes
// would go out claiming the current version, re-forging the stomp the basis
// exists to prevent — and would also overwrite unsaved typing on screen. A
// fetch must never replace a dirty entry; the entry's own save resolves it.
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

	// The typing's own save settles the entry clean (the response carries the
	// entry's exact bytes — saves post DirtyContent, so a response can only
	// differ when newer typing landed mid-flight, which must survive). A clean
	// entry IS replaced by a fetch — that's how foreign content becomes visible.
	c.PutSavedContent("10", []byte("# v1 body + local typing"), 3)
	c.PutFetchedContent("10", []byte("# fresher"), 4)
	if b, _ := c.TileContent("10"); string(b) != "# fresher" {
		t.Fatalf("clean entry not refreshed by fetch: %q", b)
	}
}

// TestStaleFetchNeverRegressesContent is the 2026-07-25 cursor-jump rollback
// (issue #189): a GetTileContent reply that was in flight while the user typed
// and an autosave completed lands LAST, carrying pre-edit bytes read under an
// older version. The entry is clean the instant the save response settles it,
// so the dirty guard doesn't apply — without a version guard the late reply
// rolls both the bytes and the basis backwards, the overlay repaints the old
// text (caret jumps), and the next save claims a stale basis (a manufactured
// 409). A fetch may only move the basis FORWARD.
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
// claims tracks the BYTES the client has seen, never the row version foreign
// events advance. Claiming the row version was the stomp mechanism — stale
// bytes + current version sails through the server's optimistic-concurrency
// check and destroys the foreign edit.
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
	// A foreign event advances the ROW to 7; the dirty entry's basis must NOT
	// follow — the client never saw version 7's bytes.
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

// TestSavedContentKeepsMidFlightTyping: with the cache entry as the ONE owner
// of unsaved typing (no DOM buffer behind it), a save response landing after
// further keystrokes must not roll the entry back to the bytes it confirmed —
// that would silently destroy the typing. The newer bytes stay, still dirty;
// only the basis advances so the follow-up save chains.
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
