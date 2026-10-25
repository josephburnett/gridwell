package sourcecache

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// The content API's bounded cache, pinned: a 200 body re-serves byte-identical
// while dark, a non-200 verdict is never cached, an oversized body streams
// live but stays uncached, and the cap evicts oldest-first.

// contentFake is a namespace whose only living method is ServeContent.
type contentFake struct {
	namespace.Namespace // nil: any other call panics, which is the assertion
	bodies              map[string]contentBody
	dark                bool
}

type contentBody struct {
	status    int64
	mediaType string
	data      []byte
	headers   []*pb.Header
}

func (d *contentFake) ServeContent(_ context.Context, in *pb.ServeContentRequest, send func(*pb.ServeContentChunk) error) error {
	if d.dark {
		return gwerr.Unheard("tunnel down")
	}
	b, ok := d.bodies[in.GetTileId()+"|"+in.GetSubpath()]
	if !ok {
		b = contentBody{status: 404, mediaType: "text/plain", data: []byte("not found")}
	}
	// The live chunk shape: chunk 1 carries status + media type.
	return sendChunked(b.data, func(chunk []byte, first bool) error {
		if first {
			return send(&pb.ServeContentChunk{Status: b.status, MediaType: b.mediaType, Data: chunk, Headers: b.headers})
		}
		return send(&pb.ServeContentChunk{Data: chunk})
	})
}

func contentFixture(t *testing.T) (*Layer, *contentFake) {
	t.Helper()
	fake := &contentFake{bodies: map[string]contentBody{}}
	cc := openLayer(t, fake, filepath.Join(t.TempDir(), "cache.db"), Options{Prefetch: true})
	return cc, fake
}

// serveContentAPI drives one content API read to completion, returning the
// status, media type and body it saw, plus the call's error.
func serveContentAPI(c *Layer, tileID, subpath string) (st int64, mediaType string, data []byte, err error) {
	err = c.ServeContent(context.Background(), &pb.ServeContentRequest{TileId: tileID, Subpath: subpath},
		func(ch *pb.ServeContentChunk) error {
			if ch.GetStatus() != 0 {
				st, mediaType = ch.GetStatus(), ch.GetMediaType()
			}
			data = append(data, ch.GetData()...)
			return nil
		})
	return st, mediaType, data, err
}

func TestServeContentServesTheCacheWhenDark(t *testing.T) {
	cc, fake := contentFixture(t)
	body := bytes.Repeat([]byte("img"), 100_000) // multi-chunk
	fake.bodies["7|photo.jpg"] = contentBody{status: 200, mediaType: "image/jpeg", data: body}

	if _, _, _, err := serveContentAPI(cc, "7", "photo.jpg"); err != nil { // the tee stores at the clean end
		t.Fatal(err)
	}

	fake.dark = true
	st, mt, data, err := serveContentAPI(cc, "7", "photo.jpg")
	if err != nil {
		t.Fatalf("a dark source must serve the cached body: %v", err)
	}
	if st != 200 || mt != "image/jpeg" || !bytes.Equal(data, body) {
		t.Errorf("cached serve = (%d, %s, %d bytes), want the exact live answer", st, mt, len(data))
	}

	// A body never seen stays a transport error; nothing is invented.
	if _, _, _, err := serveContentAPI(cc, "7", "other.jpg"); status.Code(err) != codes.Unavailable {
		t.Errorf("uncached miss = %v, want the transport error through", err)
	}
}

func TestServeContentNeverCachesVerdicts(t *testing.T) {
	cc, fake := contentFixture(t)
	// 404 flows through live…
	st, _, _, err := serveContentAPI(cc, "9", "gone.png")
	if err != nil {
		t.Fatal(err)
	}
	if st != 404 {
		t.Fatalf("live 404 = %d", st)
	}
	// …and is not cached: dark yields the transport error, never a cached
	// error page.
	fake.dark = true
	if _, _, _, err := serveContentAPI(cc, "9", "gone.png"); status.Code(err) != codes.Unavailable {
		t.Errorf("dark after 404 = %v, want Unavailable (verdicts are never served from cache)", err)
	}
}

func TestServeContentEntryCapSkipsStore(t *testing.T) {
	cc, fake := contentFixture(t)
	old := serveContentEntryCap
	serveContentEntryCap = 1024
	t.Cleanup(func() { serveContentEntryCap = old })

	big := bytes.Repeat([]byte("x"), 4096)
	fake.bodies["5|big.bin"] = contentBody{status: 200, mediaType: "application/octet-stream", data: big}
	_, _, data, err := serveContentAPI(cc, "5", "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, big) {
		t.Fatal("oversized body must still stream through live, complete")
	}
	fake.dark = true
	if _, _, _, err := serveContentAPI(cc, "5", "big.bin"); status.Code(err) != codes.Unavailable {
		t.Errorf("oversized body cached despite the entry cap: %v", err)
	}
}

func TestServeContentSourceCapEvictsOldest(t *testing.T) {
	cc, fake := contentFixture(t)
	old := serveContentSourceCap
	serveContentSourceCap = 2500
	t.Cleanup(func() { serveContentSourceCap = old })

	for i := 0; i < 4; i++ {
		key := fmt.Sprintf("%d|f.bin", i)
		fake.bodies[key] = contentBody{status: 200, mediaType: "application/octet-stream", data: bytes.Repeat([]byte{byte(i)}, 1000)}
		if _, _, _, err := serveContentAPI(cc, fmt.Sprint(i), "f.bin"); err != nil {
			t.Fatal(err)
		}
	}
	fake.dark = true
	// Newest survives; the oldest was evicted to fit the cap.
	if _, _, _, err := serveContentAPI(cc, "3", "f.bin"); err != nil {
		t.Errorf("newest entry evicted: %v", err)
	}
	if _, _, _, err := serveContentAPI(cc, "0", "f.bin"); status.Code(err) != codes.Unavailable {
		t.Errorf("oldest entry survived past the source cap: %v", err)
	}
}

// pageFake extends the content fake with just enough reads for a prefetch
// walk over one grid holding one serves_page tile.
type pageFake struct {
	contentFake
}

// Handshake is the walk's doorstep: the connection it declares is where the
// walk begins.
func (p *pageFake) Handshake(context.Context, *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	if p.dark {
		return nil, gwerr.Unheard("tunnel down")
	}
	return &pb.HandshakeResponse{Plugins: []*pb.PluginInfo{rpc.ConnectionRow("u1", "", "1", "", rpc.View{})}}, nil
}
func (p *pageFake) GetGrid(_ context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	if p.dark {
		return nil, gwerr.Unheard("tunnel down")
	}
	return &pb.GetGridResponse{
		Grid:  &pb.Grid{Id: in.GetGridId(), HostContent: true},
		Tiles: []*pb.Tile{{Id: "7", GridId: in.GetGridId(), Kind: rpc.KindURL, ServesPage: true}},
	}, nil
}
func (p *pageFake) GetTilePreview(context.Context, *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	if p.dark {
		return nil, gwerr.Unheard("tunnel down")
	}
	return &pb.GetTilePreviewResponse{}, nil
}

func TestPrefetchWalksServesPageBodies(t *testing.T) {
	fake := &pageFake{contentFake: contentFake{bodies: map[string]contentBody{
		"7|": {status: 200, mediaType: "image/png", data: bytes.Repeat([]byte("p"), 2048)},
	}}}
	cc := openLayer(t, fake, filepath.Join(t.TempDir(), "cache.db"), Options{Prefetch: true})
	ctx := context.Background()

	cc.prefetch(ctx, "")
	fake.dark = true

	st, mt, data, err := serveContentAPI(cc, "7", "")
	if err != nil {
		t.Fatalf("prefetch must have warmed the serves_page body: %v", err)
	}
	if st != 200 || mt != "image/png" || len(data) != 2048 {
		t.Errorf("warmed page = (%d, %s, %d bytes)", st, mt, len(data))
	}
}

// TestServeContentCachesAnEmptyBody: a 200 with no bytes — an empty file, a
// 200 with only headers — is an answer like any other. The same nil-binds-as-
// NULL fault that hit tile content hit the content API's cache too: the write
// failed, nothing was cached, and the cache announced itself broken.
func TestServeContentCachesAnEmptyBody(t *testing.T) {
	cc, fake := contentFixture(t)
	fake.bodies["7|empty.txt"] = contentBody{status: 200, mediaType: "text/plain", data: nil}

	if _, _, _, err := serveContentAPI(cc, "7", "empty.txt"); err != nil {
		t.Fatal(err)
	}
	cc.healthMu.Lock()
	down := cc.cacheDown
	cc.healthMu.Unlock()
	if down {
		t.Fatal("caching an empty served body broke the cache: the notice row now says it cannot store answers")
	}

	fake.dark = true
	st, mt, data, err := serveContentAPI(cc, "7", "empty.txt")
	if err != nil {
		t.Fatalf("dark serve of a cached empty body: %v", err)
	}
	if st != 200 || mt != "text/plain" || len(data) != 0 {
		t.Fatalf("dark serve = %d %q %q, want 200 text/plain and no bytes", st, mt, data)
	}
}

// Only a plain GET is the page at its subpath: a POST, a query or an answer
// carrying headers is not what a later read of that subpath would get, so
// none is stored, and a dark source answers none of them from the cache.
func TestServeContentCachesOnlyAPlainGet(t *testing.T) {
	cc, fake := contentFixture(t)
	fake.bodies["7|page"] = contentBody{status: 200, mediaType: "text/html", data: []byte("page")}
	fake.bodies["7|hdr"] = contentBody{status: 200, mediaType: "text/html", data: []byte("hdr"),
		headers: []*pb.Header{{Name: "Location", Value: "elsewhere"}}}
	reads := map[string]*pb.ServeContentRequest{
		"post":    {TileId: "7", Subpath: "page", Method: "POST", Body: []byte("x")},
		"query":   {TileId: "7", Subpath: "page", Query: "a=1"},
		"headers": {TileId: "7", Subpath: "hdr"},
	}
	for name, in := range reads {
		if err := cc.ServeContent(context.Background(), in, func(*pb.ServeContentChunk) error { return nil }); err != nil {
			t.Fatalf("%s live: %v", name, err)
		}
	}
	if _, _, _, err := serveContentAPI(cc, "7", "page"); err != nil { // the plain GET stores
		t.Fatal(err)
	}
	fake.dark = true
	for name, in := range reads {
		if err := cc.ServeContent(context.Background(), in, func(*pb.ServeContentChunk) error { return nil }); status.Code(err) != codes.Unavailable {
			t.Errorf("%s while dark = %v, want the transport error, never a cached answer", name, err)
		}
	}
}
