package store

import "errors"

// ErrorClass is the transport-neutral category of a store sentinel error.
// It lives HERE, next to the sentinels themselves, because it used to live
// twice — the server's classifyStoreError and localdb's errToStatus each
// enumerated the full sentinel list, so a new sentinel had to be wired into
// both or one transport silently degraded to Internal. Now the class is
// assigned where the sentinel is declared and every transport (Connect
// status, raw-HTTP status, gRPC status across the plugin hop) maps from this
// one table.
type ErrorClass int

const (
	ClassInternal ErrorClass = iota
	ClassNotFound
	ClassInvalidArgument
	ClassConflict
)

// sentinelClasses assigns every exported Err* sentinel its class — including
// the deliberate ClassInternal ones, so the table is total. A sentinel
// declared anywhere in this package but missing here fails
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
	// Schema divergence is a deployment/data problem, not a caller mistake:
	// nothing a client retries or corrects. Internal is the honest class.
	{ErrSchemaDivergence, ClassInternal},
}

// ClassifyError returns the class of a (possibly wrapped) store sentinel.
// nil and any non-sentinel error are ClassInternal; callers that must
// distinguish nil handle it before classifying.
func ClassifyError(err error) ErrorClass {
	for _, s := range sentinelClasses {
		if errors.Is(err, s.Err) {
			return s.Class
		}
	}
	return ClassInternal
}
