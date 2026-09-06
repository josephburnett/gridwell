package textedit

// SaveClaim is the one rule for the version a text content write claims,
// whatever flush posts it: the debounced sweep, the outbox drain, the ascent
// flush, the unload beacon.
//
// A claim asserts which bytes the edit was based on, so the cache's SaveBasis
// is the answer whenever there is one. Only content fetches and save
// responses advance the basis, so a version is never claimed apart from the
// bytes it vouches for; the grid row version moves on a foreign writer's
// event without this client ever seeing the new bytes, and claiming that
// would carry a stale buffer past the server's concurrency check and destroy
// the foreign edit.
//
// The fallback is for a dirty edit whose content entry is gone by send time —
// dropped by a refused write's reconcile, or by a removed row. The row
// version stands in, and only when that row owns the bytes: a link row's
// version tracks its own placement, never the target's content, so 0 is the
// claim, which the server refuses unless the content really is untouched.
//
// rowVersion is the snapshot the flush decided from, never a version re-read
// when the save reaches the head of the queue. A row read at send time may
// have been advanced since by a foreign writer, and claiming it is the
// silent overwrite above; the snapshot is stale in that case, so the write is
// refused and reconciles visibly.
func SaveClaim(rowOwnsContent bool, rowVersion, basis int64, haveBasis bool) int64 {
	if haveBasis {
		return basis
	}
	if rowOwnsContent {
		return rowVersion
	}
	return 0
}
