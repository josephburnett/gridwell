package textedit

// SaveClaim is the one rule for the version a text content write claims,
// whatever flush posts it. A claim asserts which bytes the edit was based on,
// so SaveBasis wins whenever there is one: the row version moves on a foreign
// writer's event without this client seeing the new bytes. It stands in only
// when the content entry is gone and that row owns the bytes; a link row
// tracks its placement, so its claim is 0. rowVersion is the snapshot the
// flush decided from, never re-read at send time.
func SaveClaim(rowOwnsContent bool, rowVersion, basis int64, haveBasis bool) int64 {
	if haveBasis {
		return basis
	}
	if rowOwnsContent {
		return rowVersion
	}
	return 0
}
