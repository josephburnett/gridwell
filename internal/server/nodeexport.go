package server

// The node export: the server re-exports each of its plugins over the SAME
// raw gRPC service a local plugin subprocess speaks — local ids, the plugin's
// own Info/OpenShell/Get-PutSession/Subscribe — selected per request by the
// gridwell-plugin metadata key. This is what a remote mounter (the ssh
// plugin's tunnel) dials: it makes "remote" literally a transport, because
// the thing on the far end IS a plugin interface, not the client-facing API
// (which speaks qualified ids and implements only the browser subset).
//
// Forwarding is not reimplemented here: each request resolves to a
// proxy.Plugin (internal/plugin/proxy) wrapped around the target plugin's
// client — the same transparent forwarder the ssh plugin itself uses, so
// there is exactly one owner of "forward the whole service".

import (
	"context"
	"net/http"
	"strings"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/proxy"
)

// PluginHeader selects which of this node's plugins a request addresses (the
// plugin's uuid or its config name). Requests without it go to the
// client-facing handler instead. The constant is owned by internal/plugin
// (NodeExportHeader) because the dialing side stamps the same key.
const PluginHeader = plugin.NodeExportHeader

// NodeHandler wraps the server's HTTP mux in h2c and routes plugin-scoped
// gRPC to the node export. One port then serves every caller:
//
//   - browsers / the Electron shell: HTTP/1.1 Connect, WS, static — the mux;
//   - unscoped gRPC (e.g. a remote mounter resolving a plugin name via
//     ListPlugins): the same mux — the Connect handler speaks the gRPC
//     protocol natively once h2c makes HTTP/2 negotiable;
//   - gRPC carrying gridwell-plugin metadata: the per-plugin export.
func (s *Server) NodeHandler() http.Handler {
	g := grpc.NewServer()
	pb.RegisterGridwellServer(g, &nodeExport{reg: s.pluginReg})
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") &&
			r.Header.Get(PluginHeader) != "" {
			g.ServeHTTP(w, r)
			return
		}
		s.mux.ServeHTTP(w, r)
	})
	return h2c.NewHandler(root, &http2.Server{})
}

// nodeExport implements the Gridwell service by delegating every method to
// the plugin selected by the request's PluginHeader metadata.
type nodeExport struct {
	pb.UnimplementedGridwellServer
	reg registryLookup
}

// registryLookup is the slice of *plugin.Registry the export needs.
type registryLookup interface {
	Get(id string) (pb.GridwellClient, bool)
	Ordered() []struct{ UUID, Kind string }
	Label(id string) string
}

// pluginFor resolves the request's PluginHeader (uuid first, then config
// name) to a transparent forwarder for that plugin.
func (n *nodeExport) pluginFor(ctx context.Context) (*proxy.Plugin, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get(PluginHeader)
	if len(vals) == 0 || vals[0] == "" {
		// Unreachable through NodeHandler (it routes headerless gRPC to the
		// mux), but the export must not guess if reached another way.
		return nil, status.Error(gcodes.InvalidArgument, "missing "+PluginHeader+" metadata")
	}
	key := vals[0]
	if c, ok := n.reg.Get(key); ok {
		return proxy.New(c), nil
	}
	for _, p := range n.reg.Ordered() {
		if n.reg.Label(p.UUID) == key {
			c, _ := n.reg.Get(p.UUID)
			return proxy.New(c), nil
		}
	}
	return nil, status.Errorf(gcodes.NotFound, "no plugin %q on this node", key)
}

// ── unary delegates ──────────────────────────────────────────────────────────

func (n *nodeExport) Info(ctx context.Context, r *pb.InfoRequest) (*pb.InfoResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.Info(ctx, r)
}

func (n *nodeExport) Probe(ctx context.Context, r *pb.ProbeRequest) (*pb.ProbeResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.Probe(ctx, r)
}

func (n *nodeExport) ListPlugins(ctx context.Context, r *pb.ListPluginsRequest) (*pb.ListPluginsResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.ListPlugins(ctx, r)
}

func (n *nodeExport) GetGrid(ctx context.Context, r *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.GetGrid(ctx, r)
}

func (n *nodeExport) GetTile(ctx context.Context, r *pb.GetTileRequest) (*pb.TileResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.GetTile(ctx, r)
}

func (n *nodeExport) GetTileContent(ctx context.Context, r *pb.GetTileContentRequest) (*pb.GetTileContentResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.GetTileContent(ctx, r)
}

func (n *nodeExport) GetTilePreview(ctx context.Context, r *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.GetTilePreview(ctx, r)
}

func (n *nodeExport) CreateTile(ctx context.Context, r *pb.CreateTileRequest) (*pb.TileResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.CreateTile(ctx, r)
}

func (n *nodeExport) SetTile(ctx context.Context, r *pb.SetTileRequest) (*pb.TileResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.SetTile(ctx, r)
}

func (n *nodeExport) MoveTile(ctx context.Context, r *pb.MoveTileRequest) (*pb.TileResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.MoveTile(ctx, r)
}

func (n *nodeExport) CloneTile(ctx context.Context, r *pb.CloneTileRequest) (*pb.TileResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.CloneTile(ctx, r)
}

func (n *nodeExport) ResizeTile(ctx context.Context, r *pb.ResizeTileRequest) (*pb.TileResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.ResizeTile(ctx, r)
}

func (n *nodeExport) UpdateText(ctx context.Context, r *pb.UpdateTextRequest) (*pb.TileResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.UpdateText(ctx, r)
}

func (n *nodeExport) DeleteTile(ctx context.Context, r *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.DeleteTile(ctx, r)
}

func (n *nodeExport) SetTileAlt(ctx context.Context, r *pb.SetTileAltRequest) (*pb.TileResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.SetTileAlt(ctx, r)
}

func (n *nodeExport) Mount(ctx context.Context, r *pb.MountRequest) (*pb.TileResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.Mount(ctx, r)
}

func (n *nodeExport) SetRootView(ctx context.Context, r *pb.SetRootViewRequest) (*pb.SetRootViewResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.SetRootView(ctx, r)
}

func (n *nodeExport) ShellSessionAlive(ctx context.Context, r *pb.ShellSessionAliveRequest) (*pb.ShellSessionAliveResponse, error) {
	p, err := n.pluginFor(ctx)
	if err != nil {
		return nil, err
	}
	return p.ShellSessionAlive(ctx, r)
}

// ── stream delegates ─────────────────────────────────────────────────────────

func (n *nodeExport) GetSession(r *pb.GetSessionRequest, stream pb.Gridwell_GetSessionServer) error {
	p, err := n.pluginFor(stream.Context())
	if err != nil {
		return err
	}
	return p.GetSession(r, stream)
}

func (n *nodeExport) PutSession(stream pb.Gridwell_PutSessionServer) error {
	p, err := n.pluginFor(stream.Context())
	if err != nil {
		return err
	}
	return p.PutSession(stream)
}

func (n *nodeExport) OpenShell(stream pb.Gridwell_OpenShellServer) error {
	p, err := n.pluginFor(stream.Context())
	if err != nil {
		return err
	}
	return p.OpenShell(stream)
}

func (n *nodeExport) Subscribe(r *pb.SubscribeRequest, stream pb.Gridwell_SubscribeServer) error {
	p, err := n.pluginFor(stream.Context())
	if err != nil {
		return err
	}
	return p.Subscribe(r, stream)
}
