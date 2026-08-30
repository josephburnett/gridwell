package node

import (
	"path/filepath"
	"testing"
)

// TestCloseIsIdempotent pins Node.Close's contract: the CLI both defers it
// and calls it explicitly, so the second call must be a harmless no-op that
// returns the first call's verdict, never a double shutdown of listeners,
// plugins, or the store behind them.
func TestCloseIsIdempotent(t *testing.T) {
	home := t.TempDir()
	cfg, err := BuildConfig(home, filepath.Join(home, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Web.Bind = "127.0.0.1:0"
	n, err := Start(Options{Home: home, Cfg: cfg})
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
