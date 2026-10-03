package rpc

import "strings"

// A plugin entry's derived address: how the node names an entry, minted row
// or not. A key-form segment (KeyTileID) carries the context key, and for a
// tile the entry key after a NUL, so one entry is answerable on its own
// though plugin.v1 has no verb for one. The address is namespace-relative;
// the router qualifies it on the way out like every other id.

const entrySep = "\x00"

// EntryGridID names a plugin context.
func EntryGridID(context string) string { return KeyTileID(context) }

// EntryTileID names the entry key lists under context.
func EntryTileID(context, key string) string { return KeyTileID(context + entrySep + key) }

// SplitEntryID decodes a key-form segment; isTile is true when it carries an
// entry key rather than a bare context.
func SplitEntryID(seg string) (context, key string, isTile, ok bool) {
	payload, ok := TileKey(seg)
	if !ok {
		return "", "", false, false
	}
	context, key, isTile = strings.Cut(payload, entrySep)
	return context, key, isTile, true
}
