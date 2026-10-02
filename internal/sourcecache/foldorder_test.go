package sourcecache

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// heldSource answers each Handshake and GetGrid only when released, so a fold
// can land while a live read is in flight.
type heldSource struct {
	*framingSource
	entered chan struct{}
	release chan struct{}
}

func (h *heldSource) wait() {
	h.entered <- struct{}{}
	<-h.release
}

func (h *heldSource) Handshake(ctx context.Context, in *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	resp, err := h.framingSource.Handshake(ctx, in)
	h.wait()
	return resp, err
}

func (h *heldSource) GetGrid(ctx context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	resp, err := h.framingSource.GetGrid(ctx, in)
	h.wait()
	return resp, err
}

// heldFixture is framingFixture with a second layer over the same cache file
// in front of a held source, so its next live read is taken and then held.
func heldFixture(t *testing.T) (*Layer, *framingSource, *heldSource) {
	t.Helper()
	warm, src := framingFixture(t)
	held := &heldSource{framingSource: src, entered: make(chan struct{}, 1), release: make(chan struct{})}
	s := &Store{db: warm.db}
	cc := s.front(held, Options{})
	t.Cleanup(func() { cc.stopWalks(); cc.revalWG.Wait() })
	return cc, src, held
}

// An answer the source gave before a fold the layer applied since must not be
// installed over that fold: the event is the newer word. Each case takes a
// live read, folds while it is held, then releases the older answer.
func TestALiveAnswerNeverInstallsOverAFoldMadeSinceItsRead(t *testing.T) {
	ctx := context.Background()

	t.Run("a framing event against a handshake", func(t *testing.T) {
		cc, src, held := heldFixture(t)
		mine, theirs := rpc.Framing{Cx: 1, Cy: 2, Zoom: 1.5}, rpc.Framing{Cx: -3, Cy: 4, Zoom: 0.5}
		src.mu.Lock()
		row := src.lists[""].Plugins[0]
		row.RootViewCx, row.RootViewCy, row.RootViewZoom = mine.Cx, mine.Cy, mine.Zoom
		src.mu.Unlock()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = cc.Handshake(ctx, &pb.HandshakeRequest{})
		}()
		<-held.entered
		cc.applyEvent(ctx, rpc.FramingEvent(farHome, theirs))
		close(held.release)
		<-done
		if got := rowFraming(remembered(t, cc, src, "").GetPlugins()[0]); !got.SameAs(theirs) {
			t.Errorf("the connection row = %+v, want the folded %+v", got, theirs)
		}
	})

	t.Run("a tile event against a revalidation", func(t *testing.T) {
		cc, src, held := heldFixture(t)
		ageGrid(t, cc, farHome)
		well := proto.Clone(src.grid.GetTiles()[0]).(*pb.Tile)
		well.ViewCx, well.ViewCy, well.ViewZoom = 5, 6, 0.25
		if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: farHome}); err != nil {
			t.Fatal(err)
		}
		<-held.entered
		cc.applyEvent(ctx, &pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: well}}})
		close(held.release)
		cc.revalWG.Wait()
		if got := rememberedGrid(t, cc, src).GetTiles()[0]; got.ViewZoom != well.ViewZoom {
			t.Errorf("the remembered well = %+v, want the folded framing", got)
		}
	})

	t.Run("a removal against a revalidation", func(t *testing.T) {
		cc, src, held := heldFixture(t)
		ageGrid(t, cc, farHome)
		if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: farHome}); err != nil {
			t.Fatal(err)
		}
		<-held.entered
		cc.applyEvent(ctx, &pb.Event{Payload: &pb.Event_TileRemoved{TileRemoved: &pb.TileRemoved{TileId: farWell}}})
		close(held.release)
		cc.revalWG.Wait()
		if n := len(rememberedGrid(t, cc, src).GetTiles()); n != 0 {
			t.Errorf("the removed well came back: %d tiles remembered", n)
		}
	})
}

// rememberedGrid reads what the layer serves for the far home with the source
// gone.
func rememberedGrid(t *testing.T, cc *Layer, src *framingSource) *pb.GetGridResponse {
	t.Helper()
	src.setDown(true)
	defer src.setDown(false)
	resp, err := cc.GetGrid(context.Background(), &pb.GetGridRequest{GridId: farHome})
	if err != nil {
		t.Fatalf("GetGrid while dark: %v", err)
	}
	return resp
}
