package sourcecache

import (
	"bytes"
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// The prefetch seam: the cache in front of the REAL transport, not a stand-in
// for it. The walk asks the fronted namespace where its content begins, and
// every prefetch test used to ask that of a home store behind a proxy — a
// namespace that answers Info, which no transport does. So the walk returned
// at its first line against the one seam it exists for, and every test stayed
// green. These run the production shape: sourcecache → connection.Server →
// a connection.

// connFixture wires one connection, dialed in-process to a far node's store,
// behind a cache layer under the given policy. far.dark makes the machine go
// away exactly as a dropped tunnel does: every call through the connection
// answers Unavailable.
func connFixture(t *testing.T, opts Options) (cc *Layer, far *darkable, farRoot, conn string) {
	t.Helper()
	return connFixtureWith(t, opts, nil)
}

// connFixtureWith is connFixture with one decorator around the namespace the
// dialer hands back, so a test can watch what the layer actually asks the far
// node for. The decorator wraps the darkable, not the store: what it sees is
// exactly what crossed the connection.
func connFixtureWith(t *testing.T, opts Options, wrap func(namespace.Namespace) namespace.Namespace) (cc *Layer, far *darkable, farRoot, conn string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	farRoot, err = st.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	far = newDarkable(local.New(st, nil))
	// The near node's own store: it owns the connections table the transport
	// writes through.
	near, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = near.Close() })
	db, err := connection.NewDB(near.SQL())
	if err != nil {
		t.Fatal(err)
	}
	conn = "farconn"
	dialed := namespace.Namespace(far)
	if wrap != nil {
		dialed = wrap(dialed)
	}
	transport, err := connection.New(db, func(dial.Config) (namespace.Namespace, func(), error) {
		return dialed, func() {}, nil
	}, "", []config.ConnectionConfig{{Name: conn, Addr: "/far/federation.sock"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	transport.ConnectAll(ctx)
	return openLayer(t, transport, filepath.Join(t.TempDir(), "cache.db"), opts), far, farRoot, conn
}

// qualify names a far id the way the transport does, and the way the cache
// therefore remembers it.
func qualify(conn, id string) string { return conn + "/" + id }

// seedNested builds root → well → (text "deep note", inner well) on the far
// store, entirely behind the cache through a raw client, so nothing is cached
// by the seeding itself. It returns the nested grid id, the inner well's child
// grid id, and the text tile id, in the source's own frame: a caller reading
// through a connection qualifies them.
func seedNested(t *testing.T, raw namespace.Namespace, root string) (nested, inner, textID string) {
	t.Helper()
	ctx := context.Background()
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

// The offline promise across the real seam: after a walk, grids and bodies
// nobody opened read while the machine is gone. This is the test that fails
// against a transport with no Info.
func TestPrefetchWarmsAWholeConnection(t *testing.T) {
	cc, far, farRoot, conn := connFixture(t, Options{Prefetch: true})
	ctx := context.Background()
	nested, inner, textID := seedNested(t, far.Namespace, farRoot)

	cc.Prefetch(ctx)
	far.goDark()

	g, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: qualify(conn, nested)})
	if err != nil {
		t.Fatalf("a never-opened grid on a dark connection must read from the prefetched cache: %v", err)
	}
	if len(g.GetTiles()) != 2 {
		t.Errorf("nested grid = %d tiles, want 2", len(g.GetTiles()))
	}
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: qualify(conn, inner)}); err != nil {
		t.Errorf("the walk must recurse to the inner grid: %v", err)
	}
	_, _, data := readContent(t, cc, qualify(conn, textID))
	if !bytes.Equal(data, []byte("deep note")) {
		t.Errorf("prefetched body = %q, want the deep note", data)
	}
}

// The trigger is the Subscribe establishment the server's fan-in makes, so
// warming needs no deliberate call: connect, and the connection is offline-
// readable.
func TestSubscribeKicksPrefetch(t *testing.T) {
	cc, far, farRoot, conn := connFixture(t, Options{Prefetch: true})
	ctx := context.Background()
	nested, _, _ := seedNested(t, far.Namespace, farRoot)

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	go func() {
		_ = cc.Subscribe(subCtx, &pb.SubscribeRequest{}, func(*pb.Event) error { return nil })
	}()
	// The kick is async; poll the cache for the never-opened grid.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, _, ok := cc.loadGrid(ctx, qualify(conn, nested)); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subscribe never warmed the nested grid")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A connection that is gone when the walk starts aborts it quietly: the
// roots are still declared (the landing is remembered config), and the first
// read through the dead tunnel ends the walk.
func TestPrefetchAbortsQuietlyWhenDark(t *testing.T) {
	cc, far, _, _ := connFixture(t, Options{Prefetch: true})
	far.goDark()
	cc.Prefetch(context.Background()) // must simply return, not wedge or panic
}

// TestSubscribeDoesNotCrawlWithoutThePolicy: the walk is a per-seam policy
// over the one engine, not part of the engine, and the default is off. The
// seam is the Subscribe trigger, so this drives the same door the transport
// does — over the same real transport, so a green result means the walk
// could have run and did not — and asserts nothing was warmed. A whole-source
// crawl is what unreachability costs a NETWORK seam; a layer fronted without
// asking for one must read through and remember, nothing more.
func TestSubscribeDoesNotCrawlWithoutThePolicy(t *testing.T) {
	cc, far, farRoot, conn := connFixture(t, Options{})
	ctx := context.Background()
	nested, _, _ := seedNested(t, far.Namespace, farRoot)

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	go func() {
		_ = cc.Subscribe(subCtx, &pb.SubscribeRequest{}, func(*pb.Event) error { return nil })
	}()
	// Give a walk every chance to happen before declaring it did not: the
	// positive twin above finds the grid well inside this window.
	time.Sleep(500 * time.Millisecond)
	if _, _, ok := cc.loadGrid(ctx, qualify(conn, nested)); ok {
		t.Fatal("a namespace without the prefetch policy crawled itself anyway")
	}
	// The layer still caches what is actually read: the engine is the same,
	// and only the crawl is policy.
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: qualify(conn, nested)}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := cc.loadGrid(ctx, qualify(conn, nested)); !ok {
		t.Fatal("a read-through answer was not remembered")
	}
}

// gridReads counts what the layer asked the far node for, by grid id. A grid
// only the walk ever visits is therefore a walk counter.
type gridReads struct {
	namespace.Namespace
	mu sync.Mutex
	n  map[string]int
}

func (g *gridReads) GetGrid(ctx context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	g.mu.Lock()
	g.n[in.GetGridId()]++
	g.mu.Unlock()
	return g.Namespace.GetGrid(ctx, in)
}

func (g *gridReads) count(id string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n[id]
}

// awaitHealth reads relayed events until one connection's health matches want.
func awaitHealth(t *testing.T, events <-chan *pb.Event, conn string, want bool) {
	t.Helper()
	for {
		select {
		case ev := <-events:
			h := ev.GetPluginHealth()
			if h != nil && h.GetPluginUuid() == conn && h.GetHealthy() == want {
				return
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("the connection's health never went %v on the relayed stream", want)
		}
	}
}

// TestOneConnectionsRecoveryDoesNotReWalkTheSource pins what trace (b) says
// does NOT happen. kickPrefetch fires from Layer.Subscribe, and the layer's
// upstream subscription is the TRANSPORT's hub stream, which is one stream for
// every connection and survives any one of them dying. So a connection going
// dark and coming back — the health round trip the layer relays and acts on —
// re-kicks nothing: the resync after a recovery is the client's blunt refetch
// of the grids it holds, not a re-walk of the source.
//
// This is deliberately a pin of CURRENT behaviour, not an endorsement: whether
// a health-up should kick the walk (so the deletes-while-away resync covers
// grids nobody re-opened) is owner-question 5 in docs/freshness.md. It is
// worth a test either way, because both comments claimed the opposite and no
// test could tell.
func TestOneConnectionsRecoveryDoesNotReWalkTheSource(t *testing.T) {
	var reads *gridReads
	cc, far, farRoot, conn := connFixtureWith(t, Options{Prefetch: true},
		func(ns namespace.Namespace) namespace.Namespace {
			reads = &gridReads{Namespace: ns, n: map[string]int{}}
			return reads
		})
	ctx := context.Background()
	nested, _, _ := seedNested(t, far.Namespace, farRoot)

	// The one subscription the server's fan-in holds for the life of a
	// client: the walk's trigger, and the stream the health rides.
	events := make(chan *pb.Event, 64)
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	go func() {
		_ = cc.Subscribe(subCtx, &pb.SubscribeRequest{}, func(ev *pb.Event) error {
			select {
			case events <- ev:
			default:
			}
			return nil
		})
	}()
	awaitWalkDone(t, cc, ctx, qualify(conn, nested))
	walked := reads.count(nested)
	if walked != 1 {
		t.Fatalf("the first walk read the nested grid %d times, want exactly 1", walked)
	}

	// The machine leaves and returns, both transitions on the stream the
	// layer relays and applies. Nothing else calls through the connection.
	far.goDark()
	awaitHealth(t, events, conn, false)
	far.goLive()
	awaitHealth(t, events, conn, true)

	// Give a re-walk every chance to happen before declaring it did not: the
	// first walk finished well inside this window.
	time.Sleep(time.Second)
	if got := reads.count(nested); got != walked {
		t.Fatalf("the nested grid was read %d times after a health round trip, want %d — "+
			"a single connection's recovery re-walked the whole source (owner-question 5)", got, walked)
	}
}

// awaitWalkDone waits for the Subscribe-triggered walk to reach the given grid
// and then finish, so a count taken after it is a whole walk's worth.
func awaitWalkDone(t *testing.T, cc *Layer, ctx context.Context, gridID string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, _, warmed := cc.loadGrid(ctx, gridID)
		cc.pf.mu.Lock()
		running := cc.pf.running
		cc.pf.mu.Unlock()
		if warmed && !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the subscribe walk never settled (warmed=%v running=%v)", warmed, running)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
