package rpc

import (
	"net/url"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/idshape"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// keySamples are the keys a plugin might name a tile by: paths with slashes,
// the empty key, unicode, raw bytes, and the characters a URL or a base64
// alphabet would otherwise argue about.
var keySamples = []string{
	"",
	"/",
	"/home/joe/x",
	"/home/joe/a b/c?d#e",
	"todo:4821",
	"https://gitlab.example/x/y/-/issues/7",
	"a+b/c=d",
	"~already-tilde",
	"12345",
	"\x00\xff\xfe binary \n\t",
	"ünïcødé — ключ",
	strings.Repeat("deep/", 40) + "leaf",
}

// A key-form segment round-trips any byte string, and it is chain-safe and
// URL-path-safe: it never contains a '/' (which would forge a hop) and it
// survives a path round-trip unescaped (base64url plus '~' are all
// unreserved), so an id built from a key is one segment everywhere.
func TestKeyTileIDRoundTrip(t *testing.T) {
	for _, key := range keySamples {
		seg := KeyTileID(key)
		if strings.Contains(seg, "/") {
			t.Fatalf("KeyTileID(%q) = %q contains '/': it would forge a hop", key, seg)
		}
		if esc := url.PathEscape(seg); esc != seg {
			t.Fatalf("KeyTileID(%q) = %q is not URL-path-safe (escapes to %q)", key, seg, esc)
		}
		got, ok := TileKey(seg)
		if !ok || got != key {
			t.Fatalf("TileKey(KeyTileID(%q)) = %q, %v; want %q, true", key, got, ok, key)
		}
	}
}

// The example the design names, spelled out so the encoding cannot drift.
func TestKeyTileIDEncoding(t *testing.T) {
	if got := KeyTileID("/home/joe/x"); got != "~L2hvbWUvam9lL3g" {
		t.Fatalf("KeyTileID(\"/home/joe/x\") = %q, want %q", got, "~L2hvbWUvam9lL3g")
	}
}

// The three shapes are disjoint and the classification is total. A row id
// stays a row id, a short id stays a namespace, and only a well-formed key
// form reads as a key — so a segment can never be two things at once.
func TestShapeOfDisjoint(t *testing.T) {
	rows := []string{"0", "1", "14", "9007199254740993"}
	namespaces := []string{"k3x9m2q", "ssh4321", "home", "a1b2c3d",
		// A '~' segment whose payload is not canonical base64url is not a
		// key form; a reader with no other information must treat it as a
		// namespace segment rather than invent a tile.
		"~not base64", "~AB==", "~/", "~AC"}
	for _, s := range rows {
		if got := ShapeOf(s); got != ShapeRow {
			t.Fatalf("ShapeOf(%q) = %q, want %q", s, got, ShapeRow)
		}
		if _, ok := TileKey(s); ok {
			t.Fatalf("TileKey(%q) succeeded: a row id is not a key form", s)
		}
	}
	for _, s := range namespaces {
		if got := ShapeOf(s); got != ShapeNamespace {
			t.Fatalf("ShapeOf(%q) = %q, want %q", s, got, ShapeNamespace)
		}
		if IsTileSegment(s) {
			t.Fatalf("IsTileSegment(%q) is true: a namespace segment is not a tile", s)
		}
	}
	for _, key := range keySamples {
		seg := KeyTileID(key)
		if got := ShapeOf(seg); got != ShapeKey {
			t.Fatalf("ShapeOf(%q) = %q, want %q", seg, got, ShapeKey)
		}
		if !IsTileSegment(seg) {
			t.Fatalf("IsTileSegment(%q) is false", seg)
		}
	}
	// TileKey succeeds exactly when ShapeOf says ShapeKey.
	for _, s := range append(append([]string{}, rows...), namespaces...) {
		_, ok := TileKey(s)
		if ok != (ShapeOf(s) == ShapeKey) {
			t.Fatalf("TileKey/ShapeOf disagree on %q", s)
		}
	}
	// The minted namespace shape and the two tile shapes cannot collide:
	// idshape.NewShortID leads with a letter, so it is neither.
	for i := 0; i < 200; i++ {
		if got := ShapeOf(idshape.NewShortID()); got != ShapeNamespace {
			t.Fatalf("a minted short id classified as %q", got)
		}
	}
}

// The empty key is a key like any other, so the codec stays total: "~" is
// its segment and decodes back to "". Nothing else spells it.
func TestKeyTileIDEmpty(t *testing.T) {
	seg := KeyTileID("")
	if seg != "~" {
		t.Fatalf("KeyTileID(\"\") = %q, want %q", seg, "~")
	}
	if got, ok := TileKey(seg); !ok || got != "" {
		t.Fatalf("TileKey(%q) = %q, %v; want \"\", true", seg, got, ok)
	}
	// It classifies as a key, not a namespace: an empty key is degenerate,
	// but it is still a tile.
	if ShapeOf(seg) != ShapeKey {
		t.Fatalf("ShapeOf(%q) = %q, want %q", seg, ShapeOf(seg), ShapeKey)
	}
}

// The id codec is shape-blind: qualify, peel, and split treat a key-form
// segment exactly as they treat a row id. This is the whole reason a key
// form is a SEGMENT and not a new kind of id — nothing above it has to
// learn about it.
func TestQualifyCodecIgnoresSegmentShape(t *testing.T) {
	key := KeyTileID("/home/joe/x")
	cases := []struct{ ns, local string }{
		{"k3x9m2q", key},
		{"ssh4321/remote9", key},
		{"k3x9m2q", "14"},
		{"ssh4321/remote9", "14"},
	}
	for _, c := range cases {
		id := QualifyID(c.ns, c.local)
		if got := NamespaceOf(id); got != c.ns {
			t.Fatalf("NamespaceOf(%q) = %q, want %q", id, got, c.ns)
		}
		if got := LocalOf(id); got != c.local {
			t.Fatalf("LocalOf(%q) = %q, want %q", id, got, c.local)
		}
		first, rest, ok := SplitID(id)
		if !ok {
			t.Fatalf("SplitID(%q) not ok", id)
		}
		wantFirst, wantRest, _ := strings.Cut(c.ns+"/"+c.local, "/")
		if first != wantFirst || rest != wantRest {
			t.Fatalf("SplitID(%q) = %q, %q; want %q, %q", id, first, rest, wantFirst, wantRest)
		}
		if got := UUIDOf(id); got != wantFirst {
			t.Fatalf("UUIDOf(%q) = %q, want %q", id, got, wantFirst)
		}
	}
	// A key form is chain-safe under LocalOf even when the key contains
	// slashes: it is base64url, so the last '/' of a qualified id is
	// always the namespace boundary.
	deep := QualifyID("k3x9m2q", KeyTileID("/a/b/c/d"))
	if LocalOf(deep) != KeyTileID("/a/b/c/d") || NamespaceOf(deep) != "k3x9m2q" {
		t.Fatalf("a slashy key leaked into the chain: %q", deep)
	}
}

// The transit rule prepends one segment and leaves the segment alone,
// whatever its shape — the seam a mounted node's key-form tile crosses.
func TestTransitQualifyKeyForm(t *testing.T) {
	key := KeyTileID("/home/joe/x")
	in := []*pb.Tile{{
		Id:           "far/" + key,
		GridId:       "far/1",
		ChildGridId:  "far/" + key,
		LinkTargetId: "far/" + key,
	}}
	out := TransitQualifyTiles("hop", in)
	want := "hop/far/" + key
	if out[0].Id != want || out[0].ChildGridId != want || out[0].LinkTargetId != want {
		t.Fatalf("key-form ids not prepended one segment: %+v", out[0])
	}
	if out[0].GridId != "hop/far/1" {
		t.Fatalf("row id and key form must chain identically: %+v", out[0])
	}
	// And it peels back to the same segment.
	if LocalOf(out[0].Id) != key {
		t.Fatalf("LocalOf(%q) = %q, want %q", out[0].Id, LocalOf(out[0].Id), key)
	}
}

// idshape.ValidateSegment and ShapeOf must agree on what a namespace segment
// is: idshape cannot import this package (its tests import idshape), so the
// two spellings are pinned here instead of shared. A shape that ShapeOf calls
// a tile must be refused as a namespace, or an id would be ambiguous in a URL
// path with no way to tell which reading was meant.
func TestValidateSegmentRefusesEveryTileShape(t *testing.T) {
	for _, seg := range []string{"14", "0", KeyTileID("/home/joe"), KeyTileID(""), "~"} {
		if IsTileSegment(seg) == (idshape.ValidateSegment("id", seg) == nil) {
			t.Errorf("%q: IsTileSegment=%v but ValidateSegment=%v — the two classifiers disagree",
				seg, IsTileSegment(seg), idshape.ValidateSegment("id", seg))
		}
	}
	// A namespace segment stays legal.
	if err := idshape.ValidateSegment("id", "ngkwanw"); err != nil {
		t.Errorf("a plain namespace segment was refused: %v", err)
	}
}
