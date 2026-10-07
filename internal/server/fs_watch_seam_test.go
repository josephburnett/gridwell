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
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
	"github.com/josephburnett/gridwell/internal/sourcecache"
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
	reg.Register(localNodeID, "home", local.New(st, nil), nil)
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
	// The client reads what it shows, so a change before the stream opens is
	// told by the open's check.
	if _, err := cl.GetGrid(ctx, shown); err != nil {
		t.Fatal(err)
	}
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
	quiet("an open over a quiet disk")
	if err := os.WriteFile(filepath.Join(root, "first.txt"), []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	await("a file written in the shown directory")
	// The change's own TileChanged may follow; only the next steps are quiet.
	for drained := false; !drained; {
		select {
		case <-events:
		case <-time.After(time.Second):
			drained = true
		}
	}

	if err := os.WriteFile(filepath.Join(root, "unshown", "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	quiet("a file written where no one looks")

	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	await("another file written in the shown directory")
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

// wideFar is a connection wide enough that the prefetch walk over it is still
// warming cache.db seconds after a client subscribes.
type wideFar struct {
	namespace.Unimplemented
}

const wideRoot = "geneva/rnode1/g"

func (wideFar) Info(context.Context, *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	return &gridwellv1.InfoResponse{}, nil
}

func (wideFar) Handshake(context.Context, *gridwellv1.HandshakeRequest) (*gridwellv1.HandshakeResponse, error) {
	return &gridwellv1.HandshakeResponse{Plugins: []*gridwellv1.PluginInfo{
		rpc.ConnectionRow("geneva", "Geneva", wideRoot, "", rpc.View{})}}, nil
}

// GetGrid answers twenty wells per grid, two levels deep.
func (wideFar) GetGrid(_ context.Context, in *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	resp := &gridwellv1.GetGridResponse{Grid: &gridwellv1.Grid{Id: in.GridId}}
	if len(in.GridId) > len(wideRoot)+4 {
		return resp, nil
	}
	for i := range 20 {
		resp.Tiles = append(resp.Tiles, &gridwellv1.Tile{
			Id: fmt.Sprintf("%s/t%d", in.GridId, i), GridId: in.GridId, Kind: "well",
			ChildGridId: fmt.Sprintf("%s%02d", in.GridId, i), X: int64(i), W: 1, H: 1,
		})
	}
	return resp, nil
}

func (wideFar) GetTilePreview(context.Context, *gridwellv1.GetTilePreviewRequest) (*gridwellv1.GetTilePreviewResponse, error) {
	return &gridwellv1.GetTilePreviewResponse{}, nil
}

func (wideFar) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, _ func(*gridwellv1.Event) error) error {
	<-ctx.Done()
	return nil
}

// A directory whose listing does not move tells a client showing it nothing,
// however busily the files in it are written. The node's own home is the case
// a user meets: shown through fs, it holds cache.db, which the connection's
// prefetch walk writes for as long as it runs, while every name in it, and so
// the listing, stays as it was.
func TestFsShowingTheNodeHomeWhileItsCacheWarmsSettles(t *testing.T) {
	const fsUUID = "pfshome"
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cache, err := sourcecache.Open(filepath.Join(home, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	cp := plugintest.Spawn(t, "fs", map[string]string{"root": home})
	a, stop := pluginhost.Start(cp, st.Namespace(fsUUID), nil, "plugin "+fsUUID+" watch")
	reg := plugin.NewRegistry()
	reg.Register(localNodeID, "home", local.New(st, nil), nil)
	reg.Register(fsUUID, "fs", a, stop)
	// node.Start's transport wiring.
	reg.SetTransport(cache.Front(wideFar{}, sourcecache.Options{Prefetch: true}), nil)
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

	// Subscribing starts the walk.
	changed := make(chan struct{}, 256)
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
			if ev.GetGridChanged().GetGridId() == shown {
				changed <- struct{}{}
			}
		}
	}()
	first, err := cl.GetGrid(ctx, shown)
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.SetInterest(ctx, []string{shown}); err != nil {
		t.Fatal(err)
	}
	walSize := func() int64 {
		fi, err := os.Stat(filepath.Join(home, "cache.db-wal"))
		if err != nil {
			return 0
		}
		return fi.Size()
	}
	before := walSize()

	// The client refetches on each change it is told of, as every client does.
	told, same := 0, 0
	window := time.After(3 * time.Second)
	for done := false; !done; {
		select {
		case <-changed:
			told++
			g, err := cl.GetGrid(ctx, shown)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(g.Tiles) == fmt.Sprint(first.Tiles) {
				same++
			}
		case <-window:
			done = true
		}
	}
	if walSize() <= before {
		t.Fatal("the walk wrote nothing to cache.db in the window, so the window tested nothing")
	}
	if told > 0 {
		t.Fatalf("nothing in the node's home was added, removed or renamed, yet a client showing it was told %d changes in 3s and refetched each; %d of them served the listing it already held", told, same)
	}
}

// A node that boots while a client shows grids tells it nothing while the
// disk is quiet: the client re-reads what it shows itself, and a context the
// scope adds only because a shown grid links into it was read by nobody, so
// it is noted, not announced. A file then written is one GridChanged for its
// directory and one for each grid linking into it, however many links each
// holds.
func TestFsBootingWhileShownAnnouncesOnlyWhatMoves(t *testing.T) {
	const fsUUID = "pfsboot"
	root := t.TempDir()
	for _, d := range []string{"d0", "d1", "d2", "d3", "d4"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, d, "a.txt"), []byte(d), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		"l0":    "d0/a.txt",
		"l0b":   "d0/a.txt",
		"l2":    "d2/a.txt",
		"l3":    "d3/a.txt",
		"d1/l0": "../d0/a.txt",
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
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
	readGrid := func(gid string) *gridwellv1.GetGridResponse {
		t.Helper()
		g, err := cl.GetGrid(ctx, gid)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	dirs := map[string]string{} // name → grid
	for _, tile := range readGrid(shown).Tiles {
		if tile.ChildGridId != "" {
			dirs[tile.AltText] = tile.ChildGridId
		}
	}
	if len(dirs) != 5 {
		t.Fatalf("the root shows %d directories, want 5: %v", len(dirs), dirs)
	}
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

	// The client's own re-read of what it shows, then its interest; d2 and d3
	// join the scope through the root's links, read by nobody.
	readGrid(dirs["d0"])
	readGrid(dirs["d1"])
	if err := cl.SetInterest(ctx, []string{shown, dirs["d0"], dirs["d1"]}); err != nil {
		t.Fatal(err)
	}
	if got := told(); len(got) != 0 {
		t.Fatalf("a quiet disk told the client %v, want nothing", got)
	}

	if err := os.WriteFile(filepath.Join(root, "d2", "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := told(), map[string]int{dirs["d2"]: 1, shown: 1}; !maps.Equal(got, want) {
		t.Fatalf("a file written in d2 told the client %v, want %v", got, want)
	}
	readGrid(shown)

	if err := os.WriteFile(filepath.Join(root, "d0", "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := told(), map[string]int{dirs["d0"]: 1, shown: 1, dirs["d1"]: 1}; !maps.Equal(got, want) {
		t.Fatalf("a file written in d0 told the client %v, want %v", got, want)
	}
	readGrid(shown)
	readGrid(dirs["d0"])
	readGrid(dirs["d1"])

	// A scope the client moves onto a grid it has just read tells it nothing.
	readGrid(dirs["d4"])
	if err := cl.SetInterest(ctx, []string{shown, dirs["d0"], dirs["d4"]}); err != nil {
		t.Fatal(err)
	}
	if got := told(); len(got) != 0 {
		t.Fatalf("moving the scope over a quiet disk told the client %v, want nothing", got)
	}
}
