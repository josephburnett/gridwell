// Package clientsync holds the pure cache-resync policy the wasm client
// applies after a mutation RPC returns. The decision — resync the cache vs
// surface the error — is data so it can be table-tested without a browser,
// a transport, or App state. The transport-specific part (deciding whether
// an error *is* a version/overlap conflict) stays in the shell; this package
// only encodes what to do once that's known.
package clientsync

// Reaction is what a mutation's RPC outcome calls for. Success is the zero
// value (neither field set). The two failure reactions are mutually
// exclusive: a conflict is an expected control-flow signal (the optimistic
// local edit lost a version/overlap race, so refetch the authoritative
// grid), whereas any other error is an unexpected failure to surface.
type Reaction struct {
	// Refetch asks the caller to re-fetch the affected grid so the cache
	// catches up to the server's authoritative state.
	Refetch bool
	// Log asks the caller to surface the error (console) — a real,
	// unexpected failure rather than a lost race.
	Log bool
}

// Classify maps an RPC outcome to a Reaction. err==nil → success (zero
// Reaction). A conflict → Refetch only (never logged: it's expected). Any
// other error → Log only. The caller supplies `conflict` because detecting
// it is transport-specific (e.g. a Connect FailedPrecondition status).
func Classify(err error, conflict bool) Reaction {
	if err == nil {
		return Reaction{}
	}
	if conflict {
		return Reaction{Refetch: true}
	}
	return Reaction{Log: true}
}

// ClassifyOptimistic is Classify for a mutation whose caller ALREADY patched
// the local cache before the RPC (an optimistic write, e.g. the well-view
// framing patched so the parent preview updates instantly). The difference
// is the non-conflict arm: a rejected optimistic patch left the cache AHEAD
// of server truth, so EVERY failure must refetch — otherwise the local
// preview shows framing the server refused until an unrelated event
// resyncs it, and the state silently snaps back on the next reload
// (issue #156; charter §7 — the cache is a view of server truth, never an
// authority). Conflicts stay silent (expected race); other errors also
// surface.
func ClassifyOptimistic(err error, conflict bool) Reaction {
	if err == nil {
		return Reaction{}
	}
	return Reaction{Refetch: true, Log: !conflict}
}
