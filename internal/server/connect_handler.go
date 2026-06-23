package server

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// connectHandler implements gridwellv1connect.GridwellHandler. It is the
// thin proto↔rpc bridge: every method converts the wire request into a
// Go rpc.* value, calls the store, and packs the result into a wire
// response. The Go rpc types remain the internal source of truth.
type connectHandler struct {
	gridwellv1connect.UnimplementedGridwellHandler
	srv *Server
}

func newConnectHandler(srv *Server) *connectHandler {
	return &connectHandler{srv: srv}
}

// ── ID translation helpers ────────────────────────────────────────────────────

// localID strips the localdb UUID prefix from an incoming ID so the store
// always receives bare decimal strings. A bare ID (no "/") is returned
// unchanged. An ID with a different plugin's UUID is also returned unchanged
// (the caller is responsible for routing to the right plugin first).
func (h *connectHandler) localID(id string) string {
	if h.srv.localdbUUID == "" || id == "" {
		return id
	}
	if uuid, local, ok := splitPluginID(id); ok && uuid == h.srv.localdbUUID {
		return local
	}
	return id
}

// localPath strips localdb UUID prefixes from every WellID in a Path.
func (h *connectHandler) localPath(p rpc.Path) rpc.Path {
	if len(p.WellIDs) == 0 {
		return p
	}
	ids := make([]string, len(p.WellIDs))
	for i, id := range p.WellIDs {
		ids[i] = h.localID(id)
	}
	return rpc.Path{WellIDs: ids}
}

// qualifyLocaldbTile converts a store Tile to its proto form and qualifies
// all IDs with the localdb UUID (when set).
func (h *connectHandler) qualifyLocaldbTile(t *rpc.Tile) *pb.Tile {
	pt := rpc.TileToProto(t)
	if pt == nil || h.srv.localdbUUID == "" {
		return pt
	}
	return qualifyTiles(h.srv.localdbUUID, []*pb.Tile{pt})[0]
}

// tileRespQ packs a store tile into a TileResponse with qualified IDs.
func (h *connectHandler) tileRespQ(t *rpc.Tile, err error) (*connect.Response[pb.TileResponse], error) {
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(&pb.TileResponse{Tile: h.qualifyLocaldbTile(t)}), nil
}

// qualifyLocaldbEvent rewrites all grid/tile IDs in a store event.
func (h *connectHandler) qualifyLocaldbEvent(e rpc.Event) *pb.Event {
	uuid := h.srv.localdbUUID
	if uuid == "" {
		return rpc.EventToProto(e)
	}
	switch e.Kind {
	case rpc.EventGridChanged:
		return &pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{
			GridId: qualifyID(uuid, e.GridChanged.GridID),
		}}}
	case rpc.EventTileChanged:
		pt := rpc.TileToProto(&e.TileChanged.Tile)
		qualified := qualifyTiles(uuid, []*pb.Tile{pt})
		return &pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: qualified[0]}}}
	case rpc.EventTileRemoved:
		return &pb.Event{Payload: &pb.Event_TileRemoved{TileRemoved: &pb.TileRemoved{
			GridId: qualifyID(uuid, e.TileRemoved.GridID),
			TileId: qualifyID(uuid, e.TileRemoved.TileID),
		}}}
	}
	return rpc.EventToProto(e)
}

// ── plugin routing ────────────────────────────────────────────────────────────

// pluginRoute resolves the plugin that owns a qualified id. It returns the
// plugin's gRPC client, the local (unprefixed) id, and the plugin uuid when id
// is a qualified reference to a non-localdb plugin present in the registry.
// ok is false for bare ids, localdb-qualified ids, and unknown plugins — all
// of which route to the local store.
func (h *connectHandler) pluginRoute(id string) (client pb.GridwellClient, local, uuid string, ok bool) {
	if h.srv.pluginReg == nil {
		return nil, "", "", false
	}
	u, l, split := splitPluginID(id)
	if !split || u == h.srv.localdbUUID {
		return nil, "", "", false
	}
	c, found := h.srv.pluginReg.Get(u)
	if !found {
		return nil, "", "", false
	}
	return c, l, u, true
}

// stripUUID removes a specific plugin uuid prefix from an id, leaving bare and
// foreign-prefixed ids untouched.
func stripUUID(id, uuid string) string {
	if u, l, ok := splitPluginID(id); ok && u == uuid {
		return l
	}
	return id
}

// localPathFor strips a plugin uuid prefix from every segment of a path, so a
// path qualified for the client becomes plugin-local on the way in.
func localPathFor(p *pb.Path, uuid string) *pb.Path {
	if p == nil {
		return nil
	}
	ids := make([]string, len(p.WellIds))
	for i, id := range p.WellIds {
		ids[i] = stripUUID(id, uuid)
	}
	return &pb.Path{WellIds: ids}
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

// ── RPC handlers ──────────────────────────────────────────────────────────────

func (h *connectHandler) Bootstrap(ctx context.Context, _ *connect.Request[pb.BootstrapRequest]) (*connect.Response[pb.BootstrapResponse], error) {
	id, err := h.srv.store.RootGridID(ctx)
	if err != nil {
		return nil, asConnectError(err)
	}
	if h.srv.localdbUUID != "" {
		id = qualifyID(h.srv.localdbUUID, id)
	}
	cx, cy, zoom, err := h.srv.store.RootView(ctx)
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(&pb.BootstrapResponse{
		RootGridId: id,
		RootViewCx: cx,
		RootViewCy: cy,
		RootZoom:   zoom,
	}), nil
}

func (h *connectHandler) GetGrid(ctx context.Context, req *connect.Request[pb.GetGridRequest]) (*connect.Response[pb.GetGridResponse], error) {
	gridID := req.Msg.GridId
	if client, local, uuid, ok := h.pluginRoute(gridID); ok {
		resp, err := client.GetGrid(ctx, &pb.GetGridRequest{GridId: local})
		if err != nil {
			return nil, asConnectError(err)
		}
		return connect.NewResponse(&pb.GetGridResponse{
			Grid:  qualifyGrid(uuid, resp.Grid),
			Tiles: qualifyTiles(uuid, resp.Tiles),
		}), nil
	}
	localGridID := h.localID(gridID)
	r, err := h.srv.store.GetGrid(ctx, localGridID)
	if err != nil {
		return nil, asConnectError(err)
	}
	pg := rpc.GridToProto(&r.Grid)
	pts := rpc.TilesToProto(r.Tiles)
	if h.srv.localdbUUID != "" {
		pg = qualifyGrid(h.srv.localdbUUID, pg)
		pts = qualifyTiles(h.srv.localdbUUID, pts)
	}
	return connect.NewResponse(&pb.GetGridResponse{Grid: pg, Tiles: pts}), nil
}

func (h *connectHandler) GetBlob(ctx context.Context, req *connect.Request[pb.GetBlobRequest]) (*connect.Response[pb.GetBlobResponse], error) {
	data, err := h.srv.store.GetBlob(ctx, req.Msg.BlobId)
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(&pb.GetBlobResponse{Data: data}), nil
}

func (h *connectHandler) GetTilePreview(ctx context.Context, req *connect.Request[pb.GetTilePreviewRequest]) (*connect.Response[pb.GetTilePreviewResponse], error) {
	jpeg, err := h.srv.store.GetTilePreview(ctx, h.localID(req.Msg.TileId))
	if err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(&pb.GetTilePreviewResponse{Jpeg: jpeg}), nil
}

func (h *connectHandler) CreateWell(ctx context.Context, req *connect.Request[pb.CreateWellRequest]) (*connect.Response[pb.TileResponse], error) {
	r := rpc.CreateWellFromProto(req.Msg)
	r.GridID = h.localID(r.GridID)
	r.Path = h.localPath(r.Path)
	return h.tileRespQ(h.srv.store.CreateWell(ctx, r))
}
func (h *connectHandler) CreateText(ctx context.Context, req *connect.Request[pb.CreateTextRequest]) (*connect.Response[pb.TileResponse], error) {
	r := rpc.CreateTextFromProto(req.Msg)
	r.GridID = h.localID(r.GridID)
	r.Path = h.localPath(r.Path)
	return h.tileRespQ(h.srv.store.CreateText(ctx, r))
}
func (h *connectHandler) CreateURL(ctx context.Context, req *connect.Request[pb.CreateURLRequest]) (*connect.Response[pb.TileResponse], error) {
	r := rpc.CreateURLFromProto(req.Msg)
	r.GridID = h.localID(r.GridID)
	r.Path = h.localPath(r.Path)
	return h.tileRespQ(h.srv.store.CreateURL(ctx, r))
}
func (h *connectHandler) CreateFileWell(ctx context.Context, req *connect.Request[pb.CreateFileWellRequest]) (*connect.Response[pb.TileResponse], error) {
	r := rpc.CreateFileWellFromProto(req.Msg)
	if strings.TrimSpace(r.FSPath) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("fs_path required"))
	}
	return h.createExitWell(ctx, "fs", map[string]string{"path": r.FSPath}, r.Path, r.GridID, r.X, r.Y, r.W, r.H)
}
func (h *connectHandler) CreateProcessWell(ctx context.Context, req *connect.Request[pb.CreateProcessWellRequest]) (*connect.Response[pb.TileResponse], error) {
	r := rpc.CreateProcessWellFromProto(req.Msg)
	if r.PID <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("pid must be positive"))
	}
	return h.createExitWell(ctx, "proc", map[string]string{"pid": strconv.FormatInt(r.PID, 10)}, r.Path, r.GridID, r.X, r.Y, r.W, r.H)
}

// createExitWell attaches the named source plugin (fs / proc) for the given
// config, then drops a well tile in the local store whose child_grid_id is the
// qualified "<plugin-uuid>/<grid-id>" the plugin returned. The plugin owns the
// grid and supplies its own display label, so the cross-plugin reference and
// the well's name come straight from Attach — no source bookkeeping lives in
// the store.
func (h *connectHandler) createExitWell(ctx context.Context, kind string, config map[string]string, path rpc.Path, gridID string, x, y, w, ht int64) (*connect.Response[pb.TileResponse], error) {
	if h.srv.pluginReg == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no %s plugin configured", kind))
	}
	uuid, client, ok := h.srv.pluginReg.FirstByKind(kind)
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no %s plugin configured", kind))
	}
	att, err := client.Attach(ctx, &pb.AttachRequest{Config: config})
	if err != nil {
		return nil, asConnectError(err)
	}
	childGridID := qualifyID(uuid, att.RootGridId)
	tile, err := h.srv.store.CreateExitWell(ctx, h.localPath(path), h.localID(gridID), x, y, w, ht, childGridID, att.Label)
	return h.tileRespQ(tile, err)
}
func (h *connectHandler) CreateShell(ctx context.Context, req *connect.Request[pb.CreateShellRequest]) (*connect.Response[pb.TileResponse], error) {
	r := rpc.CreateShellFromProto(req.Msg)
	r.GridID = h.localID(r.GridID)
	r.Path = h.localPath(r.Path)
	return h.tileRespQ(h.srv.store.CreateShell(ctx, r))
}

func (h *connectHandler) MoveTile(ctx context.Context, req *connect.Request[pb.MoveTileRequest]) (*connect.Response[pb.TileResponse], error) {
	if client, local, uuid, ok := h.pluginRoute(req.Msg.TileId); ok {
		resp, err := client.MoveTile(ctx, &pb.MoveTileRequest{
			Path:       localPathFor(req.Msg.Path, uuid),
			TileId:     local,
			Version:    req.Msg.Version,
			DestGridId: stripUUID(req.Msg.DestGridId, uuid),
			DestPath:   localPathFor(req.Msg.DestPath, uuid),
			X:          req.Msg.X,
			Y:          req.Msg.Y,
		})
		return pluginTileResp(uuid, resp, err)
	}
	r := rpc.MoveTileFromProto(req.Msg)
	r.TileID = h.localID(r.TileID)
	r.DestGridID = h.localID(r.DestGridID)
	r.Path = h.localPath(r.Path)
	r.DestPath = h.localPath(r.DestPath)
	return h.tileRespQ(h.srv.store.MoveTile(ctx, r))
}
func (h *connectHandler) CloneTile(ctx context.Context, req *connect.Request[pb.CloneTileRequest]) (*connect.Response[pb.TileResponse], error) {
	r := rpc.CloneTileFromProto(req.Msg)
	r.TileID = h.localID(r.TileID)
	r.DestGridID = h.localID(r.DestGridID)
	r.Path = h.localPath(r.Path)
	r.DestPath = h.localPath(r.DestPath)
	return h.tileRespQ(h.srv.store.CloneTile(ctx, r))
}
func (h *connectHandler) ResizeTile(ctx context.Context, req *connect.Request[pb.ResizeTileRequest]) (*connect.Response[pb.TileResponse], error) {
	if client, local, uuid, ok := h.pluginRoute(req.Msg.TileId); ok {
		resp, err := client.ResizeTile(ctx, &pb.ResizeTileRequest{
			Path:    localPathFor(req.Msg.Path, uuid),
			TileId:  local,
			Version: req.Msg.Version,
			X:       req.Msg.X,
			Y:       req.Msg.Y,
			W:       req.Msg.W,
			H:       req.Msg.H,
		})
		return pluginTileResp(uuid, resp, err)
	}
	r := rpc.ResizeTileFromProto(req.Msg)
	r.TileID = h.localID(r.TileID)
	r.Path = h.localPath(r.Path)
	return h.tileRespQ(h.srv.store.ResizeTile(ctx, r))
}
func (h *connectHandler) SetWellView(ctx context.Context, req *connect.Request[pb.SetWellViewRequest]) (*connect.Response[pb.TileResponse], error) {
	if client, local, uuid, ok := h.pluginRoute(req.Msg.TileId); ok {
		resp, err := client.SetWellView(ctx, &pb.SetWellViewRequest{
			Path:     localPathFor(req.Msg.Path, uuid),
			TileId:   local,
			Version:  req.Msg.Version,
			ViewX:    req.Msg.ViewX,
			ViewY:    req.Msg.ViewY,
			ViewZoom: req.Msg.ViewZoom,
		})
		return pluginTileResp(uuid, resp, err)
	}
	r := rpc.SetWellViewFromProto(req.Msg)
	r.TileID = h.localID(r.TileID)
	r.Path = h.localPath(r.Path)
	return h.tileRespQ(h.srv.store.SetWellView(ctx, r))
}
func (h *connectHandler) SetTextView(ctx context.Context, req *connect.Request[pb.SetTextViewRequest]) (*connect.Response[pb.TileResponse], error) {
	r := rpc.SetTextViewFromProto(req.Msg)
	r.TileID = h.localID(r.TileID)
	r.Path = h.localPath(r.Path)
	return h.tileRespQ(h.srv.store.SetTextView(ctx, r))
}
func (h *connectHandler) SetShellPreview(ctx context.Context, req *connect.Request[pb.SetShellPreviewRequest]) (*connect.Response[pb.TileResponse], error) {
	r := rpc.SetShellPreviewFromProto(req.Msg)
	r.TileID = h.localID(r.TileID)
	r.Path = h.localPath(r.Path)
	return h.tileRespQ(h.srv.store.SetShellPreview(ctx, r))
}

// ShellSessionAlive answers the wasm's per-descent probe by asking
// the streamer (which delegates to the tmux controller) whether the
// tile's session exists. An infrastructure error here is reported as
// not-alive rather than as a Connect error — the wasm doesn't care
// why the session isn't there, only that the refresh button should
// hide. Returns not-alive when no streamer is wired up (defensive;
// production always wires one).
func (h *connectHandler) ShellSessionAlive(_ context.Context, req *connect.Request[pb.ShellSessionAliveRequest]) (*connect.Response[pb.ShellSessionAliveResponse], error) {
	tileID := h.localID(req.Msg.TileId)
	if h.srv.shellStreamer == nil {
		return connect.NewResponse(rpc.ShellSessionAliveResponseToProto(&rpc.ShellSessionAliveResponse{Alive: false})), nil
	}
	alive := false
	if tileIDInt, err := strconv.ParseInt(tileID, 10, 64); err == nil {
		alive, _ = h.srv.shellStreamer.HasSession(tileIDInt)
	}
	return connect.NewResponse(rpc.ShellSessionAliveResponseToProto(&rpc.ShellSessionAliveResponse{Alive: alive})), nil
}

func (h *connectHandler) SetRootView(ctx context.Context, req *connect.Request[pb.SetRootViewRequest]) (*connect.Response[pb.SetRootViewResponse], error) {
	if err := h.srv.store.SetRootView(ctx, rpc.SetRootViewFromProto(req.Msg)); err != nil {
		return nil, asConnectError(err)
	}
	return connect.NewResponse(&pb.SetRootViewResponse{}), nil
}
func (h *connectHandler) SetURLState(ctx context.Context, req *connect.Request[pb.SetURLStateRequest]) (*connect.Response[pb.TileResponse], error) {
	r := rpc.SetURLStateFromProto(req.Msg)
	r.TileID = h.localID(r.TileID)
	r.Path = h.localPath(r.Path)
	return h.tileRespQ(h.srv.store.SetURLState(ctx, r))
}
func (h *connectHandler) UpdateText(ctx context.Context, req *connect.Request[pb.UpdateTextRequest]) (*connect.Response[pb.TileResponse], error) {
	r := rpc.UpdateTextFromProto(req.Msg)
	r.TileID = h.localID(r.TileID)
	r.Path = h.localPath(r.Path)
	return h.tileRespQ(h.srv.store.UpdateText(ctx, r))
}

func (h *connectHandler) DeleteTile(ctx context.Context, req *connect.Request[pb.DeleteTileRequest]) (*connect.Response[pb.DeleteTileResponse], error) {
	// A tile inside a plugin grid (file / process) is deleted by the plugin
	// itself — trashing the file or signalling the process. No shell-session
	// reaping applies to plugin tiles.
	if client, local, uuid, ok := h.pluginRoute(req.Msg.TileId); ok {
		if _, err := client.DeleteTile(ctx, &pb.DeleteTileRequest{
			Path:    localPathFor(req.Msg.Path, uuid),
			TileId:  local,
			Version: req.Msg.Version,
		}); err != nil {
			return nil, asConnectError(err)
		}
		return connect.NewResponse(&pb.DeleteTileResponse{}), nil
	}
	r := rpc.DeleteTileFromProto(req.Msg)
	r.TileID = h.localID(r.TileID)
	r.Path = h.localPath(r.Path)
	if err := h.srv.store.DeleteTile(ctx, r); err != nil {
		return nil, asConnectError(err)
	}
	// A deleted shell tile's tmux session would otherwise survive across
	// gridwell restarts and leak, so reap it once the row is gone. The
	// ShellTileExists guard is a belt-and-braces check that this exact id
	// really is deleted before we kill its session (a cloned shell is an
	// independent copy with its own id and no session, so deleting a copy
	// never touches the original's PTY). Fire-and-forget; a missed kill is
	// caught by the orphan-cleanup pass at startup.
	if h.srv.shellStreamer != nil {
		exists, err := h.srv.store.ShellTileExists(ctx, r.TileID)
		switch {
		case err != nil:
			log.Printf("[shellstream] kill-on-delete existence tile=%s err=%v", r.TileID, err)
		case !exists:
			if tileIDInt, perr := strconv.ParseInt(r.TileID, 10, 64); perr == nil {
				if err := h.srv.shellStreamer.Kill(tileIDInt); err != nil {
					log.Printf("[shellstream] kill-on-delete tile=%s err=%v", r.TileID, err)
				}
			}
		}
	}
	return connect.NewResponse(&pb.DeleteTileResponse{}), nil
}

// Subscribe is a server-streaming RPC. Each event from the store is
// converted and pushed onto the wire until the client disconnects or
// the store closes its subscriber channel.
func (h *connectHandler) Subscribe(ctx context.Context, _ *connect.Request[pb.SubscribeRequest], stream *connect.ServerStream[pb.Event]) error {
	ch, cancel := h.srv.store.SubscribeEvents()
	defer cancel()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(h.qualifyLocaldbEvent(ev)); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// asConnectError maps store sentinel errors to Connect status codes so
// the wire surface matches the raw-HTTP status mapping — both route through
// the single classifyStoreError categorization.
func asConnectError(err error) error {
	if err == nil {
		return nil
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
