package rpc

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
)

// A stream is open when the node says it is listening, not when the request
// left: a caller that writes, or re-reads to resync, before then can make a
// change the node tells nobody.
func TestSubscribeReturnsOnlyOnceTheNodeIsListening(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		_, h := gridwellv1connect.NewGridwellHandler(heldHeader{release: release})
		cl := NewClient(&http.Client{Transport: memTransport{h}}, "http://node", connect.WithProtoJSON())
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		opened := make(chan *EventStream, 1)
		go func() {
			s, err := cl.Subscribe(ctx)
			if err != nil {
				t.Error(err)
			}
			opened <- s
		}()
		synctest.Wait()
		select {
		case s := <-opened:
			_ = s.Close()
			t.Fatal("Subscribe returned before the node sent its header")
		default:
		}

		close(release)
		s := <-opened
		ev, ok, err := s.Recv()
		if err != nil || !ok || ev.GetGridChanged().GetGridId() != "g" {
			t.Errorf("first event after the header = %v, %v, %v; want the GridChanged sent after it", ev, ok, err)
		}
		cancel()
		_ = s.Close()
	})
}

// heldHeader is a node whose stream says it is listening only on release.
type heldHeader struct {
	gridwellv1connect.UnimplementedGridwellHandler
	release <-chan struct{}
}

func (h heldHeader) Subscribe(ctx context.Context, _ *connect.Request[pb.SubscribeRequest], stream *connect.ServerStream[pb.Event]) error {
	select {
	case <-h.release:
	case <-ctx.Done():
		return nil
	}
	if err := stream.Send(nil); err != nil {
		return err
	}
	if err := stream.Send(&pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: "g"}}}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// memTransport serves a handler in memory, so every wait in the exchange is
// a channel synctest can see: the response is returned when the handler
// first writes or flushes, as a real server's headers would arrive.
type memTransport struct{ h http.Handler }

func (m memTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	pr, pw := io.Pipe()
	w := &memWriter{header: http.Header{}, body: pw, ready: make(chan struct{})}
	go func() {
		m.h.ServeHTTP(w, req)
		w.WriteHeader(http.StatusOK)
		_ = pw.Close()
	}()
	select {
	case <-w.ready:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	return &http.Response{StatusCode: w.code, Header: w.sent, Body: pr, Request: req,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1}, nil
}

type memWriter struct {
	header, sent http.Header
	body         *io.PipeWriter
	once         sync.Once
	code         int
	ready        chan struct{}
}

func (w *memWriter) Header() http.Header { return w.header }

func (w *memWriter) WriteHeader(code int) {
	w.once.Do(func() {
		w.code, w.sent = code, w.header.Clone()
		close(w.ready)
	})
}

func (w *memWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(b)
}

func (w *memWriter) Flush() { w.WriteHeader(http.StatusOK) }

// A client named by its caller's session says that session on its stream and
// its view, so the id the shim stamps on its records is the one the node
// files the view under.
func TestDefaultClientNamesItselfByTheGivenSession(t *testing.T) {
	seen := &sessionsSeen{}
	_, h := gridwellv1connect.NewGridwellHandler(seen)
	was := http.DefaultClient.Transport
	http.DefaultClient.Transport = memTransport{h}
	defer func() { http.DefaultClient.Transport = was }()
	cl := NewDefaultClient("http://node", "s123abc")
	if err := cl.SetView(t.Context(), &pb.SetViewRequest{Label: "desktop"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	s, err := cl.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = s.Close()
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if seen.view != "s123abc" || seen.stream != "s123abc" || cl.Session() != "s123abc" {
		t.Errorf("view %q, stream %q, Session %q; want the caller's", seen.view, seen.stream, cl.Session())
	}
}

type sessionsSeen struct {
	gridwellv1connect.UnimplementedGridwellHandler
	mu           sync.Mutex
	view, stream string
}

func (h *sessionsSeen) SetView(_ context.Context, req *connect.Request[pb.SetViewRequest]) (*connect.Response[pb.SetViewResponse], error) {
	h.mu.Lock()
	h.view = req.Msg.GetSession()
	h.mu.Unlock()
	return connect.NewResponse(&pb.SetViewResponse{}), nil
}

func (h *sessionsSeen) Subscribe(ctx context.Context, req *connect.Request[pb.SubscribeRequest], stream *connect.ServerStream[pb.Event]) error {
	h.mu.Lock()
	h.stream = req.Msg.GetSession()
	h.mu.Unlock()
	if err := stream.Send(nil); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}
