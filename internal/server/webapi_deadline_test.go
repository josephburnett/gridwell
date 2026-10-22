package server

// The web API's deadline rule: a Connect server stream held open through the
// WebAPIServer shape, over real HTTP, for longer than any deadline that shape
// declares, then proven live by an event after the hold. A declared timeout is
// a fact, and its test is a wait bound to its value. The web API twin of
// api_deadline_test.go.
//
// net/http arms ReadHeaderTimeout to read the request headers and clears it
// once they are read, so an HTTP/1 response body streams on afterward; a
// WriteTimeout would deadline the whole response write and cut this stream.
// The hold is derived from WebAPIServer, so it tracks any deadline added to
// the shape. The wait is the test and must not be shortened by lowering the
// API's timeout.

import (
	"context"
	"fmt"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/rpc"
)

func TestWebAPIHoldsAConnectStreamPastAnyDeadline(t *testing.T) {
	shape := WebAPIServer(http.NotFoundHandler())
	hold := max(shape.ReadHeaderTimeout, shape.ReadTimeout, shape.WriteTimeout) + 500*time.Millisecond
	if hold < time.Second {
		hold = time.Second
	}

	_, cl, root := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan *gridwellv1.Event, 64)
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
		if _, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: int64(n), Y: 0, W: 1, H: 1}}); err != nil {
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

	// Establish, then hold the stream idle past every deadline the API
	// declares. A WriteTimeout on the shape would cut the response mid-hold; a
	// API with only ReadHeaderTimeout, cleared once headers are read, keeps
	// the stream open through an idle wait longer than that timeout.
	awaitEvent("establish")
	start := time.Now()
	select {
	case err := <-ended:
		t.Fatalf("the web API cut the Connect stream %s into the hold: %v",
			time.Since(start).Round(100*time.Millisecond), err)
	case <-time.After(hold):
	}

	// Proven live: an event created after the hold still arrives on the same
	// stream.
	for len(events) > 0 {
		<-events
	}
	awaitEvent("after the hold")

	fmt.Printf("web API deadline: a Connect server stream held %s past the API's deadlines stayed live OK\n", hold)
}

// The browser-mode shell spec holds a shell past the web API's header timeout,
// and a spec cannot import Go, so its HEADER_TIMEOUT_MS is pinned to
// WebAPIServer here: a change to either fails until the other follows.
func TestShellDeadlineSpecTracksWebAPIServer(t *testing.T) {
	spec := filepath.Join("..", "..", "apps", "desktop", "e2e-web", "web-shell-deadline.spec.ts")
	data, err := os.ReadFile(spec)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`const HEADER_TIMEOUT_MS = ([0-9_]+);`).FindSubmatch(data)
	if m == nil {
		t.Fatalf("%s declares no HEADER_TIMEOUT_MS", spec)
	}
	ms, err := strconv.Atoi(strings.ReplaceAll(string(m[1]), "_", ""))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := time.Duration(ms)*time.Millisecond, WebAPIServer(nil).ReadHeaderTimeout; got != want {
		t.Errorf("%s HEADER_TIMEOUT_MS is %v, WebAPIServer's ReadHeaderTimeout is %v: set the spec to the server's value", spec, got, want)
	}
}
