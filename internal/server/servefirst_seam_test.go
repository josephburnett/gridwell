package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/sourcecache"
)

// The serve-first loop across the whole seam it spans: the router's fan-in,
// the cache layer's synthetic event stream, and the shipped fs plugin behind
// it. A remembered plugin grid answers from the cache, the revalidation the
// read kicks finds the source changed, the GridChanged reaches the client's
// Subscribe stream qualified with the plugin's uuid — the event a client
// refetches on — and the next read serves the correction. A unit test on
// either side would miss the qualification hop and the watch declaration
// that lets the fan-in reach the layer's stream at all.
func TestServeFirstEventReachesTheClient(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	reg := plugin.NewRegistry()
	registerPrimaryLocaldb(t, reg, st)

	fsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(fsRoot, "first.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache, err := sourcecache.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	// A millisecond window: every hit is past it, so every read serves the
	// remembering and revalidates — the aged-cache shape without the wait.
	front := cache.Front(newPluginClient(t, "fs", map[string]string{"root": fsRoot}),
		sourcecache.Options{FreshWindow: time.Millisecond})
	reg.Register(fsPluginUUID, "fs", front, nil)

	srv := mustNew(t, reg, Config{})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx := context.Background()

	list, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	root := list.Plugins[1].RootGridID
	warm, err := cl.GetGrid(ctx, root)
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
	// after that. Keep making the source genuinely change — a new file, a
	// read to kick the refresh — until an event arrives (the tee test primes
	// the same way).
	deadline := time.Now().Add(15 * time.Second)
	var got *rpc.GridChanged
	for i := 0; got == nil; i++ {
		if time.Now().After(deadline) {
			t.Fatal("the revalidation's GridChanged never reached the client stream")
		}
		name := fmt.Sprintf("more-%d.txt", i)
		if err := os.WriteFile(filepath.Join(fsRoot, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := cl.GetGrid(ctx, root); err != nil {
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
				if ev.Kind == rpc.EventGridChanged && ev.GridChanged != nil && ev.GridChanged.GridID == root {
					got = ev.GridChanged
					break drain
				}
			case <-timeout:
				break drain
			}
		}
	}

	// The event is the client's cue to refetch; the refetch serves the
	// revalidation's answer, which has the new files.
	after, err := cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatalf("GetGrid after event: %v", err)
	}
	if len(after.Tiles) <= len(warm.Tiles) {
		t.Fatalf("post-event listing = %d tiles, want more than the warm %d", len(after.Tiles), len(warm.Tiles))
	}
}
