package remote

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// deadSubscribeClient's event stream never opens — a mount whose tunnel
// is down.
type deadSubscribeClient struct {
	gridwellv1.GridwellClient
}

func (deadSubscribeClient) Subscribe(context.Context, *gridwellv1.SubscribeRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[gridwellv1.Event], error) {
	return nil, status.Error(codes.Unavailable, "tunnel down")
}

// A connection whose event stream dies must SAY so: the fan-in publishes
// an EventPluginHealth down transition (the contract fanInEvents keeps
// for local plugins, issue #47). Before the fix it broke into a bare 5s
// retry — "tiles stopped updating" with no evidence.
func TestFanInRemotePublishesHealthOnStreamDeath(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := New(db, nil, "")
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
