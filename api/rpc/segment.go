package rpc

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// SegmentShape is what one segment of a chained id is: a namespace (a plugin
// or node id, or a connection name), a store row id, or a plugin key carried
// in the id itself so a tile no plugin minted a row for is still nameable.
// The three are disjoint by construction: a row is all digits, a key form
// leads with "~", a namespace segment is neither.
type SegmentShape string

const (
	ShapeNamespace SegmentShape = "namespace"
	ShapeRow       SegmentShape = "row"
	ShapeKey       SegmentShape = "key"
)

// keyTilePrefix marks a key-form tile segment. "~" is unreserved in a URL
// path, is not a base64url character, and cannot begin a row id or a
// letter-leading namespace segment.
const keyTilePrefix = "~"

// keyTileEncoding is strict, so exactly one segment spells any given key. Its
// alphabet excludes "/", which keeps a key chain-safe however many slashes
// the key contains, and needs no escaping in a URL path.
var keyTileEncoding = base64.RawURLEncoding

// KeyTileID renders a plugin key as a tile segment. Any byte string is a legal
// key and round-trips through TileKey.
func KeyTileID(key string) string {
	return keyTilePrefix + keyTileEncoding.EncodeToString([]byte(key))
}

// TileKey decodes a key-form tile segment. It succeeds exactly when ShapeOf is
// ShapeKey.
func TileKey(seg string) (key string, ok bool) {
	rest, cut := strings.CutPrefix(seg, keyTilePrefix)
	if !cut {
		return "", false
	}
	b, err := keyTileEncoding.Strict().DecodeString(rest)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// ShapeOf is total: anything that is neither a row id nor a well-formed key
// form is a namespace segment.
func ShapeOf(seg string) SegmentShape {
	if _, err := strconv.ParseInt(seg, 10, 64); err == nil {
		return ShapeRow
	}
	if _, ok := TileKey(seg); ok {
		return ShapeKey
	}
	return ShapeNamespace
}

// IsTileSegment: a tile inside a namespace, rather than a hop to another one.
func IsTileSegment(seg string) bool {
	s := ShapeOf(seg)
	return s == ShapeRow || s == ShapeKey
}

// OwnerNamespaceOf returns the namespace the node whose id is nodeID routes a
// qualified id to: Server.resolve's peel as a value, so a client and a node
// cannot disagree about whether a reference names a namespace this node
// declares. The answer is the first segment, except under the node's own id,
// where a second namespace segment is a connection name and belongs to it. ""
// for a bare id. Not NamespaceOf, which answers store identity instead.
func OwnerNamespaceOf(id, nodeID string) string {
	first, rest, ok := SplitID(id)
	if !ok {
		return ""
	}
	if first != nodeID || nodeID == "" {
		return first
	}
	second := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		second = rest[:i]
	}
	if second == "" || IsTileSegment(second) {
		return first
	}
	return first + "/" + second
}

// SharedOwner is the namespace both a and b route to at the node nodeID, or ""
// where they part. A verb naming both is the shared owner's whole to answer,
// so it runs at the nearest node that sees both ends.
func SharedOwner(a, b, nodeID string) string {
	ns := OwnerNamespaceOf(a, nodeID)
	if ns == "" || ns != OwnerNamespaceOf(b, nodeID) {
		return ""
	}
	return ns
}

// ChainedThrough reports whether a qualified id is served through the
// namespace chain ns: a prefix on a segment boundary, so "n1/laptop" is
// chained through neither "n1x" nor itself. Whoever asks which ids a source
// answers for needs the whole chain, which OwnerNamespaceOf does not give.
func ChainedThrough(id, ns string) bool {
	return ns != "" && strings.HasPrefix(id, ns+"/")
}
