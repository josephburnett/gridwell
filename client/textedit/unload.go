package textedit

// UnloadFlush is one dirty content entry's unload decision.
type UnloadFlush int

const (
	// UnloadSkip: the entry must not write (a non-text or read-only row).
	UnloadSkip UnloadFlush = iota
	// UnloadBeacon: beacon now with the returned version claim.
	UnloadBeacon
	// UnloadAsync: no claim exists to beacon with — the async path (which
	// resolves the owner row) is the only door left.
	UnloadAsync
)

// DecideUnloadFlush is the ONE rule for what a dying page does with a
// dirty text body (audit #8). The subtle case is rowKnown=false: the
// owner row living in no cached grid is NOT a dead end (audit #6 — a
// leaf link's target in a never-fetched foreign grid), because the
// SaveBasis alone is the claim, and only editable text ever becomes
// dirty; the server issues the verdict either way. The unload path used
// to skip that case silently — the one flush with no next sweep behind
// it, so the edit was guaranteed lost.
func DecideUnloadFlush(rowKnown, rowEditableText bool, rowVersion int64, basis int64, haveBasis bool) (claim int64, do UnloadFlush) {
	if rowKnown && !rowEditableText {
		return 0, UnloadSkip
	}
	if haveBasis {
		return basis, UnloadBeacon
	}
	if rowKnown {
		return rowVersion, UnloadBeacon
	}
	return 0, UnloadAsync
}
