package server_test

// The view through the web API: what each client says it shows is counted
// while that client's event stream is open, and reaches each namespace as its
// share of the union.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
	"github.com/josephburnett/gridwell/internal/trace"
)

// subscriptionSink is a namespace that records the last share it was handed.
type subscriptionSink struct {
	namespace.Unimplemented
	mu    sync.Mutex
	share []string
	heard int
}

func (s *subscriptionSink) Info(context.Context, *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	return &gridwellv1.InfoResponse{}, nil
}

func (s *subscriptionSink) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, send func(*gridwellv1.Event) error) error {
	if err := send(nil); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func (s *subscriptionSink) SetView(_ context.Context, req *gridwellv1.SetViewRequest) (*gridwellv1.SetViewResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.share, s.heard = slices.Clone(req.ShownGridIds), s.heard+1
	return &gridwellv1.SetViewResponse{}, nil
}

// await waits for the sink's share to be want.
func (s *subscriptionSink) await(t *testing.T, want []string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		got, heard := slices.Clone(s.share), s.heard
		s.mu.Unlock()
		if heard > 0 && slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("share = %v (heard %d), want %v", got, heard, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// subscribed opens cl's event stream and returns its close. The open is not
// awaited: a view sent before the node sees the stream is held until it does.
func subscribed(cl *rpc.Client) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		es, err := cl.Subscribe(ctx)
		if err != nil {
			return
		}
		defer es.Close()
		for {
			if _, ok, err := es.Recv(); err != nil || !ok {
				return
			}
		}
	}()
	return cancel
}

// Two clients showing overlapping grids of one namespace hand it the union,
// peeled to its own ids, and a grid of another namespace is not its business;
// a client whose stream closes takes its grids with it.
func TestSubscriptionReachesEachNamespaceAsItsShare(t *testing.T) {
	const a, b = "pinta01", "pintb01"
	sinkA, sinkB := &subscriptionSink{}, &subscriptionSink{}
	reg := plugin.NewRegistry()
	reg.Register(a, "feed", sinkA, nil)
	reg.Register(b, "feed", sinkB, nil)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	one := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	two := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx := context.Background()

	closeOne := subscribed(one)
	closeTwo := subscribed(two)
	if err := one.SetView(ctx, &gridwellv1.SetViewRequest{ShownGridIds: []string{a + "/1", a + "/2", b + "/9"}}); err != nil {
		t.Fatal(err)
	}
	if err := two.SetView(ctx, &gridwellv1.SetViewRequest{ShownGridIds: []string{a + "/2", a + "/3"}}); err != nil {
		t.Fatal(err)
	}
	sinkA.await(t, []string{"1", "2", "3"})
	sinkB.await(t, []string{"9"})

	closeOne()
	sinkA.await(t, []string{"2", "3"})
	sinkB.await(t, nil)
	closeTwo()
	sinkA.await(t, nil)
}

// A grid behind a connection is the far node's to watch: the transport tells
// it, peeled of the connection segment, under the connection's own session,
// and takes it back when the client here stops showing it.
func TestSubscriptionCrossesAConnection(t *testing.T) {
	const far = "pintf01"
	sink := &subscriptionSink{}
	h := newTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil,
		func(reg *plugin.Registry, _ *store.Store) { reg.Register(far, "feed", sink, nil) })
	ctx := context.Background()

	closeSub := subscribed(h.localCl)
	if err := h.localCl.SetView(ctx, &gridwellv1.SetViewRequest{ShownGridIds: []string{localNodeID + "/geneva/" + far + "/5"}}); err != nil {
		t.Fatal(err)
	}
	sink.await(t, []string{"5"})
	closeSub()
	sink.await(t, nil)
}

// A session's whole view crosses the web API and is readable from
// Sessions while its stream is open, with its label, panes and focus, and
// once the stream closes, as a phone's does in the background, it is still
// listed, away since the close and watching nothing.
func TestSessionsHoldEachViewOpenThenAway(t *testing.T) {
	const a = "pinta01"
	sink := &subscriptionSink{}
	reg := plugin.NewRegistry()
	reg.Register(a, "feed", sink, nil)
	srv := servertest.New(t, reg, server.Config{ID: localNodeID})
	hs := servertest.Serve(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx := context.Background()

	closeSub := subscribed(cl)
	view := &gridwellv1.SetViewRequest{
		Label:          "browser",
		ShownGridIds:   []string{a + "/2", a + "/7"},
		WorkspaceTiles: []string{localNodeID + "9"},
		Panes: []*gridwellv1.ViewPane{
			{Id: "p0", Anchor: a + "/1", Path: []string{a + "/4"}, GridId: a + "/2", Focused: true},
			{Id: "p1", Anchor: a + "/1", Path: []string{a + "/4"}, ContentId: a + "/5", GridId: a + "/2"},
		},
	}
	if err := cl.SetView(ctx, view); err != nil {
		t.Fatal(err)
	}
	sink.await(t, []string{"2", "7"})
	var held []server.Session
	deadline := time.Now().Add(5 * time.Second)
	for len(held) == 0 && time.Now().Before(deadline) {
		held = srv.Sessions()
		time.Sleep(10 * time.Millisecond)
	}
	if len(held) != 1 {
		t.Fatalf("sessions = %v, want the one client", held)
	}
	got := held[0]
	if got.ID == "" || got.Changed.IsZero() || !got.Open || !got.Away.IsZero() {
		t.Errorf("session %q changed %v open %v away %v, want an id, a time, open", got.ID, got.Changed, got.Open, got.Away)
	}
	want := proto.CloneOf(view)
	want.Session = got.ID
	if !proto.Equal(got.View, want) {
		t.Errorf("held view = %v, want %v", got.View, want)
	}

	closeSub()
	sink.await(t, nil)
	held = srv.Sessions()
	if len(held) != 1 || held[0].ID != got.ID || held[0].Open || held[0].Away.IsZero() || !proto.Equal(held[0].View, want) {
		t.Errorf("after the stream closed, sessions = %v, want %s away with its view", held, got.ID)
	}
}

// refusingSink is a namespace that refuses every share it is handed.
type refusingSink struct{ subscriptionSink }

func (s *refusingSink) SetView(context.Context, *gridwellv1.SetViewRequest) (*gridwellv1.SetViewResponse, error) {
	return nil, errors.New("this source cannot watch")
}

// awaitRecord waits for a record ok accepts, since a stream's records are
// written as the node gets to them.
func awaitRecord(t *testing.T, what string, ok func(tracewire.Record) bool) tracewire.Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, rec := range trace.Default().Snapshot() {
			if ok(rec) {
				return rec
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s record", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A session's stream and view cross the web API into the trace under the
// session's id: the Subscribe span and its listening record, the stream
// counted open, the view set, a namespace refusing its share, and the stream
// going away when it closes.
func TestASessionsStreamAndViewAreTracedUnderItsID(t *testing.T) {
	const a = "pintr01"
	reg := plugin.NewRegistry()
	reg.Register(a, "feed", &refusingSink{}, nil)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	session := cl.Session()
	of := func(src, kind, msg string) func(tracewire.Record) bool {
		return func(rec tracewire.Record) bool {
			return rec.Src == src && rec.Kind == kind && strings.HasPrefix(rec.Msg, msg) && rec.KV["session"] == session
		}
	}

	closeSub := subscribed(cl)
	t.Cleanup(closeSub)
	awaitRecord(t, "Subscribe start", of("router", "rpc", "Subscribe start"))
	awaitRecord(t, "Subscribe listening", of("router", "rpc", "Subscribe listening"))
	if rec := awaitRecord(t, "view/session open", of("view", "session", "open")); rec.KV["streams"] != "1" {
		t.Errorf("open record = %v, want streams 1", rec.KV)
	}
	if err := cl.SetView(context.Background(), &gridwellv1.SetViewRequest{Label: "browser", ShownGridIds: []string{a + "/4"}}); err != nil {
		t.Fatal(err)
	}
	if rec := awaitRecord(t, "view/set", of("view", "set", "")); rec.KV["label"] != "browser" {
		t.Errorf("view/set = %v, want label browser", rec.KV)
	}
	awaitRecord(t, "view/refused", func(rec tracewire.Record) bool {
		return rec.Src == "view" && rec.Kind == "refused" && rec.KV["ns"] == a && strings.Contains(rec.Msg, "cannot watch")
	})
	closeSub()
	awaitRecord(t, "view/session away", of("view", "session", "away"))
}
