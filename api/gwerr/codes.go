package gwerr

import (
	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
)

// codePairs is the ONE gRPC↔Connect status-code table. Every hop that
// translates a status between the two wires (the Connect handler's
// asConnectError on the way to a browser, the node export's statusErr on
// the way to a mounter) reads it in one direction or the other, so a code
// that survives one hop survives the next. Before this table each hop kept
// a hand-written switch with a default → Internal, and the two had drifted:
// a mount-of-mount's Unavailable came out Internal, IsTransport said "a
// verdict", and clientsync dropped an unacknowledged write it should have
// parked. The table is total over both enums (TestCodeTableIsTotal) — a
// code missing here fails the contract's test, not a user's write.
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

// GRPCCode maps a Connect error code to its gRPC twin; a code outside the
// table maps to Internal.
func GRPCCode(c connect.Code) codes.Code {
	for _, p := range codePairs {
		if p.C == c {
			return p.G
		}
	}
	return codes.Internal
}
