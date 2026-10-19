package trace

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/api/idshape"
	"github.com/josephburnett/gridwell/api/tracewire"
)

// tick is a clock that advances a millisecond per reading, so a duration in a
// record is the test's and not the machine's.
func tick(ms *int64) func() time.Time {
	return func() time.Time {
		*ms++
		return t0.Add(time.Duration(*ms) * time.Millisecond)
	}
}

// The join is the point: the id the client puts on the wire is the id its own
// records carry, or a dump has two halves that cannot be matched.
func TestEveryCallCarriesAFreshRequestIdItAlsoRecords(t *testing.T) {
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = append(sent, r.Header.Get(tracewire.RequestHeader))
		http.Error(w, "the node is not answering", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(64, "cid7abc")
	var ms int64
	cl := gridwellv1connect.NewGridwellClient(srv.Client(), srv.URL,
		connect.WithInterceptors(Interceptor(c, tick(&ms))))
	for i := 0; i < 2; i++ {
		if _, err := cl.GetGrid(context.Background(), connect.NewRequest(&pb.GetGridRequest{})); err == nil {
			t.Fatal("the stub answered 500 and the call succeeded")
		}
	}
	if len(sent) != 2 {
		t.Fatalf("%d calls reached the API, want 2", len(sent))
	}
	if sent[0] == sent[1] {
		t.Errorf("two calls shared the request id %q", sent[0])
	}
	for _, id := range sent {
		if err := idshape.ValidateSegment("request id", id); err != nil {
			t.Errorf("%v", err)
		}
	}
	recs := decode(t, mustBatch(t, c))
	if len(recs) != 4 {
		t.Fatalf("%d records for two calls, want a start and an end each:\n%v", len(recs), recs)
	}
	for i, r := range recs {
		if r.Src != "rpc" || r.Kind != "rpc" {
			t.Errorf("record %d is %s/%s", i, r.Src, r.Kind)
		}
		if r.KV["req"] != sent[i/2] {
			t.Errorf("record %d carries req %q, want the id call %d sent", i, r.KV["req"], i/2)
		}
	}
	if !strings.HasPrefix(recs[0].Msg, "GetGrid start") {
		t.Errorf("the entry record reads %q", recs[0].Msg)
	}
	if !strings.HasPrefix(recs[1].Msg, "GetGrid error") || recs[1].KV["code"] == "" {
		t.Errorf("the exit record of a refused call reads %q with code %q", recs[1].Msg, recs[1].KV["code"])
	}
}

// The end record says how long the call took and, on a failure, what the
// API said: the two questions a dump is opened for.
func TestTheEndRecordCarriesTheDurationAndTheCode(t *testing.T) {
	c := New(64, "cid7abc")
	var ms int64
	i := interceptor{c: c, now: tick(&ms)}

	i.span("k3f9x2a", "/gridwell.v1.Gridwell/SetTile", nil).end(nil)
	i.span("m2p8z1b", "/gridwell.v1.Gridwell/WriteContent", nil).end(
		connect.NewError(connect.CodeNotFound, errors.New("no such tile")))

	recs := decode(t, mustBatch(t, c))
	if len(recs) != 4 {
		t.Fatalf("%d records, want two spans", len(recs))
	}
	if recs[1].Msg != "SetTile ok" || recs[1].KV["ms"] != "1" {
		t.Errorf("the kept call reads %q in %qms", recs[1].Msg, recs[1].KV["ms"])
	}
	if _, ok := recs[1].KV["code"]; ok {
		t.Error("a call that worked carries an error code")
	}
	if recs[3].Msg != "WriteContent error: not_found: no such tile" {
		t.Errorf("the refused call reads %q", recs[3].Msg)
	}
	if recs[3].KV["code"] != "not_found" {
		t.Errorf("the refused call's code is %q", recs[3].KV["code"])
	}
}

func mustBatch(t *testing.T, c *Client) []byte {
	t.Helper()
	b, _ := c.PendingBatch()
	if len(b) == 0 {
		t.Fatal("the interceptor recorded nothing")
	}
	return b
}

// fakeStream is a server stream whose error arrives on Receive and whose
// close says nothing, which is how a Connect server stream ends in a verdict.
type fakeStream struct {
	connect.StreamingClientConn
	recv     []error
	closeErr error
}

func (f *fakeStream) Receive(any) error {
	err := f.recv[0]
	if len(f.recv) > 1 {
		f.recv = f.recv[1:]
	}
	return err
}

func (f *fakeStream) CloseResponse() error { return f.closeErr }

func (f *fakeStream) Send(any) error { return nil }

// A stream's verdict arrives on Receive, not on CloseResponse, so a span that
// reads only the close records a refused ReadContent as "ok".
func TestAStreamSpanEndsWithTheErrorItsReceiveSaw(t *testing.T) {
	c := New(64, "cid7abc")
	var ms int64
	i := interceptor{c: c, now: tick(&ms)}
	verdict := connect.NewError(connect.CodeNotFound, errors.New("plugin: no tile"))

	refused := &tracedConn{
		StreamingClientConn: &fakeStream{recv: []error{verdict, verdict}},
		s:                   i.span("k3f9x2a", "/gridwell.v1.Gridwell/ReadContent", nil),
	}
	_ = refused.Receive(nil)
	_ = refused.Receive(nil)
	_ = refused.CloseResponse()

	kept := &tracedConn{
		StreamingClientConn: &fakeStream{recv: []error{nil, io.EOF}},
		s:                   i.span("m2p8z1b", "/gridwell.v1.Gridwell/ReadContent", nil),
	}
	_ = kept.Receive(nil)
	_ = kept.Receive(nil)
	_ = kept.CloseResponse()

	closed := &tracedConn{
		StreamingClientConn: &fakeStream{recv: []error{io.EOF}, closeErr: errors.New("reset")},
		s:                   i.span("p4q7r2c", "/gridwell.v1.Gridwell/Subscribe", nil),
	}
	_ = closed.Receive(nil)
	_ = closed.CloseResponse()

	recs := decode(t, mustBatch(t, c))
	if len(recs) != 6 {
		t.Fatalf("%d records, want three spans:\n%v", len(recs), recs)
	}
	if recs[1].Msg != "ReadContent error: not_found: plugin: no tile" || recs[1].KV["code"] != "not_found" {
		t.Errorf("the refused stream reads %q with code %q", recs[1].Msg, recs[1].KV["code"])
	}
	if recs[3].Msg != "ReadContent ok" {
		t.Errorf("a stream that ended on EOF reads %q", recs[3].Msg)
	}
	if !strings.HasPrefix(recs[5].Msg, "Subscribe error: ") {
		t.Errorf("with no Receive error the close's is the span's; it reads %q", recs[5].Msg)
	}
}

// Both halves name the entity a call is about under kv.id, spelled by the one
// rpc.EntityOf: a unary call from its request, a stream from the request it
// sends, the entry record waiting for it.
func TestTheSpanNamesTheEntityTheRequestIsAbout(t *testing.T) {
	c := New(64, "cid7abc")
	var ms int64
	i := interceptor{c: c, now: tick(&ms)}

	i.span("k3f9x2a", "/gridwell.v1.Gridwell/GetGrid", &pb.GetGridRequest{GridId: "a/g1"}).end(nil)
	streamed := &tracedConn{
		StreamingClientConn: &fakeStream{recv: []error{io.EOF}},
		s:                   i.span("m2p8z1b", "/gridwell.v1.Gridwell/ReadContent", nil),
	}
	_ = streamed.Send(&pb.ReadContentRequest{TileId: "a/t1"})
	_ = streamed.Receive(nil)
	_ = streamed.CloseResponse()

	recs := decode(t, mustBatch(t, c))
	if len(recs) != 4 {
		t.Fatalf("%d records, want two spans:\n%v", len(recs), recs)
	}
	for n, want := range []string{"a/g1", "a/g1", "a/t1", "a/t1"} {
		if got := recs[n].KV["id"]; got != want {
			t.Errorf("record %d (%s) names %q, want %q", n, recs[n].Msg, got, want)
		}
	}
}
