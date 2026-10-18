package node

import (
	"path/filepath"
	"testing"
)

// The CLI both defers Close and calls it explicitly, so the second call must
// return the first call's verdict without shutting anything down twice.
func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	cfg, err := BuildConfig(dir, filepath.Join(dir, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Web.Bind = "127.0.0.1:0"
	n, err := Start(Options{Dir: dir, Cfg: cfg})
	if err != nil {
		t.Fatal(err)
	}
	n.ServeBackground()
	if err := n.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Fatalf("second Close: %v (must be a no-op)", err)
	}
}
