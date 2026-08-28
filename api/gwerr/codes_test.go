package gwerr

import (
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
)

// TestCodeTableIsTotal pins that every gRPC error code (Canceled..
// Unauthenticated) and every Connect code has a distinct partner and
// round-trips through the table unchanged. This is the drift lint for the
// two transport hops: a code absent here would fall to Internal on one
// wire and lose its meaning (a transport failure read as a verdict) on
// the next.
func TestCodeTableIsTotal(t *testing.T) {
	for c := codes.Canceled; c <= codes.Unauthenticated; c++ {
		cc := ConnectCode(c)
		if cc == connect.CodeInternal && c != codes.Internal {
			t.Errorf("gRPC %v has no Connect partner (fell to Internal)", c)
		}
		if back := GRPCCode(cc); back != c {
			t.Errorf("gRPC %v → Connect %v → gRPC %v; want the original", c, cc, back)
		}
	}
	for cc := connect.CodeCanceled; cc <= connect.CodeUnauthenticated; cc++ {
		g := GRPCCode(cc)
		if g == codes.Internal && cc != connect.CodeInternal {
			t.Errorf("Connect %v has no gRPC partner (fell to Internal)", cc)
		}
		if back := ConnectCode(g); back != cc {
			t.Errorf("Connect %v → gRPC %v → Connect %v; want the original", cc, g, back)
		}
	}
	if ConnectCode(codes.OK) != connect.CodeInternal {
		t.Errorf("codes.OK must map to Internal (a Connect error is never OK)")
	}
}
