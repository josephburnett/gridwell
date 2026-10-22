package gwerr

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// DeadLink is the answer for an id whose path ends in nothing: a namespace this
// node does not declare, or, with namespace empty, a key its declared
// namespace says is gone. NotFound, so every reader of the code is unchanged,
// carrying a pb.DeadLink detail that survives every gRPC hop and the
// web API.
func DeadLink(namespace, format string, args ...any) error {
	st, err := status.New(codes.NotFound, fmt.Sprintf(format, args...)).
		WithDetails(&pb.DeadLink{Namespace: namespace})
	if err != nil {
		return status.Errorf(codes.NotFound, format, args...)
	}
	return st.Err()
}

// IsDeadLink reports that err is DeadLink's answer.
func IsDeadLink(err error) bool {
	_, ok := detail[*pb.DeadLink](err)
	return ok
}

// detail is err's detail of type D, read off a gRPC status or a Connect error
// alike, so the node and the browser share one reading.
func detail[D any](err error) (D, bool) {
	var found []any
	var ce *connect.Error
	if errors.As(err, &ce) {
		for _, d := range ce.Details() {
			if v, derr := d.Value(); derr == nil {
				found = append(found, v)
			}
		}
	} else if st, ok := status.FromError(err); ok && err != nil {
		found = st.Details()
	}
	for _, d := range found {
		if v, ok := d.(D); ok {
			return v, true
		}
	}
	var none D
	return none, false
}

// ConnectDetails carries a status's verdict details (DeadLink,
// SourceDisabled) onto a Connect error; the web API is the one hop that
// rebuilds an error and would drop them.
func ConnectDetails(err error, ce *connect.Error) *connect.Error {
	st, ok := status.FromError(err)
	if !ok {
		return ce
	}
	for _, d := range st.Details() {
		switch v := d.(type) {
		case *pb.DeadLink, *pb.SourceDisabled:
			if cd, derr := connect.NewErrorDetail(v.(proto.Message)); derr == nil {
				ce.AddDetail(cd)
			}
		}
	}
	return ce
}
