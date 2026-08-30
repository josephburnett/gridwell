// Package clientsync holds the pure post-RPC policy the wasm client applies
// after a mutation returns: what the outcome was (Of — the one error
// classifier) and what to do about it (React* — one table per mutation
// family). The decisions are data so they can be table-tested without a
// browser, a transport, or App state.
//
// The rule this package enforces: local state may be dropped only on a
// server verdict. A dirty text buffer, a pending framing value, a typed name
// — none of it may be reconciled away because the network blinked. Transport
// failure keeps the state and retries; only Ok, Conflict, or Rejected (the
// server actually spoke) may reconcile.
package clientsync

import (
	"errors"

	"connectrpc.com/connect"
)

// Outcome is what an RPC's result actually meant. The distinction that
// carries the weight is Transport vs everything else: Transport means the
// server never spoke — nothing was accepted, nothing was refused, and no
// local state may be reconciled away on the strength of it.
type Outcome int

const (
	// OutcomeOK — the mutation landed.
	OutcomeOK Outcome = iota
	// OutcomeConflict — FailedPrecondition: a version/overlap race. An
	// expected control-flow signal; the local claim lost, refetch truth.
	OutcomeConflict
	// OutcomeRejected — the server spoke and said no (invalid argument,
	// not found, refused). The local attempt is wrong and must reconcile.
	OutcomeRejected
	// OutcomeTransport — the server never spoke: connection refused or
	// dropped, deadline, cancellation. The local state is still the only
	// truth the user has; keep it and retry when the link returns.
	OutcomeTransport
)

// Of is the one error classifier. The transport set is pinned to the wire
// by TestOfPinsWireCodes: connect-go surfaces a refused/dropped connection
// as CodeUnavailable, a timeout as CodeDeadlineExceeded, a cancellation as
// CodeCanceled. A non-connect error can only come from below the protocol
// (the transport itself), so it classifies Transport too. Every other
// coded error is a server that answered.
func Of(err error) Outcome {
	if err == nil {
		return OutcomeOK
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

// Reaction is what a mutation's outcome calls for. Success is the zero
// value.
type Reaction struct {
	// Refetch asks the caller to re-fetch the affected grid so the cache
	// catches up to the server's authoritative state. Never set on
	// Transport: the refetch would fail against the same dead link, and
	// against a flapping one it could succeed and revert an optimistic
	// patch whose write never landed, silently losing the value.
	Refetch bool
	// Log asks the caller to surface the failure (the errsurface strip).
	Log bool
	// DropLocal permits reconciling away the caller's local copy (a dirty
	// content entry, an optimistic patch's claim to eventual persistence).
	// True only when the server gave a verdict. When false on a failure,
	// the caller keeps the state parked so a retry can land it.
	DropLocal bool
	// Retry asks the caller to leave the value queued for the retry kick
	// (the reconnect drain). Set exactly on Transport.
	Retry bool
}

// React is the policy for a plain mutation — no local state was written
// ahead of the RPC (create, move, delete: the ghost is presentation only,
// and snapping it back is the honest reconcile). Transport surfaces but sets
// no Retry: there is no ledger behind these ops, so claiming a retry that
// will never happen would be a lie.
func React(o Outcome) Reaction {
	switch o {
	case OutcomeConflict:
		return Reaction{Refetch: true}
	case OutcomeRejected, OutcomeTransport:
		return Reaction{Log: true}
	}
	return Reaction{}
}

// ReactOptimistic is the policy for a mutation whose caller already patched
// the local cache before the RPC (framing writes). A server verdict rolls the
// cache back to truth, Rejected included: otherwise the cache stays ahead of
// the server. Transport keeps the patch — it is the value the retry will land
// — and refetches nothing (see Reaction.Refetch).
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

// ReactSave is the policy for a content save (the bytes in the cache's
// content entry — the one write that claims a version). On a verdict the
// unsaved bytes reconcile away: the screen shows what the server holds, not
// what it refused. On Transport the entry stays dirty — it is the only copy
// of the user's unsaved words, and the retry lands it.
//
// A conflict is surfaced (Log), unlike in the other two tables. A save
// conflict can mean only one thing: someone else changed these bytes, and the
// words on screen are about to be replaced by theirs. That is exactly the
// event the user must be told about.
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

// IsUnimplemented reports a plugin's "I don't serve this" answer — a
// normal capability property (no previews, no pages), never a failure
// to surface. Lives here so every wire-code judgment is in the one
// tested classifier.
func IsUnimplemented(err error) bool {
	var ce *connect.Error
	return errors.As(err, &ce) && ce.Code() == connect.CodeUnimplemented
}
