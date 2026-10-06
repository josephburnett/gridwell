package pluginhost_test

import (
	"context"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// capsPlugin declares the plugin.v1 handshake's capabilities, writable as
// told.
type capsPlugin struct {
	pluginv1.UnimplementedPluginServer
	writable bool
}

func (p capsPlugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "caps", DisplayName: "caps", RootContext: "r", Watch: true, Writable: p.writable}, nil
}

func (capsPlugin) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	return &pluginv1.ListResponse{Authoritative: true}, nil
}

// A plugin's grids are writable exactly as it declares, since the adapter
// writes a body through to it by key, and never accept tiles, since a plugin
// creates none. Its Subscribe — the supervisor's health and the grids its
// writes changed — is checked here too, because the server's fan-in
// subscribes to every namespace and a stream that is not there sends it into
// Unimplemented retries forever.
func TestAdapterStampsTheWriteFactsThePluginDeclares(t *testing.T) {
	for _, writable := range []bool{true, false} {
		memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = memStore.Close() })
		cp, cpCloser, err := plugintest.Loopback(capsPlugin{writable: writable})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cpCloser)
		client := pluginhost.New(cp, memStore.Namespace("p1"), nil)
		ctx := context.Background()

		g, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: rpc.EntryGridID("r")})
		if err != nil {
			t.Fatal(err)
		}
		if g.Grid.Writable != writable {
			t.Errorf("declared writable %v: the grid's writable is %v", writable, g.Grid.Writable)
		}
		if g.Grid.AcceptsTiles == nil || g.Grid.GetAcceptsTiles() {
			t.Errorf("accepts_tiles = %v, want a stamped false: a plugin creates no tiles", g.Grid.AcceptsTiles)
		}
		// The stream lives as long as its context, so the context ending is
		// how it ends — never Unimplemented.
		subCtx, subCancel := context.WithCancel(ctx)
		subCancel()
		if serr := client.Subscribe(subCtx, &gridwellv1.SubscribeRequest{}, func(*gridwellv1.Event) error { return nil }); serr != nil {
			t.Errorf("Subscribe answered %v; the adapter must serve a stream", serr)
		}
	}
}

// declaringPlugin answers Info from the fields the test hands it, so one
// plugin shape covers both a host projection and an ordinary content plugin.
type declaringPlugin struct {
	pluginv1.UnimplementedPluginServer
	info *pluginv1.InfoResponse
}

func (p declaringPlugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return p.info, nil
}

func (declaringPlugin) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	return &pluginv1.ListResponse{
		Entries:       []*pluginv1.Entry{{Key: "a", Label: "a"}},
		Authoritative: true,
	}, nil
}

// The two presentation facts a grid wears are DECLARATIONS the plugin makes,
// carried to the client on the grid it serves. Nothing between the plugin and
// the pixel knows the word "fs": a plugin that projects host state says so
// with host_content, and one that does not — the gitlab shape — leaves both
// fields zero and renders as owned content. Without this the adapter would be
// free to stamp a grid from the plugin's KIND again, which is the leak the
// declared facts replaced.
func TestGridWearsThePluginsDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name string
		info *pluginv1.InfoResponse
	}{
		{"host projection", &pluginv1.InfoResponse{
			Kind: "fsish", DisplayName: "files", RootContext: "r",
			Glyph: "folder", HostContent: true,
		}},
		{"owned content", &pluginv1.InfoResponse{
			Kind: "todoish", DisplayName: "todos", RootContext: "r",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = memStore.Close() })
			cp, cpCloser, err := plugintest.Loopback(declaringPlugin{info: tc.info})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cpCloser)
			client := pluginhost.New(cp, memStore.Namespace("p1"), nil)
			ctx := context.Background()

			info, err := client.Info(ctx, &gridwellv1.InfoRequest{})
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: plugintest.Landing(t, info)})
			if err != nil {
				t.Fatal(err)
			}
			if got := resp.Grid.GetHostContent(); got != tc.info.GetHostContent() {
				t.Errorf("grid host_content = %v, declared %v", got, tc.info.GetHostContent())
			}
			if got := resp.Grid.GetGlyph(); got != tc.info.GetGlyph() {
				t.Errorf("grid glyph = %q, declared %q", got, tc.info.GetGlyph())
			}
		})
	}
}
