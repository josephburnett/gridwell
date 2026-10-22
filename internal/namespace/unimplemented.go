package namespace

// Unimplemented is the in-process twin of pb.UnimplementedGridwellServer, for
// a test's partial namespace: every verb it is not given answers with the code
// the wire would. A production owner answers every verb itself, its refusals
// naming their reasons (test/boundary TestNoUnimplementedEmbeds).

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Unimplemented answers every verb with codes.Unimplemented.
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

func (Unimplemented) Find(context.Context, *pb.FindRequest) (*pb.FindResponse, error) {
	return nil, unimp("Find")
}

func (Unimplemented) CreateTile(context.Context, *pb.CreateTileRequest) (*pb.TileResponse, error) {
	return nil, unimp("CreateTile")
}

func (Unimplemented) SetTile(context.Context, *pb.SetTileRequest) (*pb.TileResponse, error) {
	return nil, unimp("SetTile")
}

func (Unimplemented) MoveTile(context.Context, *pb.MoveTileRequest) (*pb.TileResponse, error) {
	return nil, unimp("MoveTile")
}

func (Unimplemented) CopyTile(context.Context, *pb.CopyTileRequest) (*pb.TileResponse, error) {
	return nil, unimp("CopyTile")
}

func (Unimplemented) DeleteTile(context.Context, *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	return nil, unimp("DeleteTile")
}

func (Unimplemented) SetFraming(context.Context, *pb.SetFramingRequest) (*pb.SetFramingResponse, error) {
	return nil, unimp("SetFraming")
}

func (Unimplemented) SetSelection(context.Context, *pb.SetSelectionRequest) (*pb.SetSelectionResponse, error) {
	return nil, unimp("SetSelection")
}

func (Unimplemented) ShellTmuxAlive(context.Context, *pb.ShellTmuxAliveRequest) (*pb.ShellTmuxAliveResponse, error) {
	return nil, unimp("ShellTmuxAlive")
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

func (Unimplemented) SetView(context.Context, *pb.SetViewRequest) (*pb.SetViewResponse, error) {
	return nil, unimp("SetView")
}
