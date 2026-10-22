package namespace

// FromClient reads a gridwell.v1 gRPC client as a Namespace, for
// internal/connection/dial. Connections are then Namespace values like
// everything else, and internal/connection never touches a gRPC stream type.

import (
	"context"
	"errors"
	"io"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Rebound is recv answering first before it: a stream whose bind a hop
// rewrote.
func Rebound[T any](first T, recv func() (T, error)) func() (T, error) {
	sent := false
	return func() (T, error) {
		if sent {
			return recv()
		}
		sent = true
		return first, nil
	}
}

// Assembled is first's data and every chunk recv answers after it, once the
// stream ends cleanly: a broken stream errors, so its owner commits nothing.
func Assembled(first *pb.WriteContentRequest, recv func() (*pb.WriteContentRequest, error)) ([]byte, error) {
	data := append([]byte(nil), first.Data...)
	for {
		msg, err := recv()
		if errors.Is(err, io.EOF) {
			return data, nil
		}
		if err != nil {
			return nil, err
		}
		if data = append(data, msg.Data...); len(data) > rpc.MaxContentBytes {
			return nil, status.Error(codes.InvalidArgument, "write: content too large")
		}
	}
}

// FromClient adapts a gridwell.v1 client to the in-process interface.
func FromClient(c pb.GridwellClient) Namespace { return fromClient{c} }

type fromClient struct{ c pb.GridwellClient }

func (f fromClient) Info(ctx context.Context, req *pb.InfoRequest) (*pb.InfoResponse, error) {
	return f.c.Info(ctx, req)
}

func (f fromClient) Probe(ctx context.Context, req *pb.ProbeRequest) (*pb.ProbeResponse, error) {
	return f.c.Probe(ctx, req)
}

func (f fromClient) Handshake(ctx context.Context, req *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	return f.c.Handshake(ctx, req)
}

func (f fromClient) GetGrid(ctx context.Context, req *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	return f.c.GetGrid(ctx, req)
}

func (f fromClient) GetTile(ctx context.Context, req *pb.GetTileRequest) (*pb.TileResponse, error) {
	return f.c.GetTile(ctx, req)
}

func (f fromClient) GetTilePreview(ctx context.Context, req *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	return f.c.GetTilePreview(ctx, req)
}

func (f fromClient) Find(ctx context.Context, req *pb.FindRequest) (*pb.FindResponse, error) {
	return f.c.Find(ctx, req)
}

func (f fromClient) CreateTile(ctx context.Context, req *pb.CreateTileRequest) (*pb.TileResponse, error) {
	return f.c.CreateTile(ctx, req)
}

func (f fromClient) SetTile(ctx context.Context, req *pb.SetTileRequest) (*pb.TileResponse, error) {
	return f.c.SetTile(ctx, req)
}

func (f fromClient) MoveTile(ctx context.Context, req *pb.MoveTileRequest) (*pb.TileResponse, error) {
	return f.c.MoveTile(ctx, req)
}

func (f fromClient) CopyTile(ctx context.Context, req *pb.CopyTileRequest) (*pb.TileResponse, error) {
	return f.c.CopyTile(ctx, req)
}

func (f fromClient) DeleteTile(ctx context.Context, req *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	return f.c.DeleteTile(ctx, req)
}

func (f fromClient) SetFraming(ctx context.Context, req *pb.SetFramingRequest) (*pb.SetFramingResponse, error) {
	return f.c.SetFraming(ctx, req)
}

func (f fromClient) SetSelection(ctx context.Context, req *pb.SetSelectionRequest) (*pb.SetSelectionResponse, error) {
	return f.c.SetSelection(ctx, req)
}

func (f fromClient) ShellTmuxAlive(ctx context.Context, req *pb.ShellTmuxAliveRequest) (*pb.ShellTmuxAliveResponse, error) {
	return f.c.ShellTmuxAlive(ctx, req)
}

func (f fromClient) SetView(ctx context.Context, req *pb.SetViewRequest) (*pb.SetViewResponse, error) {
	return f.c.SetView(ctx, req)
}

func (f fromClient) ReadContent(ctx context.Context, req *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	up, err := f.c.ReadContent(ctx, req)
	if err != nil {
		return err
	}
	return pump(up.Recv, send)
}

func (f fromClient) ServeContent(ctx context.Context, req *pb.ServeContentRequest, send func(*pb.ServeContentChunk) error) error {
	up, err := f.c.ServeContent(ctx, req)
	if err != nil {
		return err
	}
	return pump(up.Recv, send)
}

func (f fromClient) Subscribe(ctx context.Context, req *pb.SubscribeRequest, send func(*pb.Event) error) error {
	up, err := f.c.Subscribe(ctx, req)
	if err != nil {
		return err
	}
	// A stream that ends unopened has no header, and Recv says how.
	if md, _ := up.Header(); md != nil {
		if err := send(nil); err != nil {
			return err
		}
	}
	return pump(up.Recv, send)
}

func (f fromClient) WriteContent(ctx context.Context, recv func() (*pb.WriteContentRequest, error)) (*pb.TileResponse, error) {
	up, err := f.c.WriteContent(ctx)
	if err != nil {
		return nil, err
	}
	for {
		msg, rerr := recv()
		if errors.Is(rerr, io.EOF) {
			// Only a clean end closes the stream, and only a close
			// commits. A broken recv returns below, having written nothing.
			return up.CloseAndRecv()
		}
		if rerr != nil {
			return nil, rerr
		}
		if serr := up.Send(msg); serr != nil {
			return nil, serr
		}
	}
}

func (f fromClient) OpenShell(ctx context.Context, recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error {
	up, err := f.c.OpenShell(ctx)
	if err != nil {
		return err
	}
	errc := make(chan error, 2)
	go func() {
		for {
			msg, rerr := recv()
			if rerr != nil {
				// The down side carries the far end's verdict, so
				// returning here on a clean io.EOF would race it away
				// and a refused attach would read as a normal detach.
				_ = up.CloseSend()
				if !errors.Is(rerr, io.EOF) {
					errc <- rerr
				}
				return
			}
			if serr := up.Send(msg); serr != nil {
				errc <- serr
				return
			}
		}
	}()
	go func() { errc <- pump(up.Recv, send) }()
	if err := <-errc; err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// pump relays a server-streaming source into send, ending at io.EOF.
func pump[T any](recv func() (*T, error), send func(*T) error) error {
	for {
		msg, err := recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := send(msg); err != nil {
			return err
		}
	}
}
