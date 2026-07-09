package server

// The node export: the same Gridwell service the browser client consumes,
// re-served over raw gRPC on the node's one port — the surface a remote
// mounter (the ssh plugin's tunnel) dials. Every request is routed by the
// QUALIFIED ids it carries, exactly like the Connect front door (the unary
// methods literally delegate to the same connectHandler, so the two surfaces
// cannot drift), plus the three plugin-grade streams Connect doesn't carry
// for browsers: OpenShell, GetSession, PutSession — each routed by the id in
// its (first) message.
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
	"strings"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// NodeHandler wraps the server's HTTP mux in h2c and routes gRPC to the node
// export. One port then serves every caller: browsers / the Electron shell
// (HTTP/1.1 Connect, WS, static) hit the mux; raw gRPC (a remote mounter's
// tunnel) hits the export.
func (s *Server) NodeHandler() http.Handler {
	g := grpc.NewServer()
	pb.RegisterGridwellServer(g, &nodeExport{srv: s, h: newConnectHandler(s)})
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			g.ServeHTTP(w, r)
			return
		}
		s.mux.ServeHTTP(w, r)
	})
	return h2c.NewHandler(root, &http2.Server{})
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

func (n *nodeExport) GetTileContent(ctx context.Context, r *pb.GetTileContentRequest) (*pb.GetTileContentResponse, error) {
	resp, err := n.h.GetTileContent(ctx, connect.NewRequest(r))
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

func (n *nodeExport) MoveTile(ctx context.Context, r *pb.MoveTileRequest) (*pb.TileResponse, error) {
	resp, err := n.h.MoveTile(ctx, connect.NewRequest(r))
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

func (n *nodeExport) ResizeTile(ctx context.Context, r *pb.ResizeTileRequest) (*pb.TileResponse, error) {
	resp, err := n.h.ResizeTile(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, statusErr(err)
	}
	return resp.Msg, nil
}

func (n *nodeExport) UpdateText(ctx context.Context, r *pb.UpdateTextRequest) (*pb.TileResponse, error) {
	resp, err := n.h.UpdateText(ctx, connect.NewRequest(r))
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

func (n *nodeExport) SetTileAlt(ctx context.Context, r *pb.SetTileAltRequest) (*pb.TileResponse, error) {
	resp, err := n.h.SetTileAlt(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, statusErr(err)
	}
	return resp.Msg, nil
}

func (n *nodeExport) SetPaneLayout(ctx context.Context, r *pb.SetPaneLayoutRequest) (*pb.TileResponse, error) {
	resp, err := n.h.SetPaneLayout(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, statusErr(err)
	}
	return resp.Msg, nil
}

func (n *nodeExport) SetContentZoom(ctx context.Context, r *pb.SetContentZoomRequest) (*pb.TileResponse, error) {
	resp, err := n.h.SetContentZoom(ctx, connect.NewRequest(r))
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

// sessionRoute resolves a session's plugin chain: "<uuid>" (this node's
// plugin, rest "") or "<uuid>/<rest>" (forward rest through the mount).
func (n *nodeExport) sessionRoute(chain string) (pb.GridwellClient, string, bool) {
	uuid, rest, qualified := rpc.SplitID(chain)
	if !qualified {
		uuid, rest = chain, ""
	}
	c, ok := n.srv.routeClient(uuid)
	return c, rest, ok
}

// OpenShell peeks the bind message for the tile id, routes to the owning
// plugin with the id peeled one segment, and pipes both directions.
func (n *nodeExport) OpenShell(stream pb.Gridwell_OpenShellServer) error {
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

// GetSession routes by the request's plugin chain (root_grid_id): the FIRST
// segment picks the plugin here, the REST forwards for the next hop — and a
// slash-less value IS the plugin ("this plugin's session", rest empty). The
// same peel-one-segment rule as every other id, applied to the session
// boundary.
func (n *nodeExport) GetSession(r *pb.GetSessionRequest, stream pb.Gridwell_GetSessionServer) error {
	c, local, ok := n.sessionRoute(r.RootGridId)
	if !ok {
		return status.Errorf(gcodes.NotFound, "no plugin for session %q", r.RootGridId)
	}
	up, err := c.GetSession(stream.Context(), &pb.GetSessionRequest{RootGridId: local})
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

// PutSession peeks the bind chunk for the plugin chain and relays upstream
// (same peel rule as GetSession).
func (n *nodeExport) PutSession(stream pb.Gridwell_PutSessionServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	c, local, ok := n.sessionRoute(first.RootGridId)
	if !ok {
		return status.Errorf(gcodes.NotFound, "no plugin for session %q", first.RootGridId)
	}
	up, err := c.PutSession(stream.Context())
	if err != nil {
		return err
	}
	if err := up.Send(&pb.PutSessionRequest{RootGridId: local, Data: first.Data}); err != nil {
		return err
	}
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			resp, cerr := up.CloseAndRecv()
			if cerr != nil {
				return cerr
			}
			return stream.SendAndClose(resp)
		}
		if err != nil {
			return err
		}
		if err := up.Send(msg); err != nil {
			return err
		}
	}
}
