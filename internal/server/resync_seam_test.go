package server_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/retry"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// A change made while the client's event stream is broken reaches an open
// grid without another event: the stream re-opens on its own and the resync
// it owes re-reads what the client shows. The client runs its own loop
// (retry.Events) over the real web API, and the break is the network's.
func TestAChangeDuringAStreamBreakReachesTheClientWithoutAnotherEvent(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/gridwell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	reg.Register(localNodeID, "home", local.New(st, nil), nil)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	writer := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home := rpc.HomeGrid(lp)

	opened := make(chan bool, 8)
	broken := make(chan struct{}, 8)
	gap := make(chan struct{})
	shown := make(chan []*gridwellv1.Tile, 8)
	subscribes := 0
	loopCtx, stopLoop := context.WithCancel(ctx)
	defer stopLoop()
	go retry.Events{
		Subscribe: func(ctx context.Context) (retry.Stream, error) {
			subscribes++
			if subscribes == 2 {
				// The change lands while no stream is open.
				<-gap
			}
			s, err := cl.Subscribe(ctx)
			if err != nil {
				return nil, err
			}
			return s, nil
		},
		Opened: func(resync bool) {
			if resync {
				g, err := cl.GetGrid(ctx, home)
				if err == nil {
					shown <- g.GetTiles()
				}
			}
			opened <- resync
		},
		Broken: func() { broken <- struct{}{} },
		Event:  func(*gridwellv1.Event) {},
	}.Run(loopCtx)

	await := func(what string, ch <-chan bool) bool {
		t.Helper()
		select {
		case v := <-ch:
			return v
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the stream never opened", what)
			return false
		}
	}
	if await("first open", opened) {
		t.Fatal("the first stream owed a resync")
	}
	hs.CloseClientConnections()
	select {
	case <-broken:
	case <-time.After(5 * time.Second):
		t.Fatal("the client never saw its stream break")
	}
	if _, err := writer.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: home,
		Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 2, Y: 2, W: 1, H: 1, AltText: "made in the gap"}}); err != nil {
		t.Fatal(err)
	}
	close(gap)
	if !await("re-open after the break", opened) {
		t.Fatal("the stream after a break owed no resync")
	}
	tiles := <-shown
	for _, tile := range tiles {
		if tile.GetAltText() == "made in the gap" {
			return
		}
	}
	t.Fatalf("the resync read %d tiles and not the one made in the gap", len(tiles))
}
