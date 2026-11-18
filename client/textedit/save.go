package textedit

import "github.com/josephburnett/gridwell/api/rpc"

// SaveClaim is the basis the beforeunload beacon claims: a version, or for a
// source's body the source's stamp. A claim asserts which bytes the edit was
// based on, so SaveBasis wins whenever there is one: the row moves on a
// foreign writer's event without this client seeing the new bytes. The row
// stands in only when the content entry is gone and that row owns the bytes;
// a link row tracks its placement, so its claim is zero.
func SaveClaim(rowOwnsContent bool, row, basis rpc.ContentBasis, haveBasis bool) rpc.ContentBasis {
	if haveBasis {
		return basis
	}
	if rowOwnsContent {
		return row
	}
	return rpc.ContentBasis{}
}

// Unsaved is the content entry a queued save reads when its turn comes.
type Unsaved interface {
	DirtyContent(id string) ([]byte, bool)
	SaveBasis(id string) (rpc.ContentBasis, bool)
}

// SaveNow is what a queued text save sends when its turn on the document's
// chain comes: the entry's unsaved bytes and the basis they were edited from,
// both read then. A save ahead of it may have landed those bytes, or been
// refused and taken them down for the row on disk, so a clean or absent entry
// sends nothing; a row the flush read never stands in for the basis.
func SaveNow(c Unsaved, cid string) (data []byte, claim rpc.ContentBasis, send bool) {
	if data, send = c.DirtyContent(cid); send {
		claim, _ = c.SaveBasis(cid)
	}
	return data, claim, send
}
