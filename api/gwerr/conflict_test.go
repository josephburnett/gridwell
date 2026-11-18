package gwerr

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A stale claim is told apart from every refusal that shares its code by the
// one predicate, on either side of the web API.
func TestIsConflict(t *testing.T) {
	asConnect := func(err error) error {
		return ConnectDetails(err, connect.NewError(ConnectCode(status.Code(err)), errors.New(status.Convert(err).Message())))
	}
	for _, tc := range []struct {
		name     string
		err      error
		conflict bool
	}{
		{"a stale version", ToStatus(ErrVersionConflict), true},
		{"a disabled source", SourceDisabled("rtb", "rtb is disabled"), false},
		{"another build", OtherBuild("a", "b"), false},
		{"a landing refusal", status.Error(codes.InvalidArgument, "lands elsewhere"), false},
		{"unheard", Unheard("down"), false},
		{"nil", nil, false},
	} {
		if got := IsConflict(tc.err); got != tc.conflict {
			t.Errorf("%s: IsConflict = %t, want %t", tc.name, got, tc.conflict)
		}
		if _, ok := tc.err.(*connect.Error); ok || tc.err == nil {
			continue
		}
		if got := IsConflict(asConnect(tc.err)); got != tc.conflict {
			t.Errorf("%s over the web API: IsConflict = %t, want %t", tc.name, got, tc.conflict)
		}
	}
}
