package gwerr

import (
	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
)

// codePairs is the one gRPC↔Connect status-code table. Gridwell answers in
// gRPC status codes everywhere — every namespace, the router, the
// node export — and exactly one hop translates: the Connect codec on
// the way to a browser (server.asConnectError). The node export
// hands a mounter the router's status error unchanged. The table is total
// and injective over the gRPC enum (TestCodeTableIsTotal), so a code
// missing here fails the contract's test rather than a user's write.
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

// ConnectCode maps a gRPC status code to its Connect twin. codes.OK has
// no Connect error code (a Connect error is never OK) and, like any code
// outside the table, maps to Internal.
func ConnectCode(c codes.Code) connect.Code {
	for _, p := range codePairs {
		if p.G == c {
			return p.C
		}
	}
	return connect.CodeInternal
}
