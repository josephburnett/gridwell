package connection

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/namespace"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// deadSubscribeClient's event stream never opens: a connection whose tunnel is
// down.
type deadSubscribeClient struct {
	namespace.Namespace
}

func (deadSubscribeClient) Subscribe(context.Context, *gridwellv1.SubscribeRequest, func(*gridwellv1.Event) error) error {
	return status.Error(codes.Unavailable, "tunnel down")
}

// A connection whose event stream dies must say so: the fan-in publishes an
// EventPluginHealth down transition, the same contract the node's plugin
// fan-in keeps. Retrying silently presents as tiles that stopped updating
// with no evidence.
func TestFanInRemotePublishesHealthOnStreamDeath(t *testing.T) {
	s := newTestServer(t, openStore(t))
	events, unsub := s.hub.Subscribe()
	t.Cleanup(unsub)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.fanInRemote(ctx, "rtb", deadSubscribeClient{})

	select {
	case ev := <-events:
		ph := ev.GetPluginHealth()
		if ph == nil {
			t.Fatalf("first event = %+v, want a plugin-health transition", ev)
		}
		if ph.Healthy || ph.PluginUuid != "rtb" || ph.Detail == "" {
			t.Fatalf("want an unhealthy rtb transition with a reason, got %+v", ph)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream death published nothing — the outage is invisible")
	}
}

// Darkness is a state, not a moment. A client that opens while the machine is
// already gone gets no transition — that fired before it existed — so the
// stream has to tell it what the transport currently knows, or the source
// cache in front of it serves a cached grid as if it were live and the
// client's tint says the connection is fine.
func TestASubscriberArrivingAfterTheOutageIsToldOfIt(t *testing.T) {
	s := newTestServer(t, openStore(t))
	first, unsub := s.hub.Subscribe()
	t.Cleanup(unsub)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.fanInRemote(ctx, "rtb", deadSubscribeClient{})
	// The transition, on a subscriber that was there for it: after this the
	// fan-in retries in silence, so nothing will ever publish it again.
	select {
	case ev := <-first:
		if ph := ev.GetPluginHealth(); ph == nil || ph.Healthy {
			t.Fatalf("first event = %+v, want the down transition", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream death published nothing — the outage is invisible")
	}

	// Now the late client.
	late := make(chan *gridwellv1.Event, 8)
	subCtx, subCancel := context.WithCancel(context.Background())
	t.Cleanup(subCancel)
	go func() {
		_ = s.Subscribe(subCtx, &gridwellv1.SubscribeRequest{}, func(ev *gridwellv1.Event) error {
			if ev == nil {
				return nil // the header
			}
			select {
			case late <- ev:
			default:
			}
			return nil
		})
	}()
	select {
	case ev := <-late:
		ph := ev.GetPluginHealth()
		if ph == nil {
			t.Fatalf("first event to the late subscriber = %+v, want the connection's health", ev)
		}
		if ph.Healthy || ph.PluginUuid != "rtb" || ph.Detail == "" {
			t.Fatalf("want rtb dark with a reason, got %+v", ph)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a subscriber arriving after the outage was never told the connection is dark")
	}
}

// refusingDialer is a connection that cannot be dialed: the far machine is
// off, or the tunnel will not open.
func refusingDialer(detail string) Dialer {
	return func(dial.Config) (namespace.Namespace, func(), error) {
		return nil, nil, errors.New(detail)
	}
}

// A dial that never connects is the same outage as a stream that died: the
// connection is unreachable. A client subscribing mid-outage has to be told,
// or the source cache in front serves a cached grid as if it were live —
// the dial failure never reached the fan-in, so nothing ever published it.
func TestASubscriberIsToldOfAConnectionThatCannotDial(t *testing.T) {
	ctx := context.Background()
	s, err := New(openStore(t), refusingDialer("no route to host"), "",
		[]config.ConnectionConfig{{Name: "rtb", Addr: "/s"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.ConnectAll(ctx)

	events := make(chan *gridwellv1.Event, 8)
	subCtx, subCancel := context.WithCancel(ctx)
	t.Cleanup(subCancel)
	go func() {
		_ = s.Subscribe(subCtx, &gridwellv1.SubscribeRequest{}, func(ev *gridwellv1.Event) error {
			if ev == nil {
				return nil // the header
			}
			select {
			case events <- ev:
			default:
			}
			return nil
		})
	}()
	select {
	case ev := <-events:
		ph := ev.GetPluginHealth()
		if ph == nil {
			t.Fatalf("first event = %+v, want the connection's health", ev)
		}
		if ph.Healthy || ph.PluginUuid != "rtb" || ph.Detail == "" {
			t.Fatalf("want rtb dark with a reason, got %+v", ph)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a connection whose dial is failing was never reported dark")
	}
}

// flakySubscribeClient's stream fails its first failN dials, then listens until
// its context ends: a tunnel that comes back.
type flakySubscribeClient struct {
	namespace.Namespace
	calls atomic.Int32
	failN int32
}

func (c *flakySubscribeClient) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, send func(*gridwellv1.Event) error) error {
	if c.calls.Add(1) <= c.failN {
		return status.Error(codes.Unavailable, "tunnel down")
	}
	if err := send(nil); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// A connection's fan-in waits namespace.DefaultBackoff, the same policy the
// node's plugin fan-in waits, and reports the outage once however many
// re-dials it takes. Lowering the policy is what proves the loop reads it.
func TestFanInRemoteRetriesOnTheDeclaredBackoff(t *testing.T) {
	was := namespace.DefaultBackoff
	t.Cleanup(func() { namespace.DefaultBackoff = was })
	namespace.DefaultBackoff = namespace.Backoff{First: 10 * time.Millisecond, Max: 20 * time.Millisecond}

	s := newTestServer(t, openStore(t))
	events, unsub := s.hub.Subscribe()
	t.Cleanup(unsub)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	start := time.Now()
	go s.fanInRemote(ctx, "rtb", &flakySubscribeClient{failN: 4})

	downs := 0
	for {
		select {
		case ev := <-events:
			ph := ev.GetPluginHealth()
			if ph == nil {
				continue
			}
			if !ph.Healthy {
				downs++
				continue
			}
			if downs != 1 {
				t.Fatalf("%d down transitions before the recovery: four re-dials are one outage", downs)
			}
			// Far under the 15s four failures would take on the declared
			// policy, and loose enough for a loaded machine.
			if took := time.Since(start); took > 3*time.Second {
				t.Errorf("recovery took %v: the fan-in is waiting on numbers of its own, not namespace.DefaultBackoff", took)
			}
			return
		case <-time.After(5 * time.Second):
			t.Fatal("the connection came back and the fan-in never published the recovery")
		}
	}
}

// lateListeningFar is a far node that comes back slowly: its first stream
// fails, and the next starts listening only after attachAfter, sending its
// header then. A change made before it listens reaches no stream.
type lateListeningFar struct {
	namespace.Namespace
	attachAfter time.Duration
	calls       atomic.Int32
	listening   atomic.Pointer[func(*gridwellv1.Event) error]
}

func (f *lateListeningFar) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, send func(*gridwellv1.Event) error) error {
	if f.calls.Add(1) == 1 {
		return status.Error(codes.Unavailable, "tunnel down")
	}
	select {
	case <-time.After(f.attachAfter):
	case <-ctx.Done():
		return nil
	}
	f.listening.Store(&send)
	if err := send(nil); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func (f *lateListeningFar) change(gridID string) {
	if send := f.listening.Load(); send != nil {
		_ = (*send)(&gridwellv1.Event{Payload: &gridwellv1.Event_GridChanged{GridChanged: &gridwellv1.GridChanged{GridId: gridID}}})
	}
}

// A connection is back when the far node says it is listening, and not
// before: a change made the moment the recovery is published reaches the near
// node, however long the far node took to start listening.
func TestAConnectionIsBackOnlyOnceTheFarNodeListens(t *testing.T) {
	was := namespace.DefaultBackoff
	t.Cleanup(func() { namespace.DefaultBackoff = was })
	namespace.DefaultBackoff = namespace.Backoff{First: 10 * time.Millisecond, Max: 20 * time.Millisecond}

	s := newTestServer(t, openStore(t))
	events, unsub := s.hub.Subscribe()
	t.Cleanup(unsub)
	far := &lateListeningFar{attachAfter: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.fanInRemote(ctx, "rtb", far)

	deadline := time.After(10 * time.Second)
	for back := false; !back; {
		select {
		case ev := <-events:
			back = ev.GetPluginHealth().GetHealthy()
		case <-deadline:
			t.Fatal("the far node came back and the connection never said so")
		}
	}
	far.change("g")
	for {
		select {
		case ev := <-events:
			if ev.GetGridChanged().GetGridId() == "rtb/g" {
				return
			}
		case <-deadline:
			t.Fatal("a change made once the connection read as back never arrived: it was back before the far node listened")
		}
	}
}
