package pluginhost_test

import (
	"context"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/josephburnett/gridwell/api/compose"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
)

// capsPlugin declares every capability the plugin.v1 handshake has.
type capsPlugin struct {
	pluginv1.UnimplementedPluginServer
}

func (capsPlugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "caps", DisplayName: "caps", RootContext: "r", Watch: true, Writable: true}, nil
}

// The adapter must never declare a door it cannot open. It has no
// Subscribe (a declared watch:true sent the server's watchPlugin into
// Unimplemented retries forever) and no WriteContent (a declared
// writable:true offered editing the adapter refuses): until it
// implements them, the node-facing Info answers both false whatever
// the plugin declares.
func TestAdapterDeclaresOnlyTheDoorsItOpens(t *testing.T) {
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, cpCloser, err := compose.PluginInProcess(capsPlugin{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cpCloser)
	client, closer, err := plugin.ServeInProcess(pluginhost.New(cp, memStore.Namespace("p1")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	ctx := context.Background()

	info, err := client.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Watch || info.Writable {
		t.Errorf("Info = watch %v writable %v; the adapter opens neither door", info.Watch, info.Writable)
	}
	// The declarations must agree with the verbs.
	if s, err := client.Subscribe(ctx, &gridwellv1.SubscribeRequest{}); err == nil {
		if _, rerr := s.Recv(); status.Code(rerr) != codes.Unimplemented {
			t.Errorf("Subscribe answered %v; a false watch declaration must mean no stream", rerr)
		}
	}
	ws, err := client.WriteContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.CloseAndRecv(); status.Code(err) != codes.Unimplemented {
		t.Errorf("WriteContent answered %v; a false writable declaration must mean no write door", err)
	}
}
