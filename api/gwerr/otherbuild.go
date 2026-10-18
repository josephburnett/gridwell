package gwerr

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/tracewire"
)

// OtherBuild is the web door's answer to a page of another build: a
// FailedPrecondition carrying a pb.OtherBuild detail naming both builds. It
// exists only at that door, so it is a Connect error and never crosses a gRPC
// hop.
func OtherBuild(node, client string) *connect.Error {
	named := "no build"
	if client != "" {
		named = tracewire.ShortCommit(client)
	}
	ce := connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"this page runs %s and the node runs %s: reload the page", named, tracewire.ShortCommit(node)))
	if d, err := connect.NewErrorDetail(&pb.OtherBuild{Node: node, Client: client}); err == nil {
		ce.AddDetail(d)
	}
	return ce
}

// OtherBuildOf answers the node's build when err is OtherBuild's answer. A
// plain FailedPrecondition (a stale save) carries no detail and is not one.
func OtherBuildOf(err error) (node string, ok bool) {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return "", false
	}
	for _, d := range ce.Details() {
		if v, derr := d.Value(); derr == nil {
			if sb, ok := v.(*pb.OtherBuild); ok {
				return sb.GetNode(), true
			}
		}
	}
	return "", false
}
