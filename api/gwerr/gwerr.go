// Package gwerr is the contract's error vocabulary: the sentinel errors a
// store or plugin answers with, and the sentinel-to-class table every
// transport maps from. It is in the api module so the host and a plugin
// share the sentinels without importing each other.
package gwerr

import (
	"errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Sentinel errors. A plugin may return one wrapped, so callers use
// errors.Is.
var (
	ErrNotFound        = errors.New("not found")
	ErrOverlap         = errors.New("footprint overlaps an existing tile")
	ErrInvalidPath     = errors.New("descent path is invalid")
	ErrInvalidArgument = errors.New("invalid argument")
	ErrNotURLTile      = errors.New("not a URL tile")
	ErrNotTextTile     = errors.New("not a text tile")
	ErrNotWellTile     = errors.New("not a well tile")
	ErrNotShellTile    = errors.New("not a shell tile")
	ErrNotPaneTile     = errors.New("not a pane tile")
	ErrVersionConflict = errors.New("version mismatch")
	// ErrSchemaDivergence is a deployment problem, hence ClassInternal.
	ErrSchemaDivergence = errors.New("database schema diverges from this binary's schema")
)

// ErrorClass is the transport-neutral category of a sentinel. Every
// transport maps from sentinelClasses, so one cannot degrade to Internal
// where another does not.
type ErrorClass int

const (
	ClassInternal ErrorClass = iota
	ClassNotFound
	ClassInvalidArgument
	ClassConflict
)

// sentinelClasses is total over the exported Err* sentinels, the
// ClassInternal ones included; TestEverySentinelIsClassified pins that.
var sentinelClasses = []struct {
	Err   error
	Class ErrorClass
}{
	{ErrNotFound, ClassNotFound},
	{ErrInvalidArgument, ClassInvalidArgument},
	{ErrInvalidPath, ClassInvalidArgument},
	{ErrNotURLTile, ClassInvalidArgument},
	{ErrNotTextTile, ClassInvalidArgument},
	{ErrNotWellTile, ClassInvalidArgument},
	{ErrNotShellTile, ClassInvalidArgument},
	{ErrNotPaneTile, ClassInvalidArgument},
	{ErrOverlap, ClassConflict},
	{ErrVersionConflict, ClassConflict},
	{ErrSchemaDivergence, ClassInternal},
}

// classCodes is the one class-to-status-code table: what a namespace answers a
// store sentinel with, and what the Connect codec answers a raw sentinel with
// through ConnectCode. Total over ErrorClass; TestStatusCodeIsTotal pins it.
var classCodes = map[ErrorClass]codes.Code{
	ClassInternal:        codes.Internal,
	ClassNotFound:        codes.NotFound,
	ClassInvalidArgument: codes.InvalidArgument,
	ClassConflict:        codes.FailedPrecondition,
}

// StatusCode is the gRPC code a class answers with.
func StatusCode(c ErrorClass) codes.Code {
	if code, ok := classCodes[c]; ok {
		return code
	}
	return codes.Internal
}

// ToStatus wraps a classified sentinel as a status error carrying its class's
// code, so the classification survives a routing hop. A status error passes
// through untouched, and so does an unclassified error, which reads as
// codes.Unknown: a namespace's own verdicts are its own.
func ToStatus(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	c := ClassifyError(err)
	if c == ClassInternal {
		return err
	}
	return status.Error(StatusCode(c), err.Error())
}

// ClassifyError returns the class of a sentinel, wrapped or not. nil and any
// other error are ClassInternal, so a caller tells nil apart first.
func ClassifyError(err error) ErrorClass {
	for _, s := range sentinelClasses {
		if errors.Is(err, s.Err) {
			return s.Class
		}
	}
	return ClassInternal
}

// IsTransport reports that the far side of a gRPC hop never spoke. Every
// server-side hop that degrades to a remembered answer keys on this alone,
// so a coded answer such as NotFound is never served from a cache.
// clientsync.Of is the client twin on the same three codes, pinned to this one
// by TestOfAgreesWithGwerrIsTransport.
func IsTransport(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled:
		return true
	}
	return false
}
