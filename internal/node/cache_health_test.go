package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
)

// A cache.db that cannot be opened fails no read: the node answers everything,
// and only serve-first and offline reading are gone. So the whole seam is the
// test — the node started over a cache path that will not open, the transport
// the cache fronts, the router's fan-in, and a real subscriber on the
// connection door — because every layer of it is what carries the fact from
// the failed open to the client's strip.

func TestAnUnopenableCacheSurfacesAsHealth(t *testing.T) {
	home := t.TempDir()
	// A directory where the file belongs: the open fails for a reason the
	// node cannot fix, without touching the node's own store.
	if err := os.Mkdir(config.CacheFile(home), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := serveNode(t, home)
	h := firstUnhealthy(t, cfg, 10*time.Second)
	if h == nil {
		t.Fatal("the node served with no source cache and said nothing; the user is owed the reason offline reading is gone")
	}
	if !strings.Contains(h.Detail, "source cache") {
		t.Errorf("health detail = %q, want the cache named in it", h.Detail)
	}
	if h.PluginUuid != cfg.ID {
		t.Errorf("health uuid = %q, want the transport's %q, or the client cannot key the notice", h.PluginUuid, cfg.ID)
	}
}

// The other half: the health is the broken cache's, not every node's.
func TestAnOpenedCacheReportsNothing(t *testing.T) {
	cfg := serveNode(t, t.TempDir())
	if h := firstUnhealthy(t, cfg, 2*time.Second); h != nil {
		t.Fatalf("a node with a working cache reported %q", h.Detail)
	}
}

// serveNode starts a real node on home and serves it until the test ends.
func serveNode(t *testing.T, home string) *config.ServerConfig {
	t.Helper()
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
	t.Cleanup(func() { _ = n.Close() })
	return cfg
}

// firstUnhealthy subscribes on the connection door as any client does and
// returns the first unhealthy report, or nil if the window closes first.
func firstUnhealthy(t *testing.T, cfg *config.ServerConfig, within time.Duration) *gridwellv1.EventPluginHealth {
	t.Helper()
	conn, err := grpc.NewClient("unix:"+cfg.Federation.Socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	stream, err := gridwellv1.NewGridwellClient(conn).Subscribe(ctx, &gridwellv1.SubscribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for {
		ev, err := stream.Recv()
		if err != nil {
			return nil
		}
		if h := ev.GetPluginHealth(); h != nil && !h.Healthy {
			return h
		}
	}
}
