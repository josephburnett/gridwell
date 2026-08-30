package namespace_test

// The codec seam: a Namespace written onto gridwell.v1 by Server, read
// back off it by FromClient, is the SAME Namespace. This is the one test
// whose purpose is the gRPC codec itself — everything else in the node
// calls the Go value directly (docs/simplify-plan.md S2) — so it runs over
// a real gRPC loopback, and the loopback lives here, in the test that
// needs it, not in production code.

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// loopback serves ns over a real in-memory gRPC hop and reads it back as a
// Namespace: Namespace → Server → gridwell.v1 → FromClient → Namespace.
func loopback(t *testing.T, ns namespace.Namespace) namespace.Namespace {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterGridwellServer(srv, namespace.Server(ns))
	go srv.Serve(lis)
	cc, err := grpc.NewClient("passthrough:///codec-seam",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		srv.Stop()
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { cc.Close(); srv.Stop() })
	return namespace.FromClient(pb.NewGridwellClient(cc))
}

// fake is a Namespace that answers from fields, so a test can pin exactly
// what crosses the codec.
type fake struct {
	tile    *pb.Tile
	err     error
	chunks  []*pb.ContentChunk
	written []*pb.WriteContentRequest
	events  []*pb.Event
	shellIn []*pb.OpenShellRequest
}

func (f *fake) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	return &pb.InfoResponse{Kind: "fake", Watch: true}, nil
}
func (f *fake) Probe(context.Context, *pb.ProbeRequest) (*pb.ProbeResponse, error) {
	return &pb.ProbeResponse{}, nil
}
func (f *fake) Handshake(context.Context, *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	return &pb.HandshakeResponse{}, nil
}
func (f *fake) GetGrid(context.Context, *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	return &pb.GetGridResponse{Tiles: []*pb.Tile{f.tile}}, nil
}
func (f *fake) GetTile(context.Context, *pb.GetTileRequest) (*pb.TileResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &pb.TileResponse{Tile: f.tile}, nil
}
func (f *fake) GetTilePreview(context.Context, *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	return &pb.GetTilePreviewResponse{}, nil
}
func (f *fake) Search(context.Context, *pb.SearchRequest) (*pb.SearchResponse, error) {
	return &pb.SearchResponse{}, nil
}
func (f *fake) CreateTile(context.Context, *pb.CreateTileRequest) (*pb.TileResponse, error) {
	return &pb.TileResponse{Tile: f.tile}, nil
}
func (f *fake) SetTile(context.Context, *pb.SetTileRequest) (*pb.TileResponse, error) {
	return &pb.TileResponse{Tile: f.tile}, nil
}
func (f *fake) PlaceTile(context.Context, *pb.PlaceTileRequest) (*pb.TileResponse, error) {
	return &pb.TileResponse{Tile: f.tile}, nil
}
func (f *fake) CloneTile(context.Context, *pb.CloneTileRequest) (*pb.TileResponse, error) {
	return &pb.TileResponse{Tile: f.tile}, nil
}
func (f *fake) DeleteTile(context.Context, *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	return &pb.DeleteTileResponse{}, nil
}
func (f *fake) SetFraming(context.Context, *pb.SetFramingRequest) (*pb.SetFramingResponse, error) {
	return &pb.SetFramingResponse{Tile: f.tile}, nil
}
func (f *fake) ShellSessionAlive(context.Context, *pb.ShellSessionAliveRequest) (*pb.ShellSessionAliveResponse, error) {
	return &pb.ShellSessionAliveResponse{Alive: true}, nil
}
func (f *fake) ReadContent(_ context.Context, _ *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	for _, c := range f.chunks {
		if err := send(c); err != nil {
			return err
		}
	}
	return f.err
}
func (f *fake) ServeContent(_ context.Context, _ *pb.ServeContentRequest, send func(*pb.ServeContentChunk) error) error {
	return send(&pb.ServeContentChunk{Status: 200, MediaType: "text/plain", Data: []byte("hi")})
}
func (f *fake) WriteContent(_ context.Context, recv func() (*pb.WriteContentRequest, error)) (*pb.TileResponse, error) {
	for {
		msg, err := recv()
		if errors.Is(err, io.EOF) {
			return &pb.TileResponse{Tile: f.tile}, nil
		}
		if err != nil {
			return nil, err
		}
		f.written = append(f.written, msg)
	}
}
func (f *fake) Subscribe(ctx context.Context, _ *pb.SubscribeRequest, send func(*pb.Event) error) error {
	for _, ev := range f.events {
		if err := send(ev); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return nil
}
func (f *fake) OpenShell(_ context.Context, recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error {
	for {
		msg, err := recv()
		if err != nil {
			return nil
		}
		f.shellIn = append(f.shellIn, msg)
		if err := send(&pb.OpenShellResponse{Data: append([]byte("echo:"), msg.Data...)}); err != nil {
			return err
		}
	}
}

// richTile carries a value in every field shape the wire has, so an
// omission in either codec shows up as a byte difference.
func richTile() *pb.Tile {
	return &pb.Tile{
		Id: "12", GridId: "3", Kind: "well", X: -4, Y: 9, W: 2, H: 3,
		AltText: "a room", Version: 7, BlobId: 11, PreviewBlobId: 13,
		ChildGridId: "p1/44", LinkTargetId: "p2/55", Reference: true,
		UrlString: "https://example.test/x", UrlHistory: "a\nb", UrlFrozen: true,
		ViewCx: 1.5, ViewCy: -2.5, ViewZoom: 0.25, ContentZoom: 1.75,
		TextX: 1, TextY: 2, TextW: 3, TextH: 4, TextMode: "edit",
		TextPresentation: "rendered", StatusDetail: "ok",
		ServesPage: true,
	}
}

func TestTileRoundTripsBytesIdentical(t *testing.T) {
	want := richTile()
	ns := loopback(t, &fake{tile: want})
	resp, err := ns.GetTile(context.Background(), &pb.GetTileRequest{TileId: "12"})
	if err != nil {
		t.Fatalf("GetTile: %v", err)
	}
	got, err := proto.Marshal(resp.GetTile())
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	wantBytes, err := proto.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if string(got) != string(wantBytes) {
		t.Fatalf("tile changed across the codec:\n got %v\nwant %v", resp.GetTile(), want)
	}
}

// The client classifies errors by CODE (client/clientsync). A code that
// collapses to Unknown at either codec turns a version conflict into an
// unrecoverable failure, so pin every code the router hands out.
func TestStatusCodesSurviveBothCodecs(t *testing.T) {
	for _, code := range []gcodes.Code{
		gcodes.NotFound, gcodes.InvalidArgument, gcodes.FailedPrecondition,
		gcodes.PermissionDenied, gcodes.Unimplemented, gcodes.Unavailable,
		gcodes.Aborted, gcodes.Internal,
	} {
		t.Run(code.String(), func(t *testing.T) {
			f := &fake{err: status.Error(code, "the reason")}
			ns := loopback(t, f)
			_, err := ns.GetTile(context.Background(), &pb.GetTileRequest{TileId: "12"})
			if status.Code(err) != code {
				t.Fatalf("code %v crossed as %v (%v)", code, status.Code(err), err)
			}
			if st, _ := status.FromError(err); st.Message() != "the reason" {
				t.Fatalf("message crossed as %q", st.Message())
			}
		})
	}
}

func TestReadContentStreamRoundTrips(t *testing.T) {
	f := &fake{chunks: []*pb.ContentChunk{
		{MediaType: "text/markdown", Version: 4, Data: []byte("one")},
		{Data: []byte("two")},
	}}
	ns := loopback(t, f)
	var got []*pb.ContentChunk
	if err := ns.ReadContent(context.Background(), &pb.ReadContentRequest{TileId: "12"},
		func(c *pb.ContentChunk) error { got = append(got, c); return nil }); err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	if len(got) != 2 || got[0].MediaType != "text/markdown" || got[0].Version != 4 ||
		string(got[0].Data) != "one" || string(got[1].Data) != "two" {
		t.Fatalf("chunks crossed as %v", got)
	}
}

func TestWriteContentStreamRoundTrips(t *testing.T) {
	f := &fake{tile: richTile()}
	ns := loopback(t, f)
	msgs := []*pb.WriteContentRequest{
		{TileId: "12", Version: 7, Data: []byte("one")},
		{Data: []byte("two")},
	}
	i := 0
	resp, err := ns.WriteContent(context.Background(), func() (*pb.WriteContentRequest, error) {
		if i == len(msgs) {
			return nil, io.EOF
		}
		i++
		return msgs[i-1], nil
	})
	if err != nil {
		t.Fatalf("WriteContent: %v", err)
	}
	if resp.GetTile().GetId() != "12" {
		t.Fatalf("response tile %v", resp.GetTile())
	}
	if len(f.written) != 2 || f.written[0].TileId != "12" || f.written[0].Version != 7 ||
		string(f.written[1].Data) != "two" {
		t.Fatalf("server saw %v", f.written)
	}
}

// A recv that FAILS must never reach the commit: the far side sees the
// stream break, not a clean end (commit-at-close).
func TestWriteContentBrokenRecvNeverCommits(t *testing.T) {
	f := &fake{tile: richTile()}
	ns := loopback(t, f)
	if _, err := ns.WriteContent(context.Background(), func() (*pb.WriteContentRequest, error) {
		return nil, errors.New("the caller's stream broke")
	}); err == nil {
		t.Fatal("a broken recv returned no error")
	}
	if len(f.written) != 0 {
		t.Fatalf("the far side saw %d messages after a broken recv", len(f.written))
	}
}

func TestSubscribeStreamRoundTrips(t *testing.T) {
	f := &fake{events: []*pb.Event{
		{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: richTile()}}},
	}}
	ns := loopback(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := make(chan *pb.Event, 1)
	go func() {
		_ = ns.Subscribe(ctx, &pb.SubscribeRequest{}, func(ev *pb.Event) error {
			got <- ev
			return nil
		})
	}()
	select {
	case ev := <-got:
		if ev.GetTileChanged().GetTile().GetId() != "12" {
			t.Fatalf("event crossed as %v", ev)
		}
	case <-ctx.Done():
		t.Fatal("no event crossed the codec")
	}
}

func TestOpenShellBidiRoundTrips(t *testing.T) {
	ns := loopback(t, &fake{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan *pb.OpenShellRequest, 2)
	in <- &pb.OpenShellRequest{TileId: "12"}
	in <- &pb.OpenShellRequest{Data: []byte("ls")}
	out := make(chan string, 4)
	go func() {
		_ = ns.OpenShell(ctx,
			func() (*pb.OpenShellRequest, error) {
				select {
				case m := <-in:
					return m, nil
				case <-ctx.Done():
					return nil, io.EOF
				}
			},
			func(r *pb.OpenShellResponse) error { out <- string(r.Data); return nil })
	}()
	for _, want := range []string{"echo:", "echo:ls"} {
		select {
		case got := <-out:
			if got != want {
				t.Fatalf("got %q want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no shell frame for %q", want)
		}
	}
}
