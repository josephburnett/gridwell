package plugin_test

// A plugin the user disables is stopped for the node's life: a real fs
// subprocess loaded the way serve loads it, so the switch the loader
// registers is the one the web door's verb reaches, and the health the
// client hears is the router's.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

const disableUUID = "pdisab1"

func TestADisabledPluginIsStoppedAndNeverRespawned(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("# notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	st, err := store.Open(config.DBFile(home))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.ServerConfig{Plugins: []config.PluginConfig{{
		ID: disableUUID, Label: "files", Kind: "fs", Binary: plugintest.Binary(t, "fs"),
		Config: map[string]string{"root": root},
	}}}
	reg := plugin.NewRegistry()
	if err := plugin.LoadInto(reg, cfg, home, st); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	srv := servertest.New(t, reg, server.Config{})
	hs := servertest.Serve(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pl, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	landing := plugintest.LandingOf(t, pl.Plugins[0])
	if _, err := cl.GetGrid(ctx, landing); err != nil {
		t.Fatal(err)
	}
	if children(t) != 1 {
		t.Fatal("the fs subprocess is not running before the disable")
	}
	// The stream opens on its first event, so it is read beside the verb.
	told := make(chan *gridwellv1.EventPluginHealth, 1)
	go func() {
		stream, err := cl.Subscribe(ctx)
		if err != nil {
			return
		}
		defer stream.Close()
		for {
			ev, ok, err := stream.Recv()
			if !ok || err != nil {
				return
			}
			if h := ev.GetPluginHealth(); h != nil && h.PluginUuid == disableUUID {
				told <- h
				return
			}
		}
	}()
	if err := cl.DisableSource(ctx, disableUUID); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-told:
		if !h.Disabled || h.Healthy {
			t.Fatalf("health after the verb = %+v, want disabled", h)
		}
	case <-ctx.Done():
		t.Fatal("the disable was never told on the event stream")
	}
	// Past the supervisor's first respawn pauses.
	time.Sleep(2 * time.Second)
	if n := children(t); n != 0 {
		t.Fatalf("%d fs subprocesses after the disable, want none: a disabled plugin is never respawned", n)
	}
	_, err = cl.GetGrid(ctx, landing)
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("read of a disabled plugin's grid = %v, want unavailable (dark)", err)
	}
}

// children counts this test process's fs plugin subprocesses.
func children(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid()), "-f", "gridwell-plugin-fs").Output()
	if err != nil {
		return 0 // pgrep exits 1 when nothing matches
	}
	n := 0
	for _, b := range out {
		if b == '\n' {
			n++
		}
	}
	return n
}
