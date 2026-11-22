package gwerr

import (
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ConnectCode maps a gRPC status code to its Connect twin: Connect numbers
// its codes as gRPC does, which the read verdict's code on the wire relies on
// too. Gridwell answers in gRPC codes everywhere; the web API, in
// server.asConnectError, is the one hop that translates. codes.OK and any
// code past Unauthenticated map to Internal.
func ConnectCode(c codes.Code) connect.Code {
	if c < codes.Canceled || c > codes.Unauthenticated {
		return connect.CodeInternal
	}
	return connect.Code(c)
}

// codeOf is err's gRPC code, read off a gRPC status or a Connect error alike.
func codeOf(err error) codes.Code {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return status.Code(err)
	}
	if c := ce.Code(); c >= connect.CodeCanceled && c <= connect.CodeUnauthenticated {
		return codes.Code(c)
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
