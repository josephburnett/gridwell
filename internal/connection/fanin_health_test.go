package connection

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/namespace"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// deadSubscribeClient's event stream never opens: a mount whose tunnel is
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
	db, err := OpenDB(filepath.Join(t.TempDir(), "remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := newTestServer(t, db)
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
// cache in front of it serves a remembered grid as if it were live and the
// client's tint says the connection is fine.
func TestASubscriberArrivingAfterTheOutageIsToldOfIt(t *testing.T) {
	s := newTestServer(t, openConnDB(t))
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

// flakySubscribeClient's stream fails its first failN dials, then lives until
// its context ends: a tunnel that comes back.
type flakySubscribeClient struct {
	namespace.Namespace
	calls atomic.Int32
	failN int32
}

func (c *flakySubscribeClient) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, _ func(*gridwellv1.Event) error) error {
	if c.calls.Add(1) <= c.failN {
		return status.Error(codes.Unavailable, "tunnel down")
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

	s := newTestServer(t, openConnDB(t))
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
