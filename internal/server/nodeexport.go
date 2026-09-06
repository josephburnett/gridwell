package server

// The node export: the same router the browser talks to, re-served over raw
// gRPC on the node's connection door, which is the surface a remote
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

// ConnectionHandler is the connection door: the Gridwell service over raw
// gRPC, what a remote mounter's ssh tunnel dials. It is ungated by design and
// served only on the 0600 unix socket node.listenConnectionDoor opens; there is no
// address form, and ssh is the authenticated transport between nodes. Serve it
// with ConnectionDoorServer, because gRPC over a cleartext tunnel needs HTTP/2
// without TLS, which plain net/http refuses by default, and because the door
// must carry no deadline (see there).
//
// This and internal/connection/dial are the connection hop's two ends, and the two
// ends of the only gRPC in the node besides the plugin subprocess: one writes
// the router onto the wire (namespace.Server), the other reads it back off
// (namespace.FromClient).
func (s *Server) ConnectionHandler() http.Handler {
	g := grpc.NewServer()
	pb.RegisterGridwellServer(g, namespace.Server(newRouter(s)))
	return g
}

// ConnectionDoorServer is the http.Server that serves the connection door:
// the one shape, so the production node and every harness put the same
// server in front of h, and a test that holds a stream through it holds it
// through what the node runs.
//
// No deadline of any kind, deliberately. The door carries long-lived gRPC
// streams (the event Subscribe above all) over unencrypted HTTP/2, and
// net/http arms ReadHeaderTimeout on the raw conn before it hands the
// conn to the HTTP/2 server (Go 1.26.6), whose only disarm is tied to
// ReadTimeout; WriteTimeout becomes a per-stream deadline there too. Any of
// them is a ticking close on every stream and every unary call that lands
// after it, seen by the mounter as "error reading from server: EOF". A
// slow-header peer is not a concern this door has: it is a 0600 unix socket
// reachable only through the owning uid's ssh, and gRPC keepalive is what
// polices a silent peer.
func ConnectionDoorServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, Protocols: NodeProtocols()}
}

// NodeProtocols is the protocol set for any http.Server serving the connection
// door: HTTP/1.1 plus unencrypted HTTP/2 for raw gRPC through an ssh tunnel,
// where the connection is already private and TLS-only h2 would refuse the
// mounter. ConnectionDoorServer is its one caller; it is named on its own so
// the protocol set reads as the decision it is.
func NodeProtocols() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	return p
}
