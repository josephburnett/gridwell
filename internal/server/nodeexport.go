package server

// The node export: the same router the browser talks to, re-served over raw
// gRPC on the node's federation socket, which is the surface a remote
// mounter's ssh tunnel dials. It is a codec, not a second router:
// namespace.Server writes the one in-process router onto gridwell.v1, exactly
// as the Connect handler writes it onto Connect. Nothing here routes.
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

// WebHandler is the browser door: static files, Connect RPCs, the /shell
// socket, and the /content/ pages, behind the password gate in auth.go. It is
// the handler for the `web.bind` listener, the only one that may face a
// network. Raw gRPC is not demuxed here, so a request for the node export on
// this door is just an unknown route and binding the web UI to a network
// address exposes exactly the gated surface and nothing else.
func (s *Server) WebHandler() http.Handler { return s.authWrap(s.mux) }

// FederationHandler is the node door: the Gridwell service over raw gRPC,
// what a remote mounter's ssh tunnel dials. It is ungated by design and served
// only on the 0600 unix socket node.listenFederation opens; there is no
// address form, and ssh is the authenticated transport between nodes. Serve it
// with NodeProtocols, because gRPC over a cleartext tunnel needs HTTP/2
// without TLS, which plain net/http refuses by default.
//
// This and internal/connection/dial are the federation hop's two ends, and the two
// ends of the only gRPC in the node besides the plugin subprocess: one writes
// the router onto the wire (namespace.Server), the other reads it back off
// (namespace.FromClient).
func (s *Server) FederationHandler() http.Handler {
	g := grpc.NewServer()
	pb.RegisterGridwellServer(g, namespace.Server(newRouter(s)))
	return g
}

// NodeProtocols is the protocol set for any http.Server serving the federation
// door: HTTP/1.1 plus unencrypted HTTP/2 for raw gRPC through an ssh tunnel,
// where the connection is already private and TLS-only h2 would refuse the
// mounter. One owner here, so the production server and every test harness
// negotiate identically.
func NodeProtocols() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	return p
}
