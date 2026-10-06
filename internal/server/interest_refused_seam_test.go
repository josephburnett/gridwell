package server_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// A plugin that cannot take what a client shows is not scoped to it, so the
// grid on screen stops updating: the client is told on a healthy event that
// live updates are off, and told again, cleared, once a share lands.
func TestAnInterestTheNamespaceCannotTakeTurnsLiveUpdatesOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "gridwell.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	poke := registerWatching(t, reg, st)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	// A second connection to the same file breaks the store's reads from
	// outside, and mends them, without a hook in the code under test.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	exec := func(q string) {
		t.Helper()
		if _, err := raw.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	events := make(chan *gridwellv1.Event, 64)
	go func() {
		es, err := cl.Subscribe(ctx)
		if err != nil {
			return
		}
		defer es.Close()
		for {
			ev, ok, err := es.Recv()
			if err != nil || !ok {
				return
			}
			events <- ev
		}
	}()
	health := func() *gridwellv1.EventPluginHealth {
		t.Helper()
		for {
			select {
			case ev := <-events:
				if hv := ev.GetPluginHealth(); hv != nil && hv.PluginUuid == watchUUID {
					return hv
				}
			case <-ctx.Done():
				t.Fatal("no health event for the plugin reached the door")
			}
		}
	}
	grid := func(c string) string { return watchUUID + "/" + rpc.KeyTileID(c) }

	// The stream is open and the share taken once a poke reaches the door;
	// only then is the store broken, so nothing but the next share hits it.
	if err := cl.SetInterest(ctx, []string{grid("all")}); err != nil {
		t.Fatal(err)
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for changed := false; !changed; {
		select {
		case ev := <-events:
			changed = ev.GetGridChanged().GetGridId() == grid("all")
		case <-tick.C:
			select {
			case poke <- struct{}{}:
			default:
			}
		case <-ctx.Done():
			t.Fatal("the plugin's watch never reached the door")
		}
	}

	exec(`ALTER TABLE grids RENAME TO grids_away`)
	if err := cl.SetInterest(ctx, []string{grid("all"), grid("other")}); err != nil {
		t.Fatal(err)
	}
	off := health()
	if !off.Healthy || off.LiveUpdatesOff == "" {
		t.Fatalf("health = %v, want healthy with live updates off", off)
	}

	exec(`ALTER TABLE grids_away RENAME TO grids`)
	if err := cl.SetInterest(ctx, []string{grid("all"), grid("other"), grid("third")}); err != nil {
		t.Fatal(err)
	}
	if on := health(); !on.Healthy || on.LiveUpdatesOff != "" {
		t.Fatalf("health = %v, want live updates back on", on)
	}
}
