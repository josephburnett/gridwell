package server

import (
	"context"
	"net"
	"net/http"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// The two APIs are different handlers: the web API no
// longer demuxes raw gRPC to the node export, so a listener bound to a
// network address serves exactly the gated surface. Pinned at the
// handler seam: a gRPC client against WebHandler gets no Gridwell
// service, while the same call against ConnectionHandler answers.
func TestWebAPIServesNoGRPC(t *testing.T) {
	srv := mustNew(t, plugin.NewRegistry(), Config{})
	serve := func(h http.Handler) string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		hs := ConnectionAPIServer(h)
		go hs.Serve(ln)
		t.Cleanup(func() { hs.Close() })
		return ln.Addr().String()
	}
	info := func(addr string) error {
		conn, err := dial.ClientConn(addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_, err = gridwellv1.NewGridwellClient(conn).Info(context.Background(), &gridwellv1.InfoRequest{})
		return err
	}
	if err := info(serve(srv.WebHandler())); err == nil {
		t.Fatal("the web API answered a raw gRPC Info: the node export leaked onto the web API")
	}
	if err := info(serve(srv.ConnectionHandler())); err != nil {
		t.Fatalf("the connection API refused Info: %v", err)
	}
}
