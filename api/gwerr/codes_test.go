package gwerr

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
)

// A code ConnectCode misses falls to Internal on the browser's wire,
// where a transport failure reads as a verdict and clientsync drops a write
// it should park.
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

// Each gRPC code maps to the Connect code of its own name, both ways.
func TestCodesMapByName(t *testing.T) {
	for c := codes.Canceled; c <= codes.Unauthenticated; c++ {
		cc := ConnectCode(c)
		if got, want := strings.ReplaceAll(cc.String(), "_", ""), strings.ToLower(c.String()); got != want {
			t.Errorf("gRPC %v maps to Connect %v", c, cc)
		}
		if back := codeOf(connect.NewError(cc, errors.New("x"))); back != c {
			t.Errorf("Connect %v reads back as gRPC %v, want %v", cc, back, c)
		}
	}
	if got := codeOf(connect.NewError(connect.Code(0), errors.New("x"))); got != codes.Unknown {
		t.Errorf("a Connect error with no code reads as %v, want Unknown", got)
	}
}
