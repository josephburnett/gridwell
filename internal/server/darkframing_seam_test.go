package server_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/sourcecache"
)

// A far grid reopens where it was last left while its machine is gone: the
// source cache in front of the real transport remembers the latest framing of
// a far root, from this node's own accepted write and from the far node's
// event alike, and a well's from its tile. Across the whole seam, because the
// framing a dark handshake answers is assembled from three layers' copies.
func TestAFarGridReopensWhereItWasLeftWhileDark(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newFrontedTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil,
		func(ns namespace.Namespace) namespace.Namespace {
			cache, err := sourcecache.Open(t.TempDir() + "/cache.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cache.Close() })
			return cache.Front(ns, sourcecache.Options{Prefetch: true})
		})
	lp, err := h.localCl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root := connectionRows(lp)[0].RootGridId
	farNS := localNodeID + "/geneva"
	if _, err := h.localCl.HandshakeNS(ctx, farNS); err != nil {
		t.Fatal(err)
	}

	// This node's own pan, with nobody subscribed: only the write can teach it.
	mine := rpc.Framing{Cx: 1, Cy: 2, Zoom: 1.5}
	if _, err := h.localCl.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		RootGridId: root, Cx: mine.Cx, Cy: mine.Cy, Zoom: mine.Zoom}); err != nil {
		t.Fatalf("SetFraming through the connection: %v", err)
	}
	// A well on the far home, framed through the connection.
	well, err := h.localCl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root,
		Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	wellFraming := rpc.Framing{Cx: 5, Cy: 6, Zoom: 0.25}
	if _, err := h.localCl.SetFraming(ctx, &gridwellv1.SetFramingRequest{
		TileId: well.Id, Cx: wellFraming.Cx, Cy: wellFraming.Cy, Zoom: wellFraming.Zoom}); err != nil {
		t.Fatalf("SetFraming a far well: %v", err)
	}
	if _, err := h.localCl.GetGrid(ctx, root); err != nil {
		t.Fatal(err)
	}

	// Then someone pans the far home on the far machine itself; this node
	// hears it only as the event.
	farHome, err := h.remoteCl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	theirs := rpc.Framing{Cx: -3, Cy: 4, Zoom: 0.5}
	events := make(chan *gridwellv1.Event, 64)
	go func() {
		es, err := h.localCl.Subscribe(ctx)
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
	awaitFraming(ctx, t, events, root, theirs, func() {
		if _, err := h.remoteCl.SetFraming(ctx, &gridwellv1.SetFramingRequest{
			RootGridId: rpc.HomeGrid(farHome), Cx: theirs.Cx, Cy: theirs.Cy, Zoom: theirs.Zoom}); err != nil {
			t.Fatalf("SetFraming on the far node: %v", err)
		}
	})

	h.stopFarNode()

	// The connection's own row is the doorway the + menu and a landing read.
	lp, err = h.localCl.Handshake(ctx)
	if err != nil {
		t.Fatalf("the node's handshake while dark: %v", err)
	}
	if got := framingOf(connectionRows(lp)[0]); !got.SameAs(theirs) {
		t.Errorf("the connection row while dark = %+v, want the last pan %+v", got, theirs)
	}
	menu, err := h.localCl.HandshakeNS(ctx, farNS)
	if err != nil {
		t.Fatalf("the far node's menu while dark: %v", err)
	}
	if got := framingOf(rpc.HomeRow(menu)); !got.SameAs(theirs) {
		t.Errorf("the far home row while dark = %+v, want the last pan %+v", got, theirs)
	}
	g, err := h.localCl.GetGrid(ctx, root)
	if err != nil {
		t.Fatalf("the far home while dark: %v", err)
	}
	for _, tl := range g.Tiles {
		if tl.Id == well.Id {
			if got := (rpc.Framing{Cx: tl.ViewCx, Cy: tl.ViewCy, Zoom: tl.ViewZoom}); !got.SameAs(wellFraming) {
				t.Errorf("the far well while dark = %+v, want %+v", got, wellFraming)
			}
		}
	}

	// A pan while dark is refused as every dark write is, and remembered
	// nowhere: the far node never took it.
	_, ferr := h.localCl.SetFraming(ctx, &gridwellv1.SetFramingRequest{RootGridId: root, Cx: 9, Cy: 9, Zoom: 3})
	_, cerr := h.localCl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 3, Y: 3, W: 1, H: 1}})
	if connect.CodeOf(ferr) != connect.CodeUnavailable || connect.CodeOf(cerr) != connect.CodeOf(ferr) {
		t.Errorf("dark SetFraming = %v, dark CreateTile = %v: want both Unavailable", ferr, cerr)
	}
	menu, err = h.localCl.HandshakeNS(ctx, farNS)
	if err != nil {
		t.Fatal(err)
	}
	if got := framingOf(rpc.HomeRow(menu)); !got.SameAs(theirs) {
		t.Errorf("after a refused pan the far home row = %+v, want %+v", got, theirs)
	}
}

func framingOf(pl *gridwellv1.PluginInfo) rpc.Framing {
	return rpc.Framing{Cx: pl.GetRootViewCx(), Cy: pl.GetRootViewCy(), Zoom: pl.GetRootViewZoom()}
}

// awaitFraming makes the write until its event reaches the client under
// gridID. The stream attaches behind a goroutine, so a write made before it
// did is nobody's; the write is a last-writer-wins overwrite of one value, so
// making it again changes nothing on the far node.
func awaitFraming(ctx context.Context, t *testing.T, events <-chan *gridwellv1.Event, gridID string, f rpc.Framing, write func()) {
	t.Helper()
	for {
		write()
		deadline := time.After(250 * time.Millisecond)
		for waiting := true; waiting; {
			select {
			case ev := <-events:
				fc := ev.GetGridFramingChanged()
				if fc.GetGridId() == gridID && f.SameAs(rpc.Framing{Cx: fc.GetViewCx(), Cy: fc.GetViewCy(), Zoom: fc.GetViewZoom()}) {
					return
				}
			case <-deadline:
				waiting = false
			case <-ctx.Done():
				t.Fatalf("the far node's framing event never reached the client under %s", gridID)
			}
		}
	}
}
