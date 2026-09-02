package namespace

// Server is one of the two codecs; the other is FromClient. It writes a
// Namespace onto the gridwell.v1 gRPC service. Exactly one place needs it,
// internal/server's ConnectionHandler, which serves the node's router to a
// remote mounter over the connection door, so the export is a codec and
// never a second router.

import (
	"context"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Server adapts a Namespace to the gridwell.v1 gRPC service.
func Server(ns Namespace) pb.GridwellServer { return &grpcServer{ns: ns} }

type grpcServer struct {
	pb.UnimplementedGridwellServer
	ns Namespace
}

func (g *grpcServer) Info(ctx context.Context, req *pb.InfoRequest) (*pb.InfoResponse, error) {
	return g.ns.Info(ctx, req)
}

func (g *grpcServer) Probe(ctx context.Context, req *pb.ProbeRequest) (*pb.ProbeResponse, error) {
	return g.ns.Probe(ctx, req)
}

func (g *grpcServer) Handshake(ctx context.Context, req *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	return g.ns.Handshake(ctx, req)
}

func (g *grpcServer) GetGrid(ctx context.Context, req *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	return g.ns.GetGrid(ctx, req)
}

func (g *grpcServer) GetTile(ctx context.Context, req *pb.GetTileRequest) (*pb.TileResponse, error) {
	return g.ns.GetTile(ctx, req)
}

func (g *grpcServer) GetTilePreview(ctx context.Context, req *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	return g.ns.GetTilePreview(ctx, req)
}

func (g *grpcServer) Search(ctx context.Context, req *pb.SearchRequest) (*pb.SearchResponse, error) {
	return g.ns.Search(ctx, req)
}

func (g *grpcServer) CreateTile(ctx context.Context, req *pb.CreateTileRequest) (*pb.TileResponse, error) {
	return g.ns.CreateTile(ctx, req)
}

func (g *grpcServer) SetTile(ctx context.Context, req *pb.SetTileRequest) (*pb.TileResponse, error) {
	return g.ns.SetTile(ctx, req)
}

func (g *grpcServer) PlaceTile(ctx context.Context, req *pb.PlaceTileRequest) (*pb.TileResponse, error) {
	return g.ns.PlaceTile(ctx, req)
}

func (g *grpcServer) CloneTile(ctx context.Context, req *pb.CloneTileRequest) (*pb.TileResponse, error) {
	return g.ns.CloneTile(ctx, req)
}

func (g *grpcServer) DeleteTile(ctx context.Context, req *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	return g.ns.DeleteTile(ctx, req)
}

func (g *grpcServer) SetFraming(ctx context.Context, req *pb.SetFramingRequest) (*pb.SetFramingResponse, error) {
	return g.ns.SetFraming(ctx, req)
}

func (g *grpcServer) ShellSessionAlive(ctx context.Context, req *pb.ShellSessionAliveRequest) (*pb.ShellSessionAliveResponse, error) {
	return g.ns.ShellSessionAlive(ctx, req)
}

func (g *grpcServer) ReadContent(req *pb.ReadContentRequest, stream pb.Gridwell_ReadContentServer) error {
	return g.ns.ReadContent(stream.Context(), req, stream.Send)
}

func (g *grpcServer) ServeContent(req *pb.ServeContentRequest, stream pb.Gridwell_ServeContentServer) error {
	return g.ns.ServeContent(stream.Context(), req, stream.Send)
}

func (g *grpcServer) Subscribe(req *pb.SubscribeRequest, stream pb.Gridwell_SubscribeServer) error {
	return g.ns.Subscribe(stream.Context(), req, stream.Send)
}

func (g *grpcServer) WriteContent(stream pb.Gridwell_WriteContentServer) error {
	resp, err := g.ns.WriteContent(stream.Context(), stream.Recv)
	if err != nil {
		return err
	}
	return stream.SendAndClose(resp)
}

func (g *grpcServer) OpenShell(stream pb.Gridwell_OpenShellServer) error {
	return g.ns.OpenShell(stream.Context(), stream.Recv, stream.Send)
}
