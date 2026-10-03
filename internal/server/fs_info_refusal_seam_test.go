package server_test

// The shipped fs binary over a root that is not there: the node lists it
// broken with the plugin's own sentence, and the same process is healthy with
// its one collection once the directory exists.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

func TestFsOnAMissingRootIsBrokenAndHealsWithoutARespawn(t *testing.T) {
	const fsUUID = "pfsrefuse"
	was := namespace.DefaultBackoff
	t.Cleanup(func() { namespace.DefaultBackoff = was })
	namespace.DefaultBackoff = namespace.Backoff{First: 10 * time.Millisecond, Max: 20 * time.Millisecond}

	root := filepath.Join(t.TempDir(), "missing")
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// Spawned once, here; nothing below spawns again.
	cp := plugintest.Spawn(t, "fs", map[string]string{"root": root})
	a, stop := pluginhost.Start(cp, st.Namespace(fsUUID), nil, "plugin "+fsUUID+" watch")
	reg := plugin.NewRegistry()
	reg.Register(fsUUID, "fs", a, stop)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := cl.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	row := pluginRow(t, cl, fsUUID)
	if want := `root "` + root + `" does not exist`; row.InfoError != want {
		t.Errorf("InfoError = %q, want the plugin's sentence %q", row.InfoError, want)
	}
	if len(row.MenuEntries) != 0 {
		t.Errorf("a refusing fs presents entries: %v", row.MenuEntries)
	}
	if down := recvHealthOf(t, stream, fsUUID); down.Healthy {
		t.Fatalf("first health event = %+v, want the refusal as down", down)
	}

	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if up := recvHealthOf(t, stream, fsUUID); !up.Healthy {
		t.Fatalf("after the root exists, health event = %+v, want up", up)
	}
	row = pluginRow(t, cl, fsUUID)
	if row.InfoError != "" || len(row.MenuEntries) != 1 {
		t.Errorf("after the root exists the row is %v, want its one collection and no error", row)
	}
}
