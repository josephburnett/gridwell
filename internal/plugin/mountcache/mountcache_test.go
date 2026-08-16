package mountcache

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/plugins/local"
	"github.com/josephburnett/gridwell/plugins/local/store"
)

// serveInProcess is plugin.ServeInProcess's shape, inlined: the loader now
// imports THIS package (the cache interposition), so the test cannot
// import the loader back without a cycle.
func serveInProcess(t *testing.T, impl pb.GridwellServer) pb.GridwellClient {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterGridwellServer(srv, impl)
	go srv.Serve(lis)
	cc, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cc.Close()
		srv.GracefulStop()
	})
	return pb.NewGridwellClient(cc)
}

// The mount-cache contract (docs/offline-plan.md phase 1): every
// successful read is remembered; a TRANSPORT-dark mount serves the
// remembered answer; an ANSWERED error — NotFound, anything coded — is
// never masked by a cached row; the cache survives a restart; and a
// successful re-read reconciles what changed while dark. The fixture is a
// real localdb behind a switchable dark proxy — the exact shape of an ssh
// mount whose tunnel dropped.

// darkable wraps the upstream client; dark=true fails every READ the way
// a dead tunnel does. Writes are not intercepted — write behavior under
// darkness is the pass-through error path, not cache behavior.
type darkable struct {
	pb.GridwellClient
	dark bool
}

func (d *darkable) offline() error { return status.Error(codes.Unavailable, "tunnel down") }

func (d *darkable) Info(ctx context.Context, in *pb.InfoRequest, opts ...grpc.CallOption) (*pb.InfoResponse, error) {
	if d.dark {
		return nil, d.offline()
	}
	return d.GridwellClient.Info(ctx, in, opts...)
}
func (d *darkable) GetGrid(ctx context.Context, in *pb.GetGridRequest, opts ...grpc.CallOption) (*pb.GetGridResponse, error) {
	if d.dark {
		return nil, d.offline()
	}
	return d.GridwellClient.GetGrid(ctx, in, opts...)
}
func (d *darkable) GetTile(ctx context.Context, in *pb.GetTileRequest, opts ...grpc.CallOption) (*pb.TileResponse, error) {
	if d.dark {
		return nil, d.offline()
	}
	return d.GridwellClient.GetTile(ctx, in, opts...)
}
func (d *darkable) GetTilePreview(ctx context.Context, in *pb.GetTilePreviewRequest, opts ...grpc.CallOption) (*pb.GetTilePreviewResponse, error) {
	if d.dark {
		return nil, d.offline()
	}
	return d.GridwellClient.GetTilePreview(ctx, in, opts...)
}
func (d *darkable) ReadContent(ctx context.Context, in *pb.ReadContentRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[pb.ContentChunk], error) {
	if d.dark {
		return nil, d.offline()
	}
	return d.GridwellClient.ReadContent(ctx, in, opts...)
}
func (d *darkable) Subscribe(ctx context.Context, in *pb.SubscribeRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[pb.Event], error) {
	if d.dark {
		return nil, d.offline()
	}
	return d.GridwellClient.Subscribe(ctx, in, opts...)
}

func fixture(t *testing.T) (cc *Client, upstream *darkable, root string, dbPath string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw := serveInProcess(t, local.New(st, nil))
	root, err = st.RootGridID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	upstream = &darkable{GridwellClient: raw}
	dbPath = filepath.Join(t.TempDir(), "cache.db")
	cc, dbClose, err := Open(upstream, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dbClose)
	return cc, upstream, root, dbPath
}

func drainContent(t *testing.T, s grpc.ServerStreamingClient[pb.ContentChunk]) (mediaType string, version int64, data []byte) {
	t.Helper()
	for {
		ch, err := s.Recv()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("content recv: %v", err)
		}
		if ch.GetMediaType() != "" {
			mediaType = ch.GetMediaType()
		}
		if ch.GetVersion() != 0 {
			version = ch.GetVersion()
		}
		data = append(data, ch.GetData()...)
	}
}

func TestServesStaleWhenDark(t *testing.T) {
	cc, upstream, root, _ := fixture(t)
	ctx := context.Background()

	txt, err := cc.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	// Body via the upstream write door (writes pass through the cache).
	stream, err := cc.WriteContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pb.WriteContentRequest{TileId: txt.GetTile().GetId(),
		Version: txt.GetTile().GetVersion(), Data: []byte("remembered words")}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		t.Fatal(err)
	}
	urlT, err := cc.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "url", X: 2, Y: 0, W: 1, H: 1, UrlString: "https://example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cc.SetTile(ctx, &pb.SetTileRequest{TileId: urlT.GetTile().GetId(),
		Version: urlT.GetTile().GetVersion(),
		Tile:    &pb.Tile{Kind: "url", UrlString: "https://example.com"},
		Preview: []byte("\xff\xd8jpegface")}); err != nil {
		t.Fatal(err)
	}

	// Warm every read path online.
	if _, err := cc.Info(ctx, &pb.InfoRequest{}); err != nil {
		t.Fatal(err)
	}
	warm, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cc.GetTile(ctx, &pb.GetTileRequest{TileId: txt.GetTile().GetId()}); err != nil {
		t.Fatal(err)
	}
	rs, err := cc.ReadContent(ctx, &pb.ReadContentRequest{TileId: txt.GetTile().GetId()})
	if err != nil {
		t.Fatal(err)
	}
	_, liveVer, liveBody := drainContent(t, rs)
	if _, err := cc.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: urlT.GetTile().GetId()}); err != nil {
		t.Fatal(err)
	}

	// THE TUNNEL DROPS.
	upstream.dark = true

	if _, err := cc.Info(ctx, &pb.InfoRequest{}); err != nil {
		t.Fatalf("dark Info should serve the remembered handshake: %v", err)
	}
	g, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatalf("dark GetGrid should serve stale: %v", err)
	}
	if len(g.GetTiles()) != len(warm.GetTiles()) {
		t.Fatalf("stale grid has %d tiles, want %d", len(g.GetTiles()), len(warm.GetTiles()))
	}
	tr, err := cc.GetTile(ctx, &pb.GetTileRequest{TileId: txt.GetTile().GetId()})
	if err != nil || tr.GetTile().GetKind() != "text" {
		t.Fatalf("dark GetTile = (%v, %v)", tr, err)
	}
	rs, err = cc.ReadContent(ctx, &pb.ReadContentRequest{TileId: txt.GetTile().GetId()})
	if err != nil {
		t.Fatalf("dark ReadContent should serve stale: %v", err)
	}
	mt, ver, body := drainContent(t, rs)
	if string(body) != "remembered words" || string(body) != string(liveBody) {
		t.Fatalf("stale body = %q, want the live bytes", body)
	}
	if ver != liveVer {
		t.Fatalf("stale version = %d, want %d (the version travels with the bytes it vouches for)", ver, liveVer)
	}
	if mt == "" {
		t.Error("stale stream lost the media type")
	}
	pv, err := cc.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: urlT.GetTile().GetId()})
	if err != nil || string(pv.GetJpeg()) != "\xff\xd8jpegface" {
		t.Fatalf("dark preview = (%q, %v)", pv.GetJpeg(), err)
	}

	// A read the cache never saw stays an honest transport error.
	if _, err := cc.GetTile(ctx, &pb.GetTileRequest{TileId: "999"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("dark miss = %v, want the original Unavailable", err)
	}
}

// failAtRecv is a ReadContent stream whose OPEN succeeded but whose first
// Recv fails — the shape a dark mount actually has on a CHAINED read: the
// open only reaches the local transit plugin (alive); the remote's
// unreachability arrives as the first frame's error. Found by the real
// federation partition gate, not by any open-fails fixture.
type failAtRecv struct {
	noopClientStream
	err error
}

func (f *failAtRecv) Recv() (*pb.ContentChunk, error) { return nil, f.err }

type recvDark struct {
	pb.GridwellClient
	dark bool
}

func (d *recvDark) ReadContent(ctx context.Context, in *pb.ReadContentRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[pb.ContentChunk], error) {
	if d.dark {
		return &failAtRecv{err: status.Error(codes.Unavailable, "tunnel down at first frame")}, nil
	}
	return d.GridwellClient.ReadContent(ctx, in, opts...)
}

func TestStaleServedWhenStreamFailsAtFirstRecv(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw := serveInProcess(t, local.New(st, nil))
	root, err := st.RootGridID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	upstream := &recvDark{GridwellClient: raw}
	cc, dbClose, err := Open(upstream, filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dbClose)
	ctx := context.Background()

	txt, err := cc.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := cc.WriteContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Send(&pb.WriteContentRequest{TileId: txt.GetTile().GetId(),
		Version: txt.GetTile().GetVersion(), Data: []byte("first-frame words")}); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.CloseAndRecv(); err != nil {
		t.Fatal(err)
	}
	rs, err := cc.ReadContent(ctx, &pb.ReadContentRequest{TileId: txt.GetTile().GetId()})
	if err != nil {
		t.Fatal(err)
	}
	drainContent(t, rs) // warm

	upstream.dark = true
	rs, err = cc.ReadContent(ctx, &pb.ReadContentRequest{TileId: txt.GetTile().GetId()})
	if err != nil {
		t.Fatal(err)
	}
	_, _, body := drainContent(t, rs)
	if string(body) != "first-frame words" {
		t.Fatalf("first-recv-dark read = %q, want the cached body", body)
	}

	// A miss stays the honest error — at Recv, exactly where it happened.
	rs, err = cc.ReadContent(ctx, &pb.ReadContentRequest{TileId: "424242"})
	if err != nil {
		t.Fatal(err)
	}
	if _, rerr := rs.Recv(); status.Code(rerr) != codes.Unavailable {
		t.Fatalf("dark miss recv = %v, want Unavailable", rerr)
	}
}

// TestVerdictNeverMasked pins the gate: an upstream that ANSWERS overrides
// any cached row. A tile read once and then deleted on the remote answers
// NotFound — serving the cached row instead would resurrect deleted
// content ("gone is never a link", cache edition).
func TestVerdictNeverMasked(t *testing.T) {
	cc, _, root, _ := fixture(t)
	ctx := context.Background()

	txt, err := cc.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	id := txt.GetTile().GetId()
	if _, err := cc.GetTile(ctx, &pb.GetTileRequest{TileId: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := cc.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: id, Version: txt.GetTile().GetVersion()}); err != nil {
		t.Fatal(err)
	}
	if _, err := cc.GetTile(ctx, &pb.GetTileRequest{TileId: id}); status.Code(err) != codes.NotFound {
		t.Fatalf("answered gone = %v, want NotFound passed through (never the cached row)", err)
	}
}

// TestEventTeeTracksMutations: the Subscribe stream the server holds
// through the wrapper keeps the cache current — a framing change upserts,
// a delete evicts — so going dark right after serves the LATEST state.
func TestEventTeeTracksMutations(t *testing.T) {
	cc, upstream, root, _ := fixture(t)
	ctx := context.Background()

	well, err := cc.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "well", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	doomed, err := cc.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", X: 2, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}

	sub, err := cc.Subscribe(ctx, &pb.SubscribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	evs := make(chan *pb.Event, 64)
	go func() {
		for {
			ev, rerr := sub.Recv()
			if rerr != nil {
				close(evs)
				return
			}
			evs <- ev
		}
	}()
	// PRIME the subscription: a gRPC stream open is lazy, so a mutation
	// fired before the server registers the subscriber is missed forever
	// (this test hung exactly that way once). Re-fire a framing write (no
	// version bump — the same claim stays valid) until its event arrives.
	primed := false
	for i := 0; i < 50 && !primed; i++ {
		if _, err := cc.SetTile(ctx, &pb.SetTileRequest{TileId: well.GetTile().GetId(),
			Version: well.GetTile().GetVersion(),
			Tile:    &pb.Tile{Kind: "well", ViewX: 1, ViewY: 1, ViewZoom: 2}}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-evs:
			primed = true
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !primed {
		t.Fatal("subscription never delivered the priming event")
	}

	if _, err := cc.SetTile(ctx, &pb.SetTileRequest{TileId: well.GetTile().GetId(),
		Version: well.GetTile().GetVersion(),
		Tile:    &pb.Tile{Kind: "well", ViewX: 9, ViewY: 9, ViewZoom: 3}}); err != nil {
		t.Fatal(err)
	}
	if _, err := cc.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: doomed.GetTile().GetId(),
		Version: doomed.GetTile().GetVersion()}); err != nil {
		t.Fatal(err)
	}
	// Pump the tee until both mutations have flowed through it.
	deadline := time.After(15 * time.Second)
	sawFraming, sawRemove := false, false
	for !(sawFraming && sawRemove) {
		select {
		case ev, ok := <-evs:
			if !ok {
				t.Fatal("event stream closed early")
			}
			switch p := ev.GetPayload().(type) {
			case *pb.Event_TileChanged:
				if p.TileChanged.GetTile().GetId() == well.GetTile().GetId() &&
					p.TileChanged.GetTile().GetViewZoom() == 3 {
					sawFraming = true
				}
			case *pb.Event_TileRemoved:
				if p.TileRemoved.GetTileId() == doomed.GetTile().GetId() {
					sawRemove = true
				}
			}
		case <-deadline:
			t.Fatalf("events never arrived: framing=%v remove=%v", sawFraming, sawRemove)
		}
	}

	upstream.dark = true
	g, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	var gotWell *pb.Tile
	for _, tl := range g.GetTiles() {
		if tl.GetId() == doomed.GetTile().GetId() {
			t.Fatal("the teed TileRemoved should have evicted the deleted tile")
		}
		if tl.GetId() == well.GetTile().GetId() {
			gotWell = tl
		}
	}
	if gotWell == nil || gotWell.GetViewZoom() != 3 {
		t.Fatalf("the teed TileChanged should have kept the framing current: %+v", gotWell)
	}
}

// TestPersistsAcrossRestart: the whole point of SQLite over memory — the
// node restarts (or the wrapper reopens) and the remembered answers are
// still there for a mount that is STILL dark.
func TestPersistsAcrossRestart(t *testing.T) {
	cc, upstream, root, dbPath := fixture(t)
	ctx := context.Background()

	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}

	reopened, closer, err := Open(upstream, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	upstream.dark = true
	if _, err := reopened.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatalf("reopened cache should serve the remembered grid: %v", err)
	}
}

// TestRefreshReconcilesWhatChangedWhileBlind: mutations the cache never
// saw (no subscription running) reconcile on the next successful GetGrid —
// the response IS the complete tile set, so a delete-while-blind vanishes
// from the cache too.
func TestRefreshReconcilesWhatChangedWhileBlind(t *testing.T) {
	cc, upstream, root, _ := fixture(t)
	ctx := context.Background()

	doomed, err := cc.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	// Deleted UPSTREAM directly — the cache never sees an event.
	if _, err := upstream.GridwellClient.DeleteTile(ctx, &pb.DeleteTileRequest{
		TileId: doomed.GetTile().GetId(), Version: doomed.GetTile().GetVersion()}); err != nil {
		t.Fatal(err)
	}
	// The refresh read replaces the grid's whole tile set.
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	upstream.dark = true
	g, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range g.GetTiles() {
		if tl.GetId() == doomed.GetTile().GetId() {
			t.Fatal("a delete-while-blind must reconcile out on the next successful GetGrid")
		}
	}
}
