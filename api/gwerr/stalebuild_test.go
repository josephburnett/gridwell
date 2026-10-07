package gwerr

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
)

// The stale-build verdict is recognizable by its detail alone, so a stale
// save, which shares its code, is never read as one.
func TestStaleBuildIsReadByItsDetail(t *testing.T) {
	err := StaleBuild("nodebuild", "")
	if err.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition", err.Code())
	}
	if node, ok := StaleBuildOf(fmt.Errorf("wrapped: %w", err)); !ok || node != "nodebuild" {
		t.Errorf("StaleBuildOf = %q,%v, want the node's build", node, ok)
	}
	plain := connect.NewError(connect.CodeFailedPrecondition, errors.New("changed on disk since it was read"))
	if _, ok := StaleBuildOf(plain); ok {
		t.Error("a plain FailedPrecondition read as a stale build")
	}
	if _, ok := StaleBuildOf(errors.New("dial tcp: refused")); ok {
		t.Error("a transport error read as a stale build")
	}
}
