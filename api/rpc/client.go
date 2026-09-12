package rpc

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
)

// Client wraps the Connect-generated gridwell client: one method per verb,
// streams assembled. Go callers use this rather than the raw connect client.
type Client struct {
	cl gridwellv1connect.GridwellClient
}

// NewClient wires a Client to a Connect-RPC server. baseURL is protocol and
// host with no path.
func NewClient(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{cl: gridwellv1connect.NewGridwellClient(httpClient, baseURL, opts...)}
}

// NewDefaultClient uses http.DefaultClient, which rides fetch under WASM, and
// the JSON-over-proto codec so dev-tools network panels show readable bodies.
func NewDefaultClient(baseURL string) *Client {
	return NewClient(http.DefaultClient, baseURL, connect.WithProtoJSON())
}

func (c *Client) GetGrid(ctx context.Context, gridID string) (*pb.GetGridResponse, error) {
	r, err := c.cl.GetGrid(ctx, connect.NewRequest(&pb.GetGridRequest{GridId: gridID}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

func (c *Client) GetTilePreview(ctx context.Context, tileID string) ([]byte, error) {
	r, err := c.cl.GetTilePreview(ctx, connect.NewRequest(&pb.GetTilePreviewRequest{TileId: tileID}))
	if err != nil {
		return nil, err
	}
	return r.Msg.Jpeg, nil
}

func (c *Client) Handshake(ctx context.Context) (*pb.HandshakeResponse, error) {
	return c.HandshakeNS(ctx, "")
}

// HandshakeNS is the routed plugin list. ns "" answers for the node this client
// talks to; a chain answers for the node it names, re-qualified per hop.
func (c *Client) HandshakeNS(ctx context.Context, ns string) (*pb.HandshakeResponse, error) {
	r, err := c.cl.Handshake(ctx, connect.NewRequest(&pb.HandshakeRequest{Namespace: ns}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

func (c *Client) GetTile(ctx context.Context, tileID string) (*pb.Tile, error) {
	r, err := c.cl.GetTile(ctx, connect.NewRequest(&pb.GetTileRequest{TileId: tileID}))
	if err != nil {
		return nil, err
	}
	return r.Msg.Tile, nil
}

// tileResp mirrors the server's tileResp.
func tileResp(r *connect.Response[pb.TileResponse], err error) (*pb.Tile, error) {
	if err != nil {
		return nil, err
	}
	return r.Msg.Tile, nil
}

// CreateTile is the one create: tile.kind selects the meaningful fields.
func (c *Client) CreateTile(ctx context.Context, req *pb.CreateTileRequest) (*pb.Tile, error) {
	return tileResp(c.cl.CreateTile(ctx, connect.NewRequest(req)))
}

// CreateWithContent follows the create with a content write, because creation
// is metadata-only on the wire. A failure between the two leaves an empty tile:
// visible and deletable, never silent.
func (c *Client) CreateWithContent(ctx context.Context, req *pb.CreateTileRequest, data []byte) (*pb.Tile, error) {
	t, err := c.CreateTile(ctx, req)
	if err != nil || len(data) == 0 {
		return t, err
	}
	return c.WriteContent(ctx, t.Id, t.Version, data)
}

// SetTile is the one capture and framing writeback; each scalar arm carries
// exactly one operation per call.
func (c *Client) SetTile(ctx context.Context, req *pb.SetTileRequest) (*pb.Tile, error) {
	return tileResp(c.cl.SetTile(ctx, connect.NewRequest(req)))
}

// SetFraming is the one framing write, routed on whichever target the request
// names. Returns nil for a root grid, which has no tile row.
func (c *Client) SetFraming(ctx context.Context, req *pb.SetFramingRequest) (*pb.Tile, error) {
	resp, err := c.cl.SetFraming(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetTile(), nil
}

// ReadContent's first chunk carries the media type and the row version the
// bytes belong to, which is the save basis. A leaf link resolves at the
// serving node.
func (c *Client) ReadContent(ctx context.Context, tileID string) (data []byte, mediaType string, version int64, err error) {
	stream, err := c.cl.ReadContent(ctx, connect.NewRequest(&pb.ReadContentRequest{TileId: tileID}))
	if err != nil {
		return nil, "", 0, err
	}
	defer stream.Close()
	first := true
	for stream.Receive() {
		msg := stream.Msg()
		if first {
			mediaType, version = msg.MediaType, msg.Version
			first = false
		}
		data = append(data, msg.Data...)
	}
	if err := stream.Err(); err != nil {
		return nil, "", 0, err
	}
	return data, mediaType, version, nil
}

// WriteContent is version-claimed and commits at close, so a failure anywhere
// leaves the old value intact. data is the complete new value.
func (c *Client) WriteContent(ctx context.Context, tileID string, version int64, data []byte) (*pb.Tile, error) {
	stream := c.cl.WriteContent(ctx)
	end := min(ContentChunkBytes, len(data))
	if err := stream.Send(&pb.WriteContentRequest{TileId: tileID, Version: version, Data: data[:end]}); err != nil {
		_, cerr := stream.CloseAndReceive()
		if cerr != nil {
			return nil, cerr
		}
		return nil, err
	}
	for off := end; off < len(data); off += ContentChunkBytes {
		e := min(off+ContentChunkBytes, len(data))
		if err := stream.Send(&pb.WriteContentRequest{Data: data[off:e]}); err != nil {
			_, cerr := stream.CloseAndReceive()
			if cerr != nil {
				return nil, cerr
			}
			return nil, err
		}
	}
	resp, err := stream.CloseAndReceive()
	if err != nil {
		return nil, err
	}
	return resp.Msg.Tile, nil
}

// PlaceTile is the single placement writeback: one verb owns (grid, x, y, w,
// h), move or resize or both.
func (c *Client) PlaceTile(ctx context.Context, req *pb.PlaceTileRequest) (*pb.Tile, error) {
	return tileResp(c.cl.PlaceTile(ctx, connect.NewRequest(req)))
}

func (c *Client) CloneTile(ctx context.Context, req *pb.CloneTileRequest) (*pb.Tile, error) {
	return tileResp(c.cl.CloneTile(ctx, connect.NewRequest(req)))
}

// ShellSessionAlive reports whether the tile's tmux session still exists.
func (c *Client) ShellSessionAlive(ctx context.Context, tileID string) (bool, error) {
	r, err := c.cl.ShellSessionAlive(ctx, connect.NewRequest(&pb.ShellSessionAliveRequest{TileId: tileID}))
	if err != nil {
		return false, err
	}
	return r.Msg.Alive, nil
}

// RenameTile is a real user edit, so the server latches alt_user and automatic
// captures defer.
func (c *Client) RenameTile(ctx context.Context, tileID string, version int64, alt string) (*pb.Tile, error) {
	return tileResp(c.cl.SetTile(ctx, connect.NewRequest(&pb.SetTileRequest{
		TileId: tileID, Version: version, Rename: alt,
	})))
}

// SetContentZoom is framing: no claim, and it never bumps version.
func (c *Client) SetContentZoom(ctx context.Context, tileID string, zoom float64) (*pb.Tile, error) {
	return tileResp(c.cl.SetTile(ctx, connect.NewRequest(&pb.SetTileRequest{
		TileId: tileID, ContentZoom: &zoom,
	})))
}

// SetURLFrozen is framing: no claim, and it never bumps version.
func (c *Client) SetURLFrozen(ctx context.Context, tileID string, frozen bool) (*pb.Tile, error) {
	return tileResp(c.cl.SetTile(ctx, connect.NewRequest(&pb.SetTileRequest{
		TileId: tileID, UrlFrozen: &frozen,
	})))
}

func (c *Client) DeleteTile(ctx context.Context, req *pb.DeleteTileRequest) error {
	_, err := c.cl.DeleteTile(ctx, connect.NewRequest(req))
	return err
}

// EventStream wraps Connect's server-stream client. Always call Close.
type EventStream struct {
	s *connect.ServerStreamForClient[pb.Event]
}

// Subscribe opens the event stream, which closes when ctx is cancelled.
func (c *Client) Subscribe(ctx context.Context) (*EventStream, error) {
	s, err := c.cl.Subscribe(ctx, connect.NewRequest(&pb.SubscribeRequest{}))
	if err != nil {
		return nil, err
	}
	return &EventStream{s: s}, nil
}

// Recv returns (nil, false, nil) at clean end-of-stream.
func (s *EventStream) Recv() (*pb.Event, bool, error) {
	if !s.s.Receive() {
		return nil, false, s.s.Err()
	}
	return s.s.Msg(), true, nil
}

func (s *EventStream) Close() error { return s.s.Close() }
