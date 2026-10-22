package gwerr

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// SourceDisabled is a node's answer to a write whose target's source the
// user disabled: a FailedPrecondition carrying a pb.SourceDisabled detail,
// which survives every gRPC hop and the web API (ConnectDetails).
func SourceDisabled(namespace, reason string) error {
	st, err := status.New(codes.FailedPrecondition, reason).
		WithDetails(&pb.SourceDisabled{Namespace: namespace})
	if err != nil {
		return status.Error(codes.FailedPrecondition, reason)
	}
	return st.Err()
}

// IsSourceDisabled reports that err is SourceDisabled's answer. A plain
// FailedPrecondition (a stale save) carries no detail and is not one.
func IsSourceDisabled(err error) bool {
	_, ok := detail[*pb.SourceDisabled](err)
	return ok
}
