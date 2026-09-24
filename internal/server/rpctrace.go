package server

// Every rpc says it started and how it ended. The two doors are codecs over
// one router, so the record is made in each codec, where the procedure name
// and the error code are already spelled, and both call the one rpcSpan.
// kv["req"] is the client's own request id off tracewire.RequestHeader: it is what
// joins a gesture to the calls it caused, and it is empty when nobody sent one.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/internal/trace"
)

// rpcSpan emits the entry record and returns the exit emitter, which takes the
// codec's own spelling of the error code.
func rpcSpan(reqID, procedure string) func(err error, code string) {
	verb := procedure[strings.LastIndex(procedure, "/")+1:]
	start := time.Now()
	trace.Emit("router", "rpc", verb+" start", map[string]string{"req": reqID})
	return func(err error, code string) {
		kv := map[string]string{"req": reqID, "ms": strconv.FormatInt(time.Since(start).Milliseconds(), 10)}
		msg := verb + " ok"
		if err != nil {
			msg = verb + " error: " + err.Error()
			kv["code"] = code
		}
		trace.Emit("router", "rpc", msg, kv)
	}
}

// traceInterceptor traces the browser codec. Streaming verbs get the same
// pair, around the whole stream.
type traceInterceptor struct{}

func (traceInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		done := rpcSpan(req.Header().Get(tracewire.RequestHeader), req.Spec().Procedure)
		resp, err := next(ctx, req)
		done(err, connectCode(err))
		return resp, err
	}
}

// The client half of the chain is not a door; nothing here traces it.
func (traceInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (traceInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		done := rpcSpan(conn.RequestHeader().Get(tracewire.RequestHeader), conn.Spec().Procedure)
		err := next(ctx, conn)
		done(err, connectCode(err))
		return err
	}
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
	done := rpcSpan(requestIDOf(ctx), info.FullMethod)
	resp, err := handler(ctx, req)
	done(err, grpcCode(err))
	return resp, err
}

func traceStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	done := rpcSpan(requestIDOf(ss.Context()), info.FullMethod)
	err := handler(srv, ss)
	done(err, grpcCode(err))
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
