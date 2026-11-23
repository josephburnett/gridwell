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

// UnheardDisabled is the transport's answer to a call on a connection the
// user disabled, which it never asks: unheard and Unavailable, so it reads as
// dark and a cache in front serves what it holds, and carrying the
// pb.SourceDisabled detail, so a reader tells it from an outage and says
// nothing of it.
func UnheardDisabled(namespace, reason string) error {
	st, err := status.New(codes.Unavailable, reason).WithDetails(&pb.SourceDisabled{Namespace: namespace})
	if err != nil {
		return Unheard("%s", reason)
	}
	return unheard{st.Err()}
}

// IsSourceDisabled reports that err is SourceDisabled's answer. A plain
// FailedPrecondition (a stale save) carries no detail and is not one.
func IsSourceDisabled(err error) bool {
	_, ok := detail[*pb.SourceDisabled](err)
	return ok
}
