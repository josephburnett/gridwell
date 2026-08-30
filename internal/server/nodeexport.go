package server

// The node export: the same router the browser talks to, re-served over raw
// gRPC on the node's federation socket — the surface a remote mounter's ssh
// tunnel dials. It is a CODEC, not a second router: namespace.Server writes
// the one in-process router (router.go) onto gridwell.v1, exactly as the
// Connect handler writes it onto Connect. Nothing here routes, and there is
// no per-method delegation left to drift.
//
// Ids compose across hops by construction: the router peels exactly one
// segment per request and prepends exactly one segment per response
// (qualifyTiles / qualifyTilesTransit), so <conn>/<plugin>/<id> chains route
// generically through any number of nodes. There is no name-based selection
// and no scoping header — routing is by id, always.

import (
	"net/http"

	"google.golang.org/grpc"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// WebHandler is the BROWSER door: static, Connect RPCs, the /shell socket,
// and the /content/ pages — behind the password gate (auth.go). This is the
// handler for the `web.bind` listener, the only one that may face a
// network. Raw gRPC is NOT demuxed here (it was, until 2026-08-26): a
// request for the node export on this door is just an unknown route, so
// binding the web UI to a tailnet address exposes exactly the gated surface
// and nothing else.
func (s *Server) WebHandler() http.Handler { return s.authWrap(s.mux) }

// FederationHandler is the NODE door: the Gridwell service over raw gRPC,
// what a remote mounter's ssh tunnel dials. Ungated by design and served
// ONLY on the 0600 unix socket node.listenFederation opens
// (config.FederationSocket; there is no address form) — ssh is the
// authenticated transport between nodes. Serve it with NodeProtocols: gRPC
// over a cleartext tunnel needs HTTP/2 without TLS, which plain net/http
// refuses by default.
//
// This and internal/remote/dial are the federation hop's two ends, and the
// two ends of the LAST gRPC in the node besides the plugin subprocess
// (docs/simplify-plan.md S2): one writes the router onto the wire
// (namespace.Server), the other reads it back off (namespace.FromClient).
func (s *Server) FederationHandler() http.Handler {
	g := grpc.NewServer()
	pb.RegisterGridwellServer(g, namespace.Server(newRouter(s)))
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
