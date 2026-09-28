package gwerr

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// DeadRef is the answer for an id whose path ends in nothing: a namespace this
// node does not declare, or, with namespace empty, a key its declared
// namespace says is gone. NotFound, so every reader of the code is unchanged,
// carrying a pb.DeadReference detail that survives every gRPC hop and the
// Connect codec.
func DeadRef(namespace, format string, args ...any) error {
	st, err := status.New(codes.NotFound, fmt.Sprintf(format, args...)).
		WithDetails(&pb.DeadReference{Namespace: namespace})
	if err != nil {
		return status.Errorf(codes.NotFound, format, args...)
	}
	return st.Err()
}

// IsDeadRef reports that err is DeadRef's answer, read off a gRPC status or a
// Connect error alike, so the node and the browser share one reading.
func IsDeadRef(err error) bool {
	if err == nil {
		return false
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		for _, d := range ce.Details() {
			if v, derr := d.Value(); derr == nil {
				if _, ok := v.(*pb.DeadReference); ok {
					return true
				}
			}
		}
		return false
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		return false
	}
	for _, d := range st.Details() {
		if _, ok := d.(*pb.DeadReference); ok {
			return true
		}
	}
	return false
}

// ConnectDetails carries a status's DeadReference onto a Connect error; the
// Connect codec is the one hop that rebuilds an error and would drop it.
func ConnectDetails(err error, ce *connect.Error) *connect.Error {
	st, ok := status.FromError(err)
	if !ok {
		return ce
	}
	for _, d := range st.Details() {
		if dr, ok := d.(*pb.DeadReference); ok {
			if cd, derr := connect.NewErrorDetail(dr); derr == nil {
				ce.AddDetail(cd)
			}
		}
	}
	return ce
}
