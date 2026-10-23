package rpc

import (
	"cmp"
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/api/idshape"
)

// Client wraps the Connect-generated gridwell client: one method per verb,
// streams assembled. Go callers use this rather than the raw connect client.
type Client struct {
	cl gridwellv1connect.GridwellClient
	// session names this client to the node, held here so its event stream
	// and its view cannot name two different clients.
	session string
}

// NewSession mints the id a client names itself by to the node, in the node's
// own id shape.
func NewSession() string { return idshape.NewShortID() }

// NewClient wires a Client to a Connect-RPC server under a fresh session.
// baseURL is protocol and host with no path.
func NewClient(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{cl: gridwellv1connect.NewGridwellClient(httpClient, baseURL, opts...), session: NewSession()}
}

// NewDefaultClient uses http.DefaultClient, which rides fetch under WASM, and
// the JSON-over-proto codec so dev-tools network panels show readable bodies.
// session is the caller's, minted before the client because the caller names
// itself by it first: the shim's trace ring stamps it on every record. opts
// are the caller's own, such as the shim's trace interceptor.
func NewDefaultClient(baseURL, session string, opts ...connect.ClientOption) *Client {
	c := NewClient(http.DefaultClient, baseURL, append([]connect.ClientOption{connect.WithProtoJSON()}, opts...)...)
	c.session = session
	return c
}

func (c *Client) GetGrid(ctx context.Context, gridID string) (*pb.GetGridResponse, error) {
	return msg(c.cl.GetGrid(ctx, connect.NewRequest(&pb.GetGridRequest{GridId: gridID})))
}

func (c *Client) GetTilePreview(ctx context.Context, tileID string) ([]byte, error) {
	m, err := msg(c.cl.GetTilePreview(ctx, connect.NewRequest(&pb.GetTilePreviewRequest{TileId: tileID})))
	return m.GetJpeg(), err
}

func (c *Client) Handshake(ctx context.Context) (*pb.HandshakeResponse, error) {
	return c.HandshakeNS(ctx, "")
}

// HandshakeNS is the routed plugin list. ns "" answers for the node this client
// talks to; a chain answers for the node it names, re-qualified per hop.
func (c *Client) HandshakeNS(ctx context.Context, ns string) (*pb.HandshakeResponse, error) {
	return msg(c.cl.Handshake(ctx, connect.NewRequest(&pb.HandshakeRequest{Namespace: ns})))
}

func (c *Client) GetTile(ctx context.Context, tileID string) (*pb.Tile, error) {
	return tileResp(c.cl.GetTile(ctx, connect.NewRequest(&pb.GetTileRequest{TileId: tileID})))
}

// msg is a call's answer, or its error.
func msg[T any](r *connect.Response[T], err error) (*T, error) {
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

// tileResp is the tile a call answers, or its error.
func tileResp(r *connect.Response[pb.TileResponse], err error) (*pb.Tile, error) {
	m, err := msg(r, err)
	return m.GetTile(), err
}

// CreateTile is the one create: tile.kind selects the meaningful fields.
func (c *Client) CreateTile(ctx context.Context, req *pb.CreateTileRequest) (*pb.Tile, error) {
	return tileResp(c.cl.CreateTile(ctx, connect.NewRequest(req)))
}

// CreateWithContent creates a text or workspace tile born with data.
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
	m, err := msg(c.cl.SetFraming(ctx, connect.NewRequest(req)))
	return m.GetTile(), err
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
	req := &pb.WriteContentRequest{TileId: tileID, Version: claim.Version, ContentStamp: claim.Stamp}
	for off := 0; off == 0 || off < len(data); off += ContentChunkBytes {
		req.Data = data[off:min(off+ContentChunkBytes, len(data))]
		if err := stream.Send(req); err != nil {
			_, cerr := stream.CloseAndReceive()
			return nil, cmp.Or(cerr, err)
		}
		req = &pb.WriteContentRequest{}
	}
	return tileResp(stream.CloseAndReceive())
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
	m, err := msg(c.cl.ShellTmuxAlive(ctx, connect.NewRequest(&pb.ShellTmuxAliveRequest{TileId: tileID})))
	return m.GetAlive(), err
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
	return msg(c.cl.DeleteTile(ctx, connect.NewRequest(req)))
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
