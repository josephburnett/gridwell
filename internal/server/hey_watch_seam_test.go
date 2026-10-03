package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// Nothing is done for nobody (docs/plugin-standard.md rule 8), across the
// whole stack on the real binary: with no grid of the plugin's shown, the CLI
// runs nothing, however short the refresh. Showing the Imbox opens the
// plugin's Watch, which starts `hey watch`; a line on the feed reaches the
// client as the Imbox changing, with no gesture; and once nothing is shown the
// feed is stopped.
func TestHeyFeedRunsOnlyWhileAGridIsShown(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	hey := heyAccount(t)
	cp := plugintest.Spawn(t, "hey", hey.Config(map[string]string{"refresh": "1ms"}))
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
			if p.Uuid == heyNS && m.Label == "imbox" {
				shown = m.GridId
			}
		}
	}
	if shown == "" {
		t.Fatal("no imbox in the handshake")
	}
	time.Sleep(300 * time.Millisecond)
	if w, b := hey.Runs("watch"), hey.Runs("box"); w != 0 || b != 0 {
		t.Fatalf("with nothing shown the CLI ran %d feeds and %d box reads", w, b)
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
	feed.Send(t, `{"change":"added","at":"2026-09-28T19:01:04.695Z","box":{"id":1,"kind":"imbox","name":"Imbox"},"posting_id":930,"thread_id":103,"new":true,"posting":{"id":930,"kind":"topic","name":"Board games","summary":"Thursday at mine?","seen":true,"created_at":"2026-01-09T19:01:04Z","creator":{"name":"Erin","email_address":"erin@example.com"}}}`)
	for told := false; !told; {
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
				told = told || strings.Contains(tl.AltText, "Board games")
			}
		case <-ctx.Done():
			t.Fatal("the feed's new mail never reached the client")
		}
	}

	if err := cl.SetInterest(ctx, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-feed.Done():
	case <-ctx.Done():
		t.Fatal("nothing is shown and the feed ran on")
	}
}
