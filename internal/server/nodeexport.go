package server

// The node export: the same Gridwell service the browser client consumes,
// re-served over raw gRPC on the node's loopback federation port — the
// surface a remote mounter's ssh tunnel dials. Every request is routed by the
// QUALIFIED ids it carries, exactly like the Connect front door (the unary
// methods literally delegate to the same connectHandler, so the two surfaces
// cannot drift), plus the streams: OpenShell (bidi PTY), ReadContent /
// WriteContent, Subscribe — each routed by the id in its (first) message.
// (GetSession/PutSession are gone — 2026-07-26, the session is host-local.)
//
// Ids compose across hops by construction: this node peels exactly one
// segment per request and prepends exactly one segment per response
// (qualifyTiles / qualifyTilesTransit), so <ssh>/<plugin>/<id> chains route
// generically through any number of nodes. There is no name-based selection
// and no scoping header — routing is by id, always.

import (
	"context"
	"errors"
	"io"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// WebHandler is the BROWSER door: static, Connect RPCs, SSE, and the
// /content/ pages — behind the password gate when one is configured
// (auth.go). This is the handler for the `web.bind` listener, the only
// one that may face a network. Raw gRPC is NOT demuxed here (it was,
// until 2026-08-26): a request for the node export on this door is
// just an unknown route, so binding the web UI to a tailnet address
// exposes exactly the gated surface and nothing else.
func (s *Server) WebHandler() http.Handler { return s.authWrap(s.mux) }

// FederationHandler is the NODE door: the Gridwell service over raw
// gRPC, what a remote mounter's ssh tunnel and the desktop's shell relay
// dial. Ungated by design and served ONLY on loopback (node.Start binds
// it to config.FederationAddr; there is no address field to bind it
// elsewhere) — ssh is the authenticated transport between nodes. Serve
// it with NodeProtocols: gRPC over a cleartext tunnel needs HTTP/2
// without TLS, which plain net/http refuses by default.
func (s *Server) FederationHandler() http.Handler {
	g := grpc.NewServer()
	pb.RegisterGridwellServer(g, &nodeExport{srv: s, h: newConnectHandler(s)})
	return g
}

// NodeProtocols is the protocol set for any http.Server serving the
// federation door: HTTP/1.1 plus UNENCRYPTED HTTP/2 for raw gRPC
// through an ssh tunnel (the connection is already private; TLS-only h2
// would refuse the mounter). This replaced the deprecated x/net h2c wrapper
// (Go 1.24's Server.Protocols is the supported form); one owner here so the
// production server and every test harness negotiate identically.
func NodeProtocols() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	return p
}

// nodeExport implements the Gridwell service over gRPC by delegating every
// unary method to the connectHandler (one routing implementation) and routing
// each stream by the id its first message carries.
type nodeExport struct {
	pb.UnimplementedGridwellServer
	srv *Server
	h   *connectHandler
}

// statusErr converts a connect error back to a gRPC status error so codes
// survive the hop (a *connect.Error returned through grpc-go would otherwise
// collapse to Unknown).
func statusErr(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		switch ce.Code() {
		case connect.CodeNotFound:
			return status.Error(gcodes.NotFound, ce.Message())
		case connect.CodeInvalidArgument:
			return status.Error(gcodes.InvalidArgument, ce.Message())
		case connect.CodeFailedPrecondition:
			return status.Error(gcodes.FailedPrecondition, ce.Message())
		case connect.CodePermissionDenied:
			return status.Error(gcodes.PermissionDenied, ce.Message())
		default:
			return status.Error(gcodes.Internal, ce.Message())
		}
	}
	return err
}

// Info describes THIS NODE: its identity, its node grid (the plugin-list
// landing page a mounter's descent lands on), and its capabilities. Watch is
// true because Subscribe fans in every plugin's events.
func (n *nodeExport) Info(ctx context.Context, _ *pb.InfoRequest) (*pb.InfoResponse, error) {
	if n.srv.cfg.NodeID == "" {
		return nil, status.Error(gcodes.FailedPrecondition, "this node has no node_id; run `gridwell serve` once to mint one")
	}
	return &pb.InfoResponse{
		Kind:       "node",
		Watch:      true,
		Writable:   false,
		RootGridId: n.srv.cfg.NodeID + "/" + nodeGridID,
	}, nil
}

// Probe routes by tile id to the owning plugin — presence is the plugin's
// verdict, never inferred from reachability.
func (n *nodeExport) Probe(ctx context.Context, r *pb.ProbeRequest) (*pb.ProbeResponse, error) {
	c, local, ok := n.srv.clientForID(r.TileId)
	if !ok {
		return nil, status.Errorf(gcodes.NotFound, "no plugin for %q", r.TileId)
	}
	return c.Probe(ctx, &pb.ProbeRequest{TileId: local})
}

// ── unary delegates: one routing implementation, two wire surfaces ───────────

func (n *nodeExport) ListPlugins(ctx context.Context, r *pb.ListPluginsRequest) (*pb.ListPluginsResponse, error) {
	resp, err := n.h.ListPlugins(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, statusErr(err)
	}
	return resp.Msg, nil
}

func (n *nodeExport) GetGrid(ctx context.Context, r *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	resp, err := n.h.GetGrid(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, statusErr(err)
	}
	return resp.Msg, nil
}

func (n *nodeExport) GetTile(ctx context.Context, r *pb.GetTileRequest) (*pb.TileResponse, error) {
	resp, err := n.h.GetTile(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, statusErr(err)
	}
	return resp.Msg, nil
}

func (n *nodeExport) GetTilePreview(ctx context.Context, r *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	resp, err := n.h.GetTilePreview(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, statusErr(err)
	}
	return resp.Msg, nil
}

func (n *nodeExport) CreateTile(ctx context.Context, r *pb.CreateTileRequest) (*pb.TileResponse, error) {
	resp, err := n.h.CreateTile(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, statusErr(err)
	}
	return resp.Msg, nil
}

func (n *nodeExport) SetTile(ctx context.Context, r *pb.SetTileRequest) (*pb.TileResponse, error) {
	resp, err := n.h.SetTile(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, statusErr(err)
	}
	return resp.Msg, nil
}

func (n *nodeExport) CloneTile(ctx context.Context, r *pb.CloneTileRequest) (*pb.TileResponse, error) {
	resp, err := n.h.CloneTile(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, statusErr(err)
	}
	return resp.Msg, nil
}

func (n *nodeExport) PlaceTile(ctx context.Context, r *pb.PlaceTileRequest) (*pb.TileResponse, error) {
	resp, err := n.h.PlaceTile(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, statusErr(err)
	}
	return resp.Msg, nil
}

func (n *nodeExport) DeleteTile(ctx context.Context, r *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	resp, err := n.h.DeleteTile(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, statusErr(err)
	}
	return resp.Msg, nil
}

func (n *nodeExport) SetRootView(ctx context.Context, r *pb.SetRootViewRequest) (*pb.SetRootViewResponse, error) {
	resp, err := n.h.SetRootView(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, statusErr(err)
	}
	return resp.Msg, nil
}

func (n *nodeExport) Search(ctx context.Context, r *pb.SearchRequest) (*pb.SearchResponse, error) {
	resp, err := n.h.Search(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, statusErr(err)
	}
	return resp.Msg, nil
}

func (n *nodeExport) ShellSessionAlive(ctx context.Context, r *pb.ShellSessionAliveRequest) (*pb.ShellSessionAliveResponse, error) {
	resp, err := n.h.ShellSessionAlive(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, statusErr(err)
	}
	return resp.Msg, nil
}

// ── streams: routed by the id in the (first) message ─────────────────────────

// Subscribe streams the whole node's fan-in — the same body the Connect
// stream serves — so a mounter hears every plugin on this node change.
func (n *nodeExport) Subscribe(_ *pb.SubscribeRequest, stream pb.Gridwell_SubscribeServer) error {
	return statusErr(n.h.subscribe(stream.Context(), stream.Send))
}

// OpenShell peeks the bind message for the tile id, routes to the owning
// plugin with the id peeled one segment, and pipes both directions.
func (n *nodeExport) OpenShell(stream pb.Gridwell_OpenShellServer) error {
	// The node-wide shell refusal (server.yaml disable_shells): every PTY
	// door on this node is closed, whichever plugin (local or mounted)
	// would hold the session.
	if n.srv.cfg.DisableShells {
		return status.Error(gcodes.PermissionDenied, "shell tiles are disabled on this node (server.yaml disable_shells)")
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	c, local, ok := n.srv.clientForID(first.TileId)
	if !ok {
		return status.Errorf(gcodes.NotFound, "no plugin for shell tile %q", first.TileId)
	}
	up, err := c.OpenShell(stream.Context())
	if err != nil {
		return err
	}
	if err := up.Send(&pb.OpenShellRequest{TileId: local, Data: first.Data, Resize: first.Resize}); err != nil {
		return err
	}
	errc := make(chan error, 2)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				_ = up.CloseSend()
				errc <- err
				return
			}
			if err := up.Send(msg); err != nil {
				errc <- err
				return
			}
		}
	}()
	go func() {
		for {
			msg, err := up.Recv()
			if err != nil {
				errc <- err
				return
			}
			if err := stream.Send(msg); err != nil {
				errc <- err
				return
			}
		}
	}()
	err = <-errc
	if err == io.EOF {
		return nil
	}
	return err
}

// ReadContent routes through contentRoute — the same link-resolution point
// the Connect door uses — so a remote mounter reading a link tile's content
// gets the target's bytes exactly like a local caller. Chunks carry no ids,
// so nothing needs re-qualification on the way back.
func (n *nodeExport) ReadContent(r *pb.ReadContentRequest, stream pb.Gridwell_ReadContentServer) error {
	c, local, err := n.srv.contentRoute(stream.Context(), r.TileId)
	if err != nil {
		return err
	}
	up, err := c.ReadContent(stream.Context(), &pb.ReadContentRequest{TileId: local})
	if err != nil {
		return err
	}
	for {
		chunk, err := up.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := stream.Send(chunk); err != nil {
			return err
		}
	}
}

// ServeContent forwards a web-content request one hop, resolving links via
// contentRoute like ReadContent — this is how a mounted remote node's pages
// reach the local /content/ door: HTTP terminates at the LOCAL door and the
// request rides this RPC through the tunnel.
func (n *nodeExport) ServeContent(r *pb.ServeContentRequest, stream pb.Gridwell_ServeContentServer) error {
	c, local, err := n.srv.contentRoute(stream.Context(), r.TileId)
	if err != nil {
		return err
	}
	up, err := c.ServeContent(stream.Context(), &pb.ServeContentRequest{TileId: local, Subpath: r.Subpath})
	if err != nil {
		return err
	}
	for {
		chunk, err := up.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := stream.Send(chunk); err != nil {
			return err
		}
	}
}

// WriteContent peeks the bind message for the tile id, peels one segment, and
// relays upstream; the upstream close — and so the commit — happens only after
// a clean inbound end-of-stream. The TileResponse carries ids, so it is
// re-qualified like every tile-returning verb.
func (n *nodeExport) WriteContent(stream pb.Gridwell_WriteContentServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	uuid, local, ok := rpc.SplitID(first.TileId)
	if !ok {
		return status.Errorf(gcodes.InvalidArgument, "unqualified id %q", first.TileId)
	}
	c, found := n.srv.routeClient(uuid)
	if !found {
		return status.Errorf(gcodes.NotFound, "no plugin %q", uuid)
	}
	up, err := c.WriteContent(stream.Context())
	if err != nil {
		return err
	}
	if err := up.Send(&pb.WriteContentRequest{TileId: local, Version: first.Version, Data: first.Data}); err != nil {
		return err
	}
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			resp, cerr := up.CloseAndRecv()
			if cerr != nil {
				return cerr
			}
			t := resp.GetTile()
			if t != nil {
				t = qualifyTilesFor(n.srv.pluginReg.Transit(uuid), uuid, []*pb.Tile{t})[0]
			}
			return stream.SendAndClose(&pb.TileResponse{Tile: t})
		}
		if err != nil {
			return err
		}
		if err := up.Send(msg); err != nil {
			return err
		}
	}
}
