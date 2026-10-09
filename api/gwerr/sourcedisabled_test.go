package gwerr

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The disabled verdict is recognizable by its detail on both sides of the
// Connect codec, and a plain FailedPrecondition (a stale save) is never read
// as one. It is not transport-shaped: the hop that carried it is not dark.
func TestSourceDisabledIsRecognizableOnBothWires(t *testing.T) {
	off := SourceDisabled("n/rtb", "rtb is disabled until the node restarts")
	if !IsSourceDisabled(off) || IsTransport(off) {
		t.Fatalf("SourceDisabled = %v (code %v), want a recognizable non-transport verdict", off, status.Code(off))
	}
	if IsSourceDisabled(status.Error(codes.FailedPrecondition, "version")) || IsSourceDisabled(nil) || IsSourceDisabled(errors.New("x")) {
		t.Fatal("an error without the detail read as disabled")
	}
	carried := ConnectDetails(off, connect.NewError(ConnectCode(status.Code(off)), errors.New(status.Convert(off).Message())))
	if !IsSourceDisabled(carried) {
		t.Fatal("the Connect codec dropped the disabled verdict")
	}
	if IsDeadRef(carried) {
		t.Fatal("the disabled verdict read as dead")
	}
}
