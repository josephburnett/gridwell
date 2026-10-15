package panepreview

import "github.com/josephburnett/gridwell/client/pane"

// Layouts memoizes each pane tile's decoded tree by the generation of the
// bytes it was decoded from (cache.BodyGen), so no tree outlives its bytes.
// Which bytes may stand for the row is the cache's: it drops bytes a foreign
// row moved past, because an older arrangement names panes and tiles the row
// no longer does and drawing one resolves them. A corrupt body is reported
// once rather than per frame. It is not safe for concurrent use, the wasm
// client being single-threaded.
type Layouts struct {
	onErr   func(tileID string, err error)
	entries map[string]*layoutEntry
}

// layoutEntry's nil tree records a decode failure for those bytes.
type layoutEntry struct {
	gen  uint64
	tree *pane.Tree
}

// NewLayouts's onErr fires once per (tile, bytes) that will not decode;
// nil silences it.
func NewLayouts(onErr func(tileID string, err error)) *Layouts {
	return &Layouts{onErr: onErr, entries: map[string]*layoutEntry{}}
}

// Tree answers a pane tile's decoded tree. blobID 0 is never arranged. gen is
// the cached bytes' cache.BodyGen, 0 when none are cached. body answers the
// bytes, false while they are still in flight, and is not asked for bytes
// already decoded. ok is false for a never-arranged tile, a layout whose bytes
// have not landed, or a corrupt body.
func (l *Layouts) Tree(tileID string, blobID int64, gen uint64, body func() ([]byte, bool)) (*pane.Tree, bool) {
	if blobID == 0 {
		return nil, false
	}
	e := l.entries[tileID]
	if e != nil && gen != 0 && e.gen == gen {
		return e.tree, e.tree != nil
	}
	data, ok := body()
	if !ok {
		return nil, false
	}
	prefix := pane.ChainPrefix(tileID)
	tree, err := pane.DecodeLayout(data, func(id string) string { return prefix + id }, "")
	if err != nil {
		l.entries[tileID] = &layoutEntry{gen: gen}
		if l.onErr != nil {
			l.onErr(tileID, err)
		}
		return nil, false
	}
	l.entries[tileID] = &layoutEntry{gen: gen, tree: tree}
	return tree, true
}
