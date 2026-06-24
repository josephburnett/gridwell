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

func (c *Client) GetGrid(ctx context.Context, gridID string) (*GetGridResponse, error) {
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

func (c *Client) GetTilePreview(ctx context.Context, tileID string) ([]byte, error) {
	r, err := c.cl.GetTilePreview(ctx, connect.NewRequest(&pb.GetTilePreviewRequest{TileId: tileID}))
	if err != nil {
		return nil, err
	}
	return r.Msg.Jpeg, nil
}

// GetTileContent fetches a text tile's descent body bytes. Routable by tile
// id, so it resolves content for plugin-owned tiles (a file's metadata, a
// process's @info) as well as local store tiles.
func (c *Client) GetTileContent(ctx context.Context, tileID string) ([]byte, error) {
	r, err := c.cl.GetTileContent(ctx, connect.NewRequest(&pb.GetTileContentRequest{TileId: tileID}))
	if err != nil {
		return nil, err
	}
	return r.Msg.Data, nil
}

// ListPlugins returns the node's configured plugins in config order, for the
// launcher / + menu.
func (c *Client) ListPlugins(ctx context.Context) ([]PluginInfo, error) {
	r, err := c.cl.ListPlugins(ctx, connect.NewRequest(&pb.ListPluginsRequest{}))
	if err != nil {
		return nil, err
	}
	out := make([]PluginInfo, len(r.Msg.Plugins))
	for i, p := range r.Msg.Plugins {
		out[i] = PluginInfo{UUID: p.Uuid, Kind: p.Kind, Label: p.Label, Writable: p.Writable, RootGridID: p.RootGridId}
	}
	return out, nil
}

// GetTile reads a single tile's metadata by id.
func (c *Client) GetTile(ctx context.Context, tileID string) (*Tile, error) {
	r, err := c.cl.GetTile(ctx, connect.NewRequest(&pb.GetTileRequest{TileId: tileID}))
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}

// tileResp unwraps a TileResponse from any of the Tile-returning RPCs into a
// Go rpc.Tile (or the transport error). The mirror of the server's tileResp:
// every Create / Move / Clone / Resize / Set / Update method ends the same way,
// so the unwrap lives in one place rather than being hand-copied per method.
func tileResp(r *connect.Response[pb.TileResponse], err error) (*Tile, error) {
	if err != nil {
		return nil, err
	}
	return TileFromProto(r.Msg.Tile), nil
}

func (c *Client) CreateWell(ctx context.Context, req *CreateWellRequest) (*Tile, error) {
	return tileResp(c.cl.CreateWell(ctx, connect.NewRequest(CreateWellToProto(req))))
}

// Mount attaches a plugin (by uuid, default config) and drops a mount well in
// the destination grid. The drag-a-plugin-onto-a-grid gesture.
func (c *Client) Mount(ctx context.Context, req *MountRequest) (*Tile, error) {
	return tileResp(c.cl.Mount(ctx, connect.NewRequest(&pb.MountRequest{
		PluginUuid: req.PluginUUID,
		Path:       PathToProto(req.Path),
		GridId:     req.GridID,
		X:          req.X,
		Y:          req.Y,
		W:          req.W,
		H:          req.H,
	})))
}
func (c *Client) CreateText(ctx context.Context, req *CreateTextRequest) (*Tile, error) {
	return tileResp(c.cl.CreateText(ctx, connect.NewRequest(CreateTextToProto(req))))
}
func (c *Client) CreateURL(ctx context.Context, req *CreateURLRequest) (*Tile, error) {
	return tileResp(c.cl.CreateURL(ctx, connect.NewRequest(CreateURLToProto(req))))
}
func (c *Client) CreateShell(ctx context.Context, req *CreateShellRequest) (*Tile, error) {
	return tileResp(c.cl.CreateShell(ctx, connect.NewRequest(CreateShellToProto(req))))
}

func (c *Client) MoveTile(ctx context.Context, req *MoveTileRequest) (*Tile, error) {
	return tileResp(c.cl.MoveTile(ctx, connect.NewRequest(MoveTileToProto(req))))
}
func (c *Client) CloneTile(ctx context.Context, req *CloneTileRequest) (*Tile, error) {
	return tileResp(c.cl.CloneTile(ctx, connect.NewRequest(CloneTileToProto(req))))
}
func (c *Client) ResizeTile(ctx context.Context, req *ResizeTileRequest) (*Tile, error) {
	return tileResp(c.cl.ResizeTile(ctx, connect.NewRequest(ResizeTileToProto(req))))
}
func (c *Client) SetWellView(ctx context.Context, req *SetWellViewRequest) (*Tile, error) {
	return tileResp(c.cl.SetWellView(ctx, connect.NewRequest(SetWellViewToProto(req))))
}
func (c *Client) SetTextView(ctx context.Context, req *SetTextViewRequest) (*Tile, error) {
	return tileResp(c.cl.SetTextView(ctx, connect.NewRequest(SetTextViewToProto(req))))
}
func (c *Client) SetShellPreview(ctx context.Context, req *SetShellPreviewRequest) (*Tile, error) {
	return tileResp(c.cl.SetShellPreview(ctx, connect.NewRequest(SetShellPreviewToProto(req))))
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
func (c *Client) SetURLState(ctx context.Context, req *SetURLStateRequest) (*Tile, error) {
	return tileResp(c.cl.SetURLState(ctx, connect.NewRequest(SetURLStateToProto(req))))
}
func (c *Client) UpdateText(ctx context.Context, req *UpdateTextRequest) (*Tile, error) {
	return tileResp(c.cl.UpdateText(ctx, connect.NewRequest(UpdateTextToProto(req))))
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
