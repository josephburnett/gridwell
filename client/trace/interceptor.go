package trace

import (
	"context"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/tracewire"
)

// Interceptor stamps every call with a fresh request id and records the pair
// of lines that brackets it. The node's own doors record the same id off
// tracewire.RequestHeader, so one dump reads from the client's gesture
// through the server's work and back.
func Interceptor(c *Client, now func() time.Time) connect.Interceptor {
	return interceptor{c: c, now: now}
}

type interceptor struct {
	c   *Client
	now func() time.Time
}

// span emits the entry record and returns the exit emitter. It is the client
// half of server.rpcSpan, which the two doors share: the msg and kv spellings
// must match for a dump to read as one story.
func (i interceptor) span(req, procedure string) func(err error) {
	verb := procedure[strings.LastIndex(procedure, "/")+1:]
	start := i.now()
	i.c.Emit(rpcSrc, rpcKind, verb+" start", map[string]string{"req": req}, start)
	return func(err error) {
		end := i.now()
		kv := map[string]string{"req": req, "ms": strconv.FormatInt(end.Sub(start).Milliseconds(), 10)}
		msg := verb + " ok"
		if err != nil {
			msg = verb + " error: " + err.Error()
			kv["code"] = connect.CodeOf(err).String()
		}
		i.c.Emit(rpcSrc, rpcKind, msg, kv, end)
	}
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
			done := i.span(id, req.Spec().Procedure)
			resp, err := next(ctx, req)
			done(err)
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
		return &tracedConn{StreamingClientConn: conn, done: i.span(id, spec.Procedure)}
	}
}

// The handler half is the node's; nothing here is a door.
func (i interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// tracedConn ends its span when the read side closes, which is where a stream
// that died and one the client let go both arrive.
type tracedConn struct {
	connect.StreamingClientConn
	done func(err error)
}

func (t *tracedConn) CloseResponse() error {
	err := t.StreamingClientConn.CloseResponse()
	if t.done != nil {
		t.done(err)
		t.done = nil
	}
	return err
}
