package connection

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// Only a connection this node declares can be switched off; the switch
// closes its transport once, however often it is thrown, and every later
// read through it is refused without a dial. The record is the reason its
// rows carry, by its label.
func TestDisableStopsOnceAndOnlyADeclaredConnection(t *testing.T) {
	ctx := context.Background()
	var dials, closes atomic.Int32
	s, err := New(openStore(t), func(dial.Config) (namespace.Namespace, func(), error) {
		dials.Add(1)
		return movingClient{root: new(string)}, func() { closes.Add(1) }, nil
	}, "", []config.ConnectionConfig{{Name: "rtb", Label: "RTB", Addr: "/s"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	_, _ = s.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: "rtb/rnode1/7"})
	if err := s.Disable("nobody"); err == nil || !strings.Contains(err.Error(), "nobody") {
		t.Errorf("Disable of an undeclared name = %v, want refused naming it", err)
	}
	if s.DisabledReason("rtb") != "" || len(s.DisabledNow()) != 0 {
		t.Fatal("a connection is on until it is disabled")
	}
	for range 2 {
		if err := s.Disable("rtb"); err != nil {
			t.Fatal(err)
		}
	}
	if got := closes.Load(); got != 1 {
		t.Errorf("the transport closed %d times, want once", got)
	}
	if got, want := s.DisabledReason("rtb"), rpc.DisabledReason("RTB"); got != want {
		t.Errorf("DisabledReason = %q, want %q", got, want)
	}
	if got := s.DisabledNow(); !slices.Equal(got, []string{"rtb"}) {
		t.Errorf("DisabledNow = %v, want [rtb]", got)
	}
	before := dials.Load()
	if _, err := s.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: "rtb/rnode1/7"}); err == nil {
		t.Error("a read through a disabled connection answered")
	}
	if dials.Load() != before {
		t.Error("a read through a disabled connection dialed")
	}
}

// heldClient holds every body read until its connection closes, and then
// fails it as a closing gRPC client does.
type heldClient struct {
	movingClient
	asked  chan struct{}
	closed chan struct{}
}

func (c heldClient) ReadContent(context.Context, *gridwellv1.ReadContentRequest, func(*gridwellv1.ContentChunk) error) error {
	c.asked <- struct{}{}
	<-c.closed
	return gwerr.Unheard("grpc: the client connection is closing")
}

// A call in flight when the user disables its connection answers as one
// asked after: the disabled verdict, unheard, never the closing transport's
// cancel, which a reader would take for an outage and tell.
func TestACallTheDisableCutsAnswersTheDisabledVerdict(t *testing.T) {
	far := heldClient{movingClient: movingClient{root: new(string)}, asked: make(chan struct{}), closed: make(chan struct{})}
	s, err := New(openStore(t), func(dial.Config) (namespace.Namespace, func(), error) {
		return far, func() { close(far.closed) }, nil
	}, "", []config.ConnectionConfig{{Name: "rtb", Label: "RTB", Addr: "/s"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	read := make(chan error, 1)
	go func() {
		read <- s.ReadContent(context.Background(), &gridwellv1.ReadContentRequest{TileId: "rtb/rnode1/7"},
			func(*gridwellv1.ContentChunk) error { return nil })
	}()
	<-far.asked
	if err := s.Disable("rtb"); err != nil {
		t.Fatal(err)
	}
	if err := <-read; !gwerr.IsSourceDisabled(err) || !gwerr.IsUnheard(err) {
		t.Errorf("the cut read answered %v, want the disabled verdict, unheard", err)
	}
}
