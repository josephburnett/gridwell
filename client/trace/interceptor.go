package trace

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/api/tracewire"
)

// Interceptor stamps every call with a fresh request id and records the pair
// of lines that brackets it. The node's own APIs record the same id off
// tracewire.RequestHeader, so one dump reads from the client's gesture
// through the server's work and back.
func Interceptor(c *Client, now func() time.Time) connect.Interceptor {
	return interceptor{c: c, now: now}
}

type interceptor struct {
	c   *Client
	now func() time.Time
}

// span is one call's pair of records, the client half of server.rpcSpan: the
// msg and kv spellings must match for a dump to read as one story. A stream's
// request is sent after the call opens, so its entry record waits for that
// message, or the end, and names the entity as a unary one does.
type span struct {
	i               interceptor
	req, verb, id   string
	start           time.Time
	started, closed bool
}

// span opens the span; msg is the request message, nil for a stream.
func (i interceptor) span(req, procedure string, msg any) *span {
	s := &span{i: i, req: req, verb: procedure[strings.LastIndex(procedure, "/")+1:], start: i.now()}
	if msg != nil {
		s.saw(msg)
	}
	return s
}

// saw names the span's entity from the first message that carries one, and
// emits the entry record on the first message.
func (s *span) saw(msg any) {
	if s.id == "" {
		s.id = rpc.EntityOf(msg)
	}
	s.open()
}

// open emits the entry record once, at the call's start.
func (s *span) open() {
	if !s.started {
		s.started = true
		s.i.c.Emit(rpcSrc, rpcKind, s.verb+" start", s.kv(), s.start)
	}
}

// end emits the exit record once.
func (s *span) end(err error) {
	if s.closed {
		return
	}
	s.open()
	s.closed = true
	end := s.i.now()
	kv := s.kv()
	kv["ms"] = strconv.FormatInt(end.Sub(s.start).Milliseconds(), 10)
	msg := s.verb + " ok"
	if err != nil {
		msg = s.verb + " error: " + err.Error()
		kv["code"] = connect.CodeOf(err).String()
	}
	s.i.c.Emit(rpcSrc, rpcKind, msg, kv, end)
}

func (s *span) kv() map[string]string {
	kv := map[string]string{"req": s.req}
	if s.id != "" {
		kv["id"] = s.id
	}
	return kv
}

const (
	rpcSrc  = "rpc"
	rpcKind = "rpc"
)

func (i interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		// A response has a header too, and stamping it would claim a request
		// id for the node's answer; only what the client sends carries one.
		if req.Spec().IsClient {
			id := NewRequestID()
			req.Header().Set(tracewire.RequestHeader, id)
			s := i.span(id, req.Spec().Procedure, req.Any())
			resp, err := next(ctx, req)
			s.end(err)
			return resp, err
		}
		return next(ctx, req)
	}
}

// WrapStreamingClient brackets the whole stream: the subscribe that never
// ends is the one whose start and end are worth having.
func (i interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		id := NewRequestID()
		conn.RequestHeader().Set(tracewire.RequestHeader, id)
		return &tracedConn{StreamingClientConn: conn, s: i.span(id, spec.Procedure, nil)}
	}
}

// The handler half is the node's; nothing here is an API.
func (i interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// tracedConn hands its span the request it sends, and ends the span when the
// read side closes, which is where a stream that died and one the client let
// go both arrive. A server stream's verdict arrives on Receive and the close
// then reports nothing, so the first non-EOF Receive error is the span's; the
// close's error only if none.
type tracedConn struct {
	connect.StreamingClientConn
	s       *span
	recvErr error
}

func (t *tracedConn) Send(msg any) error {
	t.s.saw(msg)
	return t.StreamingClientConn.Send(msg)
}

func (t *tracedConn) Receive(msg any) error {
	err := t.StreamingClientConn.Receive(msg)
	if err != nil && t.recvErr == nil && !errors.Is(err, io.EOF) {
		t.recvErr = err
	}
	return err
}

func (t *tracedConn) CloseResponse() error {
	err := t.StreamingClientConn.CloseResponse()
	if t.recvErr != nil {
		t.s.end(t.recvErr)
	} else {
		t.s.end(err)
	}
	return err
}
