package rpc

import (
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The one selector grammar: id: is exact, everything else is
// plugin-interpreted text. New selectors extend the parser, never a
// plugin.
func TestParseSearchQuery(t *testing.T) {
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
		got := ParseSearchQuery(c.in)
		if got.ID != c.id || got.Text != c.text {
			t.Errorf("ParseSearchQuery(%q) = %+v, want id=%q text=%q", c.in, got, c.id, c.text)
		}
	}
}

// A filter that cannot mean anything is refused with the reason, never read
// as no filter.
func TestCheckSearch(t *testing.T) {
	for _, c := range []struct {
		name string
		req  *pb.SearchRequest
		ok   bool
	}{
		{"plain", &pb.SearchRequest{Query: "x"}, true},
		{"grid recursive", &pb.SearchRequest{GridId: "7", Recurse: true}, true},
		{"recurse without grid", &pb.SearchRequest{Recurse: true}, false},
		{"open ends", &pb.SearchRequest{Created: &pb.TimeRange{From: 5}, Modified: &pb.TimeRange{To: 5}}, true},
		{"one instant", &pb.SearchRequest{ContentChanged: &pb.TimeRange{From: 5, To: 5}}, true},
		{"backwards", &pb.SearchRequest{ContentChanged: &pb.TimeRange{From: 6, To: 5}}, false},
		{"negative", &pb.SearchRequest{Modified: &pb.TimeRange{From: -1}}, false},
	} {
		if err := CheckSearch(c.req); (err == nil) != c.ok {
			t.Errorf("%s: CheckSearch = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// A hop peels the grid and an id: query, consumes the scope, and carries
// every filter through unchanged.
func TestPeelRequestCarriesSearchFilters(t *testing.T) {
	hop := Hop{Seg: "hop", Via: "hop", Transit: true}
	in := &pb.SearchRequest{Query: "id:hop/far/7", Scope: "hop/far/1", GridId: "hop/far/1", Recurse: true,
		Limit: 3, Selected: true, Created: &pb.TimeRange{From: 1}, Modified: &pb.TimeRange{To: 2},
		ContentChanged: &pb.TimeRange{From: 3, To: 4}}
	want := &pb.SearchRequest{Query: "id:far/7", GridId: "far/1", Recurse: true,
		Limit: 3, Selected: true, Created: &pb.TimeRange{From: 1}, Modified: &pb.TimeRange{To: 2},
		ContentChanged: &pb.TimeRange{From: 3, To: 4}}
	if got := PeelRequest(hop, in); !proto.Equal(got, want) {
		t.Errorf("peeled = %v, want %v", got, want)
	}
}
