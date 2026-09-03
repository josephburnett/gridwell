package rpc

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// The three shapes a chain segment can take. An id is a chain of segments
// (see QualifyID); every reader that has to tell "is this a namespace hop or
// a tile?" — the router's peel, the URL grammar — asks ShapeOf, so the shapes
// are decided once and cannot drift apart segment by segment.
//
//   - ShapeNamespace: a plugin or node id, letter-leading by
//     idshape.NewShortID, or a connection name.
//   - ShapeRow: a store row id, decimal digits ("14").
//   - ShapeKey: a plugin key carried in the id itself — "~" followed by the
//     key's unpadded base64url. A tile a plugin has never had to mint a row
//     for is named by what it IS, not by a row that browsing wrote.
//
// The three are disjoint by construction: a row is all digits, a key form
// leads with "~", and a namespace segment is neither.
type SegmentShape string

const (
	ShapeNamespace SegmentShape = "namespace"
	ShapeRow       SegmentShape = "row"
	ShapeKey       SegmentShape = "key"
)

// keyTilePrefix marks a key-form tile segment. "~" is unreserved in a URL
// path (RFC 3986), is not a base64url character, and cannot begin a row id or
// a letter-leading namespace segment, so one byte separates all three shapes.
const keyTilePrefix = "~"

// keyTileEncoding is the key codec: unpadded base64url, decoded strictly so
// that exactly one segment spells any given key. Its alphabet excludes "/",
// which is what keeps a key chain-safe however many slashes the key itself
// contains, and needs no percent-escaping in a URL path.
var keyTileEncoding = base64.RawURLEncoding

// KeyTileID renders a plugin key as a tile segment: "~" plus the key's
// unpadded base64url. Any byte string is a legal key — a filesystem path, a
// URL, an opaque handle — and round-trips through TileKey.
func KeyTileID(key string) string {
	return keyTilePrefix + keyTileEncoding.EncodeToString([]byte(key))
}

// TileKey decodes a key-form tile segment back to its plugin key. ok is false
// for a segment of any other shape, and for a "~" segment whose payload is
// not canonical base64url — the encoding is strict, so TileKey(seg) succeeds
// exactly when ShapeOf(seg) is ShapeKey.
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

// ShapeOf classifies one chain segment. It is total: a segment that is
// neither a row id nor a well-formed key form is a namespace segment, which
// is what a reader with no other information must assume.
func ShapeOf(seg string) SegmentShape {
	if _, err := strconv.ParseInt(seg, 10, 64); err == nil {
		return ShapeRow
	}
	if _, ok := TileKey(seg); ok {
		return ShapeKey
	}
	return ShapeNamespace
}

// IsTileSegment reports whether a segment names a tile inside a namespace
// rather than a hop to another one. It is the URL grammar's split rule and
// the routing peel's shape question, asked in one place.
func IsTileSegment(seg string) bool {
	s := ShapeOf(seg)
	return s == ShapeRow || s == ShapeKey
}

// OwnerNamespaceOf returns the namespace the node whose id is nodeID hands a
// qualified id to: the segments its router peels before anything else sees
// them. It is Server.resolve's peel as a value, so a node and a client ask
// the same question of the same id and cannot answer differently — which is
// how a client can tell that a reference names a namespace this node does not
// declare.
//
// The answer is the first segment, except under the node's own id, where a
// second NAMESPACE segment is a connection name and belongs to it
// ("<node>/<conn>"); a tile segment there is the home store, whose namespace
// is the node itself. Deeper segments are the far node's frame and are never
// this node's to name. "" for a bare, unqualified id, which names no
// namespace at all.
//
// Not NamespaceOf, which is everything before an id's LAST segment — the
// store-identity question ("do these two ids live in the same store"). The
// two differ on every chain longer than one hop, and only this one answers
// "which of my namespaces owns this".
//
// nodeID may be "" for a reader that does not know the node's own id; the
// answer is then always the first segment, which is what a receiver with no
// home of its own can say.
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
