package plugin_test

// A respawned plugin answers its declaration afresh, and the + menu the
// browser reads shows that answer, not the one the process before it gave.

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

func TestARespawnedPluginsDeclarationReachesTheMenu(t *testing.T) {
	const uuid = "pdecl01"
	root := filepath.Join(t.TempDir(), "notes")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sup, err := plugin.Supervise(uuid, "fs", plugintest.Binary(t, "fs"), map[string]string{
		"root": root, "uuid": uuid, "kind": "fs", "state_dir": t.TempDir(),
	})
	if err != nil {
		t.Fatalf("supervise: %v", err)
	}
	t.Cleanup(sup.Close)
	// As loader.go builds it.
	a, stop := pluginhost.Start(pluginv1.NewPluginClient(sup), st.Namespace(uuid), sup, "plugin "+uuid+" watch")
	t.Cleanup(stop)
	reg := plugin.NewRegistry()
	reg.Register(uuid, "fs", a, sup.Close)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	row := func() *gridwellv1.PluginInfo {
		t.Helper()
		lp, err := cl.Handshake(ctx)
		if err != nil {
			t.Fatalf("Handshake: %v", err)
		}
		return lp.Plugins[0]
	}
	if r := row(); r.InfoError != "" || len(r.PluginGrids) != 1 {
		t.Fatalf("before the respawn the row is %v, want its one grid", r)
	}

	stream, err := cl.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	health := func(want bool) {
		t.Helper()
		for {
			ev, ok, err := stream.Recv()
			if err != nil || !ok {
				t.Fatalf("the stream ended before health %v: %v", want, err)
			}
			if h := ev.GetPluginHealth(); h != nil && h.Healthy == want {
				return
			}
		}
	}
	// The next process cannot serve its root, so it refuses Info with the
	// reason; the one before answered.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pluginPID(t), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	health(false)
	health(true)

	r := row()
	if want := `root "` + root + `" does not exist`; r.InfoError != want {
		t.Errorf("after the respawn InfoError = %q, want the new process's refusal %q", r.InfoError, want)
	}
	if len(r.PluginGrids) != 0 {
		t.Errorf("after the respawn the menu still presents the old process's grids: %v", r.PluginGrids)
	}
}
