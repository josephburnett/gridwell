package server_test

// The shipped gitlab plugin through a GitLab outage, spawned against a fake
// GitLab and reached at the web door: a grid the plugin has walked keeps
// every tile while GitLab is down, the plugin's health says why, and the
// health comes back when GitLab does.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/plugintest/gitlabfake"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// gitlabAtTheDoor serves the gitlab binary, spawned over cfg, at a web door,
// and answers a client, the plugin's landing grid, and its health events.
func gitlabAtTheDoor(t *testing.T, ctx context.Context, uuid string, cfg map[string]string) (*rpc.Client, string, <-chan *gridwellv1.EventPluginHealth) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cp := plugintest.Spawn(t, "gitlab", cfg)
	a, stop := pluginhost.Start(cp, st.Namespace(uuid), nil, "plugin "+uuid+" watch")
	reg := plugin.NewRegistry()
	reg.Register(localNodeID, "home", local.New(st, nil), nil)
	reg.Register(uuid, "gitlab", a, stop)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	// Subscribe answers with its first event, so it is opened aside; a
	// subscriber arriving mid-outage is told at once.
	health := make(chan *gridwellv1.EventPluginHealth, 64)
	go func() {
		stream, err := cl.Subscribe(ctx)
		if err != nil {
			return
		}
		defer stream.Close()
		for {
			ev, ok, err := stream.Recv()
			if err != nil || !ok {
				return
			}
			if h := ev.GetPluginHealth(); h != nil && h.PluginUuid == uuid {
				health <- h
			}
		}
	}()
	var grid string
	for _, p := range pluginRows(t, ctx, cl) {
		if p.Uuid == uuid {
			grid = plugintest.LandingOf(t, p)
		}
	}
	if grid == "" {
		t.Fatalf("no plugin %s in the handshake", uuid)
	}
	return cl, grid, health
}

func pluginRows(t *testing.T, ctx context.Context, cl *rpc.Client) []*gridwellv1.PluginInfo {
	t.Helper()
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return lp.Plugins
}

// readUntilHealth reads grid until a health event matches want, asserting
// every read answers all of tiles: memory answers, and no read fails.
func readUntilHealth(t *testing.T, ctx context.Context, cl *rpc.Client, grid string, tiles int,
	health <-chan *gridwellv1.EventPluginHealth, want func(*gridwellv1.EventPluginHealth) bool) *gridwellv1.EventPluginHealth {
	t.Helper()
	for {
		g, err := cl.GetGrid(ctx, grid)
		if err != nil {
			t.Fatalf("a read failed: %v", err)
		}
		if len(g.Tiles) != tiles {
			t.Fatalf("a read showed %d tiles, want all %d", len(g.Tiles), tiles)
		}
		select {
		case h := <-health:
			if want(h) {
				return h
			}
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("the health never changed")
		}
	}
}

// GitLab down and a token refused after Info are the same to the user: the
// grid keeps every tile, and the plugin's health says why until a read works.
// Neither is a verdict on the grid (decision 3, 2026-10-03).
func TestAGitLabOutageKeepsTheGridAndSaysWhy(t *testing.T) {
	gl := gitlabfake.New(t, watchedTodo(1, "2026-08-18T10:00:00Z"), watchedTodo(2, "2026-08-25T10:00:00Z"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// A full-walk window of a nanosecond: every read walks GitLab, so each
	// read after the outage begins has a failed walk behind it.
	cl, grid, health := gitlabAtTheDoor(t, ctx, "pgloutage", gl.Config(t, map[string]string{"full_refresh": "1ns"}))

	before, err := cl.GetGrid(ctx, grid)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Tiles) != 2 {
		t.Fatalf("before the outage the todos are %v, want two weeks", before.Tiles)
	}
	for _, tc := range []struct {
		name   string
		set    func(bool)
		reason string
	}{
		{"GitLab down", gl.SetDown, "503"},
		{"the token refused", gl.SetRefuse, "401"},
	} {
		tc.set(true)
		down := readUntilHealth(t, ctx, cl, grid, 2, health, func(h *gridwellv1.EventPluginHealth) bool { return !h.Healthy })
		if !strings.Contains(down.Detail, tc.reason) {
			t.Errorf("%s: health = %+v, want GitLab's answer as the reason", tc.name, down)
		}
		tc.set(false)
		readUntilHealth(t, ctx, cl, grid, 2, health, func(h *gridwellv1.EventPluginHealth) bool { return h.Healthy })
	}
}
