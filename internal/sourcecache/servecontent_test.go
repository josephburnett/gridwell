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
	"github.com/josephburnett/gridwell/internal/namespace"
)

// The door's bounded cache (issue #255), pinned: a 200 body re-serves
// byte-identical while dark; verdicts (non-200) are never remembered; an
// oversized body streams live but stays uncached; the mount cap evicts
// oldest-first.

// doorFake is a namespace whose only living method is ServeContent.
type doorFake struct {
	namespace.Namespace // nil: any other call panics, which IS the assertion
	bodies              map[string]doorBody
	dark                bool
}

type doorBody struct {
	status    int64
	mediaType string
	data      []byte
}

func (d *doorFake) ServeContent(_ context.Context, in *pb.ServeContentRequest, send func(*pb.ServeContentChunk) error) error {
	if d.dark {
		return status.Error(codes.Unavailable, "tunnel down")
	}
	b, ok := d.bodies[in.GetTileId()+"|"+in.GetSubpath()]
	if !ok {
		b = doorBody{status: 404, mediaType: "text/plain", data: []byte("not found")}
	}
	// The live chunk shape: chunk 1 carries status + media type.
	return sendChunked(b.data, func(chunk []byte, first bool) error {
		if first {
			return send(&pb.ServeContentChunk{Status: b.status, MediaType: b.mediaType, Data: chunk})
		}
		return send(&pb.ServeContentChunk{Data: chunk})
	})
}

func doorFixture(t *testing.T) (*Layer, *doorFake) {
	t.Helper()
	fake := &doorFake{bodies: map[string]doorBody{}}
	cc := openLayer(t, fake, filepath.Join(t.TempDir(), "cache.db"), Options{Prefetch: true})
	return cc, fake
}

// serveDoor drives one door read to completion, returning the status,
// media type and body it saw, plus the call's error.
func serveDoor(c *Layer, tileID, subpath string) (st int64, mediaType string, data []byte, err error) {
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

func TestServeContentServesStaleWhenDark(t *testing.T) {
	cc, fake := doorFixture(t)
	body := bytes.Repeat([]byte("img"), 100_000) // multi-chunk
	fake.bodies["7|photo.jpg"] = doorBody{status: 200, mediaType: "image/jpeg", data: body}

	if _, _, _, err := serveDoor(cc, "7", "photo.jpg"); err != nil { // the tee stores at the clean end
		t.Fatal(err)
	}

	fake.dark = true
	st, mt, data, err := serveDoor(cc, "7", "photo.jpg")
	if err != nil {
		t.Fatalf("dark door must serve the remembered body: %v", err)
	}
	if st != 200 || mt != "image/jpeg" || !bytes.Equal(data, body) {
		t.Errorf("stale serve = (%d, %s, %d bytes), want the exact live answer", st, mt, len(data))
	}

	// A body never seen stays a transport error — no invention.
	if _, _, _, err := serveDoor(cc, "7", "other.jpg"); status.Code(err) != codes.Unavailable {
		t.Errorf("uncached miss = %v, want the transport error through", err)
	}
}

func TestServeContentNeverCachesVerdicts(t *testing.T) {
	cc, fake := doorFixture(t)
	// 404 flows through live…
	st, _, _, err := serveDoor(cc, "9", "gone.png")
	if err != nil {
		t.Fatal(err)
	}
	if st != 404 {
		t.Fatalf("live 404 = %d", st)
	}
	// …and is NOT remembered: dark yields the transport error, never a
	// stale error page.
	fake.dark = true
	if _, _, _, err := serveDoor(cc, "9", "gone.png"); status.Code(err) != codes.Unavailable {
		t.Errorf("dark after 404 = %v, want Unavailable (verdicts are never served stale)", err)
	}
}

func TestServeContentEntryCapSkipsStore(t *testing.T) {
	cc, fake := doorFixture(t)
	old := serveContentEntryCap
	serveContentEntryCap = 1024
	t.Cleanup(func() { serveContentEntryCap = old })

	big := bytes.Repeat([]byte("x"), 4096)
	fake.bodies["5|big.bin"] = doorBody{status: 200, mediaType: "application/octet-stream", data: big}
	_, _, data, err := serveDoor(cc, "5", "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, big) {
		t.Fatal("oversized body must still stream through live, complete")
	}
	fake.dark = true
	if _, _, _, err := serveDoor(cc, "5", "big.bin"); status.Code(err) != codes.Unavailable {
		t.Errorf("oversized body cached despite the entry cap: %v", err)
	}
}

func TestServeContentMountCapEvictsOldest(t *testing.T) {
	cc, fake := doorFixture(t)
	old := serveContentMountCap
	serveContentMountCap = 2500
	t.Cleanup(func() { serveContentMountCap = old })

	for i := 0; i < 4; i++ {
		key := fmt.Sprintf("%d|f.bin", i)
		fake.bodies[key] = doorBody{status: 200, mediaType: "application/octet-stream", data: bytes.Repeat([]byte{byte(i)}, 1000)}
		if _, _, _, err := serveDoor(cc, fmt.Sprint(i), "f.bin"); err != nil {
			t.Fatal(err)
		}
	}
	fake.dark = true
	// Newest survives; the oldest was evicted to fit the cap.
	if _, _, _, err := serveDoor(cc, "3", "f.bin"); err != nil {
		t.Errorf("newest entry evicted: %v", err)
	}
	if _, _, _, err := serveDoor(cc, "0", "f.bin"); status.Code(err) != codes.Unavailable {
		t.Errorf("oldest entry survived past the mount cap: %v", err)
	}
}

// pageFake extends the door fake with just enough reads for a prefetch
// walk over one grid holding one serves_page tile.
type pageFake struct {
	doorFake
}

func (p *pageFake) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	if p.dark {
		return nil, status.Error(codes.Unavailable, "tunnel down")
	}
	return &pb.InfoResponse{Kind: "fs", RootGridId: "1"}, nil
}
func (p *pageFake) Handshake(context.Context, *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	if p.dark {
		return nil, status.Error(codes.Unavailable, "tunnel down")
	}
	return &pb.HandshakeResponse{}, nil
}
func (p *pageFake) GetGrid(_ context.Context, in *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	if p.dark {
		return nil, status.Error(codes.Unavailable, "tunnel down")
	}
	return &pb.GetGridResponse{
		Grid:  &pb.Grid{Id: in.GetGridId(), SourceKind: "fs"},
		Tiles: []*pb.Tile{{Id: "7", GridId: in.GetGridId(), Kind: "url", ServesPage: true}},
	}, nil
}
func (p *pageFake) GetTilePreview(context.Context, *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	if p.dark {
		return nil, status.Error(codes.Unavailable, "tunnel down")
	}
	return &pb.GetTilePreviewResponse{}, nil
}

func TestPrefetchWalksServesPageBodies(t *testing.T) {
	fake := &pageFake{doorFake: doorFake{bodies: map[string]doorBody{
		"7|": {status: 200, mediaType: "image/png", data: bytes.Repeat([]byte("p"), 2048)},
	}}}
	cc := openLayer(t, fake, filepath.Join(t.TempDir(), "cache.db"), Options{Prefetch: true})
	ctx := context.Background()

	cc.Prefetch(ctx)
	fake.dark = true

	st, mt, data, err := serveDoor(cc, "7", "")
	if err != nil {
		t.Fatalf("prefetch must have warmed the serves_page body: %v", err)
	}
	if st != 200 || mt != "image/png" || len(data) != 2048 {
		t.Errorf("warmed page = (%d, %s, %d bytes)", st, mt, len(data))
	}
}
