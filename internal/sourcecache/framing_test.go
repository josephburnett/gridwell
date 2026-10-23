package sourcecache

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace/namespacetest"
)

// framingSource is a far node reduced to what a door's framing touches: a
// handshake per namespace, one grid, and a framing write that lands or does
// not. Its event stream is silent, so whatever the layer caches of a write
// it learned from the write itself.
type framingSource struct {
	namespacetest.Unimplemented
	mu    sync.Mutex
	down  bool
	lists map[string]*pb.HandshakeResponse
	grid  *pb.GetGridResponse
}

func (f *framingSource) dark() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.down
}

func (f *framingSource) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

func (f *framingSource) offline() error { return gwerr.Unheard("tunnel down") }

func (f *framingSource) Handshake(_ context.Context, in *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	if f.dark() {
		return nil, f.offline()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return proto.Clone(f.lists[in.GetNamespace()]).(*pb.HandshakeResponse), nil
}

func (f *framingSource) GetGrid(context.Context, *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	if f.dark() {
		return nil, f.offline()
	}
	return proto.Clone(f.grid).(*pb.GetGridResponse), nil
}

func (f *framingSource) SetFraming(context.Context, *pb.SetFramingRequest) (*pb.SetFramingResponse, error) {
	if f.dark() {
		return nil, f.offline()
	}
	return &pb.SetFramingResponse{}, nil
}

func (f *framingSource) SetTile(context.Context, *pb.SetTileRequest) (*pb.TileResponse, error) {
	if f.dark() {
		return nil, f.offline()
	}
	return nil, status.Error(codes.Unimplemented, "not in this fixture")
}

func (f *framingSource) Subscribe(ctx context.Context, _ *pb.SubscribeRequest, send func(*pb.Event) error) error {
	if err := send(nil); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

const (
	farHome  = "conn/r/g1"
	farFeed  = "conn/p/~ZmVlZA"
	farOther = "conn/r/g9"
	farDoor  = "conn/r/t1"
)

// framingFixture caches both handshakes a far root's framing rides — the
// transport's own row list and the far node's plugin list — and one grid
// holding a door, all with nothing framed yet.
func framingFixture(t *testing.T) (*Layer, *framingSource) {
	t.Helper()
	src := &framingSource{
		lists: map[string]*pb.HandshakeResponse{
			"": {Plugins: []*pb.PluginInfo{rpc.ConnectionRow("conn", "Far", farHome, "", rpc.View{})}},
			"conn": {HomeGridId: farHome, Plugins: []*pb.PluginInfo{
				{Uuid: "conn/r", RootGridId: farHome},
				{Uuid: "conn/p", PluginGrids: []*pb.PluginGrid{
					{Id: "feed", GridId: farFeed}, {Id: "other", GridId: farOther},
				}},
			}},
		},
		grid: &pb.GetGridResponse{Grid: &pb.Grid{Id: farHome}, Tiles: []*pb.Tile{
			{Id: farDoor, GridId: farHome, Kind: rpc.KindDoor, ChildGridId: "conn/r/g2", W: 1, H: 1},
		}},
	}
	cc := openLayer(t, src, filepath.Join(t.TempDir(), "cache.db"), Options{})
	ctx := context.Background()
	for _, ns := range []string{"", "conn"} {
		if _, err := cc.Handshake(ctx, &pb.HandshakeRequest{Namespace: ns}); err != nil {
			t.Fatalf("Handshake(%q): %v", ns, err)
		}
	}
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: farHome}); err != nil {
		t.Fatalf("GetGrid: %v", err)
	}
	return cc, src
}

// cached reads what the layer answers for ns with the source gone.
func cached(t *testing.T, cc *Layer, src *framingSource, ns string) *pb.HandshakeResponse {
	t.Helper()
	src.setDown(true)
	defer src.setDown(false)
	resp, err := cc.Handshake(context.Background(), &pb.HandshakeRequest{Namespace: ns})
	if err != nil {
		t.Fatalf("Handshake(%q) while dark: %v", ns, err)
	}
	return resp
}

func rowFraming(pl *pb.PluginInfo) rpc.View {
	return rpc.ViewOf(pl.RootViewCx, pl.RootViewCy, pl.RootViewZoom)
}

func entryFraming(e *pb.PluginGrid) rpc.View {
	return rpc.ViewOf(e.ViewCx, e.ViewCy, e.ViewZoom)
}

// A far root's framing event lands on every cached door rooted at that
// grid, in every cached handshake, and on nothing else; the duplicate the
// write's own echo makes changes nothing.
func TestAFramingEventReframesTheCachedDoors(t *testing.T) {
	cc, src := framingFixture(t)
	ctx := context.Background()
	f := mkFraming(3, -4, 0.5)
	g := mkFraming(7, 8, 2)
	for range 2 {
		cc.applyEvent(ctx, rpc.FramingEvent(farHome, f))
		cc.applyEvent(ctx, rpc.FramingEvent(farFeed, g))
	}

	rows := cached(t, cc, src, "").GetPlugins()
	if got := rowFraming(rows[0]); !got.SameAs(rpc.Saved(f)) {
		t.Errorf("the connection row = %+v, want %+v", got, f)
	}
	far := cached(t, cc, src, "conn")
	if got := rowFraming(rpc.HomeRow(far)); !got.SameAs(rpc.Saved(f)) {
		t.Errorf("the far home row = %+v, want %+v", got, f)
	}
	entries := far.GetPlugins()[1].GetPluginGrids()
	if got := entryFraming(entries[0]); !got.SameAs(rpc.Saved(g)) {
		t.Errorf("the grid entry = %+v, want %+v", got, g)
	}
	if got := entryFraming(entries[1]); !got.SameAs(rpc.View{}) {
		t.Errorf("an entry rooted elsewhere moved: %+v", got)
	}
}

// A framing write the source accepted is cached from its answer, whether
// or not its event ever arrives: the source is silent here.
func TestAnAcceptedRootFramingIsCached(t *testing.T) {
	cc, src := framingFixture(t)
	f := mkFraming(1, 2, 1.5)
	if _, err := cc.SetFraming(context.Background(), &pb.SetFramingRequest{
		RootGridId: farHome, Cx: f.Cx(), Cy: f.Cy(), Zoom: f.Zoom()}); err != nil {
		t.Fatalf("SetFraming: %v", err)
	}
	if got := rowFraming(cached(t, cc, src, "").GetPlugins()[0]); !got.SameAs(rpc.Saved(f)) {
		t.Errorf("the connection row = %+v, want %+v", got, f)
	}
	if got := rowFraming(rpc.HomeRow(cached(t, cc, src, "conn"))); !got.SameAs(rpc.Saved(f)) {
		t.Errorf("the far home row = %+v, want %+v", got, f)
	}
}

// A framing write the source never heard is refused as every dark write is,
// and the cache keeps the source's last word: the client's outbox owns the
// retry, not this layer.
func TestADarkFramingWriteIsRefusedAndNotCached(t *testing.T) {
	cc, src := framingFixture(t)
	ctx := context.Background()
	was := mkFraming(1, 1, 1)
	cc.applyEvent(ctx, rpc.FramingEvent(farHome, was))
	src.setDown(true)
	_, err := cc.SetFraming(ctx, &pb.SetFramingRequest{RootGridId: farHome, Cx: 9, Cy: 9, Zoom: 3})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("dark SetFraming = %v, want Unavailable", err)
	}
	_, werr := cc.SetTile(ctx, &pb.SetTileRequest{TileId: farDoor})
	if status.Code(werr) != status.Code(err) {
		t.Errorf("dark SetFraming answers %v, another dark write %v: one refusal", status.Code(err), status.Code(werr))
	}
	src.setDown(false)
	if got := rowFraming(rpc.HomeRow(cached(t, cc, src, "conn"))); !got.SameAs(rpc.Saved(was)) {
		t.Errorf("the far home row = %+v, want the last accepted %+v", got, was)
	}
}

// A door's framing rides its tile, so the tile event keeps the cached
// listing current.
func TestATileEventReframesTheCachedDoor(t *testing.T) {
	cc, src := framingFixture(t)
	ctx := context.Background()
	door := proto.Clone(src.grid.GetTiles()[0]).(*pb.Tile)
	door.ViewCx, door.ViewCy, door.ViewZoom = 5, 6, 0.25
	cc.applyEvent(ctx, &pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: door}}})
	src.setDown(true)
	resp, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: farHome})
	if err != nil {
		t.Fatalf("GetGrid while dark: %v", err)
	}
	if got := resp.GetTiles()[0]; got.ViewCx != 5 || got.ViewCy != 6 || got.ViewZoom != 0.25 {
		t.Errorf("the cached door = %+v, want the event's framing", got)
	}
}

// A live answer that says nothing about a door's framing — the transport's
// row for a connection it cannot reach — keeps the cached one, and a live
// answer that says something replaces it: coming back is the source's word.
func TestSilenceKeepsTheCachedFramingAndAnAnswerReplacesIt(t *testing.T) {
	cc, src := framingFixture(t)
	ctx := context.Background()
	f := mkFraming(3, -4, 0.5)
	cc.applyEvent(ctx, rpc.FramingEvent(farHome, f))
	resp, err := cc.Handshake(ctx, &pb.HandshakeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rowFraming(resp.GetPlugins()[0]); !got.SameAs(rpc.Saved(f)) {
		t.Errorf("a row answering no framing = %+v, want the cached %+v", got, f)
	}
	if got := rowFraming(cached(t, cc, src, "").GetPlugins()[0]); !got.SameAs(rpc.Saved(f)) {
		t.Errorf("the silence was cached over the framing: %+v", got)
	}

	g := mkFraming(1, 1, 2)
	src.mu.Lock()
	src.lists[""].Plugins[0].RootViewCx, src.lists[""].Plugins[0].RootViewCy, src.lists[""].Plugins[0].RootViewZoom = g.Cx(), g.Cy(), g.Zoom()
	src.mu.Unlock()
	if resp, err = cc.Handshake(ctx, &pb.HandshakeRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := rowFraming(resp.GetPlugins()[0]); !got.SameAs(rpc.Saved(g)) {
		t.Errorf("the source's answer = %+v, want %+v", got, g)
	}
	if got := rowFraming(cached(t, cc, src, "").GetPlugins()[0]); !got.SameAs(rpc.Saved(g)) {
		t.Errorf("cached after the answer = %+v, want %+v", got, g)
	}
}

// A root framing write names its grid, not a tile, so its outcome is the
// root's source's reachability. Read off the empty tile id it named the
// source "", which is every unchained grid's, and a refused pan made every
// such grid revalidate on each read.
func TestARootFramingWriteDarkensItsOwnSourceOnly(t *testing.T) {
	cc, src := framingFixture(t)
	src.setDown(true)
	if _, err := cc.SetFraming(context.Background(), &pb.SetFramingRequest{RootGridId: farHome, Cx: 1, Cy: 1, Zoom: 1}); err == nil {
		t.Fatal("a dark source accepted a framing write")
	}
	if cc.isDark("") {
		t.Error("a failed root write darkened the source of every unchained grid")
	}
	if !cc.isDark(rpc.UUIDOf(farHome)) {
		t.Errorf("a failed root write left its own source %q light", rpc.UUIDOf(farHome))
	}
}
