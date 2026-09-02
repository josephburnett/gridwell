package server

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/sourcecache"
)

// farConnection is the shape of a namespace reached over a network: it answers
// grids, and it has an event stream of its own that carries nothing here — a
// connection whose remote is quiet, or whose events were missed while the link
// was down. tiles is what the far side currently holds, changed behind the
// cache's back.
type farConnection struct {
	namespace.Unimplemented
	tiles atomic.Int32
}

func (f *farConnection) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	return &pb.InfoResponse{Kind: "node", Watch: true}, nil
}

func (f *farConnection) GetGrid(_ context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	resp := &pb.GetGridResponse{Grid: &pb.Grid{Id: in.GridId}}
	for i := int32(0); i < f.tiles.Load(); i++ {
		resp.Tiles = append(resp.Tiles, &pb.Tile{
			Id: in.GridId + "/t" + strconv.Itoa(int(i)), GridId: in.GridId,
			Kind: "text", X: int64(i), Y: 0, W: 1, H: 1,
		})
	}
	return resp, nil
}

func (f *farConnection) Subscribe(ctx context.Context, _ *pb.SubscribeRequest, _ func(*pb.Event) error) error {
	<-ctx.Done()
	return nil
}

// The serve-first loop across the whole seam it spans: the router's fan-in,
// the cache layer's own event stream, and a connection behind it. A remembered
// grid answers from the cache, the revalidation the read kicks finds the far
// side changed, the GridChanged reaches the client's Subscribe stream
// qualified with the node's id and the connection segment — the event a client
// refetches on — and the next read serves the correction. A unit test on
// either side would miss the qualification hop and the fan-in reaching the
// layer's stream at all.
func TestServeFirstEventReachesTheClient(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	reg := plugin.NewRegistry()
	registerPrimaryLocaldb(t, reg, st)

	far := &farConnection{}
	far.tiles.Store(1)
	cache, err := sourcecache.Open(t.TempDir() + "/cache.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	// The production transport wiring, with a millisecond window: every hit is
	// past it, so every read serves the remembering and revalidates — the
	// aged-cache shape without the wait.
	front := cache.Front(far, sourcecache.Options{Prefetch: true, FreshWindow: time.Millisecond})
	const conn = "geneva"
	root := conn + "/rnode1/g1"
	reg.SetTransport(front, func(context.Context) []plugin.ConnectionRow {
		return []plugin.ConnectionRow{{Name: conn, Label: "Geneva", RootGridID: root}}
	}, nil)

	const nodeID = "lnode1"
	srv := mustNew(t, reg, Config{ID: nodeID})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx := context.Background()

	list, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if len(list.Connections) != 1 {
		t.Fatalf("connections = %+v, want the one", list.Connections)
	}
	qualified := list.Connections[0].RootGridID
	if qualified != nodeID+"/"+root {
		t.Fatalf("connection root = %q, want %q", qualified, nodeID+"/"+root)
	}
	warm, err := cl.GetGrid(ctx, qualified)
	if err != nil {
		t.Fatalf("GetGrid warm: %v", err)
	}
	if len(warm.Tiles) != 1 {
		t.Fatalf("warm listing = %d tiles, want the seeded 1", len(warm.Tiles))
	}

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	events := make(chan rpc.Event, 64)
	go func() {
		// The connect client's Subscribe blocks until the server's first
		// event flushes the response headers, and nothing emits until the
		// priming below causes it — so the whole subscription lives here.
		stream, serr := cl.Subscribe(subCtx)
		if serr != nil {
			close(events)
			return
		}
		defer stream.Close()
		for {
			ev, ok, rerr := stream.Recv()
			if !ok || rerr != nil {
				close(events)
				return
			}
			events <- ev
		}
	}()

	// The fan-in registers with the layer asynchronously, so an event fired
	// before it lands is missed and a no-delta revalidation emits nothing
	// after that. Keep making the far side genuinely change — one more tile,
	// a read to kick the refresh — until an event arrives.
	deadline := time.Now().Add(15 * time.Second)
	var got *rpc.GridChanged
	for got == nil {
		if time.Now().After(deadline) {
			t.Fatal("the revalidation's GridChanged never reached the client stream")
		}
		far.tiles.Add(1)
		if _, err := cl.GetGrid(ctx, qualified); err != nil {
			t.Fatalf("GetGrid kick: %v", err)
		}
		timeout := time.After(300 * time.Millisecond)
	drain:
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					t.Fatal("event stream closed early")
				}
				if ev.Kind == rpc.EventGridChanged && ev.GridChanged != nil && ev.GridChanged.GridID == qualified {
					got = ev.GridChanged
					break drain
				}
			case <-timeout:
				break drain
			}
		}
	}

	// The event is the client's cue to refetch; the refetch serves the
	// revalidation's answer, which has the new tiles.
	after, err := cl.GetGrid(ctx, qualified)
	if err != nil {
		t.Fatalf("GetGrid after event: %v", err)
	}
	if len(after.Tiles) <= len(warm.Tiles) {
		t.Fatalf("post-event listing = %d tiles, want more than the warm %d", len(after.Tiles), len(warm.Tiles))
	}
}
