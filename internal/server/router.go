package server

import (
	"context"
	"io"
	"log"
	"time"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/panelayout"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// router is a namespace.Namespace of qualified ids: every verb resolves one
// segment, strips it, forwards, and re-applies it on the way out. It holds no
// state; both doors (Connect and the connection export) are codecs over it.
type router struct {
	srv *Server
}

var _ namespace.Namespace = (*router)(nil)

func newRouter(srv *Server) *router { return &router{srv: srv} }

// route resolves the namespace that owns id. Ids are always qualified; there
// is no privileged namespace a bare id falls back to.
func (rt *router) route(id string) (ns namespace.Namespace, local, uuid string, transit bool, err error) {
	if _, _, ok := rpc.SplitID(id); !ok {
		return nil, "", "", false, status.Errorf(gcodes.InvalidArgument, "unqualified id %q", id)
	}
	c, local, uuid, transit, found := rt.srv.resolve(id)
	if !found {
		return nil, "", "", false, undeclared(id)
	}
	return c, local, uuid, transit, nil
}

// hop is the inbound peel for a request routed on the qualified id routed.
func (rt *router) hop(routed string, transit bool) rpc.Hop {
	return rpc.InboundHop(routed, rt.srv.cfg.ID, transit)
}

// tileResp applies the transit rule when the owning plugin is a node mount.
func (rt *router) tileResp(uuid string, transit bool, resp *pb.TileResponse, err error) (*pb.TileResponse, error) {
	if err != nil {
		return nil, err
	}
	t := resp.GetTile()
	if t != nil {
		t = qualifyTilesFor(transit, uuid, []*pb.Tile{t})[0]
	}
	return &pb.TileResponse{Tile: t}, nil
}

// qualifyEvent re-applies a plugin's uuid to a change event's ids; the walk is
// rpc.QualifyEventIDs.
func qualifyEvent(uuid string, transit bool, ev *pb.Event) *pb.Event {
	return rpc.QualifyEventIDs(uuid, ev, func(t *pb.Tile) *pb.Tile {
		return qualifyTilesFor(transit, uuid, []*pb.Tile{t})[0]
	})
}

func (rt *router) GetGrid(ctx context.Context, req *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	c, local, uuid, transit, err := rt.route(req.GridId)
	if err != nil {
		return nil, err
	}
	resp, err := c.GetGrid(ctx, &pb.GetGridRequest{GridId: local})
	if err != nil {
		return nil, err
	}
	// Grid.writable and scratch_grid_id are the owning plugin's facts, from
	// its Info or, in transit, the remote node's stamp.
	var g *pb.Grid
	if transit {
		g = rpc.TransitQualifyGrid(uuid, resp.Grid)
	} else {
		g = qualifyGrid(uuid, resp.Grid)
		if g != nil {
			// The declared face has no other source, so a handshake that does
			// not answer fails the read rather than present a wrong face.
			info, ierr := rt.srv.pluginInfo(ctx, uuid)
			if ierr != nil {
				return nil, infoFaceError(req.GridId, uuid, ierr)
			}
			g.Writable = info.Writable
			if info.ScratchGridId != "" {
				g.ScratchGridId = rpc.QualifyID(uuid, info.ScratchGridId)
			} else if hu := rt.srv.homeUUID(); hu != "" && hu != uuid {
				// Ephemeral visits from a plugin with no scratch grid land in
				// home's, stamped on the grid so it chains through mounts.
				hinfo, herr := rt.srv.pluginInfo(ctx, hu)
				if herr != nil {
					return nil, infoFaceError(req.GridId, hu, herr)
				}
				if hinfo.ScratchGridId != "" {
					g.ScratchGridId = rpc.QualifyID(hu, hinfo.ScratchGridId)
				}
			}
			g.MenuEntries = rpc.QualifyMenuEntries(uuid, info.MenuEntries)
		}
	}
	return &pb.GetGridResponse{
		Grid:  g,
		Tiles: qualifyTilesFor(transit, uuid, resp.Tiles),
	}, nil
}

// infoFaceError says which grid could not be answered and why, transport-class
// unless the plugin gave a code, so the client re-asks rather than latching.
func infoFaceError(gridID, uuid string, err error) error {
	code := status.Code(err)
	if code == gcodes.Unknown {
		code = gcodes.Unavailable
	}
	return status.Errorf(code, "grid %s: plugin %s handshake failed, so the grid's declared face is unknown: %v", gridID, uuid, err)
}

func (rt *router) GetTilePreview(ctx context.Context, req *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	// A leaf link's preview is its target's, resolved at the serving node.
	c, local, err := rt.srv.contentRoute(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	return c.GetTilePreview(ctx, &pb.GetTilePreviewRequest{TileId: local})
}

// pluginInfoTimeout bounds each plugin's Info handshake so one hung plugin
// cannot stall the menu.
const pluginInfoTimeout = 3 * time.Second

// Handshake enumerates the configured plugins in config order for the + menu.
func (rt *router) Handshake(ctx context.Context, req *pb.HandshakeRequest) (*pb.HandshakeResponse, error) {
	// A namespaced request routes like every other read; "" stays local.
	if ns := req.GetNamespace(); ns != "" {
		hop, rest, ok := rpc.SplitID(ns)
		if !ok {
			hop, rest = ns, ""
		}
		c, found := rt.srv.routeClient(hop)
		if hop == rt.srv.cfg.ID && rest != "" {
			// "<id>/<conn>/…": the transport answers for the connection.
			c, found = rt.srv.pluginReg.Transport()
		}
		if !found {
			return nil, gwerr.DeadRef(hop, "no plugin %q", hop)
		}
		resp, err := c.Handshake(ctx, &pb.HandshakeRequest{Namespace: rest})
		if err != nil {
			return nil, err
		}
		return rpc.TransitQualifyPluginList(hop, resp), nil
	}
	var out []*pb.PluginInfo
	for _, n := range rt.srv.namespaces() {
		if n.Transit {
			// One row per connection, from the transport's own handshake
			// (connection.Server.Rows), re-qualified one hop.
			tr, err := n.NS.Handshake(ctx, &pb.HandshakeRequest{})
			if err != nil {
				return nil, err
			}
			out = append(out, rpc.TransitQualifyPluginList(n.UUID, tr).Plugins...)
			continue
		}
		// The server.yaml display name is authoritative, since the menu and a
		// mounted well must agree; buildPluginInfo owns the fallbacks.
		label := rt.srv.pluginReg.Label(n.UUID)
		// A failed Info leaves info nil and the error rides along.
		info, err := rt.srv.pluginInfo(ctx, n.UUID)
		out = append(out, buildPluginInfo(n.UUID, n.Kind, label, info, err))
	}
	resp := &pb.HandshakeResponse{
		ShellsDisabled: rt.srv.cfg.DisableShells,
		// The /content/ door's capability, handed out only here, on the
		// cookie-authenticated mux.
		ContentToken: ContentToken(rt.srv.cfg.Password),
		Plugins:      out,
	}
	// Home is the node's own store, where "/" lands.
	for _, p := range out {
		if p.Uuid == rt.srv.homeUUID() {
			resp.HomeGridId = p.RootGridId
		}
	}
	return resp, nil
}

// buildPluginInfo assembles a menu PluginInfo from the config and the plugin's
// Info, nil when Info failed with infoErr the reason: the plugin is still
// listed, and InfoError tells broken from healthy-but-rootless.
func buildPluginInfo(uuid, kind, configLabel string, info *pb.InfoResponse, infoErr error) *pb.PluginInfo {
	label := configLabel
	var rootGridID, infoError string
	var glyph string
	var menuEntries []*pb.MenuEntry
	var rootViewCx, rootViewCy, rootViewZoom float64
	if info != nil {
		if info.RootGridId != "" {
			rootGridID = rpc.QualifyID(uuid, info.RootGridId)
		}
		if label == "" {
			label = info.DisplayName
		}
		glyph = info.Glyph
		menuEntries = rpc.QualifyMenuEntries(uuid, info.MenuEntries)
		rootViewCx = info.RootViewCx
		rootViewCy = info.RootViewCy
		rootViewZoom = info.RootViewZoom
	} else if infoErr != nil {
		infoError = infoRefusal(infoErr)
	}
	// An error alongside a live Info still rides the row.
	if infoError == "" && infoErr != nil {
		infoError = infoErr.Error()
	}
	if label == "" {
		label = kind
	}
	return &pb.PluginInfo{
		Uuid:         uuid,
		Kind:         kind,
		Label:        label,
		RootGridId:   rootGridID,
		RootViewCx:   rootViewCx,
		RootViewCy:   rootViewCy,
		RootViewZoom: rootViewZoom,
		InfoError:    infoError,
		Glyph:        glyph,
		MenuEntries:  menuEntries,
	}
}

// infoRefusal is the sentence a broken row carries: the plugin's own words, or
// "not responding" when it never spoke.
func infoRefusal(err error) string {
	msg := status.Convert(err).Message()
	if gwerr.IsTransport(err) {
		return "plugin not responding: " + msg
	}
	return msg
}

func (rt *router) GetTile(ctx context.Context, req *pb.GetTileRequest) (*pb.TileResponse, error) {
	c, local, uuid, transit, err := rt.route(req.TileId)
	if err != nil {
		return nil, err
	}
	resp, err := c.GetTile(ctx, &pb.GetTileRequest{TileId: local})
	return rt.tileResp(uuid, transit, resp, err)
}

// Search routes a scope to its owner; an empty scope fans out to every
// namespace, each bounded by rpc.SearchHopTimeout with errors skipped.
func (rt *router) Search(ctx context.Context, req *pb.SearchRequest) (*pb.SearchResponse, error) {
	m := req
	if m.Scope != "" {
		c, _, uuid, transit, err := rt.route(m.Scope)
		if err != nil {
			return nil, err
		}
		resp, err := c.Search(ctx, &pb.SearchRequest{Query: rt.hop(m.Scope, transit).PeelSearchQuery(m.Query), Limit: m.Limit})
		if err != nil {
			return nil, err
		}
		return qualifySearch(transit, uuid, resp), nil
	}
	out := &pb.SearchResponse{}
	for _, n := range rt.srv.namespaces() {
		pctx, cancel := context.WithTimeout(ctx, rpc.SearchHopTimeout)
		// No routed id narrows a fan-out, so the hop is the namespace's own.
		hop := rpc.Hop{Seg: n.UUID, Via: n.UUID, Transit: n.Transit}
		resp, err := n.NS.Search(pctx, &pb.SearchRequest{Query: hop.PeelSearchQuery(m.Query), Limit: m.Limit})
		cancel()
		if err != nil {
			continue // Unimplemented, a timeout, a dead plugin: no answer here
		}
		out.Results = append(out.Results, qualifySearch(n.Transit, n.UUID, resp).Results...)
	}
	return out, nil
}

func qualifySearch(transit bool, uuid string, resp *pb.SearchResponse) *pb.SearchResponse {
	return rpc.QualifySearchResponse(resp, func(ts []*pb.Tile) []*pb.Tile {
		return qualifyTilesFor(transit, uuid, ts)
	})
}

// CreateTile forwards to the destination grid's owner, references spelled for
// that node (spellReferences).
func (rt *router) CreateTile(ctx context.Context, req *pb.CreateTileRequest) (*pb.TileResponse, error) {
	m := req
	// The node-wide shell refusal is the authority; the palette only hides.
	if rt.srv.cfg.DisableShells && m.Tile.GetKind() == rpc.KindShell {
		return nil, status.Error(gcodes.PermissionDenied,
			"shell tiles are disabled on this node (server.yaml disable_shells)")
	}
	c, _, uuid, transit, err := rt.route(m.GridId)
	if err != nil {
		return nil, err
	}
	if err := rt.mintReferences(ctx, m.Tile); err != nil {
		return nil, err
	}
	if err := rt.spellReferences(ctx, m.GridId, m.Tile); err != nil {
		return nil, err
	}
	resp, err := c.CreateTile(ctx, rpc.PeelRequest(rt.hop(m.GridId, transit), m))
	return rt.tileResp(uuid, transit, resp, err)
}

// mintReferences canonicalizes the ids a tile is about to store, so a document
// reached through a link never wears a second name.
func (rt *router) mintReferences(ctx context.Context, t *pb.Tile) error {
	if t == nil {
		return nil
	}
	child, err := rt.mintRef(ctx, t.ChildGridId)
	if err != nil {
		return err
	}
	target, err := rt.mintRef(ctx, t.LinkTargetId)
	if err != nil {
		return err
	}
	t.ChildGridId, t.LinkTargetId = child, target
	return nil
}

// mintRef canonicalizes one qualified reference; an id nothing derives
// answers itself.
func (rt *router) mintRef(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	c, local, uuid, _, ok := rt.srv.resolve(id)
	if !ok {
		return id, nil
	}
	minted, err := namespace.MintRef(ctx, c, local)
	if err != nil {
		return "", err
	}
	return rpc.QualifyID(uuid, minted), nil
}

// CloneTile runs at the nearest node that sees both ends (rpc.SharedOwner);
// where the ends part, the copy is made here (deepcopy.go). The link gesture
// is a plain CreateTile, never a clone.
func (rt *router) CloneTile(ctx context.Context, req *pb.CloneTileRequest) (*pb.TileResponse, error) {
	m := req
	c, local, uuid, transit, err := rt.route(m.TileId)
	if err != nil {
		return nil, err
	}
	if rpc.SharedOwner(m.TileId, m.DestGridId, rt.srv.cfg.ID) == "" {
		return rt.cloneAcrossPlugins(ctx, m, c, local, uuid, transit)
	}
	resp, err := c.CloneTile(ctx, rpc.PeelRequest(rt.hop(m.TileId, transit), m))
	return rt.tileResp(uuid, transit, resp, err)
}

// cloneAcrossPlugins lands a copy at the dropped cell; the per-kind copy is
// deepCopyTile's, and this level owns the host-content refusal and the
// partial-copy wording.
func (rt *router) cloneAcrossPlugins(ctx context.Context, m *pb.CloneTileRequest, src namespace.Namespace, srcLocal, srcUUID string, srcTransit bool) (*pb.TileResponse, error) {
	resp, err := src.GetTile(ctx, &pb.GetTileRequest{TileId: srcLocal})
	if err != nil {
		return nil, err
	}
	srcLocalTile := resp.GetTile()
	st := qualifyTilesFor(srcTransit, srcUUID, []*pb.Tile{srcLocalTile})[0]
	// No version claim: a clone is layout and the source row is untouched.
	dst, dstLocal, dstUUID, dstTransit, err := rt.route(m.DestGridId)
	if err != nil {
		return nil, err
	}
	if rpc.IsWellKind(st.Kind) && !st.Reference {
		// Host content is refused before anything is created: its rows are
		// metadata stubs, not the files.
		if sg, gerr := src.GetGrid(ctx, &pb.GetGridRequest{GridId: srcLocalTile.ChildGridId}); gerr == nil && sg.GetGrid().GetHostContent() {
			return nil, status.Error(gcodes.Unimplemented,
				"deep copy of a host-content well is not implemented (the copy would be metadata stubs, not the host content); left-drag creates a link")
		}
	}
	out, err := rt.deepCopyTile(ctx, src, srcTransit, srcUUID, srcLocalTile, copyDst{Namespace: dst, hop: rt.hop(m.DestGridId, dstTransit), holder: m.DestGridId}, dstLocal, m.X, m.Y)
	if err != nil && out != nil {
		// The partial is visible, so say what stopped the walk.
		return nil, status.Errorf(gcodes.Aborted,
			"deep copy incomplete (the partial copy remains, delete it if unwanted): %v", err)
	}
	return rt.tileResp(dstUUID, dstTransit, out, err)
}

// readAllContent drains a namespace's ReadContent stream into one value.
func readAllContent(ctx context.Context, c namespace.Namespace, tileID string) ([]byte, error) {
	var data []byte
	err := c.ReadContent(ctx, &pb.ReadContentRequest{TileId: tileID}, func(chunk *pb.ContentChunk) error {
		data = append(data, chunk.Data...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

func writeAllContent(ctx context.Context, c namespace.Namespace, tileID string, version int64, data []byte) (*pb.TileResponse, error) {
	sent := false
	return c.WriteContent(ctx, func() (*pb.WriteContentRequest, error) {
		if sent {
			return nil, io.EOF
		}
		sent = true
		return &pb.WriteContentRequest{TileId: tileID, Version: version, Data: data}, nil
	})
}

func (rt *router) SetTile(ctx context.Context, req *pb.SetTileRequest) (*pb.TileResponse, error) {
	c, _, uuid, transit, err := rt.route(req.TileId)
	if err != nil {
		return nil, err
	}
	resp, err := c.SetTile(ctx, rpc.PeelRequest(rt.hop(req.TileId, transit), req))
	return rt.tileResp(uuid, transit, resp, err)
}

func (rt *router) DeleteTile(ctx context.Context, req *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	m := req
	qualifiedID := m.TileId
	c, local, _, transit, err := rt.route(m.TileId)
	if err != nil {
		return nil, err
	}
	// The layout blob is the only record of a pane tile's ephemerals: capture
	// before the row dies, reap after only if it is really gone (trash keeps
	// them for a restore). Transit skips this; the owning node's router reaps.
	var candidates []string
	if !transit {
		candidates = rt.workspaceEphemeralCandidates(ctx, c, local, qualifiedID)
	}
	m.TileId = local
	if _, err := c.DeleteTile(ctx, m); err != nil {
		return nil, err
	}
	if len(candidates) > 0 {
		// Reap only on an explicit NotFound: a missed reap is reclaimed by the
		// boot sweep, a wrong one by nothing.
		if _, err := c.GetTile(ctx, &pb.GetTileRequest{TileId: local}); status.Code(err) == gcodes.NotFound {
			rt.reapWorkspaceEphemerals(ctx, candidates, qualifiedID)
		}
	}
	return &pb.DeleteTileResponse{}, nil
}

// workspaceEphemeralCandidates reads a pane tile's layout blob for leaf ids
// that might be its ephemerals, nothing when unreadable. The derivation is
// panelayout.TextFocusIDs, the same one the boot sweep protects by.
func (rt *router) workspaceEphemeralCandidates(ctx context.Context, owner namespace.Namespace, localID, qualifiedID string) []string {
	tr, err := owner.GetTile(ctx, &pb.GetTileRequest{TileId: localID})
	if err != nil || tr.GetTile() == nil || tr.GetTile().Kind != rpc.KindPane || tr.GetTile().BlobId == 0 {
		return nil
	}
	body, err := readAllContent(ctx, owner, localID)
	if err != nil || len(body) == 0 {
		return nil
	}
	// Blob ids are already in this node's frame: the encoder strips the
	// reader's transit prefix, empty for a locally-owned pane tile.
	ids, err := panelayout.TextFocusIDs(body)
	if err != nil {
		log.Printf("gridwell: delete %s: layout blob unreadable, reaping nothing: %v", qualifiedID, err)
		return nil
	}
	return ids
}

// reapWorkspaceEphemerals deletes the scratch-grid tiles among a destroyed pane
// tile's captured leaves, best-effort: it must not block the user's delete.
func (rt *router) reapWorkspaceEphemerals(ctx context.Context, candidates []string, qualifiedID string) {
	for _, id := range candidates {
		ec, elocal, euuid, transit, err := rt.route(id)
		if err != nil {
			continue
		}
		if transit {
			// A remote's scratch-grid fact is invisible through the raw
			// transit client, so its own boot sweep reclaims this.
			log.Printf("gridwell: delete %s: not reaping remote ephemeral candidate %s (transit)", qualifiedID, id)
			continue
		}
		// Ephemeral means on the owning namespace's scratch grid.
		info, err := rt.srv.pluginInfo(ctx, euuid)
		if err != nil {
			log.Printf("gridwell: delete %s: not reaping candidate %s: plugin %s handshake failed: %v", qualifiedID, id, euuid, err)
			continue
		}
		if info.ScratchGridId == "" {
			continue
		}
		et, err := ec.GetTile(ctx, &pb.GetTileRequest{TileId: elocal})
		if err != nil || et.GetTile() == nil || et.GetTile().GridId != info.ScratchGridId {
			continue // not an ephemeral: viewed content, never touched
		}
		if _, err := ec.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: elocal}); err != nil {
			log.Printf("gridwell: delete %s: reaping ephemeral %s failed: %v", qualifiedID, id, err)
		}
	}
}

// SetFraming is the one framing write. Unimplemented (a plugin that keeps no
// framing) is not an error; a root write invalidates the Info cache, which
// carries the framing.
func (rt *router) SetFraming(ctx context.Context, req *pb.SetFramingRequest) (*pb.SetFramingResponse, error) {
	m := req
	root := m.RootGridId != ""
	ref := m.TileId
	if root {
		ref = m.RootGridId
	}
	c, _, uuid, transit, err := rt.route(ref)
	if err != nil {
		return nil, err
	}
	out := &pb.SetFramingRequest{Cx: m.Cx, Cy: m.Cy, Zoom: m.Zoom}
	if root {
		out.RootGridId = ref
	} else {
		out.TileId = ref
	}
	resp, err := c.SetFraming(ctx, rpc.PeelRequest(rt.hop(ref, transit), out))
	if err != nil {
		if isUnimplemented(err) {
			return &pb.SetFramingResponse{}, nil
		}
		return nil, err
	}
	if root {
		rt.srv.invalidateInfoCache(uuid)
		return &pb.SetFramingResponse{}, nil
	}
	t := resp.GetTile()
	if t != nil {
		t = qualifyTilesFor(transit, uuid, []*pb.Tile{t})[0]
	}
	return &pb.SetFramingResponse{Tile: t}, nil
}

// ShellSessionAlive routes the per-descent probe to the namespace holding the
// PTY. With disable_shells every session is dead by design, a verdict; any
// other failure is the owner's to answer and the client's to report.
func (rt *router) ShellSessionAlive(ctx context.Context, req *pb.ShellSessionAliveRequest) (*pb.ShellSessionAliveResponse, error) {
	if rt.srv.cfg.DisableShells {
		return &pb.ShellSessionAliveResponse{Alive: false}, nil
	}
	c, local, _, _, err := rt.route(req.TileId)
	if err != nil {
		return nil, err
	}
	return c.ShellSessionAlive(ctx, &pb.ShellSessionAliveRequest{TileId: local})
}

// Subscribe fans every watching namespace's events (Info.watch) into the
// client's, re-qualified. Failures heal through namespace.Refollow and are
// told as EventPluginHealth. The stream also keeps its session's interest
// counted (interest.Book.Open).
func (rt *router) Subscribe(ctx context.Context, req *pb.SubscribeRequest, send func(*pb.Event) error) error {
	if s := req.GetSession(); s != "" {
		defer rt.srv.interest.Open(s)()
	}
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events := make(chan *pb.Event, 64)
	for _, n := range rt.srv.namespaces() {
		if n.Transit {
			// The transport has no handshake to ask.
			go watchPlugin(subCtx, n.UUID, true, n.NS,
				func(context.Context) (*pb.InfoResponse, error) { return &pb.InfoResponse{}, nil }, events)
			continue
		}
		go watchPlugin(subCtx, n.UUID, false, n.NS,
			func(ctx context.Context) (*pb.InfoResponse, error) { return rt.srv.pluginInfo(ctx, n.UUID) }, events)
	}

	for {
		select {
		case ev := <-events:
			if err := send(ev); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// watchPlugin fans plugin uuid's events into the client's stream until ctx
// ends. The Info fetch rides namespace.Refollow's loop, so a plugin slow to
// start is not excluded for good.
func watchPlugin(ctx context.Context, uuid string, transit bool, ns namespace.Namespace, infoOf func(context.Context) (*pb.InfoResponse, error), events chan<- *pb.Event) {
	namespace.Refollow{
		Label: "subscribe: plugin " + uuid,
		Down:  func(detail string) { reportHealth(ctx, events, uuid, false, detail) },
		Up:    func() { reportHealth(ctx, events, uuid, true, "") },
		Attempt: func(ctx context.Context, established func()) error {
			if _, err := infoOf(ctx); err != nil {
				return err
			}
			return namespace.Follow(ctx, ns, &pb.SubscribeRequest{},
				func(ev *pb.Event) error {
					select {
					case events <- qualifyEvent(uuid, transit, ev):
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}, established)
		},
	}.Run(ctx)
}

// reportHealth pushes an EventPluginHealth down the path a namespace's own
// events take. Best-effort against ctx ending mid-send.
func reportHealth(ctx context.Context, events chan<- *pb.Event, uuid string, healthy bool, detail string) {
	select {
	case events <- rpc.HealthEvent(uuid, healthy, detail):
	case <-ctx.Done():
	}
}

// isUnimplemented is how a namespace's "method not supported" becomes a silent
// no-op, as with SetFraming on a plugin that keeps no framing.
func isUnimplemented(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := status.FromError(err); ok {
		return st.Code() == gcodes.Unimplemented
	}
	return false
}

// Info describes this node to a mounter, on the connection door only.
func (rt *router) Info(ctx context.Context, _ *pb.InfoRequest) (*pb.InfoResponse, error) {
	// A mount lands where a direct client lands (rpc.HomeGrid).
	lp, err := rt.Handshake(ctx, &pb.HandshakeRequest{})
	if err != nil {
		return nil, err
	}
	return &pb.InfoResponse{
		Writable:   false,
		RootGridId: rpc.HomeGrid(lp),
	}, nil
}

// Probe routes by tile id: presence is the owning namespace's verdict, never
// inferred from reachability.
func (rt *router) Probe(ctx context.Context, req *pb.ProbeRequest) (*pb.ProbeResponse, error) {
	c, local, ok := rt.srv.clientForID(req.TileId)
	if !ok {
		return nil, undeclared(req.TileId)
	}
	return c.Probe(ctx, &pb.ProbeRequest{TileId: local})
}

// OpenShell attaches a tile's PTY through the one shell route, the same one
// the browser's /shell WebSocket enters by (shell_door.go).
func (rt *router) OpenShell(ctx context.Context, recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error {
	first, err := recv()
	if err != nil {
		return err
	}
	return rt.srv.openShellRoute(ctx, first, recv, send)
}
