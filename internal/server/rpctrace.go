package server

// Every rpc says it started and how it ended. The two APIs decode onto one
// router, so the record is made in each API, where the procedure name and the
// error code are already spelled, and both call the one rpcSpan. kv["req"] is
// the client's own request id off tracewire.RequestHeader: it is what joins a
// gesture to the calls it caused, and it is empty when nobody sent one.
// kv["id"] is the entity the request is about; see rpc.EntityOf. kv["session"]
// is the client session it names; see sessionOf. A select's span carries its
// counts and flags, since the ids it names are not one entity.

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

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/internal/trace"
)

// span is one rpc's pair of records. A stream's first message arrives after
// the call, so its entry record waits for that message, or the end, and names
// the entity as a unary one does.
type span struct {
	reqID, verb, id string
	extra           map[string]string // kv a caller adds; every record after carries it
	start           time.Time
	started         bool
}

// rpcSpan opens the span; req is the request message, nil for a stream.
func rpcSpan(reqID, procedure string, req any) *span {
	s := &span{reqID: reqID, verb: procedure[strings.LastIndex(procedure, "/")+1:], start: time.Now()}
	if req != nil {
		s.saw(req)
	}
	return s
}

// saw names the span's entity from the first message that carries one, and
// emits the entry record on the first message.
func (s *span) saw(msg any) {
	if s.id == "" {
		s.id = rpc.EntityOf(msg)
	}
	if session := sessionOf(msg); session != "" && s.extra["session"] == "" {
		s.add("session", session)
	}
	if sel, ok := msg.(*pb.SetSelectionRequest); ok {
		s.add("add", strconv.Itoa(len(sel.GetAdd())))
		s.add("remove", strconv.Itoa(len(sel.GetRemove())))
		s.add("clear", strconv.FormatBool(sel.GetClear()))
		s.add("recurse", strconv.FormatBool(sel.GetRecurse()))
		s.add("all", strconv.FormatBool(sel.GetAll()))
	}
	s.open()
}

func (s *span) add(k, v string) {
	if s.extra == nil {
		s.extra = map[string]string{}
	}
	s.extra[k] = v
}

// open emits the entry record once.
func (s *span) open() {
	if !s.started {
		s.started = true
		trace.Emit("router", "rpc", s.verb+" start", s.kv())
	}
}

// end emits the exit record, under the API's own spelling of the error code.
func (s *span) end(err error, code string) {
	s.open()
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
	for k, v := range s.extra {
		kv[k] = v
	}
	if s.id != "" {
		kv["id"] = s.id
	}
	return kv
}

// sessionOf is the client session a request names in a field called
// session (Subscribe, SetView), "" for one that names none.
func sessionOf(msg any) string {
	m, ok := msg.(proto.Message)
	if !ok {
		return ""
	}
	r := m.ProtoReflect()
	fd := r.Descriptor().Fields().ByName("session")
	if fd == nil || fd.Kind() != protoreflect.StringKind || fd.IsList() {
		return ""
	}
	return r.Get(fd).String()
}

// traceInterceptor traces the web API. Streaming verbs get the same
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

// The client half of the chain is not an API; nothing here traces it.
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

// traceUnary and traceStream are the connection API's half, and stamp what it
// returns as this node's answer (gwerr.Answered).
func traceUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	s := rpcSpan(requestIDOf(ctx), info.FullMethod, req)
	resp, err := handler(ctx, req)
	s.end(err, grpcCode(err))
	return resp, gwerr.Answered(err)
}

func traceStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	s := rpcSpan(requestIDOf(ss.Context()), info.FullMethod, nil)
	err := handler(srv, sawStream{ss, s})
	s.end(err, grpcCode(err))
	return gwerr.Answered(err)
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
