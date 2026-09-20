package pluginhost

// The derived address: how the node names a plugin entry, minted row or not.
// A key-form segment (rpc.KeyTileID) carries the context key, and for a tile
// the entry key after a NUL, so one entry is answerable on its own though
// plugin.v1 has no verb for one. A key→context index would copy the plugin's
// structure on every listing, so GetTile is one List of the context named.

import (
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
)

const addrSep = "\x00"

func gridAddr(context string) string { return rpc.KeyTileID(context) }

// tileAddr renders an entry as its one public id. The row a first durable fact
// mints is bookkeeping, resolved on the way in (Adapter.resolveTile) and never
// handed out: a mint that renamed the entry would take the id out from under
// whoever was standing on it.
func tileAddr(context, key string) string { return rpc.KeyTileID(context + addrSep + key) }

// splitAddr decodes a key-form segment; isTile is true when it carries an
// entry key rather than a bare context.
func splitAddr(seg string) (context, key string, isTile, ok bool) {
	payload, ok := rpc.TileKey(seg)
	if !ok {
		return "", "", false, false
	}
	context, key, isTile = strings.Cut(payload, addrSep)
	return context, key, isTile, true
}
