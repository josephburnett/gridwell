package rpc

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// SegmentShape is what one segment of a chained id is: a namespace (a node
// id or a connection name), a store row id, or a file's path carried in the
// id itself so a file the node minted no row for is still nameable.
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

// keyTileEncoding spells a key one way only: TileKey also refuses what the
// decoder would skip, a newline or a carriage return. Its alphabet excludes "/", which keeps a key chain-safe however many slashes
// the key contains, and needs no escaping in a URL path.
var keyTileEncoding = base64.RawURLEncoding

// KeyTileID renders a key, a file's path, as a tile segment. Any byte string is a legal
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
	if err != nil || keyTileEncoding.EncodeToString(b) != rest {
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
// cannot disagree about whether a link names a namespace this node
// declares. The answer is the first segment, except under the node's own id,
// where a second namespace segment is a connection name and belongs to it,
// and a key form is a path the node's files own (FilesOwner). "" for a bare
// id. Not NamespaceOf, which answers store identity instead.
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
	switch ShapeOf(second) {
	case ShapeKey:
		return FilesOwner(first)
	case ShapeRow:
		return first
	}
	if second == "" {
		return first
	}
	return first + "/" + second
}

// FilesOwner is the owner id of the files of the node or chain ns: the key
// form of the empty path under it, "<node>/~". It routes, keys the files'
// health and names their roster row; their ids are qualified with ns itself
// (QualifierOf).
func FilesOwner(ns string) string { return QualifyID(ns, KeyTileID("")) }

// QualifierOf is the chain an owner's answers are qualified with: the owner
// itself, or for the files the node they are under, since a path's id is
// "<node>/~<path>" and never "<node>/~/~<path>".
func QualifierOf(owner string) string {
	if ns, files := strings.CutSuffix(owner, "/"+keyTilePrefix); files {
		return ns
	}
	return owner
}

// IsFilesID reports whether a qualified id names a file or a directory: its
// own segment is the key form of an absolute path, wherever the chain holds
// it.
func IsFilesID(id string) bool {
	if NamespaceOf(id) == "" {
		return false
	}
	p, ok := TileKey(LocalOf(id))
	return ok && absPath(p)
}

// absPath is an absolute path on any host a node runs on: rooted, or a
// Windows volume's.
func absPath(p string) bool {
	return strings.HasPrefix(p, "/") || len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// FilePath is the path a file or directory's id names, ok false for any
// other id: the id is the path, so nothing else carries it.
func FilePath(id string) (string, bool) {
	if !IsFilesID(id) {
		return "", false
	}
	return TileKey(LocalOf(id))
}

// OnDisk reports whether an id, bare or qualified, names a file or a
// directory: its own segment is a path. A Gridwell row's is digits, wherever
// its grid is.
func OnDisk(id string) bool { return ShapeOf(LocalOf(id)) == ShapeKey }

// SameNode reports whether two qualified ids are held by one node, home's
// and the files' alike: the Gridwell layer is one space there.
func SameNode(a, b string) bool { return NamespaceOf(a) == NamespaceOf(b) }

// SourceOf is the source a qualified id belongs to: its namespace chain, or
// for a file or directory the files of that chain (FilesOwner), so home and
// the files are two sources however their ids share a node.
func SourceOf(id string) string {
	if IsFilesID(id) {
		return FilesOwner(NamespaceOf(id))
	}
	return NamespaceOf(id)
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
// namespace chain ns: under it (underChain), and never a path directly under
// a node through that node's home, which is the files'. Whoever asks which
// ids a source answers for needs the whole chain, which OwnerNamespaceOf
// does not give.
func ChainedThrough(id, ns string) bool {
	return underChain(id, ns) && (!IsFilesID(id) || NamespaceOf(id) != ns)
}

// underChain is id spelled under the chain ns: a prefix on a segment
// boundary, so "n1/laptop" is under neither "n1x" nor itself, and for the
// files' owner every path directly under its node.
func underChain(id, ns string) bool {
	if chain := QualifierOf(ns); chain != ns {
		return IsFilesID(id) && NamespaceOf(id) == chain
	}
	return ns != "" && strings.HasPrefix(id, ns+"/")
}
