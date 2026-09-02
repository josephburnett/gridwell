package sourcecache

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
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

// The cache contract: every successful read is remembered; a transport-dark
// source serves the remembered answer; an answered error — NotFound, anything
// coded — is never masked by a cached row; the cache survives a restart; and a
// successful re-read reconciles what changed while dark. The fixture is a real
// home store behind a switchable dark proxy, the shape of a connection whose
// tunnel dropped.

// darkable wraps the upstream client: a machine that can go away. Dark, every
// read fails the way a dead tunnel does AND the event stream it was serving
// ends, which is both halves of a machine going away and the second is how a
// connection's own supervisor learns of the first. Writes are not
// intercepted: write behavior under darkness is the pass-through error path,
// not cache behavior.
type darkable struct {
	namespace.Namespace
	mu   sync.Mutex
	down bool
	// fell is closed when the machine goes away, dropping the parked event
	// stream; goLive replaces it, since a machine can come back.
	fell chan struct{}
}

func newDarkable(ns namespace.Namespace) *darkable {
	return &darkable{Namespace: ns, fell: make(chan struct{})}
}

// goDark and goLive are the machine leaving and returning.
func (d *darkable) goDark() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.down {
		return
	}
	d.down = true
	close(d.fell)
}

func (d *darkable) goLive() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.down {
		return
	}
	d.down = false
	d.fell = make(chan struct{})
}

func (d *darkable) dark() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.down
}

func (d *darkable) fallen() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fell
}

func (d *darkable) offline() error { return status.Error(codes.Unavailable, "tunnel down") }

func (d *darkable) Info(ctx context.Context, in *pb.InfoRequest) (*pb.InfoResponse, error) {
	if d.dark() {
		return nil, d.offline()
	}
	return d.Namespace.Info(ctx, in)
}
func (d *darkable) GetGrid(ctx context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	if d.dark() {
		return nil, d.offline()
	}
	return d.Namespace.GetGrid(ctx, in)
}
func (d *darkable) GetTile(ctx context.Context, in *pb.GetTileRequest) (*pb.TileResponse, error) {
	if d.dark() {
		return nil, d.offline()
	}
	return d.Namespace.GetTile(ctx, in)
}
func (d *darkable) GetTilePreview(ctx context.Context, in *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	if d.dark() {
		return nil, d.offline()
	}
	return d.Namespace.GetTilePreview(ctx, in)
}
func (d *darkable) ReadContent(ctx context.Context, in *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	if d.dark() {
		return d.offline()
	}
	return d.Namespace.ReadContent(ctx, in, send)
}

// Subscribe parks like a live stream and ends when the machine goes away: an
// event stream that ended is what the connection's fan-in reads as darkness.
func (d *darkable) Subscribe(ctx context.Context, in *pb.SubscribeRequest, send func(*pb.Event) error) error {
	if d.dark() {
		return d.offline()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-d.fallen():
			cancel()
		case <-ctx.Done():
		}
	}()
	err := d.Namespace.Subscribe(ctx, in, send)
	if d.dark() {
		return d.offline()
	}
	return err
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
	upstream = newDarkable(local.New(st, nil))
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
	upstream.goDark()

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
	// Two deletes: the first parks the tile in home's trash, where it still
	// reads, and the second destroys it for real.
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

	upstream.goDark()
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
	upstream.goDark()
	if _, err := reopened.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatalf("reopened cache should serve the remembered grid: %v", err)
	}
}

// TestRefreshReconcilesWhatChangedWhileBlind: mutations the cache never
// saw (no subscription running) reconcile on the revalidation an
// out-of-window read kicks — the response IS the complete tile set, so a
// delete-while-blind vanishes from the cache too.
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
	// Deleted UPSTREAM directly — the cache never sees an event, and the
	// write never passes through this layer.
	if _, err := upstream.Namespace.DeleteTile(ctx, &pb.DeleteTileRequest{
		TileId: doomed.GetTile().GetId()}); err != nil {
		t.Fatal(err)
	}
	// An out-of-window read serves the remembered answer — doomed included:
	// that is the remembering — and kicks the revalidation that replaces the
	// grid's whole tile set.
	ageGrid(t, cc, root)
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	awaitFresh(t, cc, root)
	upstream.goDark()
	g, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range g.GetTiles() {
		if tl.GetId() == doomed.GetTile().GetId() {
			t.Fatal("a delete-while-blind must reconcile out on the revalidation")
		}
	}
}

// ── serve-first ─────────────────────────────────────────────────────────

// ageGrid pushes a remembered grid past the freshness window, the test's
// stand-in for waiting the window out: the next read serves it stamped stale
// and kicks a revalidation.
func ageGrid(t *testing.T, cc *Layer, gridID string) {
	t.Helper()
	if _, err := cc.db.Exec(`UPDATE grids SET fetched_at = fetched_at - ? WHERE id = ?`,
		int64(freshWindow/time.Second)+1, gridID); err != nil {
		t.Fatal(err)
	}
}

// awaitFresh polls until a revalidation has re-stored the grid — its
// fetched_at is back inside the window — and returns the remembered answer.
func awaitFresh(t *testing.T, cc *Layer, gridID string) *pb.GetGridResponse {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, fetchedAt, ok := cc.loadGrid(context.Background(), gridID)
		if ok && time.Since(time.Unix(fetchedAt, 0)) < freshWindow {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatal("revalidation never landed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// midwalk blocks GetGrid until released — a source mid-walk. Calls before the
// gate closes pass through.
type midwalk struct {
	namespace.Namespace
	gate chan struct{} // nil: pass through; non-nil: block until closed
}

func (g *midwalk) GetGrid(ctx context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	if g.gate != nil {
		select {
		case <-g.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.Namespace.GetGrid(ctx, in)
}

// TestServeFirstNeverWaitsOnTheSource is the feature: a remembered grid
// answers immediately while the source is mid-walk, stamped stale past its
// window, and the walk lands behind as the revalidation.
func TestServeFirstNeverWaitsOnTheSource(t *testing.T) {
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
	up := &midwalk{Namespace: local.New(st, nil)}
	cc := openLayer(t, up, filepath.Join(t.TempDir(), "cache.db"), Options{})

	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err) // warm while the gate is open
	}
	// The source changes, then goes into a slow walk.
	txt, err := up.Namespace.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	up.gate = gate
	ageGrid(t, cc, root)

	done := make(chan *pb.GetGridResponse, 1)
	go func() {
		g, gerr := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
		if gerr != nil {
			t.Error(gerr)
			done <- nil
			return
		}
		done <- g
	}()
	select {
	case g := <-done:
		if g == nil {
			t.FailNow()
		}
		if !g.GetGrid().GetStale() {
			t.Error("the served remembering must wear the stale bit")
		}
		if len(g.GetTiles()) != 0 {
			t.Errorf("served %d tiles, want the remembered 0 (the walk has not landed)", len(g.GetTiles()))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read waited on the source: serve-first must answer from the remembering")
	}

	close(gate) // the walk lands
	fresh := awaitFresh(t, cc, root)
	if len(fresh.GetTiles()) != 1 || fresh.GetTiles()[0].GetId() != txt.GetTile().GetId() {
		t.Fatalf("revalidation remembered %v, want the new tile", fresh.GetTiles())
	}
}

// streaming is the transport's shape: a namespace that HAS an event stream of
// its own, which every connection does. The layer's own stream must reach the
// subscriber ALONGSIDE it — a revalidation's GridChanged and the cache's
// health are facts only the layer knows, and an upstream with a stream must
// not bury them.
type streaming struct {
	namespace.Namespace
}

func (streaming) Subscribe(ctx context.Context, _ *pb.SubscribeRequest, _ func(*pb.Event) error) error {
	<-ctx.Done()
	return nil
}

func (streaming) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	return &pb.InfoResponse{Kind: "test", Watch: true}, nil
}

// TestRevalidationEmitsGridChanged closes the serve-first loop for a
// namespace the node never watched: stale served, refresh lands a different
// answer, GridChanged fires on the layer's own stream, and the next read
// serves the correction. Info declares the door.
func TestRevalidationEmitsGridChanged(t *testing.T) {
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
	up := &streaming{Namespace: local.New(st, nil)}
	cc := openLayer(t, up, filepath.Join(t.TempDir(), "cache.db"), Options{})

	info, err := cc.Info(ctx, &pb.InfoRequest{})
	if err != nil || !info.GetWatch() {
		t.Fatalf("layer Info = (%+v, %v), want watch: the layer has a stream to offer", info, err)
	}

	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	evs := make(chan *pb.Event, 16)
	go func() {
		_ = cc.Subscribe(subCtx, &pb.SubscribeRequest{}, func(ev *pb.Event) error {
			evs <- ev
			return nil
		})
	}()
	// The subscriber registers asynchronously; wait for it, or the emit
	// races past an empty subscriber set.
	deadline := time.Now().Add(5 * time.Second)
	for {
		cc.subsMu.Lock()
		n := len(cc.subs)
		cc.subsMu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the synthetic subscription never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The source changes while nobody is looking; an out-of-window read
	// serves the remembering and kicks the refresh that finds the change.
	txt, err := up.Namespace.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	ageGrid(t, cc, root)
	stale, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale.GetTiles()) != 0 {
		t.Fatalf("the stale answer already has %d tiles; it must be the remembering", len(stale.GetTiles()))
	}
	select {
	case ev := <-evs:
		gc := ev.GetGridChanged()
		if gc.GetGridId() != root {
			t.Fatalf("GridChanged names %q, want %q", gc.GetGridId(), root)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the revalidation's GridChanged never arrived")
	}
	again, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if again.GetGrid().GetStale() || len(again.GetTiles()) != 1 || again.GetTiles()[0].GetId() != txt.GetTile().GetId() {
		t.Fatalf("post-event read = stale:%v tiles:%v, want the fresh correction", again.GetGrid().GetStale(), again.GetTiles())
	}
}

// verdicted answers one grid with NotFound once armed — the source's word
// that the grid is gone.
type verdicted struct {
	namespace.Namespace
	gone bool
}

func (v *verdicted) GetGrid(ctx context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	if v.gone {
		return nil, status.Error(codes.NotFound, "grid gone")
	}
	return v.Namespace.GetGrid(ctx, in)
}

// TestRevalidationVerdictEvicts: an answered error is an answer. The one read
// that catches the transition still serves the remembering, but the
// revalidation evicts it, and from then on the verdict surfaces instead of a
// ghost.
func TestRevalidationVerdictEvicts(t *testing.T) {
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
	up := &verdicted{Namespace: local.New(st, nil)}
	cc := openLayer(t, up, filepath.Join(t.TempDir(), "cache.db"), Options{})

	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	up.gone = true
	ageGrid(t, cc, root)
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err) // the catching read still serves the remembering
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, _, ok := cc.loadGrid(ctx, root); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the verdict never evicted the remembered grid")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); status.Code(err) != codes.NotFound {
		t.Fatalf("post-eviction read = %v, want the NotFound verdict passed through", err)
	}
}

// TestWriteResponsesUpdateTheRememberedRows: under serve-first "the next read
// refreshes" no longer holds, so a write's response must fold into the
// remembered rows — a moved tile reads back where the user put it, and a
// deleted one is gone, straight from the remembering.
func TestWriteResponsesUpdateTheRememberedRows(t *testing.T) {
	cc, upstream, root, _ := fixture(t)
	ctx := context.Background()

	moved, err := cc.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}})
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
	if _, err := cc.PlaceTile(ctx, &pb.PlaceTileRequest{GridId: root,
		TileId: moved.GetTile().GetId(), X: 5, Y: 6, W: 2, H: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := cc.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: doomed.GetTile().GetId()}); err != nil {
		t.Fatal(err)
	}
	// Dark, within the window: the remembering is all there is, and it must
	// already say what the user did.
	upstream.goDark()
	g, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	var gotMoved *pb.Tile
	for _, tl := range g.GetTiles() {
		if tl.GetId() == doomed.GetTile().GetId() {
			t.Fatal("the deleted tile must be gone from the remembering")
		}
		if tl.GetId() == moved.GetTile().GetId() {
			gotMoved = tl
		}
	}
	if gotMoved.GetX() != 5 || gotMoved.GetY() != 6 || gotMoved.GetW() != 2 {
		t.Fatalf("moved tile remembered at (%d,%d,%d), want (5,6,2)",
			gotMoved.GetX(), gotMoved.GetY(), gotMoved.GetW())
	}
}

// TestStoreGridSurvivesAGridKeyRename: after an id migration the same grid
// can be asked for under two keys — the old minted id and the new derived
// address — and its tiles keep their ids. Storing the answer under the new
// key must move the tiles, not collide with their rows under the old key:
// the plain INSERT rolled back on tiles.id UNIQUE, the store never
// succeeded, and every refresh retried and logged forever (seen live after
// the lazy-ids upgrade).
func TestStoreGridSurvivesAGridKeyRename(t *testing.T) {
	cc, upstream, root, _ := fixture(t)
	ctx := context.Background()

	if _, err := cc.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}}); err != nil {
		t.Fatal(err)
	}
	live, err := upstream.Namespace.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	// The same answer remembered under two keys, the migration shape.
	cc.storeGrid(ctx, "old-key", live)
	cc.storeGrid(ctx, "new-key", live)
	got, _, ok := cc.loadGrid(ctx, "new-key")
	if !ok || len(got.GetTiles()) != len(live.GetTiles()) {
		t.Fatalf("new-key remembered %v (ok=%v), want the full tile set: the rename must upsert, not collide", got.GetTiles(), ok)
	}
}

// TestCacheStoreFailureSurfacesAsHealth: a cache that cannot remember is not
// a shrug in a server log — under serve-first it means every read pays the
// source's full latency, and the user deserves the error. The transition
// rides the layer's own event stream as this namespace's health: down on the
// first failing store, up on the first success after. (The tiles.id UNIQUE
// rollback ran for hours as a log-only "degraded" before this.)
func TestCacheStoreFailureSurfacesAsHealth(t *testing.T) {
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
	up := &streaming{Namespace: local.New(st, nil)}
	cc := openLayer(t, up, filepath.Join(t.TempDir(), "cache.db"), Options{})

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	evs := make(chan *pb.Event, 64)
	go func() {
		_ = cc.Subscribe(subCtx, &pb.SubscribeRequest{}, func(ev *pb.Event) error {
			evs <- ev
			return nil
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		cc.subsMu.Lock()
		n := len(cc.subs)
		cc.subsMu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the synthetic subscription never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The cache breaks: the next live read's store fails and the health goes
	// down, with the failure in the detail.
	if _, err := cc.db.Exec(`DROP TABLE tiles`); err != nil {
		t.Fatal(err)
	}
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatalf("a broken cache must never fail the live read: %v", err)
	}
	waitHealth := func(wantHealthy bool, what string) *pb.EventPluginHealth {
		for {
			select {
			case ev := <-evs:
				if h := ev.GetPluginHealth(); h != nil && h.GetHealthy() == wantHealthy {
					return h
				}
			case <-time.After(5 * time.Second):
				t.Fatal(what)
			}
		}
	}
	down := waitHealth(false, "the failing store never surfaced as health")
	if down.GetPluginUuid() != "" {
		t.Errorf("uuid = %q, want empty (the fan-in fills the namespace)", down.GetPluginUuid())
	}
	if !strings.Contains(down.GetDetail(), "cannot remember") {
		t.Errorf("detail = %q, want the cache failure named", down.GetDetail())
	}

	// The cache heals: the next successful store clears it.
	if _, err := cc.db.Exec(`CREATE TABLE tiles (id TEXT PRIMARY KEY, grid_id TEXT NOT NULL, proto BLOB NOT NULL, fetched_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ageGrid(t, cc, root) // past the window: the next read revalidates and stores
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	waitHealth(true, "the healed store never cleared the health")
}

func TestStaleBitMarksAnswersPastTheirWindow(t *testing.T) {
	cc, upstream, root, _ := fixture(t)
	ctx := context.Background()
	live, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if live.GetGrid().GetStale() {
		t.Error("a live answer must never wear the stale bit")
	}
	// Within the window a remembered answer serves as good as live: the
	// machine is gone, but nothing has learned that yet, and an unlearned
	// absence must not stamp fresh answers (dark_test.go owns the learning).
	upstream.goDark()
	fresh, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.GetGrid().GetStale() {
		t.Error("a within-window answer must not wear the stale bit")
	}
	// Past the window it says so on the wire (#256, serve-first edition) —
	// and stays said while the source is dark, since the revalidation the
	// read kicks fails transport-shaped and changes nothing.
	ageGrid(t, cc, root)
	stale, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if !stale.GetGrid().GetStale() {
		t.Error("a remembered answer past its window must say so on the wire (#256)")
	}
	// Back alive: the next read still serves the remembered answer, and the
	// revalidation it kicks lands and clears the bit, which is never stored.
	upstream.goLive()
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	awaitFresh(t, cc, root)
	again, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	if again.GetGrid().GetStale() {
		t.Error("the stale bit leaked into the stored row")
	}
}

// degrading is an upstream that answers, but with a degraded grid: the shape a
// plugin adapter takes when its source goes dark, holding the rows it minted,
// no source facts, stamped stale. The cache must not remember it, because the
// degraded answer succeeds and nothing else would ever put the good one
// back.
type degrading struct {
	namespace.Namespace
	degraded bool
}

func (d *degrading) GetGrid(ctx context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	resp, err := d.Namespace.GetGrid(ctx, in)
	if err != nil || !d.degraded {
		return resp, err
	}
	resp.Grid.Stale = true
	resp.Tiles = nil // whatever the source said is missing
	return resp, nil
}

func TestAStaleAnswerIsNeverRemembered(t *testing.T) {
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
	raw := local.New(st, nil)
	if _, err := raw.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}}); err != nil {
		t.Fatal(err)
	}
	up := &degrading{Namespace: raw}
	cc := openLayer(t, up, filepath.Join(t.TempDir(), "cache.db"), Options{})
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}

	up.degraded = true
	// Age the row so the read revalidates against the degraded upstream; the
	// revalidation must not store the degraded answer. There is no landing to
	// await — a stale answer changes nothing — so give it every chance the
	// no-prefetch test above gives a walk, then look.
	ageGrid(t, cc, root)
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	cached, _, ok := cc.loadGrid(ctx, root)
	if !ok {
		t.Fatal("the good answer was dropped")
	}
	if len(cached.GetTiles()) != 1 {
		t.Fatalf("a stale answer overwrote the good one: %d tiles remembered, want 1", len(cached.GetTiles()))
	}
	if cached.GetGrid().GetStale() {
		t.Fatal("the stale bit was stored")
	}
}
