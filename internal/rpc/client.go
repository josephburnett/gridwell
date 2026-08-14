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

func (c *Client) GetTilePreview(ctx context.Context, tileID string) ([]byte, error) {
	r, err := c.cl.GetTilePreview(ctx, connect.NewRequest(&pb.GetTilePreviewRequest{TileId: tileID}))
	if err != nil {
		return nil, err
	}
	return r.Msg.Jpeg, nil
}

// NodeIdentity returns the node's own uuid and the qualified id of its node
// grid. A convenience wrapper over the ONE handshake (ListPlugins carries
// the same fields); production boot reads PluginList directly.
func (c *Client) NodeIdentity(ctx context.Context) (nodeUUID, nodeRootGridID string, err error) {
	r, err := c.cl.ListPlugins(ctx, connect.NewRequest(&pb.ListPluginsRequest{}))
	if err != nil {
		return "", "", err
	}
	return r.Msg.NodeUuid, r.Msg.NodeRootGridId, nil
}

// PluginList is the node handshake: the plugin roster plus the node-level
// facts that ride it (shells_disabled — see data.proto).
type PluginList struct {
	Plugins []PluginInfo
	// ShellsDisabled: this node refuses shell tiles outright; the client
	// derives caps from it (no shell primitive in the + palette).
	ShellsDisabled bool
	// ContentToken: the /content/ door's path capability — the client builds
	// every plugin-served page URL as
	// <origin>/content/<ContentToken>/<tile-id>/ (see data.proto).
	ContentToken string
	// NodeUUID / NodeRootGridID: the node's durable identity and its
	// plugin-list grid — the SAME facts NodeIdentity returns; carried here
	// so boot needs ONE handshake (it used to fire ListPlugins twice, and
	// the server re-ran every plugin Info both times).
	NodeUUID       string
	NodeRootGridID string
	// NodeRootView*: the node grid's own persisted viewport (2026-08-13);
	// zero zoom = never set. The boot restore for a node-grid anchor.
	NodeRootViewCx   float64
	NodeRootViewCy   float64
	NodeRootViewZoom float64
}

func (c *Client) ListPlugins(ctx context.Context) (PluginList, error) {
	r, err := c.cl.ListPlugins(ctx, connect.NewRequest(&pb.ListPluginsRequest{}))
	if err != nil {
		return PluginList{}, err
	}
	out := make([]PluginInfo, len(r.Msg.Plugins))
	for i, p := range r.Msg.Plugins {
		out[i] = PluginInfo{
			UUID:           p.Uuid,
			Kind:           p.Kind,
			Label:          p.Label,
			Writable:       p.Writable,
			RootGridID:     p.RootGridId,
			ScratchGridID:  p.ScratchGridId,
			InstanceGridID: p.InstanceGridId,
			RootViewCx:     p.RootViewCx,
			RootViewCy:     p.RootViewCy,
			RootViewZoom:   p.RootViewZoom,
			InfoError:      p.InfoError,
		}
	}
	return PluginList{
		Plugins:          out,
		ShellsDisabled:   r.Msg.ShellsDisabled,
		ContentToken:     r.Msg.ContentToken,
		NodeUUID:         r.Msg.NodeUuid,
		NodeRootGridID:   r.Msg.NodeRootGridId,
		NodeRootViewCx:   r.Msg.NodeRootViewCx,
		NodeRootViewCy:   r.Msg.NodeRootViewCy,
		NodeRootViewZoom: r.Msg.NodeRootViewZoom,
	}, nil
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

// Create* are typed sugar over the single CreateTile RPC: each builds the
// kind-tagged CreateTileRequest (see conv.go) and sends it. The wire has one
// create; these keep call sites readable.
func (c *Client) CreateWell(ctx context.Context, req *CreateWellRequest) (*Tile, error) {
	return tileResp(c.cl.CreateTile(ctx, connect.NewRequest(CreateWellToProto(req))))
}

// CreateText makes the metadata row and, when req.Data is set, follows with
// the one content write (creation is metadata-only on the wire; this helper
// composes the two so Go callers keep a one-call shape). A failure between
// the two leaves an empty tile — visible and deletable, never silent.
func (c *Client) CreateText(ctx context.Context, req *CreateTextRequest) (*Tile, error) {
	t, err := tileResp(c.cl.CreateTile(ctx, connect.NewRequest(CreateTextToProto(req))))
	if err != nil || len(req.Data) == 0 {
		return t, err
	}
	return c.WriteContent(ctx, t.ID, t.Version, req.Data)
}
func (c *Client) CreateURL(ctx context.Context, req *CreateURLRequest) (*Tile, error) {
	return tileResp(c.cl.CreateTile(ctx, connect.NewRequest(CreateURLToProto(req))))
}
func (c *Client) CreateShell(ctx context.Context, req *CreateShellRequest) (*Tile, error) {
	return tileResp(c.cl.CreateTile(ctx, connect.NewRequest(CreateShellToProto(req))))
}

// CreatePane composes create-then-write exactly like CreateText when an
// initial layout rides req.Data.
func (c *Client) CreatePane(ctx context.Context, req *CreatePaneRequest) (*Tile, error) {
	t, err := tileResp(c.cl.CreateTile(ctx, connect.NewRequest(CreatePaneToProto(req))))
	if err != nil || len(req.Data) == 0 {
		return t, err
	}
	return c.WriteContent(ctx, t.ID, t.Version, req.Data)
}
func (c *Client) CreateLeafLink(ctx context.Context, req *CreateLeafLinkRequest) (*Tile, error) {
	return tileResp(c.cl.CreateTile(ctx, connect.NewRequest(CreateLeafLinkToProto(req))))
}

// Set* are typed sugar over the single SetTile RPC.
func (c *Client) SetWellView(ctx context.Context, req *SetWellViewRequest) (*Tile, error) {
	return tileResp(c.cl.SetTile(ctx, connect.NewRequest(SetWellViewToProto(req))))
}
func (c *Client) SetTextView(ctx context.Context, req *SetTextViewRequest) (*Tile, error) {
	return tileResp(c.cl.SetTile(ctx, connect.NewRequest(SetTextViewToProto(req))))
}
func (c *Client) SetShellPreview(ctx context.Context, req *SetShellPreviewRequest) (*Tile, error) {
	return tileResp(c.cl.SetTile(ctx, connect.NewRequest(SetShellPreviewToProto(req))))
}
func (c *Client) SetURLState(ctx context.Context, req *SetURLStateRequest) (*Tile, error) {
	return tileResp(c.cl.SetTile(ctx, connect.NewRequest(SetURLStateToProto(req))))
}
func (c *Client) AdoptChildGrid(ctx context.Context, req *AdoptChildGridRequest) (*Tile, error) {
	return tileResp(c.cl.SetTile(ctx, connect.NewRequest(AdoptChildGridToProto(req))))
}

// SetRootView persists the plugin root-grid framing. The server routes on
// root_grid_id; framing only — never bumps a content version.
func (c *Client) SetRootView(ctx context.Context, req *SetRootViewRequest) error {
	_, err := c.cl.SetRootView(ctx, connect.NewRequest(SetRootViewToProto(req)))
	return err
}

// SetRootViewToProto is the one wire conversion for the root-view write —
// shared by the ordinary call above and its unload beacon form.
func SetRootViewToProto(req *SetRootViewRequest) *pb.SetRootViewRequest {
	return &pb.SetRootViewRequest{
		RootGridId: req.RootGridID,
		Cx:         req.Cx,
		Cy:         req.Cy,
		Zoom:       req.Zoom,
	}
}

// ReadContent fetches a tile's content bytes — the one content read
// (2026-07-26 redesign). The stream is assembled here: chunk 1 carries the
// media type and the row version the bytes belong to (the save basis, paired
// with the bytes at the owner). A leaf link resolves to its target at the
// serving node, so callers never reimplement link semantics.
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

// writeContentChunkBytes bounds each upload message; the server reassembles
// and commits once, at clean close.
const writeContentChunkBytes = 256 * 1024

// WriteContent writes a tile's content bytes — the one content write:
// version-claimed, commit-at-close (a failure anywhere leaves the old value
// intact). data is the complete new value; chunking is a transport detail.
func (c *Client) WriteContent(ctx context.Context, tileID string, version int64, data []byte) (*Tile, error) {
	stream := c.cl.WriteContent(ctx)
	end := min(writeContentChunkBytes, len(data))
	if err := stream.Send(&pb.WriteContentRequest{TileId: tileID, Version: version, Data: data[:end]}); err != nil {
		_, cerr := stream.CloseAndReceive()
		if cerr != nil {
			return nil, cerr
		}
		return nil, err
	}
	for off := end; off < len(data); off += writeContentChunkBytes {
		e := min(off+writeContentChunkBytes, len(data))
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
	return TileFromProto(resp.Msg.Tile), nil
}

// PlaceTile is the single placement writeback: one verb owns
// (grid, x, y, w, h) — a move, a resize, or both in one write.
func (c *Client) PlaceTile(ctx context.Context, req *PlaceTileRequest) (*Tile, error) {
	return tileResp(c.cl.PlaceTile(ctx, connect.NewRequest(PlaceTileToProto(req))))
}

func (c *Client) CloneTile(ctx context.Context, req *CloneTileRequest) (*Tile, error) {
	return tileResp(c.cl.CloneTile(ctx, connect.NewRequest(CloneTileToProto(req))))
}
func (c *Client) ShellSessionAlive(ctx context.Context, req *ShellSessionAliveRequest) (*ShellSessionAliveResponse, error) {
	r, err := c.cl.ShellSessionAlive(ctx, connect.NewRequest(ShellSessionAliveToProto(req)))
	if err != nil {
		return nil, err
	}
	return ShellSessionAliveResponseFromProto(r.Msg), nil
}

// RenameTile is the versioned rename (2026-07-26 redesign: the absorbed
// SetTile rename arm) — a real user edit with an optimistic-concurrency
// claim; the server latches alt_user so automatic captures defer.
func (c *Client) RenameTile(ctx context.Context, tileID string, version int64, alt string) (*Tile, error) {
	return tileResp(c.cl.SetTile(ctx, connect.NewRequest(&pb.SetTileRequest{
		TileId: tileID, Version: version, Rename: alt,
	})))
}

// SetContentZoom persists a tile's content scale (framing; never bumps
// version) — the text/terminal font or page zoom (issue #82). Rides the
// absorbed SetTile content_zoom arm (2026-07-26 redesign).
func (c *Client) SetContentZoom(ctx context.Context, req *SetContentZoomRequest) (*Tile, error) {
	return tileResp(c.cl.SetTile(ctx, connect.NewRequest(&pb.SetTileRequest{
		TileId: req.TileID, Version: req.Version, ContentZoom: &req.ContentZoom,
	})))
}

// SetURLFrozen persists the user's standing freeze on a url tile (issue
// #237; framing, never bumps version). Rides the SetTile url_frozen arm.
func (c *Client) SetURLFrozen(ctx context.Context, req *SetURLFrozenRequest) (*Tile, error) {
	return tileResp(c.cl.SetTile(ctx, connect.NewRequest(&pb.SetTileRequest{
		TileId: req.TileID, Version: req.Version, UrlFrozen: &req.Frozen,
	})))
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
