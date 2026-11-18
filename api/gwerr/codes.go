package gwerr

import (
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// codePairs is the one gRPC-to-Connect status-code table. Gridwell answers
// in gRPC codes everywhere; the web API, in server.asConnectError, is
// the one hop that translates. TestCodeTableIsTotal keeps the table total
// and injective over the gRPC enum.
var codePairs = []struct {
	G codes.Code
	C connect.Code
}{
	{codes.Canceled, connect.CodeCanceled},
	{codes.Unknown, connect.CodeUnknown},
	{codes.InvalidArgument, connect.CodeInvalidArgument},
	{codes.DeadlineExceeded, connect.CodeDeadlineExceeded},
	{codes.NotFound, connect.CodeNotFound},
	{codes.AlreadyExists, connect.CodeAlreadyExists},
	{codes.PermissionDenied, connect.CodePermissionDenied},
	{codes.ResourceExhausted, connect.CodeResourceExhausted},
	{codes.FailedPrecondition, connect.CodeFailedPrecondition},
	{codes.Aborted, connect.CodeAborted},
	{codes.OutOfRange, connect.CodeOutOfRange},
	{codes.Unimplemented, connect.CodeUnimplemented},
	{codes.Internal, connect.CodeInternal},
	{codes.Unavailable, connect.CodeUnavailable},
	{codes.DataLoss, connect.CodeDataLoss},
	{codes.Unauthenticated, connect.CodeUnauthenticated},
}

// ConnectCode maps a gRPC status code to its Connect twin. codes.OK and any
// code off the table map to Internal.
func ConnectCode(c codes.Code) connect.Code {
	for _, p := range codePairs {
		if p.G == c {
			return p.C
		}
	}
	return connect.CodeInternal
}

// codeOf is err's gRPC code, read off a gRPC status or a Connect error alike.
func codeOf(err error) codes.Code {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return status.Code(err)
	}
	for _, p := range codePairs {
		if p.C == ce.Code() {
			return p.G
		}
	}
	return codes.Unknown
}

// IsConflict reports a stale claim, the one meaning a FailedPrecondition has
// with no verdict detail (SourceDisabled, OtherBuild) beside it; every other
// refusal answers another code.
func IsConflict(err error) bool {
	_, other := OtherBuildOf(err)
	return codeOf(err) == codes.FailedPrecondition && !IsSourceDisabled(err) && !other
}

// IsTmuxGone reports the PTY owner's answer that a shell's tmux no longer
// exists: NotFound, or the FailedPrecondition of a started tmux that is gone.
func IsTmuxGone(err error) bool {
	c := codeOf(err)
	return c == codes.NotFound || c == codes.FailedPrecondition
}
