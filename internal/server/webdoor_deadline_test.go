package server

// The web door's deadline rule: a Connect server stream held open through the
// web door shape, over real HTTP, for longer than any deadline WebDoorServer
// declares, then proven live by an event after the hold.
//
// THE RULE: a declared timeout is a fact, and its test is a wait bound to its
// value; a timeout with no such test is untested. This is the web-door twin of
// door_deadline_test.go, which holds a raw-gRPC Subscribe through the
// connection door shape. The web door is a different shape on purpose — it
// keeps ReadHeaderTimeout because it faces a network and carries no raw-gRPC
// stream (see WebDoorServer) — so its own long-lived stream, a Connect
// server-stream Subscribe, needs its own hold past what it declares.
//
// Why a Connect stream survives ReadHeaderTimeout: net/http arms that deadline
// to read the REQUEST headers and clears it once they are read, so an HTTP/1
// response body streams on indefinitely afterward. A re-added WriteTimeout,
// though, deadlines the whole response WRITE and would cut this stream at that
// bound. The hold is DERIVED from WebDoorServer, so it AUTO-TRACKS any deadline
// added to the shape: today it is ReadHeaderTimeout(10s) + margin, and a stream
// held that long and still delivering proves the header timeout does not cut
// it; add a WriteTimeout and the hold lengthens and the stream dies inside it.
//
// This runs under `make check` and, bound to the web door's 10s
// ReadHeaderTimeout, takes ~10.5s — the wait IS the test; it must not be
// shortened by lowering the door's timeout.

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/rpc"
)

func TestWebDoorHoldsAConnectStreamPastAnyDeadline(t *testing.T) {
	shape := WebDoorServer(http.NotFoundHandler())
	hold := max(shape.ReadHeaderTimeout, shape.ReadTimeout, shape.WriteTimeout) + 500*time.Millisecond
	if hold < time.Second {
		hold = time.Second
	}

	_, cl, root := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan rpc.Event, 64)
	ended := make(chan error, 1)
	go func() {
		sub, err := cl.Subscribe(ctx)
		if err != nil {
			ended <- err
			return
		}
		defer sub.Close()
		for {
			ev, ok, err := sub.Recv()
			if err != nil || !ok {
				ended <- err
				return
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()

	// createText makes a tile, which the hub publishes as an event to the open
	// Subscribe. Used both to establish the stream and to prove it live after
	// the hold.
	n := 0
	createText := func() {
		if _, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
			GridID: root, X: int64(n), Y: 0, W: 1, H: 1,
		}); err != nil {
			t.Fatalf("CreateText: %v", err)
		}
		n++
	}

	// awaitEvent drains until an event lands on the stream, creating tiles to
	// drive one; Subscribe registers asynchronously, so the first create can
	// race establishment.
	awaitEvent := func(what string) {
		tick := time.NewTicker(300 * time.Millisecond)
		defer tick.Stop()
		deadline := time.After(15 * time.Second)
		createText()
		for {
			select {
			case <-events:
				return
			case err := <-ended:
				t.Fatalf("%s: the Connect stream ended: %v", what, err)
			case <-tick.C:
				createText()
			case <-deadline:
				t.Fatalf("%s: no event arrived on the Connect stream", what)
			}
		}
	}

	// Establish, then hold the stream idle past every deadline the door
	// declares. A WriteTimeout on the shape would cut the response mid-hold; a
	// door with only ReadHeaderTimeout, cleared once headers are read, keeps
	// the stream open through an idle wait longer than that timeout.
	awaitEvent("establish")
	start := time.Now()
	select {
	case err := <-ended:
		t.Fatalf("the web door cut the Connect stream %s into the hold: %v",
			time.Since(start).Round(100*time.Millisecond), err)
	case <-time.After(hold):
	}

	// Proven live: an event created after the hold still arrives on the same
	// stream.
	for len(events) > 0 {
		<-events
	}
	awaitEvent("after the hold")

	fmt.Printf("web door deadline: a Connect server stream held %s past the door's deadlines stayed live OK\n", hold)
}
