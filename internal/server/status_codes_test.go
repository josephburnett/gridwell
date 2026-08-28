package server

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/josephburnett/gridwell/api/gwerr"
)

// TestStatusCodesSurviveBothHops crosses the two translation hops a
// mount-of-mount's answer takes — plugin gRPC → Connect (asConnectError)
// → node-export gRPC (statusErr) — for EVERY status code, asserting the
// code the far plugin answered with is the code the mounter reads. The
// hops used to keep two hand-written switches whose defaults collapsed to
// Internal, and they had drifted apart (Unavailable survived one hop and
// not the other): a transport failure two mounts away reached
// gwerr.IsTransport as a verdict, and clientsync dropped a parked write.
func TestStatusCodesSurviveBothHops(t *testing.T) {
	for c := codes.Canceled; c <= codes.Unauthenticated; c++ {
		in := status.Error(c, "far side says "+c.String())
		mid := asConnectError(in)
		var ce *connect.Error
		if !errors.As(mid, &ce) {
			t.Fatalf("%v: asConnectError returned %T, want *connect.Error", c, mid)
		}
		out := statusErr(mid)
		if got := status.Code(out); got != c {
			t.Errorf("%v → connect %v → grpc %v; the code must survive both hops", c, ce.Code(), got)
		}
		if gwerr.IsTransport(in) != gwerr.IsTransport(out) {
			t.Errorf("%v: IsTransport changed across the hops (in=%v out=%v)",
				c, gwerr.IsTransport(in), gwerr.IsTransport(out))
		}
		if status.Convert(out).Message() != "far side says "+c.String() {
			t.Errorf("%v: message lost: %q", c, status.Convert(out).Message())
		}
	}
}
