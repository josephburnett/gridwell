package node

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The listener seam of the two-door decision (2026-08-26): Start binds
// the web door where config says and the federation door on LOOPBACK
// regardless — the config carries a port, never an address, so the
// ungated gRPC export cannot be exposed to a network by any server.yaml.
func TestStartBindsFederationOnLoopbackOnly(t *testing.T) {
	home := t.TempDir()
	if _, err := InitPlugin(home, "local", "home", nil); err != nil {
		t.Fatal(err)
	}
	cfg, err := BuildConfig(home, filepath.Join(home, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Web.Bind = "127.0.0.1:0"
	cfg.Federation.Port = 0
	n, err := Start(Options{Home: home, Cfg: cfg, Factories: WithNativeLocal(nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	n.ServeBackground()

	fedHost, _, _ := net.SplitHostPort(n.FedLn.Addr().String())
	if fedHost != "127.0.0.1" {
		t.Fatalf("federation door bound %s, want 127.0.0.1", n.FedLn.Addr())
	}
	if n.FedLn.Addr().String() == n.Ln.Addr().String() {
		t.Fatal("the two doors share one listener")
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
	if err := info(n.FedLn.Addr().String()); err != nil {
		t.Fatalf("federation door: %v", err)
	}
	if err := info(n.Ln.Addr().String()); err == nil {
		t.Fatal("the web door answered raw gRPC")
	}
	res, err := http.Get("http://" + n.Ln.Addr().String() + "/gridwell.v1.Gridwell/ListPlugins")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		t.Fatal("the web door lost its Connect API")
	}
}
