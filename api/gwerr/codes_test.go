package gwerr

import (
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
)

// TestCodeTableIsTotal pins that every gRPC error code (Canceled..
// Unauthenticated) has a DISTINCT Connect partner, and that every Connect
// code is reachable. This is the drift lint for the one hop that
// translates (server.asConnectError): a code absent here would fall to
// Internal on the browser's wire and lose its meaning — a transport
// failure read as a verdict, and clientsync dropping a parked write.
func TestCodeTableIsTotal(t *testing.T) {
	seen := map[connect.Code]codes.Code{}
	for c := codes.Canceled; c <= codes.Unauthenticated; c++ {
		cc := ConnectCode(c)
		if cc == connect.CodeInternal && c != codes.Internal {
			t.Errorf("gRPC %v has no Connect partner (fell to Internal)", c)
		}
		if prev, dup := seen[cc]; dup {
			t.Errorf("gRPC %v and %v both map to Connect %v; the codes must stay distinguishable", prev, c, cc)
		}
		seen[cc] = c
	}
	for cc := connect.CodeCanceled; cc <= connect.CodeUnauthenticated; cc++ {
		if _, ok := seen[cc]; !ok {
			t.Errorf("Connect %v is unreachable: no gRPC code maps to it", cc)
		}
	}
	if ConnectCode(codes.OK) != connect.CodeInternal {
		t.Errorf("codes.OK must map to Internal (a Connect error is never OK)")
	}
}
