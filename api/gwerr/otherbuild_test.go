package gwerr

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
)

// The other-build verdict is recognizable by its detail alone, so a stale
// save, which shares its code, is never read as one.
func TestOtherBuildIsReadByItsDetail(t *testing.T) {
	err := OtherBuild("nodebuild", "")
	if err.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition", err.Code())
	}
	if node, ok := OtherBuildOf(fmt.Errorf("wrapped: %w", err)); !ok || node != "nodebuild" {
		t.Errorf("OtherBuildOf = %q,%v, want the node's build", node, ok)
	}
	plain := connect.NewError(connect.CodeFailedPrecondition, errors.New("changed on disk since it was read"))
	if _, ok := OtherBuildOf(plain); ok {
		t.Error("a plain FailedPrecondition read as a another build")
	}
	if _, ok := OtherBuildOf(errors.New("dial tcp: refused")); ok {
		t.Error("a transport error read as a another build")
	}
}
