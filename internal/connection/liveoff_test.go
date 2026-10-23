package connection

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/internal/namespace/namespacetest"
	"github.com/josephburnett/gridwell/internal/trace"
)

// refusingFar refuses every share until accepting is set, as a far node whose
// store will not read does.
type refusingFar struct {
	namespacetest.Unimplemented
	accepting atomic.Bool
	refusal   atomic.Pointer[error] // nil is "database is locked"
	asked     chan struct{}         // told of each share, when set
}

func (f *refusingFar) SetView(context.Context, *gridwellv1.SetViewRequest) (*gridwellv1.SetViewResponse, error) {
	if f.asked != nil {
		defer func() { f.asked <- struct{}{} }()
	}
	if !f.accepting.Load() {
		if err := f.refusal.Load(); err != nil {
			return nil, *err
		}
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

// A share the far node answered Unavailable is still its refusal, so live
// updates are off; a share it never heard is the stream's news and says
// nothing here.
func TestOnlyAnAnsweredShareRefusalTurnsLiveUpdatesOff(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		off  bool
	}{
		{"answered Unavailable", status.Error(codes.Unavailable, "the far plugin is not answering"), true},
		{"unheard", gwerr.Unheard("tunnel down"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t, openStore(t))
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			far := &refusingFar{asked: make(chan struct{}, 4)}
			far.refusal.Store(&tc.err)
			mark := traceSeq()
			if _, err := s.SetView(ctx, &gridwellv1.SetViewRequest{ShownGridIds: []string{"geneva/p/1"}}); err != nil {
				t.Fatal(err)
			}
			go s.tellFar(ctx, "geneva", far)
			<-far.asked
			// The next share is asked only once the first answer is noted.
			if _, err := s.SetView(ctx, &gridwellv1.SetViewRequest{ShownGridIds: []string{"geneva/p/2"}}); err != nil {
				t.Fatal(err)
			}
			<-far.asked
			off := false
			for _, rec := range trace.Default().Snapshot() {
				off = off || rec.Seq > mark && rec.Src == "connection" && strings.Contains(rec.Msg, status.Convert(tc.err).Message())
			}
			if off != tc.off {
				t.Errorf("live updates off for %v = %v, want %v", tc.err, off, tc.off)
			}
		})
	}
}

func traceSeq() uint64 {
	recs := trace.Default().Snapshot()
	if len(recs) == 0 {
		return 0
	}
	return recs[len(recs)-1].Seq
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
