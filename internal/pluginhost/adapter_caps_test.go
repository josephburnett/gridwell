package pluginhost_test

import (
	"context"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// capsPlugin declares every capability the plugin.v1 handshake has.
type capsPlugin struct {
	pluginv1.UnimplementedPluginServer
}

func (capsPlugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "caps", DisplayName: "caps", RootContext: "r", Watch: true, Writable: true}, nil
}

// The adapter must never declare a door it cannot open. It has no Subscribe,
// so a declared watch:true would send the server's watchPlugin into
// Unimplemented retries forever, and no WriteContent, so a declared
// writable:true would offer editing the adapter refuses. Until it implements
// them, the node-facing Info answers both false whatever the plugin
// declares.
func TestAdapterDeclaresOnlyTheDoorsItOpens(t *testing.T) {
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, cpCloser, err := plugintest.Loopback(capsPlugin{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cpCloser)
	client := pluginhost.New(cp, memStore.Namespace("p1"))
	ctx := context.Background()

	info, err := client.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Watch || info.Writable {
		t.Errorf("Info = watch %v writable %v; the adapter opens neither door", info.Watch, info.Writable)
	}
	// The declarations must agree with the verbs.
	serr := client.Subscribe(ctx, &gridwellv1.SubscribeRequest{}, func(*gridwellv1.Event) error { return nil })
	if status.Code(serr) != codes.Unimplemented {
		t.Errorf("Subscribe answered %v; a false watch declaration must mean no stream", serr)
	}
	_, werr := client.WriteContent(ctx, func() (*gridwellv1.WriteContentRequest, error) {
		return &gridwellv1.WriteContentRequest{TileId: "1"}, nil
	})
	if status.Code(werr) != codes.Unimplemented {
		t.Errorf("WriteContent answered %v; a false writable declaration must mean no write door", werr)
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
			client := pluginhost.New(cp, memStore.Namespace("p1"))
			ctx := context.Background()

			info, err := client.Info(ctx, &gridwellv1.InfoRequest{})
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
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
