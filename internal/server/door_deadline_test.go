package server_test

// The connection door's server shape, at the seam a mounter lives on: one
// gRPC Subscribe held open through the door for longer than any deadline the
// door declares, then proven live by an event. net/http arms
// ReadHeaderTimeout on the raw conn before the unencrypted HTTP/2 handoff
// (Go 1.26.6) and the h2 side only disarms a deadline when ReadTimeout is
// set, so any deadline on the door is a ticking close on every stream through
// it: the event Subscribe dies at the deadline and a unary call that lands
// after it sees the same EOF. The hold is derived from the shape under test,
// so a deadline that returns makes this test wait it out and fail.

import (
	"context"
	"net/http"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/server"
)

func TestConnectionDoorHoldsAStreamPastAnyDeadline(t *testing.T) {
	shape := server.ConnectionDoorServer(http.NotFoundHandler())
	hold := max(shape.ReadHeaderTimeout, shape.ReadTimeout, shape.WriteTimeout) + 500*time.Millisecond
	if hold < time.Second {
		hold = time.Second
	}

	c, _ := nodeServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan *gridwellv1.Event, 64)
	opened := make(chan struct{})
	ended := make(chan error, 1)
	go func() {
		ended <- namespace.Follow(ctx, c, &gridwellv1.SubscribeRequest{},
			func(ev *gridwellv1.Event) error {
				select {
				case events <- ev:
				default:
				}
				return nil
			},
			func() { close(opened) })
	}()
	select {
	case <-opened:
	case err := <-ended:
		t.Fatalf("subscribe ended before it was established: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("subscribe never established")
	}

	start := time.Now()
	select {
	case err := <-ended:
		t.Fatalf("the door closed a stream %s old: %v", time.Since(start).Round(100*time.Millisecond), err)
	case <-time.After(hold):
	}

	// Open is not the same as served: an event written after the hold must
	// arrive on the same stream.
	for len(events) > 0 {
		<-events
	}
	if _, err := c.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: homeRoot(t, c),
		Tile:   &gridwellv1.Tile{Kind: "text", X: 1, Y: 1, W: 2, H: 2},
	}); err != nil {
		t.Fatalf("CreateTile after the hold: %v", err)
	}
	select {
	case <-events:
	case err := <-ended:
		t.Fatalf("the stream ended after %s: %v", hold, err)
	case <-time.After(5 * time.Second):
		t.Fatalf("no event on a stream held %s", hold)
	}
}
