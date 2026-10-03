package server_test

// The shipped gitlab plugin's Watch, spawned as the loader spawns it against
// a fake GitLab and started through pluginhost.Start, reaching a client at the
// web door: GitLab cannot tell, so the plugin polls, and only while a client
// shows one of its grids.

import (
	"context"
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
	"github.com/josephburnett/gridwell/internal/plugintest/gitlabfake"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

func watchedTodo(id int64, created string) gitlabfake.Todo {
	var t gitlabfake.Todo
	t.ID, t.State, t.CreatedAt = id, "pending", created
	t.TargetType, t.ActionName, t.Target.IID, t.Target.Title = "MergeRequest", "review_requested", id, "change"
	return t
}

// A todo that arrives at GitLab reaches a client showing the todos as that
// grid's change, with no read on the way; while nothing is shown, before and
// after, GitLab hears no request across several refresh windows.
func TestGitLabWatchReachesAClientShowingTheTodos(t *testing.T) {
	const glUUID = "pglwatch"
	old := watchedTodo(1, "2026-08-18T10:00:00Z")
	gl := gitlabfake.New(t, old)
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// The fastest refresh the plugin allows, so a window is a second.
	cp := plugintest.Spawn(t, "gitlab", gl.Config(t, map[string]string{"refresh": "1s"}))
	a, stop := pluginhost.Start(cp, st.Namespace(glUUID), nil, "plugin "+glUUID+" watch")
	reg := plugin.NewRegistry()
	reg.Register(localNodeID, "home", local.New(st, nil), nil)
	reg.Register(glUUID, "gitlab", a, stop)
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
		if p.Uuid == glUUID {
			shown = plugintest.LandingOf(t, p)
		}
	}
	if shown == "" {
		t.Fatal("no gitlab plugin in the handshake")
	}
	flat := func(what string) {
		t.Helper()
		time.Sleep(200 * time.Millisecond) // a request already sent lands
		n := gl.Calls()
		time.Sleep(3500 * time.Millisecond)
		if got := gl.Calls(); got != n {
			t.Fatalf("%s: GitLab heard %d requests across three refresh windows", what, got-n)
		}
	}
	flat("nothing shown yet")

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
	if g, err := cl.GetGrid(ctx, shown); err != nil || len(g.Tiles) != 1 {
		t.Fatalf("the todos = (%v, %v), want one week", g.GetTiles(), err)
	}
	if err := cl.SetInterest(ctx, []string{shown}); err != nil {
		t.Fatal(err)
	}
	// Whatever the stream's open announces is not the change under test.
	settle := time.After(2 * time.Second)
	for drained := false; !drained; {
		select {
		case <-events:
		case <-settle:
			drained = true
		}
	}

	gl.Set(watchedTodo(2, "2026-09-01T10:00:00Z"), old)
	for told := false; !told; {
		select {
		case ev := <-events:
			told = ev.GetGridChanged().GetGridId() == shown
		case <-ctx.Done():
			t.Fatalf("a todo new at GitLab never reached the client showing %s", shown)
		}
	}
	if g, err := cl.GetGrid(ctx, shown); err != nil || len(g.Tiles) != 2 {
		t.Fatalf("after the change the todos = (%v, %v), want two weeks", g.GetTiles(), err)
	}

	if err := cl.SetInterest(ctx, nil); err != nil {
		t.Fatal(err)
	}
	flat("nothing shown any more")
}
