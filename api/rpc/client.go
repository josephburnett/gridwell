package rpc

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
)

// Client wraps the Connect-generated gridwell client: one method per verb,
// streams assembled. Go callers use this rather than the raw connect client.
type Client struct {
	cl gridwellv1connect.GridwellClient
	// session names this client to the node, minted here so its event
	// stream and its subscription cannot name two different clients.
	session string
}

// NewClient wires a Client to a Connect-RPC server. baseURL is protocol and
// host with no path.
func NewClient(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{cl: gridwellv1connect.NewGridwellClient(httpClient, baseURL, opts...), session: rand.Text()}
}

// NewDefaultClient uses http.DefaultClient, which rides fetch under WASM, and
// the JSON-over-proto codec so dev-tools network panels show readable bodies.
// opts are the caller's own, such as the shim's trace interceptor.
func NewDefaultClient(baseURL string, opts ...connect.ClientOption) *Client {
	return NewClient(http.DefaultClient, baseURL, append([]connect.ClientOption{connect.WithProtoJSON()}, opts...)...)
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

// CreateWithContent creates a text or pane tile born with data.
func (c *Client) CreateWithContent(ctx context.Context, req *pb.CreateTileRequest, data []byte) (*pb.Tile, error) {
	req.Content = data
	return c.CreateTile(ctx, req)
}

// SetTile is the one capture and framing writeback; each scalar arm carries
// exactly one operation per call.
func (c *Client) SetTile(ctx context.Context, req *pb.SetTileRequest) (*pb.Tile, error) {
	if err := unclaimed(req); err != nil {
		return nil, err
	}
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

// SetSelection adds tiles to a grid's selection and takes tiles out of it.
// The set on disk comes back as SelectionChanged, never in the reply.
func (c *Client) SetSelection(ctx context.Context, req *pb.SetSelectionRequest) error {
	_, err := c.cl.SetSelection(ctx, connect.NewRequest(req))
	return err
}

// ReadContent's first chunk carries the media type and the basis the bytes
// belong to, which is the save basis. A leaf link resolves at the serving
// node.
func (c *Client) ReadContent(ctx context.Context, tileID string) (data []byte, mediaType string, basis ContentBasis, err error) {
	stream, err := c.cl.ReadContent(ctx, connect.NewRequest(&pb.ReadContentRequest{TileId: tileID}))
	if err != nil {
		return nil, "", ContentBasis{}, err
	}
	defer stream.Close()
	first := true
	for stream.Receive() {
		msg := stream.Msg()
		if first {
			mediaType, basis = msg.MediaType, ContentBasis{Version: msg.Version, Stamp: msg.ContentStamp}
			first = false
		}
		data = append(data, msg.Data...)
	}
	if err := stream.Err(); err != nil {
		return nil, "", ContentBasis{}, err
	}
	return data, mediaType, basis, nil
}

// WriteContent claims the basis the bytes it replaces were read under and
// commits at close, so a failure anywhere leaves the old value intact. data is
// the complete new value.
func (c *Client) WriteContent(ctx context.Context, tileID string, claim ContentBasis, data []byte) (*pb.Tile, error) {
	stream := c.cl.WriteContent(ctx)
	end := min(ContentChunkBytes, len(data))
	if err := stream.Send(&pb.WriteContentRequest{TileId: tileID, Version: claim.Version, ContentStamp: claim.Stamp, Data: data[:end]}); err != nil {
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

// MoveTile is the single placement writeback: one verb owns (grid, x, y, w,
// h), move or resize or both.
func (c *Client) MoveTile(ctx context.Context, req *pb.MoveTileRequest) (*pb.Tile, error) {
	if err := unclaimed(req); err != nil {
		return nil, err
	}
	return tileResp(c.cl.MoveTile(ctx, connect.NewRequest(req)))
}

func (c *Client) CopyTile(ctx context.Context, req *pb.CopyTileRequest) (*pb.Tile, error) {
	return tileResp(c.cl.CopyTile(ctx, connect.NewRequest(req)))
}

// ShellTmuxAlive reports whether the tile's tmux still exists.
func (c *Client) ShellTmuxAlive(ctx context.Context, tileID string) (bool, error) {
	r, err := c.cl.ShellTmuxAlive(ctx, connect.NewRequest(&pb.ShellTmuxAliveRequest{TileId: tileID}))
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

// SetFrozen writes the standing freeze, on a url or a shell, claiming the
// freeze the writer saw. It never bumps version.
func (c *Client) SetFrozen(ctx context.Context, tileID string, frozen, claim bool) (*pb.Tile, error) {
	return c.SetTile(ctx, &pb.SetTileRequest{TileId: tileID, UrlFrozen: &frozen, ClaimFrozen: &claim})
}

func (c *Client) DeleteTile(ctx context.Context, req *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	if err := unclaimed(req); err != nil {
		return nil, err
	}
	resp, err := c.cl.DeleteTile(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// unclaimed refuses here what the API would: see UnclaimedWrite.
func unclaimed(req proto.Message) error {
	if why := UnclaimedWrite(req); why != "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New(why))
	}
	return nil
}

// EventStream wraps Connect's server-stream client. Always call Close.
type EventStream struct {
	s *connect.ServerStreamForClient[pb.Event]
}

// Session is the id this client names itself by to the node.
func (c *Client) Session() string { return c.session }

// SetView tells the node what this client shows, under this client's
// session. The node forgets it when the client's last event stream closes, so
// a client says it again after every re-open.
func (c *Client) SetView(ctx context.Context, v *pb.SetViewRequest) error {
	req := &pb.SetViewRequest{}
	if v != nil {
		req = proto.CloneOf(v)
	}
	req.Session = c.session
	_, err := c.cl.SetView(ctx, connect.NewRequest(req))
	return err
}

// DisableSource switches off one source this node declares until the node
// restarts; the health event that follows is the verdict.
func (c *Client) DisableSource(ctx context.Context, namespace string) error {
	_, err := c.cl.DisableSource(ctx, connect.NewRequest(&pb.DisableSourceRequest{Namespace: namespace}))
	return err
}

// Subscribe opens the event stream, which closes when ctx is cancelled. It
// returns once the node says it is listening, so a change made after it is told.
func (c *Client) Subscribe(ctx context.Context) (*EventStream, error) {
	s, err := c.cl.Subscribe(ctx, connect.NewRequest(&pb.SubscribeRequest{Session: c.session}))
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
