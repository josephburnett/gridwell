package server

// The node export: the same router the browser talks to, re-served over raw
// gRPC on the connection door. Ids compose across hops because the router peels
// exactly one segment per request and prepends exactly one per response, so
// there is no name-based selection and no scoping header.

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"time"

	"google.golang.org/grpc"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// WebHandler is the browser door: static files, Connect RPCs, the /shell
// socket and the /content/ pages, behind the password gate in auth.go. Raw
// gRPC is not demuxed here, so `web.bind` on a network address exposes exactly
// that gated surface.
func (s *Server) WebHandler() http.Handler { return s.authWrap(s.mux) }

// WebDoorServer is the web door's one server shape, so the node and every test
// harness put the same server in front of the browser handler; the node sets
// BaseContext per its own listener. ReadHeaderTimeout stays here alone,
// because this door carries no raw-gRPC stream for a deadline to cut, and the
// absent Protocols is what refuses raw gRPC (TestWebDoorServesNoGRPC).
func WebDoorServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}
}

// ConnectionHandler is the connection door: the Gridwell service over raw gRPC,
// what a remote mounter's ssh tunnel dials, and internal/connection/dial is the
// other end. Its gate is the kernel, the 0600 unix socket ListenConnectionDoor
// opens, with ssh as the authenticated transport. Serve it with
// ConnectionDoorServer.
func (s *Server) ConnectionHandler() http.Handler {
	g := grpc.NewServer(grpc.UnaryInterceptor(traceUnary), grpc.StreamInterceptor(traceStream))
	pb.RegisterGridwellServer(g, namespace.Server(newRouter(s)))
	return g
}

// ConnectionDoorServer is the connection door's one server shape, so a test
// that holds a stream through it holds it through what the node runs.
//
// No deadline of any kind, deliberately. net/http hands every one of them to
// the HTTP/2 server as a ticking close on a long-lived gRPC stream (Go
// 1.26.6). A slow-header peer is no concern on a 0600 unix socket, and gRPC
// keepalive polices a silent one.
func ConnectionDoorServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, Protocols: NodeProtocols()}
}

// ListenConnectionDoor opens the connection door's one listener shape: a 0600
// unix socket, whose mode is the door's whole gate. It unlinks a stale socket
// from a crashed serve first; the serve lock guarantees no live holder.
func ListenConnectionDoor(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("connection door: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("connection door: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("connection door: %w", err)
	}
	return ln, nil
}

// NodeProtocols is the protocol set for the connection door: HTTP/1.1 plus
// unencrypted HTTP/2, because the ssh tunnel is already private and TLS-only
// h2 would refuse the mounter.
func NodeProtocols() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	return p
}
