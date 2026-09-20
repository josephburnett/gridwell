package cache

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/josephburnett/gridwell/api/rpc"
)

func seedCache(t *testing.T) *Cache {
	t.Helper()
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{
		&gridwellv1.Tile{Id: "100", GridId: "1", Kind: rpc.KindWell, X: 0, Y: 0, W: 1, H: 1, ChildGridId: "2"},
		&gridwellv1.Tile{Id: "101", GridId: "1", Kind: rpc.KindText, X: 5, Y: 5, W: 1, H: 1},
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

// Tile-id keying makes this hold by construction: two clones have distinct
// ids, so an edit to one leaves the sibling's body alone.
func TestTileContentEditDoesNotLeakToClone(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{&gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText}})
	c.PutGrid(&gridwellv1.Grid{Id: "2"}, []*gridwellv1.Tile{&gridwellv1.Tile{Id: "20", GridId: "2", Kind: rpc.KindText}})
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

// The renderer reads through TileContent and an edit writes through
// PutEditedContent. One store, so a keystroke is visible at once.
func TestRenderedEditVisibleThroughRenderAccessor(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{&gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText}})
	c.PutFetchedContent("10", []byte("Hello"), 1) // what the renderer reads

	c.PutEditedContent("10", []byte("Hello world")) // what an edit writes

	got, _ := c.TileContent("10")
	if string(got) != "Hello world" {
		t.Fatalf("TileContent = %q, want %q (edit invisible to the renderer)", got, "Hello world")
	}
}

func TestApplyTileChanged(t *testing.T) {
	c := seedCache(t)
	ok := c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: &gridwellv1.Tile{Id: "100", GridId: "1", Kind: rpc.KindWell, X: 9, Y: 9, W: 2, H: 2, ChildGridId: "2"}}}})
	if !ok {
		t.Error("Apply returned false")
	}
	g, _ := c.Grid("1")
	if g.Tiles["100"].W != 2 {
		t.Errorf("tile not updated: %+v", g.Tiles["100"])
	}
}

// After a mutation's response lands at version N, an in-flight echo of N-1 is
// dropped: applying it would roll the tile back and forward, a mutation the
// user never made.
func TestApplyStaleEchoDropped(t *testing.T) {
	c := seedCache(t)
	// The mutation response landed: version 5.
	c.UpdateTile("1", &gridwellv1.Tile{Id: "100", GridId: "1", Kind: rpc.KindText, Version: 5, X: 1})

	// A stale echo (version 4) arrives late: dropped, no visible change.
	if c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: &gridwellv1.Tile{Id: "100", GridId: "1", Kind: rpc.KindText, Version: 4, X: 99}}}}) {
		t.Error("stale echo applied — the tile would roll back")
	}
	g, _ := c.Grid("1")
	if g.Tiles["100"].X != 1 || g.Tiles["100"].Version != 5 {
		t.Errorf("tile after stale echo = %+v, want the newer row untouched", g.Tiles["100"])
	}

	// Framing writes change columns without a version bump, so dropping a
	// same-version event would freeze pans.
	if !c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: &gridwellv1.Tile{Id: "100", GridId: "1", Kind: rpc.KindText, Version: 5, X: 2}}}}) {
		t.Error("same-version event dropped — framing echoes would freeze")
	}
	// And a newer one, obviously.
	if !c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: &gridwellv1.Tile{Id: "100", GridId: "1", Kind: rpc.KindText, Version: 6, X: 3}}}}) {
		t.Error("newer event dropped")
	}
	g, _ = c.Grid("1")
	if g.Tiles["100"].X != 3 {
		t.Errorf("final tile = %+v, want the newest row", g.Tiles["100"])
	}
}

func TestApplyTileRemoved(t *testing.T) {
	c := seedCache(t)
	ok := c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileRemoved{TileRemoved: &gridwellv1.TileRemoved{GridId: "1", TileId: "100"}}})
	if !ok {
		t.Error("Apply returned false")
	}
	g, _ := c.Grid("1")
	if _, ok := g.Tiles["100"]; ok {
		t.Error("tile still present")
	}
	// Idempotent: removing again returns false (nothing changed).
	if c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileRemoved{TileRemoved: &gridwellv1.TileRemoved{GridId: "1", TileId: "100"}}}) {
		t.Error("expected false on second remove")
	}
}

// A cross-grid move emits TileRemoved then TileChanged for the same tile, so
// a dirty entry, the only copy of the typing, must survive TileRemoved.
func TestTileRemovedSparesDirtyBuffer(t *testing.T) {
	c := seedCache(t)
	c.PutFetchedContent("101", []byte("saved words"), 3)
	c.PutEditedContent("101", []byte("saved words plus unsaved typing"))

	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileRemoved{TileRemoved: &gridwellv1.TileRemoved{GridId: "1", TileId: "101"}}})

	b, dirty := c.DirtyContent("101")
	if !dirty || string(b) != "saved words plus unsaved typing" {
		t.Fatalf("dirty buffer after TileRemoved = (%q, %v), want the unsaved typing kept", b, dirty)
	}
	// The surviving entry keeps its basis, so the next flush claims it.
	c.PutGrid(&gridwellv1.Grid{Id: "2"}, nil)
	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: &gridwellv1.Tile{Id: "101", GridId: "2", Kind: rpc.KindText, Version: 3}}}})
	if base, ok := c.SaveBasis("101"); !ok || base != 3 {
		t.Errorf("basis after move = (%d, %v), want (3, true)", base, ok)
	}

	// A clean entry is still dropped: a delete must not strand bodies.
	c.PutFetchedContent("100", []byte("clean"), 1)
	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileRemoved{TileRemoved: &gridwellv1.TileRemoved{GridId: "1", TileId: "100"}}})
	if _, ok := c.TileContent("100"); ok {
		t.Error("clean body survived TileRemoved — delete should sweep it")
	}
}

func TestApplyEventForUnknownGridIgnored(t *testing.T) {
	c := seedCache(t)
	ok := c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: &gridwellv1.Tile{Id: "999", GridId: "999", Kind: rpc.KindWell, ChildGridId: "1"}}}})
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
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{})
	c.PutGrid(&gridwellv1.Grid{Id: "2"}, []*gridwellv1.Tile{})

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
	c.PutGrid(&gridwellv1.Grid{Id: "10"}, []*gridwellv1.Tile{
		&gridwellv1.Tile{Id: "100", GridId: "10", Kind: rpc.KindURL, X: 0, Y: 0, W: 1, H: 1},
	})

	// Update the existing tile: change W.
	updated := &gridwellv1.Tile{Id: "100", GridId: "10", Kind: rpc.KindURL, X: 0, Y: 0, W: 3, H: 1}
	c.UpdateTile("10", updated)

	g, _ := c.Grid("10")
	if g.Tiles["100"].W != 3 {
		t.Errorf("UpdateTile did not change W; got %d", g.Tiles["100"].W)
	}

	// UpdateTile on an unknown grid is a no-op.
	c.UpdateTile("999", updated)

	// UpdateTile on an unknown tile id within a known grid is a no-op.
	stranger := &gridwellv1.Tile{Id: "999", GridId: "10", Kind: rpc.KindText}
	c.UpdateTile("10", stranger)
	if _, ok := g.Tiles["999"]; ok {
		t.Error("UpdateTile should not insert unknown tile ids")
	}
}

// The interlock belongs to the tile map, not to the path a row arrived on, so
// the response door obeys it too. The seam version is
// internal/server/outbox_seam_test.go:TestAResponseRowObeysTheInterlock.
func TestUpdateTileTakesTheOneDoor(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "10"}, []*gridwellv1.Tile{
		&gridwellv1.Tile{Id: "100", GridId: "10", Kind: rpc.KindText, Version: 5, W: 1},
	})

	// An older response is refused, exactly as an older echo is.
	c.UpdateTile("10", &gridwellv1.Tile{Id: "100", GridId: "10", Kind: rpc.KindText, Version: 4, W: 9})
	g, _ := c.Grid("10")
	if got := g.Tiles["100"]; got.Version != 5 || got.W != 1 {
		t.Errorf("an older response rolled the row back to version %d (W %d)", got.Version, got.W)
	}

	// The nav patch and the content-zoom patch are read-modify-writes of the
	// cached row carrying the version they read, so refusing equality would
	// drop every one of them.
	c.UpdateTile("10", &gridwellv1.Tile{Id: "100", GridId: "10", Kind: rpc.KindText, Version: 5, W: 7})
	g, _ = c.Grid("10")
	if got := g.Tiles["100"].W; got != 7 {
		t.Errorf("a same-version patch did not land; W = %d, want 7", got)
	}
}

// The response door reconciles cached content the same way an event and a
// refetch do. Skipping it on one path is how a version silently advances past
// the bytes it vouches for.
func TestUpdateTileAgesTheBodyToo(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "10"}, []*gridwellv1.Tile{
		&gridwellv1.Tile{Id: "100", GridId: "10", Kind: rpc.KindText, Version: 5},
	})
	c.PutFetchedContent("100", []byte("body at 5"), 5)

	// A response row at 6 — a rename, say, which is a content edit and bumps.
	c.UpdateTile("10", &gridwellv1.Tile{Id: "100", GridId: "10", Kind: rpc.KindText, Version: 6, AltText: "named"})
	if _, ok := c.TileContent("100"); ok {
		t.Error("the response door left a body vouched for by a version the row has moved past")
	}

	// A dirty body is the user's unsaved typing and survives, as everywhere.
	c.PutEditedContent("100", []byte("typing"))
	c.UpdateTile("10", &gridwellv1.Tile{Id: "100", GridId: "10", Kind: rpc.KindText, Version: 7})
	if got, ok := c.DirtyContent("100"); !ok || string(got) != "typing" {
		t.Errorf("the response door discarded unsaved typing: %q %v", got, ok)
	}
}

// A clean body is dropped on removal or it strands in the map forever; a
// dirty one survives, per TestTileRemovedSparesDirtyBuffer.
func TestRemoveTileFreesContent(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{&gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText}})
	c.PutFetchedContent("10", []byte("Goodbye"), 2)
	if _, ok := c.TileContent("10"); !ok {
		t.Fatal("content not stored")
	}

	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileRemoved{TileRemoved: &gridwellv1.TileRemoved{GridId: "1", TileId: "10"}}})
	if _, ok := c.TileContent("10"); ok {
		t.Errorf("content leaked after tile removal")
	}
}

// A pane tile's layout never bumps version, so a new blob at the same version
// must invalidate the body; the content fetch short-circuits on a cache hit
// and would serve the old layout forever.
func TestApplyBlobChangeDropsContent(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{
		&gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindPane, Version: 3, BlobId: 7},
	})
	c.PutFetchedContent("10", []byte(`{"v":1,"old":true}`), 3)

	changed := c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{
		Tile: &gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindPane, Version: 3, BlobId: 8},
	}}})
	if !changed {
		t.Fatal("same-version blob change must apply (framing writes never bump)")
	}
	if _, ok := c.TileContent("10"); ok {
		t.Fatal("stale content bytes survived a blob change — the preview would never repaint")
	}
	// Same blob again: nothing to drop, content written after the event stays.
	c.PutFetchedContent("10", []byte(`{"v":1,"new":true}`), 3)
	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{
		Tile: &gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindPane, Version: 3, BlobId: 8},
	}}})
	if _, ok := c.TileContent("10"); !ok {
		t.Fatal("an unchanged blob must not drop content")
	}
}

// A dirty entry is the unsaved typing, so no arriving row may blow it away.
// It keeps its old basis, so its save is rejected and reconciles visibly
// rather than overwriting in either direction.
func TestApplyTextEventSparesDirtyContent(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{
		&gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText, Version: 3, BlobId: 7},
	})
	c.PutFetchedContent("10", []byte("# saved state"), 3)
	c.PutEditedContent("10", []byte("# newer unsaved keystrokes"))

	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{
		Tile: &gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText, Version: 4, BlobId: 8},
	}}})
	if b, ok := c.TileContent("10"); !ok || string(b) != "# newer unsaved keystrokes" {
		t.Fatal("dirty optimistic edit buffer was dropped by an arriving row")
	}
	if base, _ := c.SaveBasis("10"); base != 3 {
		t.Fatalf("dirty entry's save basis = %d, want the version its bytes derive from (3)", base)
	}
}

// A foreign writer's advance makes a clean body provably stale, so it drops
// and the next render refetches; keeping it would leave the row version
// advancing underneath the bytes.
func TestApplyForeignTextEventDropsCleanContent(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{
		&gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText, Version: 3, BlobId: 7},
	})
	c.PutFetchedContent("10", []byte("# stale"), 3)

	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{
		Tile: &gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText, Version: 4, BlobId: 8},
	}}})
	if _, ok := c.TileContent("10"); ok {
		t.Fatal("clean stale body survived a foreign edit's event — the remote change would never appear")
	}

	// Pans and scrolls never bump version, so a same-version event must not
	// evict the body and refetch on every pan echo.
	c.PutFetchedContent("10", []byte("# current"), 4)
	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{
		Tile: &gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText, Version: 4, BlobId: 8},
	}}})
	if b, ok := c.TileContent("10"); !ok || string(b) != "# current" {
		t.Fatal("same-version (framing) event evicted the body")
	}
}

// A capture does not bump the version, so its event carries the version the
// cached body derives from. Both must hold: the body survives, or every
// freeze refetches content, and the row still replaces the cached one, so the
// new name and preview reach the screen.
func TestCaptureEventKeepsTheBodyAndStillRenders(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{
		&gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText, Version: 3, BlobId: 7, AltText: "old name"},
	})
	c.PutFetchedContent("10", []byte("# the body"), 3)

	capture := &gridwellv1.Tile{
		Id: "10", GridId: "1", Kind: rpc.KindText, Version: 3, BlobId: 7,
		AltText: "captured name", PreviewBlobId: 42,
	}
	if !c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: capture}}}) {
		t.Fatal("a capture event must report a redraw — it changed what the tile looks like")
	}
	if b, ok := c.TileContent("10"); !ok || string(b) != "# the body" {
		t.Error("a capture evicted the cached body")
	}
	g, _ := c.Grid("1")
	if got := g.Tiles["10"]; got.AltText != "captured name" || got.PreviewBlobId != 42 {
		t.Errorf("the capture did not reach the cached row: %+v", got)
	}
}

// The same event while typing: the dirty entry and its basis are untouched,
// so the save that follows still claims a version the server accepts.
func TestCaptureDuringAnEditKeepsTheKeystrokes(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{
		&gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText, Version: 3, BlobId: 7},
	})
	c.PutFetchedContent("10", []byte("# saved state"), 3)
	c.PutEditedContent("10", []byte("# words still being typed"))

	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{
		Tile: &gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText, Version: 3, BlobId: 7, AltText: "captured"},
	}}})

	data, dirty := c.DirtyContent("10")
	if !dirty || string(data) != "# words still being typed" {
		t.Fatalf("capture disturbed the unsaved edit: %q dirty=%v", data, dirty)
	}
	if base, _ := c.SaveBasis("10"); base != 3 {
		t.Errorf("save basis = %d, want 3 — a capture must not move what the edit claims", base)
	}
}

// A refetch and an event are the same fact on two paths. A PutGrid that
// replaced rows without touching content would advance the version a save
// claims past the bytes it vouches for.
func TestPutGridReconcilesContentLikeAnEvent(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{
		&gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText, Version: 3},
		&gridwellv1.Tile{Id: "11", GridId: "1", Kind: rpc.KindText, Version: 3},
	})
	c.PutFetchedContent("10", []byte("# clean stale"), 3)
	c.PutFetchedContent("11", []byte("# saved"), 3)
	c.PutEditedContent("11", []byte("# dirty typing"))

	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{
		&gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText, Version: 5},
		&gridwellv1.Tile{Id: "11", GridId: "1", Kind: rpc.KindText, Version: 5},
	})
	if _, ok := c.TileContent("10"); ok {
		t.Fatal("clean stale body survived a refetch that advanced the row version")
	}
	if b, ok := c.TileContent("11"); !ok || string(b) != "# dirty typing" {
		t.Fatal("dirty edit buffer was dropped by a grid refetch")
	}
}

// A save is queued with frozen bytes but claims its basis at send time, so a
// fetch completing in that window would send stale bytes under the current
// version. A fetch never replaces a dirty entry.
func TestFetchNeverClobbersDirtyContent(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{&gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText, Version: 1}})
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

	// Saves post DirtyContent, so a response differs only when newer typing
	// landed mid-flight. A clean entry is replaced by a fetch, which is how
	// foreign content becomes visible.
	c.PutSavedContent("10", []byte("# v1 body + local typing"), 3)
	c.PutFetchedContent("10", []byte("# fresher"), 4)
	if b, _ := c.TileContent("10"); string(b) != "# fresher" {
		t.Fatalf("clean entry not refreshed by fetch: %q", b)
	}
}

// A read in flight while the user typed and an autosave completed lands last
// with pre-edit bytes under an older version, and the entry is clean by then,
// so the dirty guard does not apply. Without the version guard the reply
// rolls bytes and basis backwards.
func TestStaleFetchNeverRegressesContent(t *testing.T) {
	c := New()
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

	// Same-version and fresher replies still apply.
	c.PutFetchedContent("10", []byte("# same version"), 3)
	if b, _ := c.TileContent("10"); string(b) != "# same version" {
		t.Fatalf("same-version fetch refused: %q", b)
	}
	c.PutFetchedContent("10", []byte("# fresher"), 4)
	if b, _ := c.TileContent("10"); string(b) != "# fresher" {
		t.Fatalf("fresher fetch refused: %q", b)
	}
}

// The version a save claims tracks the bytes the client has seen, never the
// row version foreign events advance, which would send stale bytes straight
// through the server's concurrency check.
func TestSaveBasisFollowsBytesNotRow(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{
		&gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText, Version: 3},
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
	// The basis does not follow the row to 7: the client never saw those
	// bytes.
	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{
		Tile: &gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindText, Version: 7},
	}}})
	if base, _ := c.SaveBasis("10"); base != 3 {
		t.Fatalf("basis after foreign event = %d, want 3 (claiming 7 would stomp the foreign edit)", base)
	}
	// A confirmed save advances it: the server accepted these bytes as v8.
	c.PutSavedContent("10", []byte("# merged"), 8)
	if base, _ := c.SaveBasis("10"); base != 8 {
		t.Fatalf("basis after save = %d, want 8", base)
	}
}

// The cache entry is the one owner of unsaved typing, so a response landing
// after further keystrokes advances only the basis.
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

// The debounced sweep's worklist is keyed by tile id, not by whichever pane
// has focus.
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

// A renderer holding an unfetched grid asks anyway, so "not known" answers no
// rather than making every call site spell the nil check.
func TestGridDeclarationsAreNilSafe(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1", HostContent: true}, nil)
	c.PutGrid(&gridwellv1.Grid{Id: "2"}, nil)

	g, ok := c.Grid("1")
	if !ok || !g.HostContent() {
		t.Errorf("declared grid: HostContent=%v, want true", g.HostContent())
	}
	plain, ok := c.Grid("2")
	if !ok || plain.HostContent() {
		t.Errorf("undeclared grid: HostContent=%v, want false", plain.HostContent())
	}
	var missing *Grid
	if missing.HostContent() {
		t.Error("an unfetched grid declares nothing")
	}
}

// Grid hands out the cached rows themselves, so a gesture that shapes one
// into something of its own clones first: the bar's promote drag carries the
// visited row at 1x1, and writing that through the handed-out pointer would
// resize the row the crumb is standing on. A gesture is a read, and a read
// leaves the cache byte-identical.
func TestShapingAHandedOutRowLeavesTheCachedRowAlone(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{
		{Id: "100", GridId: "1", Kind: rpc.KindURL, X: 4, Y: 7, W: 3, H: 2, Version: 9},
	})
	before := proto.CloneOf(mustRow(t, c, "1", "100"))

	ghost := proto.CloneOf(mustRow(t, c, "1", "100"))
	ghost.W, ghost.H = 1, 1

	if got := mustRow(t, c, "1", "100"); !proto.Equal(got, before) {
		t.Errorf("a read mutated the cache: row = %v, want %v", got, before)
	}
	if ghost.W != 1 || ghost.H != 1 {
		t.Errorf("ghost = %dx%d, want 1x1", ghost.W, ghost.H)
	}
}

// The other half of the same contract: a caller that means the change hands
// the clone back, and only then does the cached row move.
func TestAClonedRowLandsThroughUpdateTile(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{
		{Id: "100", GridId: "1", Kind: rpc.KindURL, X: 4, Y: 7, W: 3, H: 2, Version: 9},
	})

	patched := proto.CloneOf(mustRow(t, c, "1", "100"))
	patched.UrlString = "https://example.com/"
	c.UpdateTile("1", patched)

	if got := mustRow(t, c, "1", "100"); got.UrlString != "https://example.com/" {
		t.Errorf("url_string = %q, want the handed-back clone's", got.UrlString)
	}
}

// PatchTile is the one optimistic patch: the caller's row stays as it was
// handed out, and the change lands on the cached row through the same arm a
// TileChanged event takes.
func TestPatchTileEditsACloneAndLands(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{
		{Id: "100", GridId: "1", Kind: rpc.KindWell, W: 1, H: 1, Version: 9},
	})
	handed := mustRow(t, c, "1", "100")
	before := proto.CloneOf(handed)

	if !c.PatchTile(handed, func(n *gridwellv1.Tile) { n.ViewZoom = 2.5 }) {
		t.Fatal("PatchTile reported no change")
	}

	if !proto.Equal(handed, before) {
		t.Errorf("the handed-out row was edited: %v, want %v", handed, before)
	}
	if got := mustRow(t, c, "1", "100"); got.ViewZoom != 2.5 {
		t.Errorf("view_zoom = %v, want the patch's 2.5", got.ViewZoom)
	}
}

// The patch rides Apply, so the version interlock decides it: a row the cache
// already holds at a later version is not rolled back by a stale patch.
func TestPatchTileObeysTheVersionInterlock(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{
		{Id: "100", GridId: "1", Kind: rpc.KindText, W: 1, H: 1, Version: 9},
	})
	stale := proto.CloneOf(mustRow(t, c, "1", "100"))
	stale.Version = 8

	if c.PatchTile(stale, func(n *gridwellv1.Tile) { n.TextX = 42 }) {
		t.Error("a patch older than the cached row landed")
	}
	if got := mustRow(t, c, "1", "100"); got.TextX != 0 {
		t.Errorf("text_x = %d, want the cached row's 0", got.TextX)
	}
}

// A patch for a grid this client never fetched is dropped, like any event for
// one: nothing asks for a room nobody is in.
func TestPatchTileDropsAnUncachedGrid(t *testing.T) {
	c := New()
	if c.PatchTile(&gridwellv1.Tile{Id: "100", GridId: "9", Kind: rpc.KindWell},
		func(n *gridwellv1.Tile) { n.ViewZoom = 2 }) {
		t.Error("a patch into an uncached grid reported a change")
	}
}

func mustRow(t *testing.T, c *Cache, gridID, tileID string) *gridwellv1.Tile {
	t.Helper()
	g, ok := c.Grid(gridID)
	if !ok {
		t.Fatalf("grid %s not cached", gridID)
	}
	n, ok := g.Tiles[tileID]
	if !ok {
		t.Fatalf("tile %s not cached", tileID)
	}
	return n
}
