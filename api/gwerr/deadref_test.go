package gwerr

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The dead verdict is NotFound to every reader of codes, and recognizable by
// its detail alone on both sides of the Connect codec, so a plain NotFound (a
// tile that is gone) is never read as a dead link.
func TestDeadRefIsRecognizableOnBothWires(t *testing.T) {
	dead := DeadRef("toc", "connection: no connection %q", "toc")
	if status.Code(dead) != codes.NotFound || !IsDeadRef(dead) {
		t.Fatalf("DeadRef = %v (code %v), want a recognizable NotFound", dead, status.Code(dead))
	}
	if IsDeadRef(fmt.Errorf("forwarded: %w", status.Error(codes.NotFound, "no such tile"))) {
		t.Fatal("a plain NotFound read as dead")
	}
	if IsDeadRef(nil) || IsDeadRef(errors.New("x")) {
		t.Fatal("a non-status error read as dead")
	}

	carried := ConnectDetails(dead, connect.NewError(connect.CodeNotFound, errors.New(status.Convert(dead).Message())))
	if !IsDeadRef(carried) {
		t.Fatal("the Connect codec dropped the dead verdict")
	}
	plain := ConnectDetails(status.Error(codes.NotFound, "gone"), connect.NewError(connect.CodeNotFound, errors.New("gone")))
	if IsDeadRef(plain) {
		t.Fatal("a plain Connect NotFound read as dead")
	}
}
