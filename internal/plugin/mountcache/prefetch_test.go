package mountcache

import (
	"bytes"
	"context"
	"testing"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The #254 promise, pinned: after a prefetch, grids and bodies the user
// NEVER opened are readable while the mount is dark — offline readability
// is whole-mount, not touched-only.

// seedNested builds root → well → (text "deep note", inner well) on the
// upstream store, entirely BEHIND the cache (raw client), so nothing is
// cached by the seeding itself. Returns the nested grid id, the inner
// well's child grid id, and the text tile id.
func seedNested(t *testing.T, cc *Client, upstream *darkable, root string) (nested, inner, textID string) {
	t.Helper()
	ctx := context.Background()
	raw := upstream.Namespace
	well, err := raw.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "well", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	nested = well.GetTile().GetChildGridId()
	txt, err := raw.CreateTile(ctx, &pb.CreateTileRequest{GridId: nested,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	textID = txt.GetTile().GetId()
	writeOne(t, raw, textID, txt.GetTile().GetVersion(), []byte("deep note"))
	iw, err := raw.CreateTile(ctx, &pb.CreateTileRequest{GridId: nested,
		Tile: &pb.Tile{Kind: "well", X: 2, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	inner = iw.GetTile().GetChildGridId()
	return nested, inner, textID
}

func TestPrefetchMakesUntouchedGridsReadableDark(t *testing.T) {
	cc, upstream, root, _ := fixture(t)
	ctx := context.Background()
	nested, inner, textID := seedNested(t, cc, upstream, root)

	cc.Prefetch(ctx)
	upstream.dark = true

	g, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: nested})
	if err != nil {
		t.Fatalf("never-opened grid must read from the prefetched cache: %v", err)
	}
	if len(g.GetTiles()) != 2 {
		t.Errorf("nested grid = %d tiles, want 2", len(g.GetTiles()))
	}
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: inner}); err != nil {
		t.Errorf("recursion must reach the inner grid: %v", err)
	}
	_, _, data := readContent(t, cc, textID)
	if !bytes.Equal(data, []byte("deep note")) {
		t.Errorf("prefetched body = %q, want the deep note", data)
	}
}

func TestPrefetchAbortsQuietlyWhenDark(t *testing.T) {
	cc, upstream, _, _ := fixture(t)
	upstream.dark = true
	cc.Prefetch(context.Background()) // must simply return, not wedge or panic
}

func TestSubscribeKicksPrefetch(t *testing.T) {
	cc, upstream, root, _ := fixture(t)
	ctx := context.Background()
	nested, _, _ := seedNested(t, cc, upstream, root)

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	go func() {
		_ = cc.Subscribe(subCtx, &pb.SubscribeRequest{}, func(*pb.Event) error { return nil })
	}()
	// The kick is async; poll the cache for the never-opened grid.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := cc.loadGrid(ctx, nested); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subscribe never warmed the nested grid")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStaleBitMarksCacheServedGrids(t *testing.T) {
	cc, upstream, root, _ := fixture(t)
	ctx := context.Background()
	live, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if live.GetGrid().GetStale() {
		t.Error("a live answer must never wear the stale bit")
	}
	upstream.dark = true
	stale, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if !stale.GetGrid().GetStale() {
		t.Error("a cache-served grid must say so on the wire (#256)")
	}
	// Back alive: the bit clears (it is never stored).
	upstream.dark = false
	again, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if again.GetGrid().GetStale() {
		t.Error("the stale bit leaked into the stored row")
	}
}
