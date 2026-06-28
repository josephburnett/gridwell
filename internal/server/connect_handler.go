package server

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
)

// connectHandler implements gridwellv1connect.GridwellHandler as a thin router
// over the plugin registry. It holds no Gridwell state: every request is
// forwarded to the plugin that owns the id it carries (the primary localdb,
// a mounted localdb, fs, proc, …), with the plugin-uuid prefix stripped on the
// way in and re-applied to the response on the way out. Subscribe fans in every
// localdb plugin's event stream; ShellSessionAlive routes to the owning plugin.
type connectHandler struct {
	gridwellv1connect.UnimplementedGridwellHandler
	srv *Server
}

func newConnectHandler(srv *Server) *connectHandler {
	return &connectHandler{srv: srv}
}

// ── routing ────────────────────────────────────────────────────────────────────

// route resolves the plugin that owns id, returning its client, the local
// (unprefixed) id, and the owning plugin uuid. Ids are always qualified in the
// rootless model (there is no privileged plugin a bare id could fall back to).
func (h *connectHandler) route(id string) (client pb.GridwellClient, local, uuid string, err error) {
	uuid, local, ok := splitPluginID(id)
	if !ok {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unqualified id %q", id))
	}
	c, found := h.srv.pluginReg.Get(uuid)
	if !found {
		return nil, "", "", connect.NewError(connect.CodeNotFound, fmt.Errorf("no plugin %q", uuid))
	}
	return c, local, uuid, nil
}

// stripUUID removes a specific plugin uuid prefix from an id, leaving bare and
// foreign-prefixed ids untouched (so a cross-plugin reference stays qualified
// and the target plugin rejects it rather than misresolving it locally).
func stripUUID(id, uuid string) string {
	if u, l, ok := splitPluginID(id); ok && u == uuid {
		return l
	}
	return id
}

// localPathFor returns the descent path local to the target plugin. A path can
// cross plugin boundaries (`…/<p1>/<t>/<p2>/<u>`); only the trailing run of
// segments owned by the target plugin form its COW spine, so segments above the
// last boundary (owned by other plugins) are dropped and the rest are stripped
// of the uuid prefix.
func localPathFor(p *pb.Path, uuid string) *pb.Path {
	if p == nil {
		return nil
	}
	start := len(p.WellIds)
	for start > 0 {
		if u, _, ok := splitPluginID(p.WellIds[start-1]); !ok || u != uuid {
			break // a bare or foreign-plugin segment marks the boundary
		}
		start--
	}
	out := make([]string, 0, len(p.WellIds)-start)
	for _, id := range p.WellIds[start:] {
		out = append(out, stripUUID(id, uuid))
	}
	return &pb.Path{WellIds: out}
}

// pluginTileResp qualifies a plugin's TileResponse for the client.
func pluginTileResp(uuid string, resp *pb.TileResponse, err error) (*connect.Response[pb.TileResponse], error) {
	if err != nil {
		return nil, asConnectError(err)
	}
	t := resp.GetTile()
	if t != nil {
		t = qualifyTiles(uuid, []*pb.Tile{t})[0]
	}
	return connect.NewResponse(&pb.TileResponse{Tile: t}), nil
}

// qualifyEvent re-applies a plugin's uuid to the ids in a change event.
func qualifyEvent(uuid string, ev *pb.Event) *pb.Event {
	switch p := ev.Payload.(type) {
	case *pb.Event_GridChanged:
		return &pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{
			GridId: qualifyID(uuid, p.GridChanged.GridId),
		}}}
	case *pb.Event_TileChanged:
		return &pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{
			Tile: qualifyTiles(uuid, []*pb.Tile{p.TileChanged.Tile})[0],
		}}}
	case *pb.Event_TileRemoved:
		return &pb.Event{Payload: &pb.Event_TileRemoved{TileRemoved: &pb.TileRemoved{
			GridId: qualifyID(uuid, p.TileRemoved.GridId),
			TileId: qualifyID(uuid, p.TileRemoved.TileId),
		}}}
	}
	return ev
}

// ── reads ──────────────────────────────────────────────────────────────────────

func (h *connectHandler) GetGrid(ctx context.Context, req *connect.Request[pb.GetGridRequest]) (*connect.Response[pb.GetGridResponse], error) {
	c, local, uuid, err := h.route(req.Msg.GridId)
	if err != nil {
		return nil, err
	}
	resp, err := c.GetGrid(ctx, &pb.GetGridRequest{GridId: local})
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(&pb.GetGridResponse{
		Grid:  qualifyGrid(uuid, resp.Grid),
		Tiles: qualifyTiles(uuid, resp.Tiles),
	}), nil
}

func (h *connectHandler) GetTilePreview(ctx context.Context, req *connect.Request[pb.GetTilePreviewRequest]) (*connect.Response[pb.GetTilePreviewResponse], error) {
	c, local, _, err := h.route(req.Msg.TileId)
	if err != nil {
		return nil, err
	}
	resp, err := c.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: local})
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(resp), nil
}

func (h *connectHandler) GetTileContent(ctx context.Context, req *connect.Request[pb.GetTileContentRequest]) (*connect.Response[pb.GetTileContentResponse], error) {
	c, local, _, err := h.route(req.Msg.TileId)
	if err != nil {
		return nil, err
	}
	resp, err := c.GetTileContent(ctx, &pb.GetTileContentRequest{TileId: local})
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(resp), nil
}

// ListPlugins enumerates the configured plugins in config order so the client
// can build the launcher / + menu. label comes from each plugin's Info;
// writable (accepts new tiles) is derived from kind — only localdb grids can
// hold created primitives.
func (h *connectHandler) ListPlugins(ctx context.Context, _ *connect.Request[pb.ListPluginsRequest]) (*connect.Response[pb.ListPluginsResponse], error) {
	var out []*pb.PluginInfo
	for _, p := range h.srv.pluginReg.Ordered() {
		// The server.yaml display name is authoritative (the menu and a
		// mounted well must agree). Fall back to the plugin's own Info name,
		// then its kind, only when no name was configured.
		label := h.srv.pluginReg.Label(p.UUID)
		var rootGridID, scratchGridID string
		if c, ok := h.srv.pluginReg.Get(p.UUID); ok {
			// Info is the whole handshake: it carries the plugin's default root
			// grid id (fs its configured root, proc pid 1, localdb its root) so
			// the client can click-enter straight into it, plus a fallback label.
			if info, err := c.Info(ctx, &pb.InfoRequest{}); err == nil {
				if info.RootGridId != "" {
					rootGridID = qualifyID(p.UUID, info.RootGridId)
				}
				// Qualified scratch grid id (ephemeral-url target); empty for
				// plugins that don't support ephemeral visits.
				if info.ScratchGridId != "" {
					scratchGridID = qualifyID(p.UUID, info.ScratchGridId)
				}
				if label == "" {
					label = info.DisplayName
				}
			}
		}
		if label == "" {
			label = p.Kind
		}
		out = append(out, &pb.PluginInfo{
			Uuid:          p.UUID,
			Kind:          p.Kind,
			Label:         label,
			Writable:      p.Kind == "localdb",
			RootGridId:    rootGridID,
			ScratchGridId: scratchGridID,
		})
	}
	return connect.NewResponse(&pb.ListPluginsResponse{Plugins: out}), nil
}

func (h *connectHandler) GetTile(ctx context.Context, req *connect.Request[pb.GetTileRequest]) (*connect.Response[pb.TileResponse], error) {
	c, local, uuid, err := h.route(req.Msg.TileId)
	if err != nil {
		return nil, err
	}
	resp, err := c.GetTile(ctx, &pb.GetTileRequest{TileId: local})
	return pluginTileResp(uuid, resp, err)
}
func (h *connectHandler) SetTileAlt(ctx context.Context, req *connect.Request[pb.SetTileAltRequest]) (*connect.Response[pb.TileResponse], error) {
	c, local, uuid, err := h.route(req.Msg.TileId)
	if err != nil {
		return nil, err
	}
	resp, err := c.SetTileAlt(ctx, &pb.SetTileAltRequest{TileId: local, Alt: req.Msg.Alt})
	return pluginTileResp(uuid, resp, err)
}

// ── creates ──────────────────────────────────────────────────────────────────

// CreateTile is the single create router: resolve the owning plugin by
// destination grid, localize grid_id + path, forward. tile.kind selects the
// primitive; tile.child_grid_id (an exit well's cross-plugin reference) stays
// qualified.
func (h *connectHandler) CreateTile(ctx context.Context, req *connect.Request[pb.CreateTileRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.GridId)
	if err != nil {
		return nil, err
	}
	m.GridId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.CreateTile(ctx, m)
	return pluginTileResp(uuid, resp, err)
}

// Mount drops an exit well in the destination grid pointing at plugin_uuid's
// default root (learned from its Info) — the drag-a-plugin gesture.
func (h *connectHandler) Mount(ctx context.Context, req *connect.Request[pb.MountRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	srcClient, found := h.srv.pluginReg.Get(m.PluginUuid)
	if !found {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no plugin %q", m.PluginUuid))
	}
	info, err := srcClient.Info(ctx, &pb.InfoRequest{})
	if err != nil {
		return nil, asConnectError(err)
	}
	if info.RootGridId == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("plugin %q has no root grid", m.PluginUuid))
	}
	childGridID := qualifyID(m.PluginUuid, info.RootGridId)

	// The mounted well's label is the server.yaml display name — exactly the
	// label the + menu showed for this plugin — so menu and dropped tile
	// agree. Fall back to the plugin's Info name only when unconfigured.
	label := h.srv.pluginReg.Label(m.PluginUuid)
	if label == "" {
		label = info.DisplayName
	}

	dstClient, dstLocal, dstUUID, err := h.route(m.GridId)
	if err != nil {
		return nil, err
	}
	// An exit well is a well tile whose child_grid_id is a cross-plugin
	// reference; alt_text is its label.
	resp, err := dstClient.CreateTile(ctx, &pb.CreateTileRequest{
		Path:   localPathFor(m.Path, dstUUID),
		GridId: dstLocal,
		Tile: &pb.Tile{
			Kind: "well", X: m.X, Y: m.Y, W: m.W, H: m.H,
			ChildGridId: childGridID, AltText: label,
		},
	})
	return pluginTileResp(dstUUID, resp, err)
}

// ── mutations ──────────────────────────────────────────────────────────────────

func (h *connectHandler) MoveTile(ctx context.Context, req *connect.Request[pb.MoveTileRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.DestGridId = stripUUID(m.DestGridId, uuid)
	m.Path = localPathFor(m.Path, uuid)
	m.DestPath = localPathFor(m.DestPath, uuid)
	resp, err := c.MoveTile(ctx, m)
	return pluginTileResp(uuid, resp, err)
}
func (h *connectHandler) CloneTile(ctx context.Context, req *connect.Request[pb.CloneTileRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.DestGridId = stripUUID(m.DestGridId, uuid)
	m.Path = localPathFor(m.Path, uuid)
	m.DestPath = localPathFor(m.DestPath, uuid)
	resp, err := c.CloneTile(ctx, m)
	return pluginTileResp(uuid, resp, err)
}
func (h *connectHandler) ResizeTile(ctx context.Context, req *connect.Request[pb.ResizeTileRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.ResizeTile(ctx, m)
	return pluginTileResp(uuid, resp, err)
}
// SetTile is the single framing/preview writeback router. The owning plugin
// dispatches on the target tile's kind to the one operation that kind supports
// (well/text framing → no version bump; url/shell preview → bump).
func (h *connectHandler) SetTile(ctx context.Context, req *connect.Request[pb.SetTileRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.SetTile(ctx, m)
	return pluginTileResp(uuid, resp, err)
}
func (h *connectHandler) UpdateText(ctx context.Context, req *connect.Request[pb.UpdateTextRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.UpdateText(ctx, m)
	return pluginTileResp(uuid, resp, err)
}

func (h *connectHandler) DeleteTile(ctx context.Context, req *connect.Request[pb.DeleteTileRequest]) (*connect.Response[pb.DeleteTileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.Path = localPathFor(m.Path, uuid)
	// The owning plugin reaps the tile's shell session (if any) as part of
	// DeleteTile — the PTY lives behind the interface now.
	if _, err := c.DeleteTile(ctx, m); err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(&pb.DeleteTileResponse{}), nil
}

// ShellSessionAlive routes the wasm's per-descent probe to the owning plugin,
// which holds the PTY and answers whether the tile's session is alive. An infra
// error is reported as not-alive rather than a Connect error — the wasm only
// cares whether the refresh button should hide.
func (h *connectHandler) ShellSessionAlive(ctx context.Context, req *connect.Request[pb.ShellSessionAliveRequest]) (*connect.Response[pb.ShellSessionAliveResponse], error) {
	c, local, _, err := h.route(req.Msg.TileId)
	if err != nil {
		return connect.NewResponse(&pb.ShellSessionAliveResponse{Alive: false}), nil
	}
	resp, err := c.ShellSessionAlive(ctx, &pb.ShellSessionAliveRequest{TileId: local})
	if err != nil {
		return connect.NewResponse(&pb.ShellSessionAliveResponse{Alive: false}), nil
	}
	return connect.NewResponse(resp), nil
}

// Subscribe fans every localdb plugin's change-event stream into the client's
// stream, re-qualifying each event's ids with the emitting plugin's uuid.
// (fs/proc are polled via GetGrid; only localdb plugins emit events.) With no
// single root, a client subscribes to the whole federation at once.
func (h *connectHandler) Subscribe(ctx context.Context, _ *connect.Request[pb.SubscribeRequest], stream *connect.ServerStream[pb.Event]) error {
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events := make(chan *pb.Event, 64)
	for _, p := range h.srv.pluginReg.Ordered() {
		if p.Kind != "localdb" {
			continue
		}
		c, ok := h.srv.pluginReg.Get(p.UUID)
		if !ok {
			continue
		}
		go func(uuid string, client pb.GridwellClient) {
			ps, err := client.Subscribe(subCtx, &pb.SubscribeRequest{})
			if err != nil {
				return
			}
			for {
				ev, err := ps.Recv()
				if err != nil {
					return // EOF, cancel, or transport error: this plugin's stream ends
				}
				select {
				case events <- qualifyEvent(uuid, ev):
				case <-subCtx.Done():
					return
				}
			}
		}(p.UUID, c)
	}

	for {
		select {
		case ev := <-events:
			if err := stream.Send(ev); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// asConnectError maps an error returned from a plugin (or, on the borrowed
// store paths, a raw store sentinel) to a Connect status code. Plugin errors
// arrive as gRPC status errors — the plugins translate store sentinels into
// codes (see localdb.errToStatus) so NotFound / InvalidArgument / overlap and
// version conflicts survive the routing hop; a non-gRPC error falls through to
// the same classifyStoreError categorization used by the raw-HTTP endpoints.
func asConnectError(err error) error {
	if err == nil {
		return nil
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case gcodes.NotFound:
			return connect.NewError(connect.CodeNotFound, errors.New(st.Message()))
		case gcodes.InvalidArgument:
			return connect.NewError(connect.CodeInvalidArgument, errors.New(st.Message()))
		case gcodes.FailedPrecondition:
			return connect.NewError(connect.CodeFailedPrecondition, errors.New(st.Message()))
		default:
			return connect.NewError(connect.CodeInternal, errors.New(st.Message()))
		}
	}
	switch classifyStoreError(err) {
	case classNotFound:
		return connect.NewError(connect.CodeNotFound, err)
	case classInvalidArgument:
		return connect.NewError(connect.CodeInvalidArgument, err)
	case classConflict:
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
