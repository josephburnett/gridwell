// Package clientsync holds the post-RPC policy: what the outcome was (Of) and
// what to do about it (one React table per mutation family). Local state may be
// dropped only on a server verdict.
package clientsync

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/client/inflight"
)

// Outcome is what an RPC's result meant.
type Outcome int

const (
	OutcomeOK Outcome = iota
	// OutcomeConflict is a version or overlap race; the local claim lost.
	OutcomeConflict
	// OutcomeRejected is the server saying no.
	OutcomeRejected
	// OutcomeTransport is the server never speaking.
	OutcomeTransport
	// OutcomeDead is a dead link (gwerr.IsDeadRef), a state rather than an
	// error.
	OutcomeDead
	// OutcomeAbandoned is a call whose context the client cancelled itself,
	// so nothing was heard and nothing is said; only reads are cancelled
	// (inflight.Reads.CancelIf), by a caller that asks again.
	OutcomeAbandoned
)

// Of reads a non-connect error as Transport and every coded error as a server
// that answered. A context deadline is the client's own timer
// (inflight.Deadline), so it is Transport: read as a verdict it would drop
// bytes. A Canceled the far side sent wraps no context.Canceled, so it stays
// Transport.
func Of(err error) Outcome {
	if err == nil {
		return OutcomeOK
	}
	if errors.Is(err, context.Canceled) {
		return OutcomeAbandoned
	}
	if errors.Is(err, context.DeadlineExceeded) {
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
	if gwerr.IsDeadRef(ce) {
		return OutcomeDead
	}
	return OutcomeRejected
}

// Unheard reports a call the server never answered. A write treats both alike:
// its bytes are still owed.
func Unheard(o Outcome) bool {
	return o == OutcomeTransport || o == OutcomeAbandoned
}

// ReadSurfaces reports whether a read's failure goes on the error strip: every
// failure but the dead verdict, which the dead face carries alone, and the
// client's own cancel, which is no failure.
func ReadSurfaces(o Outcome) bool {
	return o != OutcomeOK && o != OutcomeDead && o != OutcomeAbandoned
}

// PlaceReadSurfaces reports whether a by-id tile read's latch goes on the
// strip. A place has no dead face, so dead surfaces like any refusal; an
// outage is the grid read's to name.
func PlaceReadSurfaces(v inflight.Verdict) bool {
	return v == inflight.Refused || v == inflight.Dead
}

// Reaction is what a mutation's outcome calls for; success is the zero value.
type Reaction struct {
	// Refetch is never set on Transport: it could revert a patch whose write
	// never landed.
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
	case OutcomeRejected, OutcomeDead, OutcomeTransport, OutcomeAbandoned:
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
	case OutcomeRejected, OutcomeDead:
		return Reaction{Refetch: true, Log: true, DropLocal: true}
	case OutcomeTransport, OutcomeAbandoned:
		return Reaction{Log: true, Retry: true}
	}
	return Reaction{}
}

// ReactSave is for a content save, the one write that claims a version. A
// conflict surfaces here where the other tables leave it silent: the screen
// is about to show someone else's bytes.
func ReactSave(o Outcome) Reaction {
	switch o {
	case OutcomeConflict:
		return Reaction{Refetch: true, Log: true, DropLocal: true}
	case OutcomeRejected, OutcomeDead:
		return Reaction{Refetch: true, Log: true, DropLocal: true}
	case OutcomeTransport, OutcomeAbandoned:
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

// OwnNotice is what a write's own-words notice says, for a write that has
// words of its own (a source and a failText).
type OwnNotice int

const (
	OwnNone OwnNotice = iota
	// OwnRetry is the Info line "server unreachable — will retry".
	OwnRetry
	// OwnFailed is the Error line carrying the server's reason.
	OwnFailed
)

// Notices is which notices a finished write posts: the generic rpc: line, the
// write's own, and the "changed elsewhere — reloaded" line, which only a
// conflict earns.
type Notices struct {
	Generic  bool
	Own      OwnNotice
	Reloaded bool
}

// NoticesFor is the one table, over the reaction the outcome already earned.
func NoticesFor(r Reaction, o Outcome, ownWords bool) Notices {
	n := Notices{Generic: r.Log, Reloaded: r.Refetch && o == OutcomeConflict}
	if !ownWords {
		return n
	}
	switch o {
	case OutcomeOK:
	case OutcomeTransport, OutcomeAbandoned:
		n.Generic = false
		n.Own = OwnRetry
	default:
		n.Own = OwnFailed
	}
	return n
}

// ReactRead is the one table over a read's outcome for the asked key's
// failure latch.
func ReactRead(o Outcome) inflight.Verdict {
	switch o {
	case OutcomeOK:
		return inflight.Answered
	case OutcomeTransport:
		return inflight.Unreachable
	case OutcomeDead:
		return inflight.Dead
	case OutcomeAbandoned:
		return inflight.Abandoned
	}
	return inflight.Refused
}

// GridRead is what one GetGrid answer calls for. An answer under another id
// would strand the pane loading forever, so it is a verdict on the asked id,
// though its rows are still stored.
type GridRead struct {
	Latch inflight.Verdict
	// Store puts the answered rows in the cache.
	Store bool
	// Renamed is the answered-under-another-id case, which the notice names.
	Renamed bool
	// Surface puts the failure on the strip (ReadSurfaces); Resolve takes the
	// grid's notice down. An abandoned read does neither.
	Surface bool
	Resolve bool
}

// ReactGridRead is the one table for a grid read's outcome.
func ReactGridRead(asked, answered string, o Outcome) GridRead {
	r := GridRead{Latch: ReactRead(o), Store: o == OutcomeOK, Surface: ReadSurfaces(o)}
	if o == OutcomeOK && answered != asked {
		r.Latch, r.Renamed = inflight.Refused, true
	}
	r.Resolve = !r.Surface && !r.Renamed && o != OutcomeAbandoned
	return r
}

// PreviewReaction is what one preview fetch's outcome calls for. A face is
// asked for on every draw until something settles it.
type PreviewReaction struct {
	// Store keeps the bytes as the face.
	Store bool
	// Settle records that this blob has no image, so the next draw does not
	// ask again.
	Settle bool
	// Surface reports the failure; Latch is ReactRead's verdict on it.
	Surface bool
	Latch   inflight.Verdict
}

// ReactPreview is the one table for a preview fetch's outcome.
func ReactPreview(err error, empty bool) PreviewReaction {
	switch {
	case err == nil && empty:
		return PreviewReaction{Settle: true}
	case err == nil:
		return PreviewReaction{Store: true}
	case IsUnimplemented(err):
		return PreviewReaction{Settle: true}
	}
	o := Of(err)
	return PreviewReaction{Surface: ReadSurfaces(o), Latch: ReactRead(o)}
}
