package node

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The listener seam of the two-door decision (2026-08-26): Start binds
// the web door where config says and the federation door as a 0600 UNIX
// SOCKET at federation.socket — never TCP — so the ungated gRPC export
// is reachable by the owning uid only, and the web door keeps its
// Connect API. A fresh home's init minted the password, so the web door
// is gated from the first serve.
func TestStartBindsFederationOnASocketOnly(t *testing.T) {
	home := t.TempDir()
	if _, err := InitPlugin(home, "local", "home", nil); err != nil {
		t.Fatal(err)
	}
	cfg, err := BuildConfig(home, filepath.Join(home, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Web.Password == "" || cfg.Federation.Socket != filepath.Join(home, "federation.sock") {
		t.Fatalf("built config: web %+v federation %+v", cfg.Web, cfg.Federation)
	}
	cfg.Web.Bind = "127.0.0.1:0"
	n, err := Start(Options{Home: home, Cfg: cfg, Factories: WithNativeLocal(nil)})
	if err != nil {
		t.Fatal(err)
	}
	n.ServeBackground()

	if n.FedLn.Addr().Network() != "unix" || n.FedLn.Addr().String() != cfg.Federation.Socket {
		t.Fatalf("federation door = %s %s, want the unix socket %s", n.FedLn.Addr().Network(), n.FedLn.Addr(), cfg.Federation.Socket)
	}
	st, err := os.Stat(cfg.Federation.Socket)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v (%v), want 0600", st.Mode(), err)
	}
	info := func(target string) error {
		conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_, err = gridwellv1.NewGridwellClient(conn).Info(context.Background(), &gridwellv1.InfoRequest{})
		return err
	}
	if err := info("unix:" + cfg.Federation.Socket); err != nil {
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
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the web door answered %d without the cookie, want 401 (the password is required)", res.StatusCode)
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.Federation.Socket); !os.IsNotExist(err) {
		t.Errorf("socket not unlinked on close: %v", err)
	}
	// A home without a password does not serve.
	bare := t.TempDir()
	if _, err := InitPlugin(bare, "local", "home", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bare, "server.yaml"), []byte("plugins:\n  - id: abc1234\n    name: home\n    kind: local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildConfig(bare, filepath.Join(bare, "server.yaml")); err == nil || !strings.Contains(err.Error(), "web.password") {
		t.Fatalf("a passwordless home must refuse to serve, naming web.password: %v", err)
	}
}
