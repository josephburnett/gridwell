package node

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The listener seam of the two doors: Start binds the web door where config
// says and the federation door as a 0600 unix socket at federation.socket,
// never TCP, so the ungated gRPC export is reachable by the owning uid only
// while the web door keeps its Connect API. A fresh home's first BuildConfig
// mints the password, so the web door is gated from the first serve.
func TestStartBindsFederationOnASocketOnly(t *testing.T) {
	home := t.TempDir()
	cfg, err := BuildConfig(home, filepath.Join(home, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebPassword == "" || cfg.Federation.Socket != filepath.Join(home, "federation.sock") {
		t.Fatalf("built config: web %+v federation %+v", cfg.Web, cfg.Federation)
	}
	cfg.Web.Bind = "127.0.0.1:0"
	n, err := Start(Options{Home: home, Cfg: cfg})
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
	res, err := http.Get("http://" + n.Ln.Addr().String() + "/gridwell.v1.Gridwell/Handshake")
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
	// The password is the file beside the config: minted by BuildConfig,
	// stable across serves, rotated by deleting it.
	pwFile := filepath.Join(home, "web-password")
	if st, err := os.Stat(pwFile); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("web-password file = %v %v, want 0600", st, err)
	}
	same, err := BuildConfig(home, filepath.Join(home, "server.yaml"))
	if err != nil || same.WebPassword != cfg.WebPassword {
		t.Fatalf("password changed between serves: %v", err)
	}
	if err := os.Remove(pwFile); err != nil {
		t.Fatal(err)
	}
	rotated, err := BuildConfig(home, filepath.Join(home, "server.yaml"))
	if err != nil || rotated.WebPassword == cfg.WebPassword {
		t.Fatal("deleting web-password must rotate on the next serve")
	}
}
