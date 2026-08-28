package server

import (
	"context"
	"net"
	"net/http"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// The two doors are DIFFERENT handlers (2026-08-26): the web door no
// longer demuxes raw gRPC to the node export, so a listener bound to a
// network address serves exactly the gated surface. Pinned at the
// handler seam: a gRPC client against WebHandler gets no Gridwell
// service, while the same call against FederationHandler answers.
func TestWebDoorServesNoGRPC(t *testing.T) {
	srv := mustNew(t, plugin.NewRegistry(), Config{NodeID: "node1"})
	serve := func(h http.Handler) string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		hs := &http.Server{Handler: h, Protocols: NodeProtocols()}
		go hs.Serve(ln)
		t.Cleanup(func() { hs.Close() })
		return ln.Addr().String()
	}
	info := func(addr string) error {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_, err = gridwellv1.NewGridwellClient(conn).Info(context.Background(), &gridwellv1.InfoRequest{})
		return err
	}
	if err := info(serve(srv.WebHandler())); err == nil {
		t.Fatal("the web door answered a raw gRPC Info: the node export leaked onto the browser listener")
	}
	if err := info(serve(srv.FederationHandler())); err != nil {
		t.Fatalf("the federation door refused Info: %v", err)
	}
}
