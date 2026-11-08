package server

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace/namespacetest"
)

// The header is the stream's first word even with a source switched off:
// anything sent before it flushes the response, and a client takes that as
// the node listening (rpc.Client.Subscribe) and resyncs before it is.
func TestTheHeaderGoesOutBeforeADisabledSourceIsTold(t *testing.T) {
	reg := &Sources{}
	home, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = home.Close() })
	node, _ := registerPrimaryLocaldb(t, reg, home)
	sw := &oneSwitch{name: "geneva"}
	reg.Switches = sw
	if err := sw.Disable("geneva"); err != nil {
		t.Fatal(err)
	}
	srv := mustNew(t, reg, Config{ID: node})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := errors.New("stop after the first word")
	var said []*gridwellv1.Event
	err = newRouter(srv).Subscribe(ctx, &gridwellv1.SubscribeRequest{}, func(ev *gridwellv1.Event) error {
		said = append(said, ev)
		return first
	})
	if !errors.Is(err, first) {
		t.Fatalf("Subscribe = %v", err)
	}
	if said[0] != nil {
		t.Errorf("the stream's first word = %v, want the header", said[0])
	}
}

// A source that fails while another is still settling is told after the
// header too, for the same reason.
func TestTheHeaderGoesOutBeforeAFailedSourceIsTold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		reg := &Sources{}
		reg.Files = heldStream{release: release}
		reg.Transport = namespacetest.Unimplemented{}
		srv := mustNew(t, reg, Config{ID: "lnode1"})

		ctx, cancel := context.WithCancel(t.Context())
		said := make(chan *gridwellv1.Event, 16)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = newRouter(srv).Subscribe(ctx, &gridwellv1.SubscribeRequest{}, func(ev *gridwellv1.Event) error {
				said <- ev
				return nil
			})
		}()
		synctest.Wait()
		select {
		case ev := <-said:
			t.Errorf("before every source settled the stream said %v", ev)
		default:
		}
		close(release)
		if ev := <-said; ev != nil {
			t.Errorf("the stream's first word = %v, want the header", ev)
		}
		cancel()
		<-done
	})
}

// heldStream is a source whose stream says it is listening only on release.
type heldStream struct {
	namespacetest.Unimplemented
	release <-chan struct{}
}

func (heldStream) Info(context.Context, *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	return &gridwellv1.InfoResponse{}, nil
}

func (h heldStream) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, send func(*gridwellv1.Event) error) error {
	select {
	case <-h.release:
	case <-ctx.Done():
		return nil
	}
	if err := send(nil); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}
