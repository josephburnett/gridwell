package plugin_test

// The supervision seam, end to end: a real gridwell-plugin-fs subprocess under
// a supervisor, the adapter's event stream over it, the server's fan-in, and
// the wire client a browser uses. Killing the subprocess must reach the user
// as this namespace's health going down, and the respawn must reach them as it
// coming back — with the SAME client answering live afterwards, because the
// supervisor swapped the process underneath it.
//
// A unit test on the supervisor would prove the respawn and not that anyone is
// told; a unit test on the adapter would prove the event and not that a real
// process ever died. The bug this shape catches is the two disagreeing.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

const respawnUUID = "presp01"

// pluginPID is the pid of the plugin subprocess this test spawned: the one
// child of the test process running the fs binary. Killing it out of band is
// the only honest way to crash a plugin — the supervisor's own kill is a
// stop, not a crash — and the supervisor exposes no handle for it, because a
// production accessor no shipped path calls is dead code.
func pluginPID(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid()), "-f", "gridwell-plugin-fs").Output()
	if err != nil {
		t.Fatalf("pgrep for the plugin subprocess: %v", err)
	}
	lines := strings.Fields(strings.TrimSpace(string(out)))
	if len(lines) != 1 {
		t.Fatalf("pgrep found %d fs plugin children, want exactly the one this test spawned: %q", len(lines), out)
	}
	pid, err := strconv.Atoi(lines[0])
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func TestAPluginSubprocessCrashSurfacesAndRespawns(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("# notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The plugin inherits this process's environment, so the test's own home
	// is redirected: nothing may write into the developer's.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	sup, err := plugin.Supervise(respawnUUID, "fs", plugintest.Binary(t, "fs"), map[string]string{
		"root": root, "uuid": respawnUUID, "kind": "fs", "state_dir": t.TempDir(),
	})
	if err != nil {
		t.Fatalf("supervise: %v", err)
	}
	t.Cleanup(sup.Close)
	reg := plugin.NewRegistry()
	reg.Register(respawnUUID, "fs", pluginhost.New(pluginv1.NewPluginClient(sup), st.Namespace(respawnUUID), sup), sup.Close)
	srv := servertest.New(t, reg, server.Config{})
	hs := servertest.Serve(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pl, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := pl.Plugins[0].RootGridID
	if _, err := cl.GetGrid(ctx, rootGrid); err != nil {
		t.Fatal(err)
	}

	events := make(chan rpc.Event, 64)
	go func() {
		stream, serr := cl.Subscribe(ctx)
		if serr != nil {
			close(events)
			return
		}
		defer stream.Close()
		for {
			ev, ok, rerr := stream.Recv()
			if !ok || rerr != nil {
				close(events)
				return
			}
			events <- ev
		}
	}()
	await := func(want bool, what string) rpc.PluginHealth {
		t.Helper()
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					t.Fatal("the event stream ended")
				}
				h := ev.PluginHealth
				if ev.Kind != rpc.EventPluginHealth || h == nil {
					continue
				}
				if h.PluginUUID != respawnUUID {
					t.Fatalf("health event uuid = %q, want the namespace it came from", h.PluginUUID)
				}
				if h.Healthy == want {
					return *h
				}
			case <-ctx.Done():
				t.Fatal(what)
			}
		}
	}

	// The fan-in subscribes asynchronously, and this stream has no backlog: an
	// event fired before it lands is simply missed. So prime with a write —
	// which the adapter announces as a GridChanged, the other half of what
	// this stream carries — and only crash the plugin once one has arrived.
	prime := func() {
		t.Helper()
		g, gerr := cl.GetGrid(ctx, rootGrid)
		if gerr != nil {
			t.Fatal(gerr)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			if time.Now().After(deadline) {
				t.Fatal("a write's GridChanged never reached the client stream")
			}
			if _, perr := cl.PlaceTile(ctx, &rpc.PlaceTileRequest{TileID: g.Tiles[0].ID, X: 4, Y: 4, W: 1, H: 1}); perr != nil {
				t.Fatal(perr)
			}
			timeout := time.After(300 * time.Millisecond)
		drain:
			for {
				select {
				case ev, ok := <-events:
					if !ok {
						t.Fatal("the event stream ended")
					}
					if ev.Kind == rpc.EventGridChanged && ev.GridChanged != nil && ev.GridChanged.GridID == rootGrid {
						return
					}
				case <-timeout:
					break drain
				}
			}
		}
	}
	prime()

	pid := pluginPID(t)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", pid, err)
	}
	down := await(false, "the crashed subprocess never surfaced as health")
	if !strings.Contains(down.Detail, strconv.Itoa(pid)) {
		t.Errorf("health-down detail = %q, want the reason and the pid that died", down.Detail)
	}
	up := await(true, "the plugin never came back")
	if up.Detail != "" {
		t.Errorf("a health-up carries no complaint, got %q", up.Detail)
	}

	// The same wire client, the same adapter, the same registry entry: the
	// process behind them is a new one and nothing above had to be rebuilt.
	if newPid := pluginPID(t); newPid == pid {
		t.Fatalf("pid after respawn = %d, want a new process", newPid)
	}
	g, err := cl.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatalf("read after respawn: %v", err)
	}
	if len(g.Tiles) == 0 {
		t.Fatal("the respawned plugin answered an empty grid")
	}
}
