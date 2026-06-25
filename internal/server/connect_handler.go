package server

import (
	"context"
	"errors"
	"fmt"
	"log"

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
// way in and re-applied to the response on the way out. Id-less RPCs
// (Bootstrap, SetRootView, Subscribe) target the primary localdb plugin.
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

// Bootstrap returns nothing meaningful in the rootless model: startup is empty
// panes. The client builds the launcher from ListPlugins and enters a plugin to
// get a grid. Kept so existing clients get a clean empty response.
func (h *connectHandler) Bootstrap(_ context.Context, _ *connect.Request[pb.BootstrapRequest]) (*connect.Response[pb.BootstrapResponse], error) {
	return connect.NewResponse(&pb.BootstrapResponse{}), nil
}

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

// GetBlob is addressed by a bare int64 blob id, which carries no plugin
// namespace, so it is not routable in the rootless model — every plugin has its
// own blob id space. Clients fetch text bodies via GetTileContent (routable by
// tile id) instead.
func (h *connectHandler) GetBlob(_ context.Context, _ *connect.Request[pb.GetBlobRequest]) (*connect.Response[pb.GetBlobResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("GetBlob is not routable without a root; use GetTileContent"))
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
		var rootGridID string
		if c, ok := h.srv.pluginReg.Get(p.UUID); ok {
			if label == "" {
				if info, err := c.Info(ctx, &pb.InfoRequest{}); err == nil && info.DisplayName != "" {
					label = info.DisplayName
				}
			}
			// Attach with default config to learn the plugin's root grid id
			// (fs uses its configured root, proc pid 1, localdb its root), so
			// the client can click-enter straight into it. getOrCreate semantics
			// make this idempotent.
			if att, err := c.Attach(ctx, &pb.AttachRequest{Config: map[string]string{}}); err == nil && att.RootGridId != "" {
				rootGridID = qualifyID(p.UUID, att.RootGridId)
			}
		}
		if label == "" {
			label = p.Kind
		}
		out = append(out, &pb.PluginInfo{
			Uuid:       p.UUID,
			Kind:       p.Kind,
			Label:      label,
			Writable:   p.Kind == "localdb",
			RootGridId: rootGridID,
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

func (h *connectHandler) CreateWell(ctx context.Context, req *connect.Request[pb.CreateWellRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.GridId)
	if err != nil {
		return nil, err
	}
	m.GridId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.CreateWell(ctx, m)
	return pluginTileResp(uuid, resp, err)
}
func (h *connectHandler) CreateText(ctx context.Context, req *connect.Request[pb.CreateTextRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.GridId)
	if err != nil {
		return nil, err
	}
	m.GridId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.CreateText(ctx, m)
	return pluginTileResp(uuid, resp, err)
}
func (h *connectHandler) CreateURL(ctx context.Context, req *connect.Request[pb.CreateURLRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.GridId)
	if err != nil {
		return nil, err
	}
	m.GridId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.CreateURL(ctx, m)
	return pluginTileResp(uuid, resp, err)
}
func (h *connectHandler) CreateShell(ctx context.Context, req *connect.Request[pb.CreateShellRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.GridId)
	if err != nil {
		return nil, err
	}
	m.GridId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.CreateShell(ctx, m)
	return pluginTileResp(uuid, resp, err)
}

// Mount attaches plugin_uuid (default config) and drops an exit well in the
// destination grid pointing at the attached root — the drag-a-plugin gesture.
func (h *connectHandler) Mount(ctx context.Context, req *connect.Request[pb.MountRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	srcClient, found := h.srv.pluginReg.Get(m.PluginUuid)
	if !found {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no plugin %q", m.PluginUuid))
	}
	att, err := srcClient.Attach(ctx, &pb.AttachRequest{Config: map[string]string{}})
	if err != nil {
		return nil, asConnectError(err)
	}
	childGridID := qualifyID(m.PluginUuid, att.RootGridId)

	// The mounted well's label is the server.yaml display name — exactly the
	// label the + menu showed for this plugin — so menu and dropped tile
	// agree. Fall back to the plugin's Attach label only when unconfigured.
	label := h.srv.pluginReg.Label(m.PluginUuid)
	if label == "" {
		label = att.Label
	}

	dstClient, dstLocal, dstUUID, err := h.route(m.GridId)
	if err != nil {
		return nil, err
	}
	resp, err := dstClient.CreateWell(ctx, &pb.CreateWellRequest{
		Path:        localPathFor(m.Path, dstUUID),
		GridId:      dstLocal,
		X:           m.X,
		Y:           m.Y,
		W:           m.W,
		H:           m.H,
		ChildGridId: childGridID,
		Label:       label,
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
func (h *connectHandler) SetWellView(ctx context.Context, req *connect.Request[pb.SetWellViewRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.SetWellView(ctx, m)
	return pluginTileResp(uuid, resp, err)
}
func (h *connectHandler) SetTextView(ctx context.Context, req *connect.Request[pb.SetTextViewRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.SetTextView(ctx, m)
	return pluginTileResp(uuid, resp, err)
}
func (h *connectHandler) SetShellPreview(ctx context.Context, req *connect.Request[pb.SetShellPreviewRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.SetShellPreview(ctx, m)
	return pluginTileResp(uuid, resp, err)
}
func (h *connectHandler) SetURLState(ctx context.Context, req *connect.Request[pb.SetURLStateRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.Path = localPathFor(m.Path, uuid)
	resp, err := c.SetURLState(ctx, m)
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

// SetRootView is a no-op in the rootless model: there is no app root, and a
// pane's viewport is persisted in the URL, not server-side.
func (h *connectHandler) SetRootView(_ context.Context, _ *connect.Request[pb.SetRootViewRequest]) (*connect.Response[pb.SetRootViewResponse], error) {
	return connect.NewResponse(&pb.SetRootViewResponse{}), nil
}

func (h *connectHandler) DeleteTile(ctx context.Context, req *connect.Request[pb.DeleteTileRequest]) (*connect.Response[pb.DeleteTileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	qualifiedID := m.TileId
	m.TileId = local
	m.Path = localPathFor(m.Path, uuid)
	if _, err := c.DeleteTile(ctx, m); err != nil {
		return nil, asConnectError(err)
	}
	// Reap the tmux session keyed by the qualified id once the row is gone, so
	// it can't survive a restart and leak. Harmless for non-shell tiles (no
	// session → Kill is a no-op).
	if h.srv.shellStreamer != nil {
		h.reapShellIfGone(ctx, qualifiedID)
	}
	return connect.NewResponse(&pb.DeleteTileResponse{}), nil
}

// reapShellIfGone kills the tmux session for a just-deleted tile (keyed by its
// qualified id) when that exact id is truly gone — a cloned shell is an
// independent copy with its own id and no session, so deleting a copy never
// touches the original's PTY. Fire-and-forget; a missed kill is caught by the
// startup orphan-cleanup pass.
func (h *connectHandler) reapShellIfGone(ctx context.Context, qualifiedID string) {
	client, local, ok := h.srv.clientForID(qualifiedID)
	if !ok {
		return
	}
	resp, err := client.Probe(ctx, &pb.ProbeRequest{TileId: local})
	switch {
	case err != nil:
		log.Printf("[shellstream] kill-on-delete probe tile=%s err=%v", qualifiedID, err)
	case resp.Presence != pb.ProbeResponse_PRESENCE_PRESENT:
		if err := h.srv.shellStreamer.Kill(qualifiedID); err != nil {
			log.Printf("[shellstream] kill-on-delete tile=%s err=%v", qualifiedID, err)
		}
	}
}

// ShellSessionAlive answers the wasm's per-descent probe by asking the streamer
// (the tmux controller) whether the tile's session exists. The session is keyed
// by the qualified tile id. An infra error is reported as not-alive rather than
// a Connect error — the wasm only cares whether the refresh button should hide.
func (h *connectHandler) ShellSessionAlive(_ context.Context, req *connect.Request[pb.ShellSessionAliveRequest]) (*connect.Response[pb.ShellSessionAliveResponse], error) {
	if h.srv.shellStreamer == nil {
		return connect.NewResponse(&pb.ShellSessionAliveResponse{Alive: false}), nil
	}
	alive, _ := h.srv.shellStreamer.HasSession(req.Msg.TileId)
	return connect.NewResponse(&pb.ShellSessionAliveResponse{Alive: alive}), nil
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
