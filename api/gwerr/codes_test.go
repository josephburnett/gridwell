package gwerr

import (
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
)

// TestCodeTableIsTotal pins that every gRPC error code has a distinct
// Connect partner and that every Connect code is reachable. It is the
// drift lint for the one hop that translates (server.asConnectError): a
// code absent here falls to Internal on the browser's wire and loses its
// meaning, so a transport failure reads as a verdict and clientsync drops
// a write it should have parked.
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
