package gwerr

import (
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// SourceDisabled is a node's answer to a write whose target's source the
// user disabled: a FailedPrecondition carrying a pb.SourceDisabled detail,
// which survives every gRPC hop and the Connect codec (ConnectDetails).
func SourceDisabled(namespace, reason string) error {
	st, err := status.New(codes.FailedPrecondition, reason).
		WithDetails(&pb.SourceDisabled{Namespace: namespace})
	if err != nil {
		return status.Error(codes.FailedPrecondition, reason)
	}
	return st.Err()
}

// IsSourceDisabled reports that err is SourceDisabled's answer, read off a
// gRPC status or a Connect error alike. A plain FailedPrecondition (a stale
// save) carries no detail and is not one.
func IsSourceDisabled(err error) bool {
	if err == nil {
		return false
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		for _, d := range ce.Details() {
			if v, derr := d.Value(); derr == nil {
				if _, ok := v.(*pb.SourceDisabled); ok {
					return true
				}
			}
		}
		return false
	}
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	for _, d := range st.Details() {
		if _, ok := d.(*pb.SourceDisabled); ok {
			return true
		}
	}
	return false
}
