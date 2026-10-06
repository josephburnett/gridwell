package sourcecache

import (
	"bytes"
	"context"
	"fmt"
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
	conn = "farconn"
	dialed := namespace.Namespace(far)
	if wrap != nil {
		dialed = wrap(dialed)
	}
	transport, err := connection.New(near, func(dial.Config) (namespace.Namespace, func(), error) {
		return dialed, func() {}, nil
	}, "", []config.ConnectionConfig{{Name: conn, Addr: "/far/federation.sock"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	transport.ConnectAll(ctx)
	return openLayer(t, transport, filepath.Join(t.TempDir(), "cache.db"), opts), far, farRoot, conn
}

// farNode is one machine behind a multi-connection fixture: what it answers,
// and a count of what it was asked.
type farNode struct {
	far   *darkable
	reads *gridReads
}

// twoConnFixture wires TWO connections, each to its own far node, behind one
// cache layer — the shape a per-source behaviour has to be isolated in. The
// dial is routed by address, so what each machine was asked is separately
// countable.
func twoConnFixture(t *testing.T, opts Options) (cc *Layer, a, b *farNode, roots, conns [2]string) {
	t.Helper()
	ctx := context.Background()
	conns = [2]string{"connone", "conntwo"}
	nodes := [2]*farNode{}
	byAddr := map[string]*farNode{}
	cfgs := []config.ConnectionConfig{}
	for i, name := range conns {
		st, err := store.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if roots[i], err = st.RootGridID(ctx); err != nil {
			t.Fatal(err)
		}
		n := &farNode{far: newDarkable(local.New(st, nil))}
		n.reads = &gridReads{Namespace: n.far, n: map[string]int{}}
		addr := "/far/" + name + "/federation.sock"
		nodes[i], byAddr[addr] = n, n
		cfgs = append(cfgs, config.ConnectionConfig{Name: name, Addr: addr})
	}
	near, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = near.Close() })
	transport, err := connection.New(near, func(cfg dial.Config) (namespace.Namespace, func(), error) {
		n, ok := byAddr[cfg.Addr]
		if !ok {
			return nil, nil, fmt.Errorf("no far node at %s", cfg.Addr)
		}
		return n.reads, func() {}, nil
	}, "", cfgs, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	transport.ConnectAll(ctx)
	return openLayer(t, transport, filepath.Join(t.TempDir(), "cache.db"), opts), nodes[0], nodes[1], roots, conns
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

	cc.prefetch(ctx, "")
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
	cc.prefetch(context.Background(), "") // must simply return, not wedge or panic
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
// only the walk ever visits is therefore a walk counter. It can also hold one
// grid's read open, so a test can land triggers while a walk is provably in
// flight rather than hoping to.
type gridReads struct {
	namespace.Namespace
	mu     sync.Mutex
	n      map[string]int
	holdID string
	hold   chan struct{}
}

func (g *gridReads) GetGrid(ctx context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	g.mu.Lock()
	g.n[in.GetGridId()]++
	hold, holdID := g.hold, g.holdID
	g.mu.Unlock()
	if hold != nil && in.GetGridId() == holdID {
		<-hold // the count is already in: the caller can see the walk is here
	}
	return g.Namespace.GetGrid(ctx, in)
}

func (g *gridReads) count(id string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n[id]
}

// holdOn parks every read of one grid until the returned channel is closed.
func (g *gridReads) holdOn(id string) chan struct{} {
	ch := make(chan struct{})
	g.mu.Lock()
	defer g.mu.Unlock()
	g.holdID, g.hold = id, ch
	return ch
}

// awaitCount polls one grid's read count up to want.
func (g *gridReads) awaitCount(t *testing.T, id string, want int, why string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for g.count(id) < want {
		if time.Now().After(deadline) {
			t.Fatalf("%s: %s was read %d times, want %d", why, id, g.count(id), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// health is one connection's health as it arrives on the stream this layer
// relays: the uuid is the source key, one segment for a connection.
func health(source string, healthy bool) *pb.Event {
	return &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{
		PluginUuid: source, Healthy: healthy,
	}}}
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

// The second trigger across the real seam. The layer's upstream subscription is
// the transport's hub stream, one for every connection, which survives any one
// of them dying, so a single connection's recovery never reaches the walk that
// way. What reaches it is the health-up transition applyEvent sees, which kicks
// the walk for that source: the client's own refetch covers the grids it holds,
// and this covers the ones nobody re-opened.
func TestOneConnectionsRecoveryReWalksThatSource(t *testing.T) {
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
	// layer relays and applies. Nothing else calls through the connection, so
	// a second read of this grid can only be the walk.
	far.goDark()
	awaitHealth(t, events, conn, false)
	far.goLive()
	awaitHealth(t, events, conn, true)

	reads.awaitCount(t, nested, walked+1,
		"a recovered connection must re-walk its own source, not wait for the next establishment")
}

// The other direction of the same fact, and the one a real recovery usually
// takes first: nobody's health event has arrived yet, but a pass-through call
// answers again, so the layer knows the source is back. That is a recovery
// too, and it walks. (The health arm is one writer of dark; this is the
// other, and hanging the trigger off the health arm alone made the whole
// feature a no-op against real binaries, where the client's refetch beats the
// fan-in's backoff.) No subscription here, so nothing but the read can be
// what triggered the walk.
func TestARecoveryNoticedByAReadWalksTheSourceToo(t *testing.T) {
	var reads *gridReads
	cc, far, farRoot, conn := connFixtureWith(t, Options{Prefetch: true},
		func(ns namespace.Namespace) namespace.Namespace {
			reads = &gridReads{Namespace: ns, n: map[string]int{}}
			return reads
		})
	ctx := context.Background()
	nested, _, _ := seedNested(t, far.Namespace, farRoot)

	far.goDark()
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: qualify(conn, farRoot)}); err == nil {
		t.Fatal("a read through a dark connection must fail: nothing is remembered yet")
	}
	far.goLive()
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: qualify(conn, farRoot)}); err != nil {
		t.Fatal(err)
	}
	reads.awaitCount(t, nested, 1,
		"a call answering again is a recovery, and the source is walked")
}

// A recovery warms the source that recovered and nothing else: the walk is a
// whole machine's traversal, and running every one of them because one came
// back would spend a second machine's bandwidth on news about the first.
func TestARecoveryWalksOnlyTheSourceThatRecovered(t *testing.T) {
	cc, a, b, roots, conns := twoConnFixture(t, Options{Prefetch: true})
	ctx := context.Background()
	nestedA, _, _ := seedNested(t, a.far.Namespace, roots[0])
	nestedB, _, _ := seedNested(t, b.far.Namespace, roots[1])

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
	// The establishment walk covers both machines; from here their counts
	// diverge only by what a recovery walks.
	awaitWalkDone(t, cc, ctx, qualify(conns[0], nestedA))
	awaitWalkDone(t, cc, ctx, qualify(conns[1], nestedB))
	walkedA, walkedB := a.reads.count(nestedA), b.reads.count(nestedB)

	a.far.goDark()
	awaitHealth(t, events, conns[0], false)
	a.far.goLive()
	awaitHealth(t, events, conns[0], true)
	a.reads.awaitCount(t, nestedA, walkedA+1, "the recovered source is walked")

	if got := b.reads.count(nestedB); got != walkedB {
		t.Fatalf("the untouched connection's nested grid was read %d times, want %d — "+
			"one source's recovery walked another source", got, walkedB)
	}
}

// A connection that flaps is a trigger storm, and the guard is the walk's
// single-flight keyed by source: twenty recoveries must not mean twenty
// traversals of the same machine. The health arm is driven directly, because a
// storm is a matter of the layer's own timing and a real fan-in's backoff
// cannot produce one; TestOneConnectionsRecoveryReWalksThatSource pins that the
// relayed stream reaches this arm at all.
func TestAFlapStormDoesNotStackWalks(t *testing.T) {
	var reads *gridReads
	cc, far, farRoot, conn := connFixtureWith(t, Options{Prefetch: true},
		func(ns namespace.Namespace) namespace.Namespace {
			reads = &gridReads{Namespace: ns, n: map[string]int{}}
			return reads
		})
	ctx := context.Background()
	nested, _, _ := seedNested(t, far.Namespace, farRoot)

	// Hold the walk inside its first read, so every trigger below lands while
	// a walk is provably running rather than between two of them.
	release := reads.holdOn(farRoot)
	flap := func() {
		cc.applyEvent(ctx, health(conn, false))
		cc.applyEvent(ctx, health(conn, true))
	}
	flap()
	reads.awaitCount(t, farRoot, 1, "the first recovery must start a walk")
	for i := 0; i < 20; i++ {
		flap()
	}
	close(release)
	awaitWalkIdle(t, cc)

	if got := reads.count(farRoot); got != 1 {
		t.Fatalf("the source root was read %d times, want 1 — a flap storm stacked walks", got)
	}
	if got := reads.count(nested); got != 1 {
		t.Fatalf("the nested grid was read %d times, want 1 — a flap storm stacked walks", got)
	}
}

// awaitWalkDone waits for the Subscribe-triggered walk to reach the given grid
// and then finish, so a count taken after it is a whole walk's worth.
func awaitWalkDone(t *testing.T, cc *Layer, ctx context.Context, gridID string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, _, warmed := cc.loadGrid(ctx, gridID)
		if warmed && !walkRunning(cc) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the subscribe walk never settled (warmed=%v running=%v)", warmed, walkRunning(cc))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// awaitWalkIdle waits for every walk to be out.
func awaitWalkIdle(t *testing.T, cc *Layer) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for walkRunning(cc) {
		if time.Now().After(deadline) {
			t.Fatal("a walk never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func walkRunning(cc *Layer) bool {
	cc.pf.mu.Lock()
	defer cc.pf.mu.Unlock()
	return len(cc.pf.running) > 0
}
