// Package gwerr is the contract's error vocabulary: the sentinel errors a
// Gridwell store or plugin answers with, and the one sentinel→class table
// every transport maps from (Connect status, raw-HTTP status, the gRPC
// plugin hop). It lives in the api module because the host maps these
// classes and a third-party plugin answers with the same sentinels;
// neither may import the other's implementation.
package gwerr

import (
	"errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Sentinel errors. Callers use errors.Is; plugins return them (wrapped is
// fine) so every transport classifies identically.
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
	// ErrSchemaDivergence: the DB's schema differs from the binary's — a
	// deployment/data problem, not a caller mistake.
	ErrSchemaDivergence = errors.New("database schema diverges from this binary's schema")
)

// ErrorClass is the transport-neutral category of a sentinel. The class is
// assigned where the sentinel is declared, and every transport maps from
// this one table, so a new sentinel cannot degrade to Internal on one
// transport and not another.
type ErrorClass int

const (
	ClassInternal ErrorClass = iota
	ClassNotFound
	ClassInvalidArgument
	ClassConflict
)

// sentinelClasses assigns every exported Err* sentinel its class,
// including the deliberate ClassInternal ones, so the table is total. A
// sentinel declared in this package but missing here fails
// TestEverySentinelIsClassified: classification is part of declaring one.
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

// ClassifyError returns the class of a (possibly wrapped) sentinel. nil
// and any non-sentinel error are ClassInternal; callers that must
// distinguish nil handle it before classifying.
func ClassifyError(err error) ErrorClass {
	for _, s := range sentinelClasses {
		if errors.Is(err, s.Err) {
			return s.Class
		}
	}
	return ClassInternal
}

// IsTransport reports a transport-shaped gRPC failure: the far side never
// spoke (Unavailable — refused or dropped; DeadlineExceeded — timed out;
// Canceled — the caller gave up). Every server-side hop that degrades to a
// remembered answer keys on exactly this and nothing else. A coded answer
// — NotFound, a tombstone, InvalidArgument — is an answer and passes
// through verbatim, never resurrected from a cache or turned into a link.
// This is the one classifier for gRPC hops (the pluginhost read-through
// cache, the source cache, the cross-plugin deep copy); clientsync.Of is
// its Connect-wire twin on the client, pinned to the same three codes.
func IsTransport(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled:
		return true
	}
	return false
}
