package server_test

// The shipped fs plugin's Watch, spawned as the loader spawns it and started
// through pluginhost.Start, reaching a client at the web door: the OS change
// notification, the plugin's scope, the node's stream and the fan-out, with
// no read on the way.

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
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
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// A file written in a directory a client shows reaches it as that grid's
// change; one written in a directory nobody shows reaches it as nothing.
func TestFsWatchReachesAClientShowingTheDirectory(t *testing.T) {
	const fsUUID = "pfswatch"
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "unshown"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cp := plugintest.Spawn(t, "fs", map[string]string{"root": root})
	a, stop := pluginhost.Start(cp, st.Namespace(fsUUID), nil, "plugin "+fsUUID+" watch")
	reg := plugin.NewRegistry()
	reg.Register(fsUUID, "fs", a, stop)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var shown string
	for _, p := range lp.Plugins {
		if p.Uuid == fsUUID {
			shown = plugintest.LandingOf(t, p)
		}
	}
	if shown == "" {
		t.Fatal("no fs plugin in the handshake")
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
	if err := cl.SetInterest(ctx, []string{shown}); err != nil {
		t.Fatal(err)
	}
	await := func(what string) {
		t.Helper()
		for {
			select {
			case ev := <-events:
				if ev.GetGridChanged().GetGridId() == shown {
					return
				}
			case <-ctx.Done():
				t.Fatalf("%s: no change to %s reached the client", what, shown)
			}
		}
	}
	// The stream's open is announced once the plugin watches the directory.
	await("the Watch opening")
	quiet := func(what string) {
		t.Helper()
		for {
			select {
			case ev := <-events:
				t.Fatalf("%s: the client was told %v", what, ev)
			case <-time.After(time.Second): // well past the plugin's debounce window
				return
			}
		}
	}
	quiet("after the open")

	if err := os.WriteFile(filepath.Join(root, "unshown", "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	quiet("a file written where no one looks")

	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	await("a file written in the shown directory")
}

// Showing what a client has just read tells it nothing: entering a directory
// of thirty subdirectories, each read for its preview, announces no grid
// while nothing changed. A file written in one shown subdirectory reaches the
// client as that one grid, and one written in a subdirectory while nobody
// showed it is announced once when it is shown again, and nothing else is.
func TestFsShowingWhatWasReadAnnouncesNothing(t *testing.T) {
	const fsUUID = "pfsscope"
	root := t.TempDir()
	for i := range 30 {
		dir := filepath.Join(root, fmt.Sprintf("d%02d", i))
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cp := plugintest.Spawn(t, "fs", map[string]string{"root": root})
	a, stop := pluginhost.Start(cp, st.Namespace(fsUUID), nil, "plugin "+fsUUID+" watch")
	reg := plugin.NewRegistry()
	reg.Register(localNodeID, "home", local.New(st, nil), nil)
	reg.Register(fsUUID, "fs", a, stop)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var shown string
	for _, p := range lp.Plugins {
		if p.Uuid == fsUUID {
			shown = plugintest.LandingOf(t, p)
		}
	}
	if shown == "" {
		t.Fatal("no fs plugin in the handshake")
	}

	events := make(chan *gridwellv1.Event, 256)
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

	// The client reads the directory, then each subdirectory for its preview.
	g, err := cl.GetGrid(ctx, shown)
	if err != nil {
		t.Fatal(err)
	}
	subs := map[string]string{} // label → grid
	for _, tile := range g.Tiles {
		if tile.ChildGridId != "" {
			subs[tile.AltText] = tile.ChildGridId
		}
	}
	if len(subs) != 30 {
		t.Fatalf("the directory shows %d subdirectories, want 30: %v", len(subs), subs)
	}
	readSub := func(name string) {
		t.Helper()
		if _, err := cl.GetGrid(ctx, subs[name]); err != nil {
			t.Fatal(err)
		}
	}
	all := []string{shown}
	for name, gid := range subs {
		readSub(name)
		all = append(all, gid)
	}
	// told is every GridChanged in the next two seconds, well past the
	// plugin's debounce window.
	told := func() map[string]int {
		got := map[string]int{}
		deadline := time.After(2 * time.Second)
		for {
			select {
			case ev := <-events:
				if id := ev.GetGridChanged().GetGridId(); id != "" {
					got[id]++
				}
			case <-deadline:
				return got
			}
		}
	}
	if err := cl.SetInterest(ctx, all); err != nil {
		t.Fatal(err)
	}
	if got := told(); len(got) != 0 {
		t.Fatalf("showing 31 grids just read told the client %d changes: %v", len(got), got)
	}

	if err := os.WriteFile(filepath.Join(root, "d03", "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := told(), map[string]int{subs["d03"]: 1}; !maps.Equal(got, want) {
		t.Fatalf("a file written in d03 told the client %v, want %v", got, want)
	}
	readSub("d03")

	if err := cl.SetInterest(ctx, []string{shown}); err != nil {
		t.Fatal(err)
	}
	if got := told(); len(got) != 0 {
		t.Fatalf("hiding the subdirectories told the client %v", got)
	}
	if err := os.WriteFile(filepath.Join(root, "d05", "gap.txt"), []byte("gap"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cl.SetInterest(ctx, all); err != nil {
		t.Fatal(err)
	}
	if got, want := told(), map[string]int{subs["d05"]: 1}; !maps.Equal(got, want) {
		t.Fatalf("showing them again after a file was written in d05 told the client %v, want %v", got, want)
	}
}
