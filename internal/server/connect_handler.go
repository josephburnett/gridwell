package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"

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
// (unprefixed) id, and the owning plugin uuid. A bare id (no "<uuid>/" prefix)
// is treated as belonging to the primary plugin.
func (h *connectHandler) route(id string) (client pb.GridwellClient, local, uuid string, err error) {
	uuid, local, ok := splitPluginID(id)
	if !ok {
		uuid, local = h.srv.primaryUUID, id
	}
	c, found := h.srv.pluginReg.Get(uuid)
	if !found {
		return nil, "", "", connect.NewError(connect.CodeNotFound, fmt.Errorf("no plugin %q", uuid))
	}
	return c, local, uuid, nil
}

// primaryClient resolves the primary localdb plugin (for id-less RPCs).
func (h *connectHandler) primaryClient() (pb.GridwellClient, string, error) {
	c, found := h.srv.pluginReg.Get(h.srv.primaryUUID)
	if !found {
		return nil, "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no primary plugin registered"))
	}
	return c, h.srv.primaryUUID, nil
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

func (h *connectHandler) Bootstrap(ctx context.Context, _ *connect.Request[pb.BootstrapRequest]) (*connect.Response[pb.BootstrapResponse], error) {
	c, uuid, err := h.primaryClient()
	if err != nil {
		return nil, err
	}
	resp, err := c.Bootstrap(ctx, &pb.BootstrapRequest{})
	if err != nil {
		return nil, asConnectError(err)
	}
	resp.RootGridId = qualifyID(uuid, resp.RootGridId)
	return connect.NewResponse(resp), nil
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

// GetBlob is addressed by an int64 blob id, which carries no plugin namespace,
// so it resolves against the primary localdb. Tiles owned by other plugins
// serve their body via GetTileContent (routable by tile id) instead.
func (h *connectHandler) GetBlob(ctx context.Context, req *connect.Request[pb.GetBlobRequest]) (*connect.Response[pb.GetBlobResponse], error) {
	c, _, err := h.primaryClient()
	if err != nil {
		return nil, err
	}
	resp, err := c.GetBlob(ctx, req.Msg)
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(resp), nil
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

func (h *connectHandler) CreateFileWell(ctx context.Context, req *connect.Request[pb.CreateFileWellRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	if strings.TrimSpace(m.FsPath) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("fs_path required"))
	}
	return h.createExitWell(ctx, "fs", map[string]string{"path": m.FsPath}, m.Path, m.GridId, m.X, m.Y, m.W, m.H)
}
func (h *connectHandler) CreateProcessWell(ctx context.Context, req *connect.Request[pb.CreateProcessWellRequest]) (*connect.Response[pb.TileResponse], error) {
	m := req.Msg
	if m.Pid <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("pid must be positive"))
	}
	return h.createExitWell(ctx, "proc", map[string]string{"pid": strconv.FormatInt(m.Pid, 10)}, m.Path, m.GridId, m.X, m.Y, m.W, m.H)
}

// createExitWell attaches the named source plugin (fs / proc) for config, then
// asks the destination plugin (the one that owns gridID — wherever the user
// dropped the well) to create a well tile whose child_grid_id is the qualified
// "<src-uuid>/<grid-id>" the source returned. Pure cross-plugin orchestration:
// no store touch, no localdb special-casing.
func (h *connectHandler) createExitWell(ctx context.Context, kind string, config map[string]string, path *pb.Path, gridID string, x, y, w, ht int64) (*connect.Response[pb.TileResponse], error) {
	srcUUID, srcClient, ok := h.srv.pluginReg.FirstByKind(kind)
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no %s plugin configured", kind))
	}
	att, err := srcClient.Attach(ctx, &pb.AttachRequest{Config: config})
	if err != nil {
		return nil, asConnectError(err)
	}
	childGridID := qualifyID(srcUUID, att.RootGridId)

	dstClient, dstLocal, dstUUID, err := h.route(gridID)
	if err != nil {
		return nil, err
	}
	resp, err := dstClient.CreateWell(ctx, &pb.CreateWellRequest{
		Path:        localPathFor(path, dstUUID),
		GridId:      dstLocal,
		X:           x,
		Y:           y,
		W:           w,
		H:           ht,
		ChildGridId: childGridID,
		Label:       att.Label,
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

// SetRootView frames the app root, which lives in the primary plugin.
func (h *connectHandler) SetRootView(ctx context.Context, req *connect.Request[pb.SetRootViewRequest]) (*connect.Response[pb.SetRootViewResponse], error) {
	c, _, err := h.primaryClient()
	if err != nil {
		return nil, err
	}
	resp, err := c.SetRootView(ctx, req.Msg)
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(resp), nil
}

func (h *connectHandler) DeleteTile(ctx context.Context, req *connect.Request[pb.DeleteTileRequest]) (*connect.Response[pb.DeleteTileResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.TileId)
	if err != nil {
		return nil, err
	}
	m.TileId = local
	m.Path = localPathFor(m.Path, uuid)
	if _, err := c.DeleteTile(ctx, m); err != nil {
		return nil, asConnectError(err)
	}
	// Shell PTYs only back tiles in the primary localdb; reap the tmux session
	// once the row is gone so it can't survive a restart and leak.
	if uuid == h.srv.primaryUUID && h.srv.shellStreamer != nil {
		h.reapShellIfGone(ctx, local)
	}
	return connect.NewResponse(&pb.DeleteTileResponse{}), nil
}

// reapShellIfGone kills the tmux session for a just-deleted primary tile when
// that exact id is truly gone (a cloned shell is an independent copy with its
// own id and no session, so deleting a copy never touches the original's PTY).
// Fire-and-forget; a missed kill is caught by the startup orphan-cleanup pass.
func (h *connectHandler) reapShellIfGone(ctx context.Context, localTileID string) {
	c, ok := h.srv.rootClient()
	if !ok {
		return
	}
	resp, err := c.Probe(ctx, &pb.ProbeRequest{TileId: localTileID})
	switch {
	case err != nil:
		log.Printf("[shellstream] kill-on-delete probe tile=%s err=%v", localTileID, err)
	case resp.Presence != pb.ProbeResponse_PRESENCE_PRESENT:
		if tileIDInt, perr := strconv.ParseInt(localTileID, 10, 64); perr == nil {
			if err := h.srv.shellStreamer.Kill(tileIDInt); err != nil {
				log.Printf("[shellstream] kill-on-delete tile=%s err=%v", localTileID, err)
			}
		}
	}
}

// ShellSessionAlive answers the wasm's per-descent probe by asking the streamer
// (the tmux controller) whether the tile's session exists. Shell tiles live in
// the primary localdb, so the id is stripped of the primary prefix. An infra
// error is reported as not-alive rather than a Connect error — the wasm only
// cares whether the refresh button should hide.
func (h *connectHandler) ShellSessionAlive(_ context.Context, req *connect.Request[pb.ShellSessionAliveRequest]) (*connect.Response[pb.ShellSessionAliveResponse], error) {
	if h.srv.shellStreamer == nil {
		return connect.NewResponse(&pb.ShellSessionAliveResponse{Alive: false}), nil
	}
	tileID := stripUUID(req.Msg.TileId, h.srv.primaryUUID)
	alive := false
	if tileIDInt, err := strconv.ParseInt(tileID, 10, 64); err == nil {
		alive, _ = h.srv.shellStreamer.HasSession(tileIDInt)
	}
	return connect.NewResponse(&pb.ShellSessionAliveResponse{Alive: alive}), nil
}

// Subscribe proxies the primary plugin's change-event stream to the client,
// re-qualifying each event's ids with the primary uuid. (Mounted plugins are
// polled via GetGrid; only the primary streams live events for now.)
func (h *connectHandler) Subscribe(ctx context.Context, _ *connect.Request[pb.SubscribeRequest], stream *connect.ServerStream[pb.Event]) error {
	c, uuid, err := h.primaryClient()
	if err != nil {
		return err
	}
	ps, err := c.Subscribe(ctx, &pb.SubscribeRequest{})
	if err != nil {
		return asConnectError(err)
	}
	for {
		ev, err := ps.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if err := stream.Send(qualifyEvent(uuid, ev)); err != nil {
			return err
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
