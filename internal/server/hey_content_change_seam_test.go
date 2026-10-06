package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/plugintest/heyfake"
)

// A reply landing on a thread a client shows reaches it as that thread's
// tile changed in place, under the stamp HEY moved (its active_at), on the
// real binary: the shipped plugin tells it as its entry in everything
// (plugin standard rule 18).
func TestHeyReplyReachesTheClientAsItsThreadsChange(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	hey := heyAccount(t)
	cp := plugintest.Spawn(t, "hey", hey.Config(nil))
	a, stop := pluginhost.Start(cp, st.Namespace(heyNS), nil, "plugin "+heyNS+" watch")
	reg := plugin.NewRegistry()
	reg.Register(heyNS, "hey", a, stop)
	hs := serveWeb(t, mustNew(t, reg, Config{}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var shown string
	for _, p := range lp.Plugins {
		for _, m := range p.MenuEntries {
			if p.Uuid == heyNS && m.Label == "everything" {
				shown = m.GridId
			}
		}
	}
	if shown == "" {
		t.Fatal("no everything in the handshake")
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
	if err := cl.SetInterest(ctx, []string{shown}); err != nil {
		t.Fatal(err)
	}
	feed := hey.AwaitFeed(t)
	feed.Ready(t)
	th := heyfake.Thread{TopicID: 103, Subject: "Board games", Summary: "Thursday at mine?", From: "Erin", Email: "erin@example.com",
		Created: time.Date(2026, 1, 9, 19, 1, 4, 0, time.UTC), Active: time.Date(2026, 1, 9, 19, 1, 4, 0, time.UTC)}
	feed.Add(t, "imbox", th)
	var tile *gridwellv1.Tile
	for tile == nil {
		select {
		case ev := <-events:
			if ev.GetGridChanged().GetGridId() != shown {
				continue
			}
			g, err := cl.GetGrid(ctx, shown)
			if err != nil {
				t.Fatal(err)
			}
			for _, tl := range g.Tiles {
				if tl.AltText == "Erin: Board games" {
					tile = tl
				}
			}
		case <-ctx.Done():
			t.Fatal("the thread never reached the client")
		}
	}

	th.Active = th.Active.Add(time.Hour)
	feed.Add(t, "imbox", th)
	want := th.Active.Format(time.RFC3339Nano)
	for {
		select {
		case ev := <-events:
			tc := ev.GetTileChanged()
			if tc.GetTile().GetId() == tile.Id && tc.GetContentChanged() && tc.GetTile().GetContentStamp() == want {
				return
			}
		case <-ctx.Done():
			t.Fatalf("the reply never reached the client as thread %s changed in place under %s", tile.Id, want)
		}
	}
}
