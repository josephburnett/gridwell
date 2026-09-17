// Package clientsync holds the post-RPC policy: what the outcome was (Of) and
// what to do about it (one React table per mutation family). Local state may be
// dropped only on a server verdict.
package clientsync

import (
	"context"
	"errors"

	"connectrpc.com/connect"
)

// Outcome is what an RPC's result meant.
type Outcome int

const (
	OutcomeOK Outcome = iota
	// OutcomeConflict is a version or overlap race; the local claim lost.
	OutcomeConflict
	// OutcomeRejected is the server saying no.
	OutcomeRejected
	// OutcomeTransport is the server never speaking, so the local state is
	// still the only truth the user has.
	OutcomeTransport
)

// Of reads a non-connect error as Transport, coming from below the protocol,
// and every other coded error as a server that answered. A context deadline is
// checked first and by identity, because the bound is inflight.Deadline, the
// client's own timer, and reading its expiry as a verdict would drop bytes.
func Of(err error) Outcome {
	if err == nil {
		return OutcomeOK
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return OutcomeTransport
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return OutcomeTransport
	}
	switch ce.Code() {
	case connect.CodeFailedPrecondition:
		return OutcomeConflict
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeCanceled:
		return OutcomeTransport
	}
	return OutcomeRejected
}

// Reaction is what a mutation's outcome calls for; success is the zero value.
type Reaction struct {
	// Refetch is never set on Transport, where against a flapping link it
	// could succeed and revert a patch whose write never landed.
	Refetch bool
	// Log surfaces the failure through errsurface.
	Log bool
	// DropLocal is true only on a server verdict; false means the caller
	// parks the state for a retry.
	DropLocal bool
	// Retry queues the value for the reconnect drain, exactly on Transport.
	Retry bool
}

// React is for a mutation that wrote no local state ahead of the RPC.
// Transport sets no Retry: there is no ledger behind these ops.
func React(o Outcome) Reaction {
	switch o {
	case OutcomeConflict:
		return Reaction{Refetch: true}
	case OutcomeRejected, OutcomeTransport:
		return Reaction{Log: true}
	}
	return Reaction{}
}

// ReactOptimistic is for a caller that patched the local cache before the RPC.
// Any server verdict rolls it back, or the cache stays ahead of the server;
// Transport keeps the patch, which is the value the retry will land.
func ReactOptimistic(o Outcome) Reaction {
	switch o {
	case OutcomeConflict:
		return Reaction{Refetch: true, DropLocal: true}
	case OutcomeRejected:
		return Reaction{Refetch: true, Log: true, DropLocal: true}
	case OutcomeTransport:
		return Reaction{Log: true, Retry: true}
	}
	return Reaction{}
}

// ReactSave is for a content save, the one write that claims a version. On
// Transport the entry stays dirty, being the only copy of the user's unsaved
// words. A conflict surfaces here where the other tables leave it silent:
// someone else changed these bytes and the screen is about to show theirs.
func ReactSave(o Outcome) Reaction {
	switch o {
	case OutcomeConflict:
		return Reaction{Refetch: true, Log: true, DropLocal: true}
	case OutcomeRejected:
		return Reaction{Refetch: true, Log: true, DropLocal: true}
	case OutcomeTransport:
		return Reaction{Log: true, Retry: true}
	}
	return Reaction{}
}

// IsUnimplemented reports a plugin's answer that it does not serve this call.
// It is a capability, never a failure to surface.
func IsUnimplemented(err error) bool {
	var ce *connect.Error
	return errors.As(err, &ce) && ce.Code() == connect.CodeUnimplemented
}

// GridRead is what one GetGrid answer calls for. The cache keys a grid by the
// id it was answered under and every frame resolves by the id it was asked
// for, so an answer under another name would strand the pane loading forever
// with a 200 behind it: that is a verdict on the asked id, latched and
// reported, though the rows are still worth remembering under their own.
type GridRead struct {
	Latch LatchVerdict
	// Store puts the answered rows in the cache.
	Store bool
	// Renamed is the answered-under-another-id case, which the notice names.
	Renamed bool
}

// LatchVerdict is what an answer says about the asked id's failure latch. A
// transport failure says nothing, so it leaves the latch as it found it.
type LatchVerdict int

const (
	LatchKeep LatchVerdict = iota
	LatchSet
	LatchClear
)

// ReactGridRead is the one table for a grid read's outcome.
func ReactGridRead(asked, answered string, o Outcome) GridRead {
	switch o {
	case OutcomeOK:
		if answered != asked {
			return GridRead{Latch: LatchSet, Store: true, Renamed: true}
		}
		return GridRead{Latch: LatchClear, Store: true}
	case OutcomeTransport:
		return GridRead{}
	}
	return GridRead{Latch: LatchSet}
}
