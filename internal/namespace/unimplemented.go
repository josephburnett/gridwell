package namespace

// Unimplemented is the embeddable "this namespace does not offer that
// verb" default — the in-process twin of pb.UnimplementedGridwellServer,
// so a namespace declares only what it actually serves and everything else
// answers with the SAME Unimplemented code it answered with over the wire.
// The code is load-bearing: the router treats Unimplemented as a silent
// no-op in exactly one place (SetFraming on a namespace that keeps no
// framing), and the client's classifier reads it everywhere else.

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Unimplemented answers every verb with codes.Unimplemented. Embed it in a
// Namespace implementation and override what that namespace serves.
type Unimplemented struct{}

func unimp(method string) error {
	return status.Errorf(codes.Unimplemented, "method %s not implemented", method)
}

func (Unimplemented) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	return nil, unimp("Info")
}

func (Unimplemented) Probe(context.Context, *pb.ProbeRequest) (*pb.ProbeResponse, error) {
	return nil, unimp("Probe")
}

func (Unimplemented) Handshake(context.Context, *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	return nil, unimp("Handshake")
}

func (Unimplemented) GetGrid(context.Context, *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	return nil, unimp("GetGrid")
}

func (Unimplemented) GetTile(context.Context, *pb.GetTileRequest) (*pb.TileResponse, error) {
	return nil, unimp("GetTile")
}

func (Unimplemented) GetTilePreview(context.Context, *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	return nil, unimp("GetTilePreview")
}

func (Unimplemented) Search(context.Context, *pb.SearchRequest) (*pb.SearchResponse, error) {
	return nil, unimp("Search")
}

func (Unimplemented) CreateTile(context.Context, *pb.CreateTileRequest) (*pb.TileResponse, error) {
	return nil, unimp("CreateTile")
}

func (Unimplemented) SetTile(context.Context, *pb.SetTileRequest) (*pb.TileResponse, error) {
	return nil, unimp("SetTile")
}

func (Unimplemented) PlaceTile(context.Context, *pb.PlaceTileRequest) (*pb.TileResponse, error) {
	return nil, unimp("PlaceTile")
}

func (Unimplemented) CloneTile(context.Context, *pb.CloneTileRequest) (*pb.TileResponse, error) {
	return nil, unimp("CloneTile")
}

func (Unimplemented) DeleteTile(context.Context, *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	return nil, unimp("DeleteTile")
}

func (Unimplemented) SetFraming(context.Context, *pb.SetFramingRequest) (*pb.SetFramingResponse, error) {
	return nil, unimp("SetFraming")
}

func (Unimplemented) ShellSessionAlive(context.Context, *pb.ShellSessionAliveRequest) (*pb.ShellSessionAliveResponse, error) {
	return nil, unimp("ShellSessionAlive")
}

func (Unimplemented) ReadContent(context.Context, *pb.ReadContentRequest, func(*pb.ContentChunk) error) error {
	return unimp("ReadContent")
}

func (Unimplemented) ServeContent(context.Context, *pb.ServeContentRequest, func(*pb.ServeContentChunk) error) error {
	return unimp("ServeContent")
}

func (Unimplemented) WriteContent(context.Context, func() (*pb.WriteContentRequest, error)) (*pb.TileResponse, error) {
	return nil, unimp("WriteContent")
}

func (Unimplemented) OpenShell(context.Context, func() (*pb.OpenShellRequest, error), func(*pb.OpenShellResponse) error) error {
	return unimp("OpenShell")
}

func (Unimplemented) Subscribe(context.Context, *pb.SubscribeRequest, func(*pb.Event) error) error {
	return unimp("Subscribe")
}
