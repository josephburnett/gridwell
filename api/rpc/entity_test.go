package rpc

import (
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// A request names its entity by its first id field set: SetFraming's door
// tile, or the root grid when there is none.
func TestTheEntityIsTheFirstIDFieldSet(t *testing.T) {
	cases := []struct {
		req  any
		want string
	}{
		{&pb.SetFramingRequest{TileId: "a/t1", RootGridId: "a/g1"}, "a/t1"},
		{&pb.SetFramingRequest{RootGridId: "a/g1"}, "a/g1"},
		{&pb.MoveTileRequest{TileId: "a/t1", GridId: "a/g1"}, "a/t1"},
		{&pb.CreateTileRequest{GridId: "a/g1"}, "a/g1"},
		{&pb.SubscribeRequest{}, ""},
		{"not a message", ""},
	}
	for _, c := range cases {
		if got := EntityOf(c.req); got != c.want {
			t.Errorf("EntityOf(%T %v) = %q, want %q", c.req, c.req, got, c.want)
		}
	}
}
