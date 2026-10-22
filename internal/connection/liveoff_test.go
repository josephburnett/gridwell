package connection

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/namespace/namespacetest"
)

// refusingFar refuses every share until accepting is set, as a far node whose
// store will not read does.
type refusingFar struct {
	namespacetest.Unimplemented
	accepting atomic.Bool
}

func (f *refusingFar) SetView(context.Context, *gridwellv1.SetViewRequest) (*gridwellv1.SetViewResponse, error) {
	if !f.accepting.Load() {
		return nil, status.Error(codes.Internal, "database is locked")
	}
	return &gridwellv1.SetViewResponse{}, nil
}

// A far node that refuses this node's share watches nothing shown here, so
// the refusal is the connection's live updates off: on its healthy health
// event as a plugin's is, replayed to a stream that opens later, and cleared
// once a share lands.
func TestAShareTheFarNodeRefusesTurnsItsLiveUpdatesOff(t *testing.T) {
	s := newTestServer(t, openStore(t))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	far := &refusingFar{}
	events, stop := s.hub.Subscribe()
	t.Cleanup(stop)
	if _, err := s.SetView(ctx, &gridwellv1.SetViewRequest{ShownGridIds: []string{"geneva/p/1"}}); err != nil {
		t.Fatal(err)
	}
	go s.tellFar(ctx, "geneva", far)

	h := nextHealth(t, events, "geneva")
	if !h.GetHealthy() || h.GetLiveUpdatesOff() == "" {
		t.Fatalf("after the refusal: %v, want healthy with live updates off", h)
	}
	// A stream opened now hears it in the opening replay.
	var replay []*gridwellv1.Event
	sctx, scancel := context.WithTimeout(ctx, 2*time.Second)
	defer scancel()
	_ = s.Subscribe(sctx, &gridwellv1.SubscribeRequest{}, func(ev *gridwellv1.Event) error {
		if ev != nil {
			replay = append(replay, ev)
			scancel()
		}
		return nil
	})
	if len(replay) != 1 || replay[0].GetPluginHealth().GetPluginUuid() != "geneva" || replay[0].GetPluginHealth().GetLiveUpdatesOff() == "" {
		t.Errorf("opening replay = %v, want the connection's live updates off", replay)
	}

	far.accepting.Store(true)
	if _, err := s.SetView(ctx, &gridwellv1.SetViewRequest{ShownGridIds: []string{"geneva/p/2"}}); err != nil {
		t.Fatal(err)
	}
	h = nextHealth(t, events, "geneva")
	if !h.GetHealthy() || h.GetLiveUpdatesOff() != "" {
		t.Fatalf("after a share landed: %v, want healthy with live updates on", h)
	}
}

func nextHealth(t *testing.T, events <-chan *gridwellv1.Event, conn string) *gridwellv1.EventPluginHealth {
	t.Helper()
	for {
		select {
		case ev := <-events:
			if h := ev.GetPluginHealth(); h != nil && h.GetPluginUuid() == conn {
				return h
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no health event for %s", conn)
		}
	}
}
