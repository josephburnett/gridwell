package pluginhost

// The derived address: how the node names a plugin entry it has never had to
// mint a row for.
//
// An id is a chain of segments (docs/ids.md). Digits name a row. A key-form
// segment — "~" plus base64url, rpc.KeyTileID — names a thing by what it IS,
// and the payload inside it is this package's business alone: everything
// between here and the browser treats the segment as opaque, so the grammar
// has exactly one owner.
//
// Two positions, one payload:
//
//	grid: the context key                    "~" + b64("/home/joe")
//	tile: the context key, NUL, the entry key "~" + b64("/home" NUL "/home/joe")
//
// The context half is what makes a tile answerable on its own. The node keeps
// no key→context index — that would be a second copy of the plugin's own
// structure, written on every listing, which is exactly the row we are here
// to stop minting — and plugin.v1 has no verb that describes one entry. So the
// address carries the listing that names it, and GetTile on an untouched entry
// is one List of its context, the same call GetGrid would make.
//
// NUL cannot appear in a plugin key that is a path, a URL, or a handle, and
// base64url hides it from the URL either way; a payload without one is a
// context, with one is an entry.

import (
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
)

// addrSep separates the context half of a tile address from the entry key.
const addrSep = "\x00"

// gridAddr renders a context key as a grid segment.
func gridAddr(context string) string { return rpc.KeyTileID(context) }

// tileAddr renders an entry as a tile segment: the context that lists it and
// its key.
func tileAddr(context, key string) string { return rpc.KeyTileID(context + addrSep + key) }

// splitAddr decodes a key-form segment. isTile distinguishes the two
// positions: a tile address carries an entry key, a grid address is a bare
// context. ok is false for a segment of any other shape.
func splitAddr(seg string) (context, key string, isTile, ok bool) {
	payload, ok := rpc.TileKey(seg)
	if !ok {
		return "", "", false, false
	}
	context, key, isTile = strings.Cut(payload, addrSep)
	return context, key, isTile, true
}
