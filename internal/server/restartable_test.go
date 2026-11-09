package server_test

import (
	"context"
	"sync/atomic"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/server"
)

// restartable is a node's transport a test replaces while the node serves, as
// a restart whose server.yaml declares other connections does (restart).
type restartable struct {
	cur atomic.Pointer[namespace.Namespace]
}

// restart installs ns as reg's transport, replacing the one it serves.
func restart(reg *server.Sources, ns namespace.Namespace) {
	r, ok := reg.Transport.(*restartable)
	if !ok {
		r = &restartable{}
		reg.Transport = r
	}
	r.cur.Store(&ns)
}

// transportOf is the transport reg serves now.
func transportOf(reg *server.Sources) namespace.Namespace {
	return *reg.Transport.(*restartable).cur.Load()
}

func (r *restartable) ns() namespace.Namespace { return *r.cur.Load() }

func (r *restartable) Info(ctx context.Context, req *pb.InfoRequest) (*pb.InfoResponse, error) {
	return r.ns().Info(ctx, req)
}

func (r *restartable) Probe(ctx context.Context, req *pb.ProbeRequest) (*pb.ProbeResponse, error) {
	return r.ns().Probe(ctx, req)
}

func (r *restartable) Handshake(ctx context.Context, req *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	return r.ns().Handshake(ctx, req)
}

func (r *restartable) GetGrid(ctx context.Context, req *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	return r.ns().GetGrid(ctx, req)
}

func (r *restartable) GetTile(ctx context.Context, req *pb.GetTileRequest) (*pb.TileResponse, error) {
	return r.ns().GetTile(ctx, req)
}

func (r *restartable) GetTilePreview(ctx context.Context, req *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	return r.ns().GetTilePreview(ctx, req)
}

func (r *restartable) Find(ctx context.Context, req *pb.FindRequest) (*pb.FindResponse, error) {
	return r.ns().Find(ctx, req)
}

func (r *restartable) CreateTile(ctx context.Context, req *pb.CreateTileRequest) (*pb.TileResponse, error) {
	return r.ns().CreateTile(ctx, req)
}

func (r *restartable) SetTile(ctx context.Context, req *pb.SetTileRequest) (*pb.TileResponse, error) {
	return r.ns().SetTile(ctx, req)
}

func (r *restartable) MoveTile(ctx context.Context, req *pb.MoveTileRequest) (*pb.TileResponse, error) {
	return r.ns().MoveTile(ctx, req)
}

func (r *restartable) CopyTile(ctx context.Context, req *pb.CopyTileRequest) (*pb.TileResponse, error) {
	return r.ns().CopyTile(ctx, req)
}

func (r *restartable) DeleteTile(ctx context.Context, req *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	return r.ns().DeleteTile(ctx, req)
}

func (r *restartable) SetFraming(ctx context.Context, req *pb.SetFramingRequest) (*pb.SetFramingResponse, error) {
	return r.ns().SetFraming(ctx, req)
}

func (r *restartable) SetSelection(ctx context.Context, req *pb.SetSelectionRequest) (*pb.SetSelectionResponse, error) {
	return r.ns().SetSelection(ctx, req)
}

func (r *restartable) ReadContent(ctx context.Context, req *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	return r.ns().ReadContent(ctx, req, send)
}

func (r *restartable) ServeContent(ctx context.Context, req *pb.ServeContentRequest, send func(*pb.ServeContentChunk) error) error {
	return r.ns().ServeContent(ctx, req, send)
}

func (r *restartable) WriteContent(ctx context.Context, recv func() (*pb.WriteContentRequest, error)) (*pb.TileResponse, error) {
	return r.ns().WriteContent(ctx, recv)
}

func (r *restartable) ShellTmuxAlive(ctx context.Context, req *pb.ShellTmuxAliveRequest) (*pb.ShellTmuxAliveResponse, error) {
	return r.ns().ShellTmuxAlive(ctx, req)
}

func (r *restartable) PasteShell(ctx context.Context, req *pb.PasteShellRequest) (*pb.PasteShellResponse, error) {
	return r.ns().PasteShell(ctx, req)
}

func (r *restartable) OpenShell(ctx context.Context, recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error {
	return r.ns().OpenShell(ctx, recv, send)
}

func (r *restartable) Subscribe(ctx context.Context, req *pb.SubscribeRequest, send func(*pb.Event) error) error {
	return r.ns().Subscribe(ctx, req, send)
}

func (r *restartable) SetView(ctx context.Context, req *pb.SetViewRequest) (*pb.SetViewResponse, error) {
	return r.ns().SetView(ctx, req)
}
