package rpc

import (
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The one selector grammar: id: is exact, everything else is
// plugin-interpreted text. New selectors extend the parser, never a
// plugin.
func TestParseFindQuery(t *testing.T) {
	cases := []struct {
		in       string
		id, text string
	}{
		{"id:aabb/7", "aabb/7", ""},
		{"  id: aabb/7 ", "aabb/7", ""},
		{"gopher meeting", "", "gopher meeting"},
		{"identity crisis", "", "identity crisis"}, // no accidental prefix match
		{"", "", ""},
	}
	for _, c := range cases {
		got := ParseFindQuery(c.in)
		if got.ID != c.id || got.Text != c.text {
			t.Errorf("ParseFindQuery(%q) = %+v, want id=%q text=%q", c.in, got, c.id, c.text)
		}
	}
}

// A filter that cannot mean anything is refused with the reason, never read
// as no filter.
func TestCheckFind(t *testing.T) {
	for _, c := range []struct {
		name string
		req  *pb.FindRequest
		ok   bool
	}{
		{"plain", &pb.FindRequest{Query: "x"}, true},
		{"grid recursive", &pb.FindRequest{GridId: "7", Recurse: true}, true},
		{"recurse without grid", &pb.FindRequest{Recurse: true}, false},
		{"open ends", &pb.FindRequest{Created: &pb.TimeRange{From: 5}, Modified: &pb.TimeRange{To: 5}}, true},
		{"one instant", &pb.FindRequest{ContentChanged: &pb.TimeRange{From: 5, To: 5}}, true},
		{"backwards", &pb.FindRequest{ContentChanged: &pb.TimeRange{From: 6, To: 5}}, false},
		{"negative", &pb.FindRequest{Modified: &pb.TimeRange{From: -1}}, false},
		{"a kind", &pb.FindRequest{Kind: KindWorkspace}, true},
		{"an unknown kind", &pb.FindRequest{Kind: "panes"}, false},
	} {
		if err := CheckFind(c.req); (err == nil) != c.ok {
			t.Errorf("%s: CheckFind = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// A hop peels the grid and an id: query, consumes the scope, and carries
// every filter through unchanged.
func TestPeelRequestCarriesFindFilters(t *testing.T) {
	hop := Hop{Seg: "hop", Via: "hop", Transit: true}
	in := &pb.FindRequest{Query: "id:hop/far/7", Scope: "hop/far/1", GridId: "hop/far/1", Recurse: true,
		Limit: 3, Selected: true, Created: &pb.TimeRange{From: 1}, Modified: &pb.TimeRange{To: 2},
		ContentChanged: &pb.TimeRange{From: 3, To: 4}, Kind: KindText}
	want := &pb.FindRequest{Query: "id:far/7", GridId: "far/1", Recurse: true,
		Limit: 3, Selected: true, Created: &pb.TimeRange{From: 1}, Modified: &pb.TimeRange{To: 2},
		ContentChanged: &pb.TimeRange{From: 3, To: 4}, Kind: KindText}
	if got := PeelRequest(hop, in); !proto.Equal(got, want) {
		t.Errorf("peeled = %v, want %v", got, want)
	}
}

// A kind narrows like any filter, so an empty query with a kind alone lists;
// it needs no row, since an entry with none has its kind.
func TestAKindIsAFilterThatNeedsNoRow(t *testing.T) {
	req := &pb.FindRequest{Kind: KindWorkspace}
	if !FindFiltered(req) || FindNeedsRow(req) {
		t.Errorf("kind alone: filtered %v, needs a row %v; want filtered, no row", FindFiltered(req), FindNeedsRow(req))
	}
}
