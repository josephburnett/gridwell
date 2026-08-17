package mountcache

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The door's bounded cache (issue #255), pinned: a 200 body re-serves
// byte-identical while dark; verdicts (non-200) are never remembered; an
// oversized body streams live but stays uncached; the mount cap evicts
// oldest-first.

// doorFake is a GridwellClient whose only living method is ServeContent.
type doorFake struct {
	pb.GridwellClient // nil: any other call panics, which IS the assertion
	bodies            map[string]doorBody
	dark              bool
}

type doorBody struct {
	status    int64
	mediaType string
	data      []byte
}

func (d *doorFake) ServeContent(ctx context.Context, in *pb.ServeContentRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[pb.ServeContentChunk], error) {
	if d.dark {
		return nil, status.Error(codes.Unavailable, "tunnel down")
	}
	b, ok := d.bodies[in.GetTileId()+"|"+in.GetSubpath()]
	if !ok {
		b = doorBody{status: 404, mediaType: "text/plain", data: []byte("not found")}
	}
	return newMemServeStream(ctx, b.status, b.mediaType, b.data), nil
}

func doorFixture(t *testing.T) (*Client, *doorFake) {
	t.Helper()
	fake := &doorFake{bodies: map[string]doorBody{}}
	cc, closer, err := Open(fake, filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	return cc, fake
}

func drainServe(t *testing.T, s grpc.ServerStreamingClient[pb.ServeContentChunk]) (status int64, mediaType string, data []byte) {
	t.Helper()
	for {
		ch, err := s.Recv()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("serve recv: %v", err)
		}
		if ch.GetStatus() != 0 {
			status, mediaType = ch.GetStatus(), ch.GetMediaType()
		}
		data = append(data, ch.GetData()...)
	}
}

func TestServeContentServesStaleWhenDark(t *testing.T) {
	cc, fake := doorFixture(t)
	ctx := context.Background()
	body := bytes.Repeat([]byte("img"), 100_000) // multi-chunk
	fake.bodies["7|photo.jpg"] = doorBody{status: 200, mediaType: "image/jpeg", data: body}

	s, err := cc.ServeContent(ctx, &pb.ServeContentRequest{TileId: "7", Subpath: "photo.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	drainServe(t, s) // the tee stores at EOF

	fake.dark = true
	s, err = cc.ServeContent(ctx, &pb.ServeContentRequest{TileId: "7", Subpath: "photo.jpg"})
	if err != nil {
		t.Fatalf("dark door must serve the remembered body: %v", err)
	}
	st, mt, data := drainServe(t, s)
	if st != 200 || mt != "image/jpeg" || !bytes.Equal(data, body) {
		t.Errorf("stale serve = (%d, %s, %d bytes), want the exact live answer", st, mt, len(data))
	}

	// A body never seen stays a transport error — no invention.
	if _, err := cc.ServeContent(ctx, &pb.ServeContentRequest{TileId: "7", Subpath: "other.jpg"}); status.Code(err) != codes.Unavailable {
		t.Errorf("uncached miss = %v, want the transport error through", err)
	}
}

func TestServeContentNeverCachesVerdicts(t *testing.T) {
	cc, fake := doorFixture(t)
	ctx := context.Background()
	// 404 flows through live…
	s, err := cc.ServeContent(ctx, &pb.ServeContentRequest{TileId: "9", Subpath: "gone.png"})
	if err != nil {
		t.Fatal(err)
	}
	if st, _, _ := drainServe(t, s); st != 404 {
		t.Fatalf("live 404 = %d", st)
	}
	// …and is NOT remembered: dark yields the transport error, never a
	// stale error page.
	fake.dark = true
	if _, err := cc.ServeContent(ctx, &pb.ServeContentRequest{TileId: "9", Subpath: "gone.png"}); status.Code(err) != codes.Unavailable {
		t.Errorf("dark after 404 = %v, want Unavailable (verdicts are never served stale)", err)
	}
}

func TestServeContentEntryCapSkipsStore(t *testing.T) {
	cc, fake := doorFixture(t)
	ctx := context.Background()
	old := serveContentEntryCap
	serveContentEntryCap = 1024
	t.Cleanup(func() { serveContentEntryCap = old })

	big := bytes.Repeat([]byte("x"), 4096)
	fake.bodies["5|big.bin"] = doorBody{status: 200, mediaType: "application/octet-stream", data: big}
	s, err := cc.ServeContent(ctx, &pb.ServeContentRequest{TileId: "5", Subpath: "big.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, data := drainServe(t, s); !bytes.Equal(data, big) {
		t.Fatal("oversized body must still stream through live, complete")
	}
	fake.dark = true
	if _, err := cc.ServeContent(ctx, &pb.ServeContentRequest{TileId: "5", Subpath: "big.bin"}); status.Code(err) != codes.Unavailable {
		t.Errorf("oversized body cached despite the entry cap: %v", err)
	}
}

func TestServeContentMountCapEvictsOldest(t *testing.T) {
	cc, fake := doorFixture(t)
	ctx := context.Background()
	old := serveContentMountCap
	serveContentMountCap = 2500
	t.Cleanup(func() { serveContentMountCap = old })

	for i := 0; i < 4; i++ {
		key := fmt.Sprintf("%d|f.bin", i)
		fake.bodies[key] = doorBody{status: 200, mediaType: "application/octet-stream", data: bytes.Repeat([]byte{byte(i)}, 1000)}
		s, err := cc.ServeContent(ctx, &pb.ServeContentRequest{TileId: fmt.Sprint(i), Subpath: "f.bin"})
		if err != nil {
			t.Fatal(err)
		}
		drainServe(t, s)
	}
	fake.dark = true
	// Newest survives; the oldest was evicted to fit the cap.
	if _, err := cc.ServeContent(ctx, &pb.ServeContentRequest{TileId: "3", Subpath: "f.bin"}); err != nil {
		t.Errorf("newest entry evicted: %v", err)
	}
	if _, err := cc.ServeContent(ctx, &pb.ServeContentRequest{TileId: "0", Subpath: "f.bin"}); status.Code(err) != codes.Unavailable {
		t.Errorf("oldest entry survived past the mount cap: %v", err)
	}
}

// pageFake extends the door fake with just enough reads for a prefetch
// walk over one grid holding one serves_page tile.
type pageFake struct {
	doorFake
}

func (p *pageFake) Info(ctx context.Context, _ *pb.InfoRequest, _ ...grpc.CallOption) (*pb.InfoResponse, error) {
	if p.dark {
		return nil, status.Error(codes.Unavailable, "tunnel down")
	}
	return &pb.InfoResponse{Kind: "fs", RootGridId: "1"}, nil
}
func (p *pageFake) ListPlugins(ctx context.Context, _ *pb.ListPluginsRequest, _ ...grpc.CallOption) (*pb.ListPluginsResponse, error) {
	if p.dark {
		return nil, status.Error(codes.Unavailable, "tunnel down")
	}
	return &pb.ListPluginsResponse{}, nil
}
func (p *pageFake) GetGrid(ctx context.Context, in *pb.GetGridRequest, _ ...grpc.CallOption) (*pb.GetGridResponse, error) {
	if p.dark {
		return nil, status.Error(codes.Unavailable, "tunnel down")
	}
	return &pb.GetGridResponse{
		Grid:  &pb.Grid{Id: in.GetGridId(), SourceKind: "fs"},
		Tiles: []*pb.Tile{{Id: "7", GridId: in.GetGridId(), Kind: "url", ServesPage: true}},
	}, nil
}
func (p *pageFake) GetTilePreview(ctx context.Context, _ *pb.GetTilePreviewRequest, _ ...grpc.CallOption) (*pb.GetTilePreviewResponse, error) {
	if p.dark {
		return nil, status.Error(codes.Unavailable, "tunnel down")
	}
	return &pb.GetTilePreviewResponse{}, nil
}

func TestPrefetchWalksServesPageBodies(t *testing.T) {
	fake := &pageFake{doorFake: doorFake{bodies: map[string]doorBody{
		"7|": {status: 200, mediaType: "image/png", data: bytes.Repeat([]byte("p"), 2048)},
	}}}
	cc, closer, err := Open(fake, filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	ctx := context.Background()

	cc.Prefetch(ctx)
	fake.dark = true

	s, err := cc.ServeContent(ctx, &pb.ServeContentRequest{TileId: "7", Subpath: ""})
	if err != nil {
		t.Fatalf("prefetch must have warmed the serves_page body: %v", err)
	}
	st, mt, data := drainServe(t, s)
	if st != 200 || mt != "image/png" || len(data) != 2048 {
		t.Errorf("warmed page = (%d, %s, %d bytes)", st, mt, len(data))
	}
}
