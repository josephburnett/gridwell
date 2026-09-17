package panepreview

import "github.com/josephburnett/gridwell/client/pane"

// Layouts memoizes each pane tile's decoded tree by blob generation: another
// view's layout write invalidates through the tile row's blob id. Until the
// new bytes land the last decoded arrangement keeps drawing, which beats a
// blank flash, and a corrupt blob is reported once rather than per frame. It
// is not safe for concurrent use, the wasm client being single-threaded.
type Layouts struct {
	onErr   func(tileID string, err error)
	entries map[string]*layoutEntry
}

// layoutEntry's nil tree records a decode failure for that blob.
type layoutEntry struct {
	blobID int64
	tree   *pane.Tree
}

// NewLayouts's onErr fires once per (tile, blob) whose bytes will not decode;
// nil silences it.
func NewLayouts(onErr func(tileID string, err error)) *Layouts {
	return &Layouts{onErr: onErr, entries: map[string]*layoutEntry{}}
}

// Tree answers a pane tile's decoded tree. blobID 0 is never arranged. body
// answers the tile's bytes, false while they are still in flight, and is not
// asked for a blob already decoded. ok is false for a never-arranged tile, a
// not-yet-fetched layout with nothing older to show, or a corrupt blob.
func (l *Layouts) Tree(tileID string, blobID int64, body func() ([]byte, bool)) (*pane.Tree, bool) {
	if blobID == 0 {
		return nil, false
	}
	e := l.entries[tileID]
	if e != nil && e.blobID == blobID {
		return e.tree, e.tree != nil
	}
	data, ok := body()
	if !ok {
		if e != nil && e.tree != nil {
			return e.tree, true
		}
		return nil, false
	}
	prefix := pane.ChainPrefix(tileID)
	tree, err := pane.DecodeLayout(data, func(id string) string { return prefix + id }, "")
	if err != nil {
		l.entries[tileID] = &layoutEntry{blobID: blobID}
		if l.onErr != nil {
			l.onErr(tileID, err)
		}
		return nil, false
	}
	l.entries[tileID] = &layoutEntry{blobID: blobID, tree: tree}
	return tree, true
}
