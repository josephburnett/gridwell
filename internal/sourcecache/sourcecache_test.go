package sourcecache

import (
	"context"
	"io"
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

// writeOne sends one complete value through a namespace's WriteContent —
// the caller's half of the stream, ending at io.EOF (the clean end that
// commits).
func writeOne(t *testing.T, c namespace.Namespace, tileID string, version int64, data []byte) {
	t.Helper()
	sent := false
	if _, err := c.WriteContent(context.Background(), func() (*pb.WriteContentRequest, error) {
		if sent {
			return nil, io.EOF
		}
		sent = true
		return &pb.WriteContentRequest{TileId: tileID, Version: version, Data: data}, nil
	}); err != nil {
		t.Fatalf("WriteContent: %v", err)
	}
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
	namespace.Namespace
	dark bool
}

func (d *darkable) offline() error { return status.Error(codes.Unavailable, "tunnel down") }

func (d *darkable) Info(ctx context.Context, in *pb.InfoRequest) (*pb.InfoResponse, error) {
	if d.dark {
		return nil, d.offline()
	}
	return d.Namespace.Info(ctx, in)
}
func (d *darkable) GetGrid(ctx context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	if d.dark {
		return nil, d.offline()
	}
	return d.Namespace.GetGrid(ctx, in)
}
func (d *darkable) GetTile(ctx context.Context, in *pb.GetTileRequest) (*pb.TileResponse, error) {
	if d.dark {
		return nil, d.offline()
	}
	return d.Namespace.GetTile(ctx, in)
}
func (d *darkable) GetTilePreview(ctx context.Context, in *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	if d.dark {
		return nil, d.offline()
	}
	return d.Namespace.GetTilePreview(ctx, in)
}
func (d *darkable) ReadContent(ctx context.Context, in *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	if d.dark {
		return d.offline()
	}
	return d.Namespace.ReadContent(ctx, in, send)
}
func (d *darkable) Subscribe(ctx context.Context, in *pb.SubscribeRequest, send func(*pb.Event) error) error {
	if d.dark {
		return d.offline()
	}
	return d.Namespace.Subscribe(ctx, in, send)
}

// openLayer is the production wiring in one line: one cache file, one
// layer in front of one namespace, under the given policy.
func openLayer(t *testing.T, upstream namespace.Namespace, dbPath string, opts Options) *Layer {
	t.Helper()
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.Front(upstream, opts)
}

// fixture is the TRANSPORT's shape: the cache in front of a namespace
// whose absence is a machine going dark, so the prefetch policy is on.
func fixture(t *testing.T) (cc *Layer, upstream *darkable, root string, dbPath string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err = st.RootGridID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	upstream = &darkable{Namespace: local.New(st, nil)}
	dbPath = filepath.Join(t.TempDir(), "cache.db")
	return openLayer(t, upstream, dbPath, Options{Prefetch: true}), upstream, root, dbPath
}

func readContent(t *testing.T, c namespace.Namespace, tileID string) (mediaType string, version int64, data []byte) {
	t.Helper()
	if err := c.ReadContent(context.Background(), &pb.ReadContentRequest{TileId: tileID},
		func(ch *pb.ContentChunk) error {
			if ch.GetMediaType() != "" {
				mediaType = ch.GetMediaType()
			}
			if ch.GetVersion() != 0 {
				version = ch.GetVersion()
			}
			data = append(data, ch.GetData()...)
			return nil
		}); err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	return mediaType, version, data
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
	writeOne(t, cc, txt.GetTile().GetId(), txt.GetTile().GetVersion(), []byte("remembered words"))
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
	_, liveVer, liveBody := readContent(t, cc, txt.GetTile().GetId())
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
	mt, ver, body := readContent(t, cc, txt.GetTile().GetId())
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

// halfDark fails a ReadContent AFTER letting one chunk through — the
// tunnel dropping mid-body. The cache must NOT splice its remembered bytes
// onto the half-delivered stream: that would hand the caller a body nobody
// ever had. (Before S2 this shape was "the open succeeded but the first
// Recv failed", because a chained read opened only as far as the local
// transit plugin; in-process there is no open to succeed separately, so
// the surviving distinction is the one that always mattered — before any
// chunk, or after.)
type halfDark struct {
	namespace.Namespace
	dark bool
}

func (d *halfDark) ReadContent(ctx context.Context, in *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	if !d.dark {
		return d.Namespace.ReadContent(ctx, in, send)
	}
	if err := send(&pb.ContentChunk{MediaType: "text/markdown", Version: 1, Data: []byte("half a ")}); err != nil {
		return err
	}
	return status.Error(codes.Unavailable, "tunnel down mid-body")
}

// TestHalfDeliveredStreamIsNeverSpliced: once a chunk has flowed, a
// failure passes through as itself. Serving the remembered body from
// halfway would fabricate a value the caller never saw whole — silent
// corruption, the one thing a cache must never do.
func TestHalfDeliveredStreamIsNeverSpliced(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := st.RootGridID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	upstream := &halfDark{Namespace: local.New(st, nil)}
	cc := openLayer(t, upstream, filepath.Join(t.TempDir(), "cache.db"), Options{Prefetch: true})
	ctx := context.Background()

	txt, err := cc.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	writeOne(t, cc, txt.GetTile().GetId(), txt.GetTile().GetVersion(), []byte("first-frame words"))
	readContent(t, cc, txt.GetTile().GetId()) // warm

	upstream.dark = true
	var got []byte
	rerr := cc.ReadContent(ctx, &pb.ReadContentRequest{TileId: txt.GetTile().GetId()},
		func(ch *pb.ContentChunk) error { got = append(got, ch.GetData()...); return nil })
	if status.Code(rerr) != codes.Unavailable {
		t.Fatalf("half-delivered read = %v, want the honest Unavailable", rerr)
	}
	if string(got) != "half a " {
		t.Fatalf("caller saw %q; the cache must not splice its %q onto a half-live stream", got, "first-frame words")
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
	// Two deletes: the first parks the tile in the local plugin's trash
	// (#262 — it still reads), the second destroys it for real.
	if _, err := cc.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := cc.GetTile(ctx, &pb.GetTileRequest{TileId: id}); err != nil {
		t.Fatalf("trashed tile must still read: %v", err)
	}
	if _, err := cc.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: id}); err != nil {
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

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	evs := make(chan *pb.Event, 64)
	go func() {
		_ = cc.Subscribe(subCtx, &pb.SubscribeRequest{}, func(ev *pb.Event) error {
			select {
			case evs <- ev:
			case <-subCtx.Done():
			}
			return nil
		})
		close(evs)
	}()
	// PRIME the subscription: the fan-in goroutine registers with the
	// store's hub asynchronously, so a mutation fired before it lands is
	// missed forever (this test hung exactly that way once). Re-fire a
	// framing write (no version bump — the same claim stays valid) until
	// its event arrives.
	primed := false
	for i := 0; i < 50 && !primed; i++ {
		if _, err := cc.SetFraming(ctx, &pb.SetFramingRequest{TileId: well.GetTile().GetId(),
			Cx: 1, Cy: 1, Zoom: 2}); err != nil {
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

	if _, err := cc.SetFraming(ctx, &pb.SetFramingRequest{TileId: well.GetTile().GetId(),
		Cx: 9, Cy: 9, Zoom: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := cc.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: doomed.GetTile().GetId()}); err != nil {
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

	reopened := openLayer(t, upstream, dbPath, Options{Prefetch: true})
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
	if _, err := upstream.Namespace.DeleteTile(ctx, &pb.DeleteTileRequest{
		TileId: doomed.GetTile().GetId()}); err != nil {
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
