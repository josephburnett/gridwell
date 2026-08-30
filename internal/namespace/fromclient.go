package namespace

// FromClient is ONE of the two codecs (the other is Server): it reads a
// gridwell.v1 gRPC client as a Namespace. Exactly one place needs it —
// internal/remote/dial, which dials another node's federation socket — so
// the transport's connections are Namespace values like everything else
// and internal/remote never touches a gRPC stream type.

import (
	"context"
	"errors"
	"io"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

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

func (f fromClient) Search(ctx context.Context, req *pb.SearchRequest) (*pb.SearchResponse, error) {
	return f.c.Search(ctx, req)
}

func (f fromClient) CreateTile(ctx context.Context, req *pb.CreateTileRequest) (*pb.TileResponse, error) {
	return f.c.CreateTile(ctx, req)
}

func (f fromClient) SetTile(ctx context.Context, req *pb.SetTileRequest) (*pb.TileResponse, error) {
	return f.c.SetTile(ctx, req)
}

func (f fromClient) PlaceTile(ctx context.Context, req *pb.PlaceTileRequest) (*pb.TileResponse, error) {
	return f.c.PlaceTile(ctx, req)
}

func (f fromClient) CloneTile(ctx context.Context, req *pb.CloneTileRequest) (*pb.TileResponse, error) {
	return f.c.CloneTile(ctx, req)
}

func (f fromClient) DeleteTile(ctx context.Context, req *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	return f.c.DeleteTile(ctx, req)
}

func (f fromClient) SetFraming(ctx context.Context, req *pb.SetFramingRequest) (*pb.SetFramingResponse, error) {
	return f.c.SetFraming(ctx, req)
}

func (f fromClient) ShellSessionAlive(ctx context.Context, req *pb.ShellSessionAliveRequest) (*pb.ShellSessionAliveResponse, error) {
	return f.c.ShellSessionAlive(ctx, req)
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
			// Only a CLEAN end closes the stream, and only a close
			// commits: a broken recv returns below with nothing written.
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
				// The caller has no more to say. CloseSend and let the
				// DOWN side finish: that is the half carrying the far
				// end's verdict, and returning here on a clean io.EOF
				// would race the verdict away (a refused attach read as
				// a normal detach).
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

// pump relays a server-streaming source into a send callback, ending
// cleanly at io.EOF. The one relay loop behind every read stream here.
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
