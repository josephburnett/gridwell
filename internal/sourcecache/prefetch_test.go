package sourcecache

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// After a prefetch, grids and bodies the user never opened are readable while
// the source is dark: offline readability is whole-source, not touched-only.

// seedNested builds root → well → (text "deep note", inner well) on the
// upstream store, entirely behind the cache through a raw client, so nothing
// is cached by the seeding itself. It returns the nested grid id, the inner
// well's child grid id, and the text tile id.
func seedNested(t *testing.T, cc *Layer, upstream *darkable, root string) (nested, inner, textID string) {
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
		if _, _, ok := cc.loadGrid(ctx, nested); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subscribe never warmed the nested grid")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestSubscribeDoesNotCrawlWithoutThePolicy: the walk is a per-seam policy
// over the one engine, not part of the engine, and the default is off. The
// seam is the Subscribe trigger, so this drives the same door the transport
// does and asserts nothing was warmed. A whole-source crawl is what
// unreachability costs a NETWORK seam; a layer fronted without asking for one
// must read through and remember, nothing more.
func TestSubscribeDoesNotCrawlWithoutThePolicy(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	root, err := st.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	upstream := &darkable{Namespace: local.New(st, nil)}
	cc := openLayer(t, upstream, filepath.Join(t.TempDir(), "cache.db"), Options{})
	nested, _, _ := seedNested(t, cc, upstream, root)

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	go func() {
		_ = cc.Subscribe(subCtx, &pb.SubscribeRequest{}, func(*pb.Event) error { return nil })
	}()
	// Give a walk every chance to happen before declaring it did not: the
	// positive twin above finds the grid well inside this window.
	time.Sleep(500 * time.Millisecond)
	if _, _, ok := cc.loadGrid(ctx, nested); ok {
		t.Fatal("a namespace without the prefetch policy crawled itself anyway")
	}
	// The layer still caches what is actually read: the engine is the same,
	// and only the crawl is policy.
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: nested}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := cc.loadGrid(ctx, nested); !ok {
		t.Fatal("a read-through answer was not remembered")
	}
}

func TestStaleBitMarksAnswersPastTheirWindow(t *testing.T) {
	cc, upstream, root, _ := fixture(t)
	ctx := context.Background()
	live, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if live.GetGrid().GetStale() {
		t.Error("a live answer must never wear the stale bit")
	}
	// Within the window a remembered answer serves as good as live — even
	// dark, since nothing consults the source.
	upstream.dark = true
	fresh, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.GetGrid().GetStale() {
		t.Error("a within-window answer must not wear the stale bit")
	}
	// Past the window it says so on the wire (#256, serve-first edition) —
	// and stays said while the source is dark, since the revalidation the
	// read kicks fails transport-shaped and changes nothing.
	ageGrid(t, cc, root)
	stale, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if !stale.GetGrid().GetStale() {
		t.Error("a remembered answer past its window must say so on the wire (#256)")
	}
	// Back alive: the next read still serves the remembered answer, and the
	// revalidation it kicks lands and clears the bit, which is never stored.
	upstream.dark = false
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	awaitFresh(t, cc, root)
	again, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if again.GetGrid().GetStale() {
		t.Error("the stale bit leaked into the stored row")
	}
}

// degrading is an upstream that answers, but with a degraded grid: the shape a
// plugin adapter takes when its source goes dark, holding the rows it minted,
// no source facts, stamped stale. The cache must not remember it, because the
// degraded answer succeeds and nothing else would ever put the good one
// back.
type degrading struct {
	namespace.Namespace
	degraded bool
}

func (d *degrading) GetGrid(ctx context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	resp, err := d.Namespace.GetGrid(ctx, in)
	if err != nil || !d.degraded {
		return resp, err
	}
	resp.Grid.Stale = true
	resp.Tiles = nil // whatever the source said is missing
	return resp, nil
}

func TestAStaleAnswerIsNeverRemembered(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	root, err := st.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw := local.New(st, nil)
	if _, err := raw.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}}); err != nil {
		t.Fatal(err)
	}
	up := &degrading{Namespace: raw}
	cc := openLayer(t, up, filepath.Join(t.TempDir(), "cache.db"), Options{})
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}

	up.degraded = true
	// Age the row so the read revalidates against the degraded upstream; the
	// revalidation must not store the degraded answer. There is no landing to
	// await — a stale answer changes nothing — so give it every chance the
	// no-prefetch test above gives a walk, then look.
	ageGrid(t, cc, root)
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	cached, _, ok := cc.loadGrid(ctx, root)
	if !ok {
		t.Fatal("the good answer was dropped")
	}
	if len(cached.GetTiles()) != 1 {
		t.Fatalf("a stale answer overwrote the good one: %d tiles remembered, want 1", len(cached.GetTiles()))
	}
	if cached.GetGrid().GetStale() {
		t.Fatal("the stale bit was stored")
	}
}
