package server

import (
	"cmp"
	"context"
	"errors"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/panelayout"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/trace"
)

// router is a namespace.Namespace of qualified ids: every verb resolves one
// segment, strips it, forwards, and re-applies it on the way out. It holds no
// state; both APIs (Connect and the connection export) decode onto it.
type router struct {
	srv *Server
	// farAsked is the connection API's router: a far node asking. Only the
	// all-clear reads it (SetSelection).
	farAsked bool
}

var _ namespace.Namespace = (*router)(nil)

func newRouter(srv *Server) *router { return &router{srv: srv} }

// refuseUnclaimed is both APIs' refusal of a user write that names no claim
// (rpc.UnclaimedWrite). It lives on the router because the router serves only
// the APIs; the node's own writers call their namespace directly.
func refuseUnclaimed(req proto.Message) error {
	if why := rpc.UnclaimedWrite(req); why != "" {
		return status.Error(gcodes.InvalidArgument, why)
	}
	return nil
}

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

// tileResp applies the transit rule when the owning plugin is a connection.
func (rt *router) tileResp(ctx context.Context, uuid string, transit bool, resp *pb.TileResponse, err error) (*pb.TileResponse, error) {
	if err != nil {
		return nil, err
	}
	t := resp.GetTile()
	if t != nil {
		t = rt.qualifyOut(ctx, transit, uuid, []*pb.Tile{t})[0]
	}
	return &pb.TileResponse{Tile: t}, nil
}

// qualifyOut is qualifyTilesFor for an answer leaving the router: a row this
// node holds takes its derived facts too (stampPresented); a far row arrives
// with its own node's.
func (rt *router) qualifyOut(ctx context.Context, transit bool, uuid string, tiles []*pb.Tile) []*pb.Tile {
	out := qualifyTilesFor(transit, uuid, tiles)
	if !transit {
		rt.srv.stampPresented(ctx, out...)
	}
	return out
}

// qualifyEvent re-applies a plugin's uuid to a change event's ids, each row
// as qualifyOut leaves it; the walk is rpc.QualifyEventIDs.
func (rt *router) qualifyEvent(ctx context.Context, uuid string, transit bool, ev *pb.Event) *pb.Event {
	return rpc.QualifyEventIDs(uuid, ev, func(t *pb.Tile) *pb.Tile {
		return rt.qualifyOut(ctx, transit, uuid, []*pb.Tile{t})[0]
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
	// plugin_grids is the owning plugin's fact, from its Info; scratch_grid_id
	// is the node's, from home's. In transit both are the remote node's stamp.
	var g *pb.Grid
	if transit {
		g = rpc.TransitQualifyGrid(uuid, resp.Grid)
	} else {
		g = qualifyGrid(uuid, resp.Grid)
		if g != nil {
			// The declared facts have no other source, so a handshake that does
			// not answer fails the read rather than present wrong ones.
			info, ierr := rt.srv.pluginInfo(ctx, uuid)
			if ierr != nil {
				return nil, infoDeclarationError(req.GridId, uuid, ierr)
			}
			g.PluginGrids = rpc.QualifyPluginGrids(uuid, info.PluginGrids)
			if g.ScratchGridId, ierr = rt.srv.homeScratch(ctx); ierr != nil {
				return nil, infoDeclarationError(req.GridId, rt.srv.homeUUID(), ierr)
			}
		}
	}
	tiles := rt.qualifyOut(ctx, transit, uuid, resp.Tiles)
	rt.srv.stampDisabled(req.GridId, g, tiles)
	return &pb.GetGridResponse{Grid: g, Tiles: tiles}, nil
}

// infoDeclarationError says which grid could not be answered and why, transport-class
// unless the plugin gave a code, so the client re-asks rather than latching.
func infoDeclarationError(gridID, uuid string, err error) error {
	code := status.Code(err)
	if code == gcodes.Unknown {
		code = gcodes.Unavailable
	}
	return status.Errorf(code, "grid %s: plugin %s handshake failed, so the grid's declarations are unknown: %v", gridID, uuid, err)
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
			return nil, gwerr.DeadLink(hop, "no plugin %q", hop)
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
		// door to the plugin must agree; buildPluginInfo owns the fallbacks.
		label := rt.srv.pluginReg.Label(n.UUID)
		// A failed Info leaves info nil and the error rides along.
		info, err := rt.srv.pluginInfo(ctx, n.UUID)
		out = append(out, buildPluginInfo(n.UUID, n.Kind, label, info, err))
	}
	resp := &pb.HandshakeResponse{
		ShellsDisabled: rt.srv.cfg.DisableShells,
		// The /content/ API's capability, handed out only here, on the
		// cookie-authenticated mux.
		ContentToken: ContentToken(rt.srv.cfg.Password),
		Build:        rt.srv.build,
		Plugins:      out,
		Routes:       rt.srv.cfg.Routes,
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
	out := &pb.PluginInfo{Uuid: uuid, Label: configLabel}
	if info != nil {
		out.RootGridId, out.PluginGrids = rpc.QualifyOptionalID(uuid, info.RootGridId), rpc.QualifyPluginGrids(uuid, info.PluginGrids)
		out.Label, out.Glyph = cmp.Or(configLabel, info.DisplayName), info.Glyph
		out.RootViewCx, out.RootViewCy, out.RootViewZoom = info.RootViewCx, info.RootViewCy, info.RootViewZoom
		out.Presents = info.Presents
	} else if infoErr != nil {
		out.InfoError = infoRefusal(infoErr)
	}
	// An error alongside a live Info still rides the row.
	if out.InfoError == "" && infoErr != nil {
		out.InfoError = infoErr.Error()
	}
	out.Label = cmp.Or(out.Label, kind)
	return out
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
	out, err := rt.tileResp(ctx, uuid, transit, resp, err)
	if err == nil {
		rt.srv.stampDisabledTile(out.GetTile())
	}
	return out, err
}

// Find routes a grid_id or a scope to its owner, and an id: lookup is its
// own scope; free text with no scope fans out to every namespace, each bounded
// by rpc.FindHopTimeout, and names each it went without (rpc.FindHop).
// Every hop carries the filters (rpc.PeelRequest).
func (rt *router) Find(ctx context.Context, req *pb.FindRequest) (*pb.FindResponse, error) {
	m := req
	if err := rpc.CheckFind(m); err != nil {
		return nil, status.Error(gcodes.InvalidArgument, err.Error())
	}
	scope := cmp.Or(m.GridId, m.Scope, rpc.ParseFindQuery(m.Query).ID)
	if scope != "" {
		c, _, uuid, transit, err := rt.route(scope)
		if err != nil {
			return nil, err
		}
		resp, err := c.Find(ctx, rpc.PeelRequest(rt.hop(scope, transit), m))
		if err != nil {
			return nil, err
		}
		return rt.qualifyFind(ctx, transit, uuid, resp), nil
	}
	out := &pb.FindResponse{}
	for _, n := range rt.srv.namespaces() {
		pctx, cancel := context.WithTimeout(ctx, rpc.FindHopTimeout)
		// No routed id narrows a fan-out, so the hop is the namespace's own.
		hop := rpc.Hop{Seg: n.UUID, Via: n.UUID, Transit: n.Transit}
		resp, err := n.NS.Find(pctx, rpc.PeelRequest(hop, m))
		cancel()
		q := rt.qualifyFind(ctx, n.Transit, n.UUID, rpc.FindHop(resp, err))
		out.Results = append(out.Results, q.Results...)
		out.Skipped = append(out.Skipped, q.Skipped...)
	}
	return out, nil
}

func (rt *router) qualifyFind(ctx context.Context, transit bool, uuid string, resp *pb.FindResponse) *pb.FindResponse {
	return rpc.QualifyFindResponse(uuid, resp, func(ts []*pb.Tile) []*pb.Tile {
		return rt.qualifyOut(ctx, transit, uuid, ts)
	})
}

// CreateTile forwards to the destination grid's owner, links spelled for
// that node (spellLinks).
func (rt *router) CreateTile(ctx context.Context, req *pb.CreateTileRequest) (*pb.TileResponse, error) {
	m := req
	// The node-wide shell refusal is the authority; the menu only hides.
	if rt.srv.cfg.DisableShells && rpc.KindOf(m.Tile.GetKind()).Tmux {
		return nil, status.Error(gcodes.PermissionDenied,
			"shell tiles are disabled on this node (server.yaml disable_shells)")
	}
	c, _, uuid, transit, err := rt.route(m.GridId)
	if err != nil {
		return nil, err
	}
	if err := rt.refuseDisabled(m.GridId); err != nil {
		return nil, err
	}
	if err := rt.mintLinks(ctx, m.Tile); err != nil {
		return nil, err
	}
	if err := rt.spellLinks(ctx, m.GridId, m.Tile); err != nil {
		return nil, err
	}
	resp, err := c.CreateTile(ctx, rpc.PeelRequest(rt.hop(m.GridId, transit), m))
	return rt.tileResp(ctx, uuid, transit, resp, err)
}

// mintLinks canonicalizes the ids a tile is about to store, so a document
// reached through a link never wears a second name.
func (rt *router) mintLinks(ctx context.Context, t *pb.Tile) error {
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

// mintRef canonicalizes one qualified link; an id nothing derives
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

// CopyTile runs at the nearest node that sees both ends (rpc.SharedOwner);
// where the ends part, the copy is made here (deepcopy.go). The link gesture
// is a plain CreateTile, never a copy.
func (rt *router) CopyTile(ctx context.Context, req *pb.CopyTileRequest) (*pb.TileResponse, error) {
	m := req
	c, local, uuid, transit, err := rt.route(m.TileId)
	if err != nil {
		return nil, err
	}
	// The source is only read; the destination is written.
	if err := rt.refuseDisabled(m.DestGridId); err != nil {
		return nil, err
	}
	if rpc.SharedOwner(m.TileId, m.DestGridId, rt.srv.cfg.ID) == "" {
		return rt.copyAcrossPlugins(ctx, m, c, local, uuid, transit)
	}
	resp, err := c.CopyTile(ctx, rpc.PeelRequest(rt.hop(m.TileId, transit), m))
	return rt.tileResp(ctx, uuid, transit, resp, err)
}

// copyAcrossPlugins lands a copy at the dropped cell; the per-kind copy is
// deepCopyTile's, and this level owns the partial-copy wording.
func (rt *router) copyAcrossPlugins(ctx context.Context, m *pb.CopyTileRequest, src namespace.Namespace, srcLocal, srcUUID string, srcTransit bool) (*pb.TileResponse, error) {
	resp, err := src.GetTile(ctx, &pb.GetTileRequest{TileId: srcLocal})
	if err != nil {
		return nil, err
	}
	srcLocalTile := resp.GetTile()
	// No version claim: a copy is layout and the source row is untouched.
	dst, dstLocal, dstUUID, dstTransit, err := rt.route(m.DestGridId)
	if err != nil {
		return nil, err
	}
	out, err := rt.deepCopyTile(ctx, src, srcTransit, srcUUID, srcLocalTile, copyDst{Namespace: dst, hop: rt.hop(m.DestGridId, dstTransit), holder: m.DestGridId}, dstLocal, m.X, m.Y)
	if err != nil && out != nil {
		// The partial is visible, so say what stopped the walk.
		return nil, status.Errorf(gcodes.Aborted,
			"deep copy incomplete (the partial copy remains, delete it if unwanted): %v", err)
	}
	return rt.tileResp(ctx, dstUUID, dstTransit, out, err)
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

func (rt *router) SetTile(ctx context.Context, req *pb.SetTileRequest) (*pb.TileResponse, error) {
	if err := refuseUnclaimed(req); err != nil {
		return nil, err
	}
	c, _, uuid, transit, err := rt.route(req.TileId)
	if err != nil {
		return nil, err
	}
	if err := rt.refuseDisabled(req.TileId); err != nil {
		return nil, err
	}
	resp, err := c.SetTile(ctx, rpc.PeelRequest(rt.hop(req.TileId, transit), req))
	return rt.tileResp(ctx, uuid, transit, resp, err)
}

func (rt *router) DeleteTile(ctx context.Context, req *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	if err := refuseUnclaimed(req); err != nil {
		return nil, err
	}
	qualifiedID := req.TileId
	c, local, _, transit, err := rt.route(req.TileId)
	if err != nil {
		return nil, err
	}
	if err := rt.refuseDisabled(req.TileId); err != nil {
		return nil, err
	}
	// The layout blob is the only record of a workspace tile's ephemerals: capture
	// before the row dies, reap after only if it is really gone (trash keeps
	// them for a restore). Transit skips this; the owning node's router reaps.
	var candidates []string
	if !transit {
		candidates = rt.workspaceEphemeralCandidates(ctx, c, local, qualifiedID)
	}
	m := rpc.PeelRequest(rt.hop(req.TileId, transit), req)
	m.TileId = local
	resp, err := c.DeleteTile(ctx, m)
	if err != nil {
		return nil, err
	}
	if len(candidates) > 0 {
		// Reap only on an explicit NotFound: a missed reap is reclaimed by the
		// boot sweep, a wrong one by nothing.
		if _, err := c.GetTile(ctx, &pb.GetTileRequest{TileId: local}); status.Code(err) == gcodes.NotFound {
			left := append([]string{resp.GetTmuxLeft()}, rt.reapWorkspaceEphemerals(ctx, candidates, qualifiedID)...)
			resp = &pb.DeleteTileResponse{TmuxLeft: strings.Join(slices.DeleteFunc(left, func(s string) bool { return s == "" }), "; ")}
		}
	}
	return resp, nil
}

// workspaceEphemeralCandidates reads a workspace tile's layout blob for leaf ids
// that might be its ephemerals, nothing when unreadable. The derivation is
// panelayout.TextFocusIDs, the same one the boot sweep protects by.
func (rt *router) workspaceEphemeralCandidates(ctx context.Context, owner namespace.Namespace, localID, qualifiedID string) []string {
	tr, err := owner.GetTile(ctx, &pb.GetTileRequest{TileId: localID})
	if err != nil || tr.GetTile() == nil || rpc.KindOf(tr.GetTile().Kind).Content != rpc.ContentLayout || tr.GetTile().BlobId == 0 {
		return nil
	}
	body, err := readAllContent(ctx, owner, localID)
	if err != nil || len(body) == 0 {
		return nil
	}
	// Blob ids are already in this node's terms: the encoder strips the
	// reader's transit prefix, empty for a locally-owned workspace tile.
	ids, err := panelayout.TextFocusIDs(body)
	if err != nil {
		log.Printf("gridwell: delete %s: layout blob unreadable, reaping nothing: %v", qualifiedID, err)
		return nil
	}
	return ids
}

// reapWorkspaceEphemerals deletes the scratch-grid tiles among a destroyed workspace
// tile's captured leaves, best-effort: it must not block the user's delete. It
// answers why each shell's tmux it should have ended is still running.
func (rt *router) reapWorkspaceEphemerals(ctx context.Context, candidates []string, qualifiedID string) (left []string) {
	scratch, err := rt.srv.homeScratch(ctx)
	if err != nil {
		log.Printf("gridwell: delete %s: not reaping ephemeral candidates: home handshake failed: %v", qualifiedID, err)
		return nil
	}
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
		// Ephemeral means on the node's scratch grid.
		et, err := ec.GetTile(ctx, &pb.GetTileRequest{TileId: elocal})
		if err != nil || et.GetTile() == nil || rpc.QualifyID(euuid, et.GetTile().GridId) != scratch {
			continue // not an ephemeral: viewed content, never touched
		}
		dr, err := ec.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: elocal})
		switch {
		case err != nil && rpc.KindOf(et.GetTile().Kind).Tmux:
			left = append(left, "the shell "+id+" was not removed: "+status.Convert(err).Message())
		case err != nil:
			log.Printf("gridwell: delete %s: reaping ephemeral %s failed: %v", qualifiedID, id, err)
		default:
			left = append(left, dr.GetTmuxLeft())
		}
	}
	return left
}

// SetFraming is the one framing write. Unimplemented (a plugin that keeps no
// framing) is not an error.
func (rt *router) SetFraming(ctx context.Context, req *pb.SetFramingRequest) (*pb.SetFramingResponse, error) {
	f, err := rpc.FramingOf(req)
	if err != nil {
		return nil, status.Error(gcodes.InvalidArgument, err.Error())
	}
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
	if err := rt.refuseDisabled(ref); err != nil {
		return nil, err
	}
	out := &pb.SetFramingRequest{Cx: f.Cx(), Cy: f.Cy(), Zoom: f.Zoom()}
	if root {
		out.RootGridId = ref
	} else {
		out.TileId = ref
	}
	resp, err := c.SetFraming(ctx, rpc.PeelRequest(rt.hop(ref, transit), out))
	if err != nil {
		if status.Code(err) == gcodes.Unimplemented { // a namespace that keeps no framing
			return &pb.SetFramingResponse{}, nil
		}
		return nil, err
	}
	if root {
		return &pb.SetFramingResponse{}, nil
	}
	t := resp.GetTile()
	if t != nil {
		t = rt.qualifyOut(ctx, transit, uuid, []*pb.Tile{t})[0]
	}
	return &pb.SetFramingResponse{Tile: t}, nil
}

// clearAllSelections is the all-clear: every namespace this node declares
// clears its own grids, each in its own write, and the transport forwards it
// to each live far node. A far node's sets are its rows, so one asked over
// the connection API clears its home and plugins and forwards nothing,
// which is what keeps a cycle of connections from echoing it. A disabled
// source is read-only and skipped. Every namespace is tried, and every
// refusal is in the answer, since each is a set left uncleared.
func (rt *router) clearAllSelections(ctx context.Context, req *pb.SetSelectionRequest) (*pb.SetSelectionResponse, error) {
	var errs []error
	for _, n := range rt.srv.namespaces() {
		verdict := "ok"
		switch {
		case n.Transit && rt.farAsked:
			verdict = "skipped: asked by a far node"
		case !n.Transit && rt.srv.pluginReg.DisabledReason(n.UUID) != "":
			verdict = "skipped: disabled"
		default:
			if _, err := n.NS.SetSelection(ctx, req); err != nil {
				errs = append(errs, err)
				verdict = "refused: " + err.Error()
			}
		}
		trace.Emit("selection", "clear", verdict, map[string]string{"ns": n.UUID})
	}
	return &pb.SetSelectionResponse{}, errors.Join(errs...)
}

// SetSelection routes a selection write to the namespace owning the grid,
// every id it names peeled for that hop.
func (rt *router) SetSelection(ctx context.Context, req *pb.SetSelectionRequest) (*pb.SetSelectionResponse, error) {
	if why := rpc.SelectRefusal(req); why != "" {
		return nil, status.Error(gcodes.InvalidArgument, why)
	}
	if req.All {
		return rt.clearAllSelections(ctx, req)
	}
	c, _, _, transit, err := rt.route(req.GridId)
	if err != nil {
		return nil, err
	}
	if err := rt.refuseDisabled(req.GridId); err != nil {
		return nil, err
	}
	if _, err := c.SetSelection(ctx, rpc.PeelRequest(rt.hop(req.GridId, transit), req)); err != nil {
		return nil, err
	}
	return &pb.SetSelectionResponse{}, nil
}

// ShellTmuxAlive routes the per-descent probe to the namespace holding the
// PTY. With disable_shells every tmux is dead by design, a verdict; any
// other failure is the owner's to answer and the client's to report.
func (rt *router) ShellTmuxAlive(ctx context.Context, req *pb.ShellTmuxAliveRequest) (*pb.ShellTmuxAliveResponse, error) {
	if rt.srv.cfg.DisableShells {
		return &pb.ShellTmuxAliveResponse{Alive: false}, nil
	}
	c, local, _, _, err := rt.route(req.TileId)
	if err != nil {
		return nil, err
	}
	return c.ShellTmuxAlive(ctx, &pb.ShellTmuxAliveRequest{TileId: local})
}

// Subscribe fans every watching namespace's events (Info.watch) into the
// client's, re-qualified. Failures heal through namespace.Refollow and are
// told as EventPluginHealth; a disabled source is told so first, and only so
// after (asSwitched). Once every namespace's stream has settled the node is
// listening, and only then: the header goes out, which is what a client's
// Subscribe returns on and its resync waits for (retry.Events), and its
// session's view is counted (subscription.Book.Open), since the subscription
// is what starts a source watching and a change it reports before this stream hears
// it would reach no one.
func (rt *router) Subscribe(ctx context.Context, req *pb.SubscribeRequest, send func(*pb.Event) error) error {
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Attached before the record is read, so a disable racing this open is
	// told at least once. Nothing is sent before the header: the first word
	// flushes the response, and a client takes that for listening.
	off, detach := rt.srv.switchedOff.Subscribe()
	defer detach()
	disabled := rt.srv.pluginReg.DisabledNow()

	events := make(chan *pb.Event, 64)
	var settling sync.WaitGroup
	for _, n := range rt.srv.namespaces() {
		settling.Add(1)
		settled := sync.OnceFunc(settling.Done)
		if n.Transit {
			// The transport has no handshake to ask.
			go rt.watchPlugin(subCtx, n.UUID, true, n.NS,
				func(context.Context) (*pb.InfoResponse, error) { return &pb.InfoResponse{}, nil }, events, settled)
			continue
		}
		go rt.watchPlugin(subCtx, n.UUID, false, n.NS,
			func(ctx context.Context) (*pb.InfoResponse, error) { return rt.srv.pluginInfo(ctx, n.UUID) }, events, settled)
	}
	listening := make(chan struct{})
	go func() { settling.Wait(); close(listening) }()
	if s := req.GetSession(); s != "" {
		counted := make(chan func(), 1)
		go func() {
			<-listening
			if subCtx.Err() != nil {
				counted <- func() {}
				return
			}
			counted <- rt.srv.subscription.Open(s)
		}()
		defer func() { cancel(); (<-counted)() }()
	}

	select {
	case <-listening:
	case <-ctx.Done():
		return nil
	}
	if err := send(nil); err != nil {
		return err
	}
	trace.Emit("router", "rpc", "Subscribe listening", map[string]string{"session": req.GetSession()})
	for _, ns := range disabled {
		if err := send(rpc.DisabledEvent(ns)); err != nil {
			return err
		}
	}
	for {
		select {
		case ev := <-events:
			if err := send(rt.srv.asSwitched(ev)); err != nil {
				return err
			}
		case ev := <-off:
			if err := send(ev); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// asSwitched is the one place a disabled source's events are told: whatever
// a layer under the router says of its health, its process dying or its
// fan-in failing, it is disabled, and a row it changes takes no edits
// (stampDisabled).
func (s *Server) asSwitched(ev *pb.Event) *pb.Event {
	if h := ev.GetPluginHealth(); h != nil && s.pluginReg.Disabled(h.GetPluginUuid()) {
		return rpc.DisabledEvent(h.GetPluginUuid())
	}
	if t := ev.GetTileChanged().GetTile(); t != nil {
		if _, reason := s.disabledReason(t.GetId()); reason != "" {
			ev = proto.Clone(ev).(*pb.Event)
			s.stampDisabledTile(ev.GetTileChanged().GetTile())
		}
	}
	return ev
}

// DisableSource switches off a plugin or connection this node declares until
// the node exits (plugin.Registry.Disable), and tells every open stream, and
// every view showing one of its grids (announceDisabled).
func (rt *router) DisableSource(_ context.Context, req *pb.DisableSourceRequest) (*pb.DisableSourceResponse, error) {
	ns := req.GetNamespace()
	if err := rt.srv.pluginReg.Disable(ns); err != nil {
		return nil, status.Error(gcodes.FailedPrecondition, "disable: "+err.Error())
	}
	rt.srv.switchedOff.Publish(rpc.DisabledEvent(ns))
	rt.srv.announceDisabled(ns)
	return &pb.DisableSourceResponse{}, nil
}

// watchPlugin fans plugin uuid's events into the client's stream until ctx
// ends. The Info fetch rides namespace.Refollow's loop, so a plugin slow to
// start is not excluded for good. settled is called once the first attempt is
// established or has failed, or ctx has ended.
func (rt *router) watchPlugin(ctx context.Context, uuid string, transit bool, ns namespace.Namespace, infoOf func(context.Context) (*pb.InfoResponse, error), events chan<- *pb.Event, settled func()) {
	defer settled()
	namespace.Refollow{
		Label: "subscribe: plugin " + uuid,
		// Settled first: until every source has, nothing drains events.
		Down: func(detail string) { settled(); reportHealth(ctx, events, uuid, false, detail) },
		Up:   func() { reportHealth(ctx, events, uuid, true, "") },
		Attempt: func(ctx context.Context, established func()) error {
			if _, err := infoOf(ctx); err != nil {
				return err
			}
			return namespace.Follow(ctx, ns, &pb.SubscribeRequest{},
				func(ev *pb.Event) error {
					q := rt.qualifyEvent(ctx, uuid, transit, ev)
					// The hubs below key by the namespace's own id; this is
					// the one record holding both keys.
					trace.Emit("router", "event", "qualify", map[string]string{
						"key": rpc.EventKey(q), "local": rpc.EventKey(ev), "ns": uuid})
					select {
					case events <- q:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}, func() { established(); settled() })
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

// Info describes this node to a connecting node, on the connection API only.
func (rt *router) Info(ctx context.Context, _ *pb.InfoRequest) (*pb.InfoResponse, error) {
	// A connection lands where a direct client lands (rpc.HomeGrid).
	lp, err := rt.Handshake(ctx, &pb.HandshakeRequest{})
	if err != nil {
		return nil, err
	}
	return &pb.InfoResponse{RootGridId: rpc.HomeGrid(lp)}, nil
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
// the browser's /shell WebSocket enters by (shell_api.go).
func (rt *router) OpenShell(ctx context.Context, recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error {
	first, err := recv()
	if err != nil {
		return err
	}
	return rt.srv.openShellRoute(ctx, first, recv, send)
}
