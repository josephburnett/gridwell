package node

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/connection/dial"
)

// The listener seam of the two doors: the web door binds where config says,
// the connection door is a 0600 unix socket at `federation:` and never TCP,
// and a fresh home is password-gated from the first serve.
func TestStartBindsTheConnectionDoorOnASocketOnly(t *testing.T) {
	home := t.TempDir()
	cfg, err := BuildConfig(home, filepath.Join(home, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebPassword == "" || cfg.Federation.Socket != filepath.Join(home, "federation.sock") {
		t.Fatalf("built config: web %+v connection door %+v", cfg.Web, cfg.Federation)
	}
	cfg.Web.Bind = "127.0.0.1:0"
	n, err := Start(Options{Home: home, Cfg: cfg})
	if err != nil {
		t.Fatal(err)
	}
	n.ServeBackground()

	if n.ConnLn.Addr().Network() != "unix" || n.ConnLn.Addr().String() != cfg.Federation.Socket {
		t.Fatalf("connection door = %s %s, want the unix socket %s", n.ConnLn.Addr().Network(), n.ConnLn.Addr(), cfg.Federation.Socket)
	}
	st, err := os.Stat(cfg.Federation.Socket)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v (%v), want 0600", st.Mode(), err)
	}
	info := func(target string) error {
		conn, err := dial.ClientConn(target)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_, err = gridwellv1.NewGridwellClient(conn).Info(context.Background(), &gridwellv1.InfoRequest{})
		return err
	}
	if err := info("unix:" + cfg.Federation.Socket); err != nil {
		t.Fatalf("connection door: %v", err)
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
	// The password is the file beside the config, stable across serves.
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
