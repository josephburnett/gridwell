package rpc

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
)

// Client is the Go-idiomatic wrapper around the Connect-generated
// gridwell client. It accepts and returns rpc.* values (Go field
// casing) so callers never see proto-generated Id/Pid/UrlString style.
// Tests, the WASM client, and any future Go callers should use this
// rather than the raw connect client.
type Client struct {
	cl gridwellv1connect.GridwellClient
}

// NewClient wires a Client to a Connect-RPC server reachable via the
// given HTTP client + base URL. baseURL is the protocol + host, with
// no path (e.g. "http://localhost:3137"); Connect appends the
// per-method procedure path.
func NewClient(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{cl: gridwellv1connect.NewGridwellClient(httpClient, baseURL, opts...)}
}

// NewDefaultClient is the WASM-friendly constructor: it uses
// http.DefaultClient (under WASM this rides on fetch via syscall/js)
// and Connect's JSON-over-proto codec so dev-tools network panels
// still show readable bodies.
func NewDefaultClient(baseURL string) *Client {
	return NewClient(http.DefaultClient, baseURL, connect.WithProtoJSON())
}

func (c *Client) Bootstrap(ctx context.Context) (*BootstrapResponse, error) {
	r, err := c.cl.Bootstrap(ctx, connect.NewRequest(&pb.BootstrapRequest{}))
	if err != nil {
		return nil, err
	}
	return &BootstrapResponse{
		RootGridID: r.Msg.RootGridId,
		RootViewCx: r.Msg.RootViewCx,
		RootViewCy: r.Msg.RootViewCy,
		RootZoom:   r.Msg.RootZoom,
	}, nil
}

func (c *Client) GetGrid(ctx context.Context, gridID int64) (*GetGridResponse, error) {
	r, err := c.cl.GetGrid(ctx, connect.NewRequest(&pb.GetGridRequest{GridId: gridID}))
	if err != nil {
		return nil, err
	}
	out := &GetGridResponse{Tiles: TilesFromProto(r.Msg.Tiles)}
	if g := GridFromProto(r.Msg.Grid); g != nil {
		out.Grid = *g
	}
	return out, nil
}

func (c *Client) GetBlob(ctx context.Context, blobID int64) ([]byte, error) {
	r, err := c.cl.GetBlob(ctx, connect.NewRequest(&pb.GetBlobRequest{BlobId: blobID}))
	if err != nil {
		return nil, err
	}
	return r.Msg.Data, nil
}

func (c *Client) GetTilePreview(ctx context.Context, tileID int64) ([]byte, error) {
	r, err := c.cl.GetTilePreview(ctx, connect.NewRequest(&pb.GetTilePreviewRequest{TileId: tileID}))
	if err != nil {
		return nil, err
	}
	return r.Msg.Jpeg, nil
}

func (c *Client) CreateWell(ctx context.Context, req *CreateWellRequest) (*Tile, error) {
	r, err := c.cl.CreateWell(ctx, connect.NewRequest(CreateWellToProto(req)))
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}
func (c *Client) CreateText(ctx context.Context, req *CreateTextRequest) (*Tile, error) {
	r, err := c.cl.CreateText(ctx, connect.NewRequest(CreateTextToProto(req)))
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}
func (c *Client) CreateURL(ctx context.Context, req *CreateURLRequest) (*Tile, error) {
	r, err := c.cl.CreateURL(ctx, connect.NewRequest(CreateURLToProto(req)))
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}
func (c *Client) CreateBlackHole(ctx context.Context, req *CreateBlackHoleRequest) (*Tile, error) {
	r, err := c.cl.CreateBlackHole(ctx, connect.NewRequest(CreateBlackHoleToProto(req)))
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}
func (c *Client) CreateFileWell(ctx context.Context, req *CreateFileWellRequest) (*Tile, error) {
	r, err := c.cl.CreateFileWell(ctx, connect.NewRequest(CreateFileWellToProto(req)))
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}
func (c *Client) CreateProcessWell(ctx context.Context, req *CreateProcessWellRequest) (*Tile, error) {
	r, err := c.cl.CreateProcessWell(ctx, connect.NewRequest(CreateProcessWellToProto(req)))
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}
func (c *Client) CreateShell(ctx context.Context, req *CreateShellRequest) (*Tile, error) {
	r, err := c.cl.CreateShell(ctx, connect.NewRequest(CreateShellToProto(req)))
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}

func (c *Client) MoveTile(ctx context.Context, req *MoveTileRequest) (*Tile, error) {
	r, err := c.cl.MoveTile(ctx, connect.NewRequest(MoveTileToProto(req)))
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}
func (c *Client) CloneTile(ctx context.Context, req *CloneTileRequest) (*Tile, error) {
	r, err := c.cl.CloneTile(ctx, connect.NewRequest(CloneTileToProto(req)))
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}
func (c *Client) ResizeTile(ctx context.Context, req *ResizeTileRequest) (*Tile, error) {
	r, err := c.cl.ResizeTile(ctx, connect.NewRequest(ResizeTileToProto(req)))
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}
func (c *Client) SetWellView(ctx context.Context, req *SetWellViewRequest) (*Tile, error) {
	r, err := c.cl.SetWellView(ctx, connect.NewRequest(SetWellViewToProto(req)))
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}
func (c *Client) SetTextView(ctx context.Context, req *SetTextViewRequest) (*Tile, error) {
	r, err := c.cl.SetTextView(ctx, connect.NewRequest(SetTextViewToProto(req)))
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}
func (c *Client) SetShellPreview(ctx context.Context, req *SetShellPreviewRequest) (*Tile, error) {
	r, err := c.cl.SetShellPreview(ctx, connect.NewRequest(SetShellPreviewToProto(req)))
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}
func (c *Client) ShellSessionAlive(ctx context.Context, req *ShellSessionAliveRequest) (*ShellSessionAliveResponse, error) {
	r, err := c.cl.ShellSessionAlive(ctx, connect.NewRequest(ShellSessionAliveToProto(req)))
	if err != nil {
		return nil, err
	}
	return ShellSessionAliveResponseFromProto(r.Msg), nil
}
func (c *Client) SetRootView(ctx context.Context, req *SetRootViewRequest) error {
	_, err := c.cl.SetRootView(ctx, connect.NewRequest(SetRootViewToProto(req)))
	return err
}
func (c *Client) UpdateText(ctx context.Context, req *UpdateTextRequest) (*Tile, error) {
	r, err := c.cl.UpdateText(ctx, connect.NewRequest(UpdateTextToProto(req)))
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}
func (c *Client) DeleteTile(ctx context.Context, req *DeleteTileRequest) error {
	_, err := c.cl.DeleteTile(ctx, connect.NewRequest(DeleteTileToProto(req)))
	return err
}

// EventStream is the typed wrapper around Connect's server-stream
// client. Recv blocks until the next event arrives, or returns false
// if the stream ended cleanly. Always call Close.
type EventStream struct {
	s *connect.ServerStreamForClient[pb.Event]
}

// Subscribe opens the event stream. The returned context-tied stream
// closes when ctx is cancelled.
func (c *Client) Subscribe(ctx context.Context) (*EventStream, error) {
	s, err := c.cl.Subscribe(ctx, connect.NewRequest(&pb.SubscribeRequest{}))
	if err != nil {
		return nil, err
	}
	return &EventStream{s: s}, nil
}

// Recv returns the next event, or (zero, false, nil) on clean
// end-of-stream. Errors during read surface as (zero, false, err).
func (s *EventStream) Recv() (Event, bool, error) {
	if !s.s.Receive() {
		return Event{}, false, s.s.Err()
	}
	return EventFromProto(s.s.Msg()), true, nil
}

// Close releases the underlying HTTP connection.
func (s *EventStream) Close() error { return s.s.Close() }
