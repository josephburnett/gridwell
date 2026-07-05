package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

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
// watching plugin's event stream (Info.watch); ShellSessionAlive routes to the
// owning plugin.
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
// segments owned by the target plugin is meaningful to it, so segments above
// the last boundary (owned by other plugins) are dropped and the rest are
// stripped of the uuid prefix.
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

// pluginInfoTimeout bounds each plugin's Info handshake during ListPlugins so
// one slow or hung plugin can't stall (or block) the whole launcher. On timeout
// the plugin is still listed from its config (kind + configured label), just
// without a clickable root — graceful degradation beats a frozen launcher.
const pluginInfoTimeout = 3 * time.Second

// ListPlugins enumerates the configured plugins in config order so the client
// can build the launcher / + menu. label comes from each plugin's Info, and so
// does writable (accepts new tiles) — a capability the handshake declares,
// never derived from the kind string.
func (h *connectHandler) ListPlugins(ctx context.Context, _ *connect.Request[pb.ListPluginsRequest]) (*connect.Response[pb.ListPluginsResponse], error) {
	var out []*pb.PluginInfo
	for _, p := range h.srv.pluginReg.Ordered() {
		// The server.yaml display name is authoritative (the menu and a mounted
		// well must agree); buildPluginInfo falls back to Info's name then kind.
		label := h.srv.pluginReg.Label(p.UUID)
		// Info is the whole handshake (default root grid + fallback label +
		// capabilities), served from the per-uuid cache after the first
		// success. Bounded, so a hung plugin degrades to a config-only entry
		// instead of hanging the launcher; a failed Info → info stays nil, and
		// the error rides along so buildPluginInfo can distinguish "broken"
		// from "healthy but rootless" — previously dropped on the floor here,
		// which made both cases identical on the wire (issue #47).
		info, err := h.srv.pluginInfo(ctx, p.UUID)
		out = append(out, buildPluginInfo(p.UUID, p.Kind, label, info, err))
	}
	return connect.NewResponse(&pb.ListPluginsResponse{Plugins: out}), nil
}

// buildPluginInfo assembles a launcher PluginInfo from the config (uuid, kind,
// configured label) and the plugin's Info handshake. info is nil when Info
// failed or timed out (infoErr is the reason): the plugin is still listed (so
// the launcher never blanks a configured plugin), but with no clickable
// root/scratch grid, no writable bit, and a label that falls back to the
// configured name, then the kind. Pure, so the fallback rules are
// unit-tested without standing up a plugin.
//
// info == nil (Info failed/timed out — "broken") and info != nil but
// info.RootGridId == "" (Info succeeded, the plugin just has no root
// configured — "rootless") both leave RootGridId == "" on the result; without
// InfoError they are the SAME PluginInfo and the launcher tile (and a click on
// it) cannot tell them apart. InfoError is what makes them distinguishable: set
// only in the broken case, always empty in the rootless one.
//
// writable comes from the Info handshake, NEVER from the kind string: a remote
// localdb reached through the ssh proxy has local kind "ssh" but is every bit
// as writable — the proxy forwards its Info verbatim, so the capability
// travels while a kind check would strand it read-only.
func buildPluginInfo(uuid, kind, configLabel string, info *pb.InfoResponse, infoErr error) *pb.PluginInfo {
	label := configLabel
	var rootGridID, scratchGridID, infoError string
	var writable bool
	var rootViewCx, rootViewCy, rootViewZoom float64
	if info != nil {
		if info.RootGridId != "" {
			rootGridID = qualifyID(uuid, info.RootGridId)
		}
		// Qualified scratch grid id (ephemeral-url target); empty for plugins
		// that don't support ephemeral visits.
		if info.ScratchGridId != "" {
			scratchGridID = qualifyID(uuid, info.ScratchGridId)
		}
		if label == "" {
			label = info.DisplayName
		}
		writable = info.Writable
		// Root-view viewport forwarded verbatim from Info (localdb fills it;
		// fs/proc return zero). The client seeds enterPlugin framing from this.
		rootViewCx = info.RootViewCx
		rootViewCy = info.RootViewCy
		rootViewZoom = info.RootViewZoom
	} else if infoErr != nil {
		infoError = "plugin not responding: " + infoErr.Error()
	}
	if label == "" {
		label = kind
	}
	return &pb.PluginInfo{
		Uuid:          uuid,
		Kind:          kind,
		Label:         label,
		Writable:      writable,
		RootGridId:    rootGridID,
		ScratchGridId: scratchGridID,
		RootViewCx:    rootViewCx,
		RootViewCy:    rootViewCy,
		RootViewZoom:  rootViewZoom,
		InfoError:     infoError,
	}
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

// SetRootView persists the plugin root-grid framing (the same fact as SetTile
// for a well, but for the synthetic plugin root which has no tile row). Routes
// on root_grid_id; localdb stores to the system KV table; fs/proc are no-ops
// (the UnimplementedGridwellServer returns Unimplemented — ignored here so a
// read-only plugin's ascent doesn't surface an error to the user).
// After a successful write, the per-plugin Info cache is invalidated so the
// next ListPlugins (e.g. after a page refresh) returns fresh root-view fields.
func (h *connectHandler) SetRootView(ctx context.Context, req *connect.Request[pb.SetRootViewRequest]) (*connect.Response[pb.SetRootViewResponse], error) {
	m := req.Msg
	c, local, uuid, err := h.route(m.RootGridId)
	if err != nil {
		return nil, err
	}
	_, err = c.SetRootView(ctx, &pb.SetRootViewRequest{
		RootGridId: local,
		Cx:         m.Cx,
		Cy:         m.Cy,
		Zoom:       m.Zoom,
	})
	if err != nil {
		// Unimplemented is not an error — fs/proc don't persist a root view.
		if isUnimplemented(err) {
			return connect.NewResponse(&pb.SetRootViewResponse{}), nil
		}
		return nil, asConnectError(err)
	}
	// Invalidate the Info cache: root_view_* travel in the Info handshake
	// and now differ from the cached values. The next ListPlugins (page
	// refresh) must re-fetch Info to see the updated viewport.
	h.srv.invalidateInfoCache(uuid)
	return connect.NewResponse(&pb.SetRootViewResponse{}), nil
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

// Subscribe fans every watching plugin's change-event stream into the client's
// stream, re-qualifying each event's ids with the emitting plugin's uuid. A
// plugin declares that it emits events via Info.watch — a capability from the
// handshake, never the kind string, so a remote node's events (an ssh-proxied
// localdb) flow exactly like a local plugin's. (fs/proc report watch=false and
// are polled via GetGrid.) With no single root, a client subscribes to the
// whole federation at once.
//
// Failures surface and heal rather than silently ending a plugin's events for
// the life of the client stream: an Info or Subscribe failure is logged with
// the plugin uuid, and watchPlugin re-dials (Info) / fanInEvents re-dials
// (the event stream) with backoff while the client stream lives — so a plugin
// that crashes and restarts resumes delivering events without the client
// reconnecting, AND the client is told about the outage and the recovery via
// an EventPluginHealth (issue #47) instead of tiles just quietly going stale.
func (h *connectHandler) Subscribe(ctx context.Context, _ *connect.Request[pb.SubscribeRequest], stream *connect.ServerStream[pb.Event]) error {
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events := make(chan *pb.Event, 64)
	for _, p := range h.srv.pluginReg.Ordered() {
		c, ok := h.srv.pluginReg.Get(p.UUID)
		if !ok {
			continue
		}
		go watchPlugin(subCtx, p.UUID, c, h.srv, events)
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

// watchPlugin resolves whether plugin uuid supports live events (Info.watch)
// and, if so, hands off to fanInEvents for the life of ctx. The Info fetch is
// retried with the same backoff fanInEvents itself uses for a dead stream —
// previously a single failed Info at Subscribe time did `continue`, which
// PERMANENTLY excluded the plugin's fan-in for the life of the client stream
// (a plugin that was merely slow to start on server boot, or briefly
// unreachable, silently never got its events fanned in even after it came
// up). Emits the down/recovery EventPluginHealth transition itself when the
// failure is at this Info stage; once Info succeeds and Watch is true,
// fanInEvents owns the health state for the rest of ctx's life (a plugin
// capability doesn't flip false again — only its stream can go down).
func watchPlugin(ctx context.Context, uuid string, client pb.GridwellClient, srv *Server, events chan<- *pb.Event) {
	backoff := time.Second
	healthy := true // assume healthy until the first failure
	for {
		info, err := srv.pluginInfo(ctx, uuid)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("gridwell: subscribe: info %s: %v — retrying fan-in in %v", uuid, err, backoff)
			if healthy {
				healthy = false
				reportHealth(ctx, events, uuid, false, err.Error())
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		if !info.Watch {
			return // this plugin kind doesn't emit events (fs/proc) — nothing to fan in, not a failure
		}
		if !healthy {
			reportHealth(ctx, events, uuid, true, "")
		}
		fanInEvents(ctx, uuid, client, events) // owns health from here; returns only when ctx ends
		return
	}
}

// fanInEvents relays one plugin's Subscribe stream into events until ctx ends,
// re-dialing with exponential backoff (1s..30s) after any stream failure so a
// plugin restart resumes its events. Failures are logged, never swallowed —
// a plugin whose events silently stop presents to the user as "tiles stopped
// updating" with no evidence (the silent-disappearance class) — and reported
// as an EventPluginHealth transition (down on first failure, up on recovery;
// not once per retry attempt, so a flapping plugin doesn't spam the client).
func fanInEvents(ctx context.Context, uuid string, client pb.GridwellClient, events chan<- *pb.Event) {
	backoff := time.Second
	healthy := true // caller (watchPlugin) already reported recovery if it was ever down
	for {
		ps, err := client.Subscribe(ctx, &pb.SubscribeRequest{})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("gridwell: subscribe: plugin %s stream open failed: %v (retrying in %v)", uuid, err, backoff)
			if healthy {
				healthy = false
				reportHealth(ctx, events, uuid, false, err.Error())
			}
		} else {
			if !healthy {
				healthy = true
				reportHealth(ctx, events, uuid, true, "")
			}
			backoff = time.Second // healthy stream: reset for the next outage
			for {
				ev, rerr := ps.Recv()
				if rerr != nil {
					if ctx.Err() != nil {
						return
					}
					log.Printf("gridwell: subscribe: plugin %s stream ended: %v (retrying in %v)", uuid, rerr, backoff)
					if healthy {
						healthy = false
						reportHealth(ctx, events, uuid, false, rerr.Error())
					}
					break
				}
				select {
				case events <- qualifyEvent(uuid, ev):
				case <-ctx.Done():
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// reportHealth pushes an EventPluginHealth into the fan-in channel, the same
// path a plugin's own re-qualified events take. Best-effort against ctx
// ending mid-send (the client stream is closing anyway in that case).
func reportHealth(ctx context.Context, events chan<- *pb.Event, uuid string, healthy bool, detail string) {
	ev := &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{
		PluginUuid: uuid,
		Healthy:    healthy,
		Detail:     detail,
	}}}
	select {
	case events <- ev:
	case <-ctx.Done():
	}
}

// isUnimplemented reports whether a gRPC/Connect error carries an Unimplemented
// code. Used to treat a plugin's "method not supported" as a silent no-op
// (e.g. SetRootView on fs/proc which have no persistent root view).
func isUnimplemented(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := status.FromError(err); ok {
		return st.Code() == gcodes.Unimplemented
	}
	return false
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
