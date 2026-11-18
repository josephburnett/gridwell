package connection

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// orderedFar is a far node whose event stream and Info each answer as the
// test says: a stream refused (listens false) or listening, and an Info held
// until answer is closed, then landing or failing.
type orderedFar struct {
	landingClient
	listens   bool
	listening chan struct{} // closed once the stream has sent its header
	answer    chan struct{}
	infoErr   error
}

func (f *orderedFar) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, send func(*gridwellv1.Event) error) error {
	if !f.listens {
		return status.Error(codes.Unavailable, "tunnel down")
	}
	if err := send(nil); err != nil {
		return err
	}
	close(f.listening)
	<-ctx.Done()
	return nil
}

func (f *orderedFar) Info(ctx context.Context, req *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	select {
	case <-f.answer:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if f.infoErr != nil {
		return nil, f.infoErr
	}
	return f.landingClient.Info(ctx, req)
}

func orderedTransport(t *testing.T, far *orderedFar) *Server {
	t.Helper()
	s, err := New(openStore(t), func(dial.Config) (namespace.Namespace, func(), error) {
		return far, func() {}, nil
	}, "", []config.ConnectionConfig{{Name: "rtb", Addr: "/s"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// An Info that answers while the event stream is down is an answer, so the
// connection is not dark, and its changes are still not told: the stream is
// what tells them, and it has not said it is listening.
func TestAnInfoAfterTheStreamFailedIsUpButNotTold(t *testing.T) {
	far := &orderedFar{landingClient: landingClient{root: "rnode1/7"}, answer: make(chan struct{})}
	s := orderedTransport(t, far)
	events, unsub := s.hub.Subscribe()
	t.Cleanup(unsub)

	s.kickRootFetch(s.conns["rtb"])
	if h := nextHealth(t, events, "rtb"); h.GetHealthy() {
		t.Fatalf("after the stream failed: %v, want down", h)
	}
	close(far.answer)
	h := nextHealth(t, events, "rtb")
	if !h.GetHealthy() || h.Told == nil || h.GetTold() {
		t.Fatalf("after Info answered with the stream down: %v, want healthy and not told", h)
	}
	if st := s.stateOf("rtb"); !st.up || st.told {
		t.Fatalf("state = %+v, want up and not told", st)
	}
	var standing bool
	for _, ev := range s.healthNow() {
		if h := ev.GetSourceHealth(); h.GetUuid() == "rtb" && h.Told != nil && !h.GetTold() {
			standing = true
		}
	}
	if !standing {
		t.Fatal("a stream opening later is not told that rtb's changes are not told")
	}
}

// An Info that fails while the event stream is listening says nothing the
// stream does not contradict: the far node is answering.
func TestAnInfoFailingWhileTheStreamListensStaysUp(t *testing.T) {
	far := &orderedFar{landingClient: landingClient{root: "rnode1/7"}, listens: true,
		listening: make(chan struct{}), answer: make(chan struct{}), infoErr: status.Error(codes.Internal, "database is locked")}
	s := orderedTransport(t, far)
	go func() {
		select {
		case <-far.listening:
		case <-time.After(5 * time.Second):
		}
		close(far.answer)
	}()
	if _, err := s.learnRoot(s.conns["rtb"]); err == nil {
		t.Fatal("the learn answered; want its Info's failure")
	}
	if st := s.stateOf("rtb"); !st.up {
		t.Fatalf("state = %+v after an Info failed with the stream listening, want up", st)
	}
}

// derive over the facts each writer owns: every order the stream and the
// landing can speak in, and each fact that outranks them.
func TestDeriveHealth(t *testing.T) {
	failed := func(at uint64, why string) reading { return reading{detail: why, at: at} }
	answered := func(at uint64) reading { return reading{ok: true, at: at} }
	for _, tc := range []struct {
		name               string
		f                  connFacts
		up, told, mismatch bool
		detail             string
	}{
		{name: "nothing known", up: true, told: true},
		{name: "disabled outranks a listening stream", f: connFacts{off: true, stream: answered(1)}, told: true, detail: rpc.DisabledDetail},
		{name: "a dial that cannot run", f: connFacts{dialErr: "no route"}, told: true, detail: "no route"},
		{name: "a mismatch outranks a listening stream", f: connFacts{stream: answered(1), landing: failed(2, "lands elsewhere"), mismatch: true}, told: true, mismatch: true, detail: "lands elsewhere"},
		{name: "a listening stream outlasts a failed Info", f: connFacts{stream: answered(1), landing: failed(2, "locked")}, up: true, told: true, detail: "locked"},
		{name: "an Info after the stream failed", f: connFacts{stream: failed(1, "tunnel down"), landing: answered(2)}, up: true},
		{name: "the stream failing after the Info", f: connFacts{landing: answered(1), stream: failed(2, "tunnel down")}, detail: "tunnel down"},
		{name: "a failed Info before any stream", f: connFacts{landing: failed(1, "timeout")}, told: true, detail: "timeout"},
		{name: "both failed, the stream last", f: connFacts{landing: failed(1, "timeout"), stream: failed(2, "tunnel down")}, detail: "tunnel down"},
		{name: "the far node refused the share", f: connFacts{stream: answered(1), liveOff: "locked"}, up: true},
	} {
		st := derive(tc.f)
		if st.up != tc.up || st.told != tc.told || st.mismatch != tc.mismatch || st.detail != tc.detail || st.liveOff != tc.f.liveOff {
			t.Errorf("%s: %+v, want up %t told %t mismatch %t detail %q", tc.name, st, tc.up, tc.told, tc.mismatch, tc.detail)
		}
	}
}
