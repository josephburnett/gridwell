package sourcecache

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/eventhub"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// Darkness is the layer's own fact about a source: while it holds, a
// remembered grid serves without ever asking the source, and every read
// revalidates behind. It is learned two ways, and both are tested across the
// real transport seam, because both are facts that cross it: a pass-through
// call that fails, and the connection's own health on the stream this layer
// relays.

// The two directions driven through every transition and compared. They are one
// fact, so the map must end in the same place whichever direction taught it,
// and the only difference allowed is the announcement: noteReach found it alone
// and must tell the client, while the health arm is relaying the very event the
// client is also receiving. The announcements are counted, not merely observed,
// so a second bare writer of c.dark that grew an emit or lost one is red.
func TestBothDirectionsLearnTheSameDarkness(t *testing.T) {
	for _, tc := range []struct {
		name string
		from bool // what the layer already believed
		to   bool // what this outcome says
	}{
		{"light stays light", false, false},
		{"light goes dark", false, true},
		{"dark stays dark", true, true},
		{"dark comes back", true, false},
	} {
		transition := tc.from != tc.to
		t.Run(tc.name, func(t *testing.T) {
			// Direction one: a pass-through call's outcome. Announces on the
			// transition, because nobody else saw the call fail.
			byCall, calls := darkFixture(t)
			seedDark(byCall, tc.from)
			var callErr error
			if tc.to {
				callErr = status.Error(codes.Unavailable, "tunnel down")
			}
			byCall.noteReachGrid(callErr, "conn/g1")

			// Direction two: the source's own health, on the stream this layer
			// relays. Never announces.
			byHealth, healths := darkFixture(t)
			seedDark(byHealth, tc.from)
			byHealth.applyEvent(context.Background(), &pb.Event{
				Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{
					PluginUuid: "conn", Healthy: !tc.to,
				}},
			})

			// One fact: the two directions must agree on it.
			if got, want := byCall.isDark("conn"), tc.to; got != want {
				t.Errorf("after the call, dark = %v, want %v", got, want)
			}
			if got, want := byHealth.isDark("conn"), tc.to; got != want {
				t.Errorf("after the health event, dark = %v, want %v", got, want)
			}
			if byCall.isDark("conn") != byHealth.isDark("conn") {
				t.Error("the two directions disagree about the same source")
			}

			// One asymmetry, and exactly this much of it.
			wantCalls := 0
			if transition {
				wantCalls = 1
			}
			if got := len(announced(t, byCall, calls)); got != wantCalls {
				t.Errorf("the call direction announced %d times, want %d", got, wantCalls)
			}
			if got := len(announced(t, byHealth, healths)); got != 0 {
				t.Errorf("the health direction announced %d times, want 0: "+
					"the client is receiving this same event on this same stream", got)
			}
		})
	}
}

// darkFixture is a layer with nothing but a dark map and one subscriber, which
// is all setDark touches. Its announcements land in the returned channel.
func darkFixture(t *testing.T) (*Layer, <-chan *pb.Event) {
	t.Helper()
	c := &Layer{dark: map[string]bool{}, hub: eventhub.New(rpc.EventKey)}
	ch, detach := c.hub.Subscribe()
	t.Cleanup(detach)
	return c, ch
}

// seedDark puts the layer in the "before" state without going through either
// direction, so the transition under test is the first thing either one does.
func seedDark(c *Layer, dark bool) {
	c.darkMu.Lock()
	defer c.darkMu.Unlock()
	c.dark["conn"] = dark
}

// announced takes the announcements a direction owed. The fan-out delivers on
// a pump of its own, so a sentinel published behind them is what makes "none"
// provable: delivery is in first-touch order, so anything announced before the
// sentinel is in hand by the time it arrives.
func announced(t *testing.T, c *Layer, ch <-chan *pb.Event) []*pb.Event {
	t.Helper()
	c.emitGridChanged("sentinel")
	var out []*pb.Event
	for {
		select {
		case ev := <-ch:
			if ev.GetGridChanged().GetGridId() == "sentinel" {
				return out
			}
			out = append(out, ev)
		case <-time.After(10 * time.Second):
			t.Fatal("the sentinel never arrived; the announcement stream is stuck")
			return out
		}
	}
}

// awaitDark polls until the layer has learned that source is not answering.
func awaitDark(t *testing.T, cc *Layer, source string, why string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if cc.isDark(source) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the layer never learned the source was dark", why)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A failed call is darkness: the room the user re-enters right after the
// machine died is inside its freshness window, so its age says nothing. What
// says the source is not answering is that a call through the connection
// failed — and the remembered room still serves, which is the point of
// knowing.
func TestAFailedCallIsDarkness(t *testing.T) {
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

	// The machine goes away, and nothing has noticed yet. Believing it here
	// would call every source dark the moment any connection anywhere blinked.
	far.goDark()
	if cc.isDark(conn) {
		t.Fatal("no call has failed yet: nothing knows the connection is gone")
	}

	// One call through the connection fails; now the layer knows, and the
	// remembered room still serves, whole.
	if err := cc.ReadContent(ctx, &pb.ReadContentRequest{TileId: tileID},
		func(*pb.ContentChunk) error { return nil }); status.Code(err) != codes.Unavailable {
		t.Fatalf("read of an unremembered body on a dark connection = %v, want Unavailable", err)
	}
	if !cc.isDark(conn) {
		t.Fatal("a call that failed transport-shaped is how this layer learns (#256)")
	}
	dark, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatalf("a dark connection must still serve the remembered room: %v", err)
	}
	if len(dark.GetTiles()) != 1 || dark.GetTiles()[0].GetId() != tileID {
		t.Fatalf("the dark serve = %v, want the remembered tile", dark.GetTiles())
	}

	// The machine is back and answers: the next answer clears it.
	far.goLive()
	if _, err := cc.GetTile(ctx, &pb.GetTileRequest{TileId: tileID}); err != nil {
		t.Fatal(err)
	}
	if cc.isDark(conn) {
		t.Fatal("darkness must clear on the next answer")
	}
}

// The other direction, and the one the user actually meets: the machine dies
// while nobody is calling it. The connection's fan-in sees its event stream
// end and says so on the stream this layer relays, so the layer knows without
// a call of its own having to fail first.
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
	// Nothing here calls through the connection, so only the relayed health
	// event can teach this.
	awaitDark(t, cc, conn, "the connection's health went down")
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

// Discovering darkness announces the grid at hand, because this layer found
// it alone: nobody else saw the call fail, so without the event the room the
// client is holding goes on being refreshed from a source that is gone, and
// nothing ever revalidates it.
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
	awaitSubscriber(t, cc, events)

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
