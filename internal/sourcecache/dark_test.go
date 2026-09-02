package sourcecache

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// The stale bit means "this serve is a memory". Past the window a remembered
// answer is one because nothing has confirmed it; inside the window it is one
// as soon as the connection is known dark, because then a memory is all it
// can be. Darkness is learned two ways, and both are tested across the real
// transport seam, because both are facts that cross it: a pass-through call
// that fails, and the connection's own health on the stream this layer
// relays.

// awaitStale polls a grid read until the answer says it is a memory.
func awaitStale(t *testing.T, cc *Layer, gridID string, why string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		g, err := cc.GetGrid(context.Background(), &pb.GetGridRequest{GridId: gridID})
		if err == nil && g.GetGrid().GetStale() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the within-window read never said it was a memory (%v)", why, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A failed call is darkness: the room the user re-enters right after the
// machine died is inside its freshness window, so nothing about the answer's
// age says it is a memory. What says so is that the connection cannot be
// reached.
func TestAFailedCallMakesAWithinWindowServeAMemory(t *testing.T) {
	cc, far, farRoot, conn := connFixture(t, Options{})
	ctx := context.Background()
	root := qualify(conn, farRoot)
	txt, err := far.CreateTile(ctx, &pb.CreateTileRequest{GridId: farRoot,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	tileID := qualify(conn, txt.GetTile().GetId())
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}

	// The machine goes away, and nothing has noticed yet: a within-window
	// serve is still a serve. Stamping here would call every fresh answer a
	// memory the moment any connection anywhere blinked.
	far.goDark()
	g, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if g.GetGrid().GetStale() {
		t.Fatal("a within-window serve is not a memory while the connection is not known dark")
	}

	// One call through the connection fails; now the layer knows.
	if err := cc.ReadContent(ctx, &pb.ReadContentRequest{TileId: tileID},
		func(*pb.ContentChunk) error { return nil }); status.Code(err) != codes.Unavailable {
		t.Fatalf("read of an unremembered body on a dark connection = %v, want Unavailable", err)
	}
	stale, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if !stale.GetGrid().GetStale() {
		t.Fatal("a within-window serve from a connection known dark is a memory (#256)")
	}

	// The machine is back and answers: the next serve is a serve again.
	far.goLive()
	if _, err := cc.GetTile(ctx, &pb.GetTileRequest{TileId: tileID}); err != nil {
		t.Fatal(err)
	}
	again, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if again.GetGrid().GetStale() {
		t.Fatal("darkness must clear on the next answer: this serve is live again")
	}
}

// The other direction, and the one the user actually meets: the machine dies
// while nobody is calling it. The connection's fan-in sees its event stream
// end and says so on the stream this layer relays, so the room re-entered a
// second later already says it is a memory — no call of the cache's own has
// to fail first.
func TestAConnectionsHealthIsDarkness(t *testing.T) {
	cc, far, farRoot, conn := connFixture(t, Options{})
	ctx := context.Background()
	root := qualify(conn, farRoot)
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	// The server's fan-in holds one subscription through the layer for the
	// life of a client; that is the stream the health rides.
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	go func() {
		_ = cc.Subscribe(subCtx, &pb.SubscribeRequest{}, func(*pb.Event) error { return nil })
	}()

	far.goDark()
	// Nothing here calls through the connection: every read below is a
	// within-window hit, answered without touching the source. Only the
	// health event can make it say memory.
	awaitStale(t, cc, root, "the connection's health went down")
}

// halfOpen answers everything but one read, which fails transport-shaped:
// the link where the stream is fine and the calls are not, so the health
// event never comes and the failed call is the only discovery there is.
type halfOpen struct {
	namespace.Namespace
}

func (halfOpen) GetTilePreview(context.Context, *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	return nil, status.Error(codes.Unavailable, "tunnel down")
}

// Discovering darkness announces the grid at hand, because a client already
// holding that room is looking at a memory and does not know it: it refetches
// on GridChanged, and the refetch is what carries the stamp and the cached
// chip to the screen. Without the event the room looks live until something
// else happens to make the client read again.
func TestDarkDiscoveryTellsTheClientToReRead(t *testing.T) {
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
	cc := openLayer(t, &halfOpen{Namespace: local.New(st, nil)},
		filepath.Join(t.TempDir(), "cache.db"), Options{})
	txt, err := cc.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}

	events := make(chan *pb.Event, 16)
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
	// The subscription registers asynchronously; the discovery is a one-shot
	// transition, so let the stream be there before causing it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		cc.subsMu.Lock()
		n := len(cc.subs)
		cc.subsMu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the layer's own stream never registered the subscriber")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := cc.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: txt.GetTile().GetId()}); err == nil {
		t.Fatal("the preview read was supposed to fail transport-shaped")
	}
	for {
		select {
		case ev := <-events:
			if gc := ev.GetGridChanged(); gc != nil && gc.GetGridId() == root {
				return
			}
		case <-time.After(10 * time.Second):
			t.Fatal("discovering darkness told no client to re-read the room it was looking at")
		}
	}
}
