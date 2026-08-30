package gwerr

import (
	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
)

// codePairs is the ONE gRPC↔Connect status-code table. Gridwell answers in
// gRPC status codes everywhere — every namespace, the router, the
// federation export — and exactly one hop translates: the Connect codec on
// the way to a browser (server.asConnectError). Before this table that hop
// and the export's inverse each kept a hand-written switch with a default
// → Internal, and the two had drifted: a mount-of-mount's Unavailable came
// out Internal, IsTransport said "a verdict", and clientsync dropped an
// unacknowledged write it should have parked. (The inverse is gone with
// the export's per-method delegation — 2026-08-29, docs/simplify-plan.md
// S2: the export hands a mounter the router's status error unchanged.) The
// table is total and injective over the gRPC enum
// (TestCodeTableIsTotal) — a code missing here fails the contract's test,
// not a user's write.
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
