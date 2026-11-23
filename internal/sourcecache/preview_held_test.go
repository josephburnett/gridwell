package sourcecache

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
	"google.golang.org/protobuf/proto"
)

// heldFar is a far node holding one url tile, counting the previews it is
// asked for, whose stream tells what the test sends.
type heldFar struct {
	namespace.Namespace
	events chan *pb.Event

	mu    sync.Mutex
	key   int64
	asked int
}

func (u *heldFar) tile() *pb.Tile {
	u.mu.Lock()
	defer u.mu.Unlock()
	return &pb.Tile{Id: "u1/1", GridId: "u1/g1", Kind: "url", W: 1, H: 1, PreviewBlobId: u.key}
}

func (u *heldFar) GetGrid(context.Context, *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	return &pb.GetGridResponse{Grid: &pb.Grid{Id: "u1/g1"}, Tiles: []*pb.Tile{u.tile()}}, nil
}

func (u *heldFar) GetTilePreview(context.Context, *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.asked++
	return &pb.GetTilePreviewResponse{Jpeg: []byte{0xff, 0xd8, byte(u.key)}}, nil
}

func (u *heldFar) Subscribe(ctx context.Context, _ *pb.SubscribeRequest, send func(*pb.Event) error) error {
	if err := send(nil); err != nil {
		return err
	}
	for {
		select {
		case ev := <-u.events:
			if err := send(ev); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (u *heldFar) askedFor() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.asked
}

// heldLayer is a layer over a heldFar keyed 7 whose grid was read, and a
// stream folding its events when subscribed.
func heldLayer(t *testing.T, subscribed bool) (*Layer, *heldFar, func(*pb.Event)) {
	t.Helper()
	far := &heldFar{events: make(chan *pb.Event), key: 7}
	cc := openLayer(t, far, filepath.Join(t.TempDir(), "cache.db"), Options{})
	ctx := context.Background()
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: "u1/g1"}); err != nil {
		t.Fatal(err)
	}
	seen := make(chan *pb.Event, 16)
	tell := func(ev *pb.Event) {
		t.Helper()
		far.events <- ev
		for got := range seen {
			if proto.Equal(got, ev) {
				return
			}
		}
	}
	if subscribed {
		sub, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		t.Cleanup(func() { cancel(); <-done })
		go func() {
			defer close(done)
			_ = cc.Subscribe(sub, &pb.SubscribeRequest{}, func(ev *pb.Event) error {
				if ev != nil {
					seen <- ev
				}
				return nil
			})
		}()
		tell(&pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: "u1/g1"}}})
	}
	return cc, far, tell
}

func askPreview(t *testing.T, cc *Layer) []byte {
	t.Helper()
	resp, err := cc.GetTilePreview(context.Background(), &pb.GetTilePreviewRequest{TileId: "u1/1"})
	if err != nil {
		t.Fatal(err)
	}
	return resp.GetJpeg()
}

// A preview held under the key the told row names is the same bytes the far
// node would send, so it is served here; a new key is asked for.
func TestAHeldPreviewIsServedUnderTheKeyTheRowNames(t *testing.T) {
	cc, far, tell := heldLayer(t, true)
	askPreview(t, cc)
	askPreview(t, cc)
	if n := far.askedFor(); n != 1 {
		t.Fatalf("the far node was asked %d times for a preview held under its key, want 1", n)
	}

	far.mu.Lock()
	far.key = 8
	far.mu.Unlock()
	tell(&pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: far.tile()}}})
	if got := askPreview(t, cc); got[2] != 8 || far.askedFor() != 2 {
		t.Fatalf("after the key moved: bytes %v and %d asks, want the new preview asked once more", got, far.askedFor())
	}
	askPreview(t, cc)
	if n := far.askedFor(); n != 2 {
		t.Fatalf("the new key's preview was asked again: %d asks", n)
	}
}

// With no stream folding the far node's changes, or one whose source says
// they go untold, the row may name a key the far node has moved past, so the
// far node is asked.
func TestAPreviewIsAskedWhenTheRowIsNotKeptCurrent(t *testing.T) {
	t.Run("no stream", func(t *testing.T) {
		cc, far, _ := heldLayer(t, false)
		askPreview(t, cc)
		askPreview(t, cc)
		if n := far.askedFor(); n != 2 {
			t.Fatalf("asked %d times with no stream, want every time", n)
		}
	})
	t.Run("not told", func(t *testing.T) {
		cc, far, tell := heldLayer(t, true)
		askPreview(t, cc)
		ev := rpc.HealthEvent("u1", true, "")
		ev.GetSourceHealth().Told = proto.Bool(false)
		tell(ev)
		askPreview(t, cc)
		if n := far.askedFor(); n != 2 {
			t.Fatalf("asked %d times while not told, want every time", n)
		}
		ev = rpc.HealthEvent("u1", true, "")
		ev.GetSourceHealth().Told = proto.Bool(true)
		tell(ev)
		askPreview(t, cc)
		if n := far.askedFor(); n != 2 {
			t.Fatalf("asked again once told: %d asks", n)
		}
	})
	t.Run("no row", func(t *testing.T) {
		far := &heldFar{events: make(chan *pb.Event), key: 7}
		cc := openLayer(t, far, filepath.Join(t.TempDir(), "cache.db"), Options{})
		askPreview(t, cc)
		askPreview(t, cc)
		if n := far.askedFor(); n != 2 {
			t.Fatalf("asked %d times for a tile with no row, want every time", n)
		}
	})
}
