package server

// Every rpc says it started and how it ended. The two doors are codecs over
// one router, so the record is made in each codec, where the procedure name
// and the error code are already spelled, and both call the one rpcSpan.
// kv["req"] is the client's own request id off tracewire.RequestHeader: it is what
// joins a gesture to the calls it caused, and it is empty when nobody sent one.
// kv["id"] is the entity the request is about; see entityOf.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/internal/trace"
)

// span is one rpc's pair of records. A unary request is known at the entry
// record; a stream's first message arrives after it, so a stream's entity
// rides only the exit record.
type span struct {
	reqID, verb, id string
	start           time.Time
}

// rpcSpan emits the entry record. req is the request message, nil for a
// stream.
func rpcSpan(reqID, procedure string, req any) *span {
	s := &span{reqID: reqID, verb: procedure[strings.LastIndex(procedure, "/")+1:], start: time.Now()}
	s.saw(req)
	trace.Emit("router", "rpc", s.verb+" start", s.kv())
	return s
}

// saw names the span's entity from the first message that carries one.
func (s *span) saw(msg any) {
	if s.id == "" {
		s.id = entityOf(msg)
	}
}

// end emits the exit record, under the codec's own spelling of the error code.
func (s *span) end(err error, code string) {
	kv := s.kv()
	kv["ms"] = strconv.FormatInt(time.Since(s.start).Milliseconds(), 10)
	msg := s.verb + " ok"
	if err != nil {
		msg = s.verb + " error: " + err.Error()
		kv["code"] = code
	}
	trace.Emit("router", "rpc", msg, kv)
}

func (s *span) kv() map[string]string {
	kv := map[string]string{"req": s.reqID}
	if s.id != "" {
		kv["id"] = s.id
	}
	return kv
}

// entityOf is a request's primary entity: its first string field named *_id
// that is set, in declaration order. Every request spells its tile or grid
// that way, so a verb the service gains is covered with no table to grow.
func entityOf(msg any) string {
	m, ok := msg.(proto.Message)
	if !ok {
		return ""
	}
	r := m.ProtoReflect()
	fields := r.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.Kind() != protoreflect.StringKind || fd.IsList() || !strings.HasSuffix(string(fd.Name()), "_id") {
			continue
		}
		if v := r.Get(fd).String(); v != "" {
			return v
		}
	}
	return ""
}

// traceInterceptor traces the browser codec. Streaming verbs get the same
// pair, around the whole stream.
type traceInterceptor struct{}

func (traceInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		s := rpcSpan(req.Header().Get(tracewire.RequestHeader), req.Spec().Procedure, req.Any())
		resp, err := next(ctx, req)
		s.end(err, connectCode(err))
		return resp, err
	}
}

// The client half of the chain is not a door; nothing here traces it.
func (traceInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (traceInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		s := rpcSpan(conn.RequestHeader().Get(tracewire.RequestHeader), conn.Spec().Procedure, nil)
		err := next(ctx, sawConn{conn, s})
		s.end(err, connectCode(err))
		return err
	}
}

// sawConn and sawStream hand a stream's received messages to its span.
type sawConn struct {
	connect.StreamingHandlerConn
	s *span
}

func (c sawConn) Receive(msg any) error {
	err := c.StreamingHandlerConn.Receive(msg)
	if err == nil {
		c.s.saw(msg)
	}
	return err
}

type sawStream struct {
	grpc.ServerStream
	s *span
}

func (st sawStream) RecvMsg(msg any) error {
	err := st.ServerStream.RecvMsg(msg)
	if err == nil {
		st.s.saw(msg)
	}
	return err
}

func connectCode(err error) string {
	if err == nil {
		return ""
	}
	return connect.CodeOf(err).String()
}

func grpcCode(err error) string {
	if err == nil {
		return ""
	}
	return status.Code(err).String()
}

// traceUnary and traceStream are the connection door's half.
func traceUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	s := rpcSpan(requestIDOf(ctx), info.FullMethod, req)
	resp, err := handler(ctx, req)
	s.end(err, grpcCode(err))
	return resp, err
}

func traceStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	s := rpcSpan(requestIDOf(ss.Context()), info.FullMethod, nil)
	err := handler(srv, sawStream{ss, s})
	s.end(err, grpcCode(err))
	return err
}

// requestIDOf reads the header off gRPC metadata, which lowercases it.
func requestIDOf(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if v := md.Get(tracewire.RequestHeader); len(v) > 0 {
		return v[0]
	}
	return ""
}
