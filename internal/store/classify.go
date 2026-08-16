package store

import "github.com/josephburnett/gridwell/api/gwerr"

// The sentinel→class table lives in api/gwerr (2026-08-15) — the error
// vocabulary is contract, and the host maps it without importing any
// store. These aliases keep the store-side names for its own tree.

type ErrorClass = gwerr.ErrorClass

const (
	ClassInternal        = gwerr.ClassInternal
	ClassNotFound        = gwerr.ClassNotFound
	ClassInvalidArgument = gwerr.ClassInvalidArgument
	ClassConflict        = gwerr.ClassConflict
)

// ClassifyError is gwerr.ClassifyError.
func ClassifyError(err error) ErrorClass { return gwerr.ClassifyError(err) }
