package textedit

import "github.com/josephburnett/gridwell/api/rpc"

// SaveClaim is the one rule for the basis a text content write claims,
// whatever flush posts it: a version, or for a plugin's body the source's
// stamp. A claim asserts which bytes the edit was based on, so SaveBasis wins
// whenever there is one: the row moves on a foreign writer's event without
// this client seeing the new bytes. The row stands in only when the content
// entry is gone and that row owns the bytes; a link row tracks its placement,
// so its claim is zero. row is the snapshot the flush decided from, never
// re-read at send time.
func SaveClaim(rowOwnsContent bool, row, basis rpc.ContentBasis, haveBasis bool) rpc.ContentBasis {
	if haveBasis {
		return basis
	}
	if rowOwnsContent {
		return row
	}
	return rpc.ContentBasis{}
}
