package server_test

// A plugin whose refresh fails answers from memory and says why in
// ListResponse.unreachable. Across the plugin.v1 wire, the adapter, the router
// and the web door, a subscribed client reads every row through the outage and
// is told the source is down with the plugin's sentence, then up again.

import (
	"context"
	"sync/atomic"
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

const memoryUUID = "pmemory"

const memoryReason = "the mail server is not answering, so this is the box as last seen"

// memoryMail lists three threads. While remembering it answers the two it
// remembers, claiming authority, and says why.
type memoryMail struct {
	pluginv1.UnimplementedPluginServer
	remembering *atomic.Bool
}

func (memoryMail) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "mail", DisplayName: "mail",
		MenuEntries: []*pluginv1.MenuEntry{{Id: "inbox", Context: "inbox"}}}, nil
}

func (p memoryMail) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	resp := &pluginv1.ListResponse{Authoritative: true, Entries: []*pluginv1.Entry{
		{Key: "t1", Kind: rpc.KindText, Label: "t1"},
		{Key: "t2", Kind: rpc.KindText, Label: "t2"},
	}}
	if p.remembering.Load() {
		resp.Unreachable = memoryReason
		return resp, nil
	}
	resp.Entries = append(resp.Entries, &pluginv1.Entry{Key: "t3", Kind: rpc.KindText, Label: "t3"})
	return resp, nil
}

func TestAMemoryAnswerKeepsEveryRowAndReportsTheReason(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/gridwell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var remembering atomic.Bool
	cp, closer, err := plugintest.Loopback(memoryMail{remembering: &remembering})
	if err != nil {
		t.Fatal(err)
	}
	a, stop := pluginhost.Start(cp, st.Namespace(memoryUUID), nil, "plugin "+memoryUUID+" watch")
	reg := plugin.NewRegistry()
	reg.Register(memoryUUID, "mail", a, func() { stop(); closer() })
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var grid string
	for _, p := range lp.Plugins {
		if p.Uuid == memoryUUID {
			grid = plugintest.LandingOf(t, p)
		}
	}
	// Subscribe answers with its first event, so it is opened aside.
	health := make(chan *gridwellv1.EventPluginHealth, 16)
	go func() {
		stream, err := cl.Subscribe(ctx)
		if err != nil {
			return
		}
		defer stream.Close()
		for {
			ev, ok, err := stream.Recv()
			if err != nil || !ok {
				return
			}
			if h := ev.GetPluginHealth(); h != nil && h.PluginUuid == memoryUUID {
				health <- h
			}
		}
	}()
	next := func() *gridwellv1.EventPluginHealth {
		t.Helper()
		select {
		case h := <-health:
			return h
		case <-ctx.Done():
			t.Fatal("no health event for the remembering plugin reached the door")
			return nil
		}
	}

	before, err := cl.GetGrid(ctx, grid)
	if err != nil {
		t.Fatal(err)
	}
	// The thread the memory does not hold is one the user arranged, so a
	// memory taken for a verdict would retire the user's own row.
	var t3 *gridwellv1.Tile
	for _, tile := range before.Tiles {
		if tile.AltText == "t3" {
			t3 = tile
		}
	}
	if t3 == nil {
		t.Fatalf("no t3 in %v", before.Tiles)
	}
	if _, err := cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: t3.Id, X: 6, Y: 6, W: 1, H: 1}); err != nil {
		t.Fatal(err)
	}

	remembering.Store(true)
	during, err := cl.GetGrid(ctx, grid)
	if err != nil {
		t.Fatalf("a memory answer failed the read: %v", err)
	}
	if len(during.Tiles) != len(before.Tiles) {
		t.Fatalf("during the outage the grid shows %v, want all %d rows", during.Tiles, len(before.Tiles))
	}
	down := next()
	if down.Healthy || down.Detail != memoryReason {
		t.Fatalf("health = %+v, want down with the plugin's sentence", down)
	}

	remembering.Store(false)
	if _, err := cl.GetGrid(ctx, grid); err != nil {
		t.Fatal(err)
	}
	up := next()
	for !up.Healthy && up.Detail == memoryReason {
		up = next()
	}
	if !up.Healthy || up.Detail != "" {
		t.Fatalf("health = %+v, want up once a live answer lands", up)
	}
}
