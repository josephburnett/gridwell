package server

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/josephburnett/gridwell/api/gwerr"
)

// TestStatusCodesSurviveTheConnectCodec crosses the browser codec's
// translation — the namespace's gRPC status to Connect, through
// asConnectError, and back through gwerr's inverse — for every status code,
// asserting that the code the owning namespace answered with is the code the
// client classifies on. A code that collapses to Internal turns a transport
// failure two hops away into a verdict, and clientsync then drops a parked
// write. The federation codec's half of this is
// namespace.TestStatusCodesSurviveBothCodecs.
func TestStatusCodesSurviveTheConnectCodec(t *testing.T) {
	for c := codes.Canceled; c <= codes.Unauthenticated; c++ {
		in := status.Error(c, "far side says "+c.String())
		mid := asConnectError(in)
		var ce *connect.Error
		if !errors.As(mid, &ce) {
			t.Fatalf("%v: asConnectError returned %T, want *connect.Error", c, mid)
		}
		if want := gwerr.ConnectCode(c); ce.Code() != want {
			t.Errorf("%v → connect %v, want %v", c, ce.Code(), want)
		}
		out := status.Error(c, ce.Message()) // what the client reconstructs from the code
		if got := status.Code(out); got != c {
			t.Errorf("%v → connect %v → grpc %v; the code must survive", c, ce.Code(), got)
		}
		if gwerr.IsTransport(in) != gwerr.IsTransport(out) {
			t.Errorf("%v: IsTransport changed across the codec (in=%v out=%v)",
				c, gwerr.IsTransport(in), gwerr.IsTransport(out))
		}
		if status.Convert(out).Message() != "far side says "+c.String() {
			t.Errorf("%v: message lost: %q", c, status.Convert(out).Message())
		}
	}
}
