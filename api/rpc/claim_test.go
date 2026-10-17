package rpc

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

func TestUnclaimedWriteNamesTheMissingClaim(t *testing.T) {
	yes, no := true, false
	row := &pb.Tile{Id: "u1/7", GridId: "u1/2", Version: 3, W: 1, H: 1}
	for _, c := range []struct {
		name string
		req  proto.Message
		want string // a word of the reason, "" for none
	}{
		{"move without claim", &pb.PlaceTileRequest{TileId: "u1/7"}, "placement"},
		{"move with claim", &pb.PlaceTileRequest{TileId: "u1/7", Claim: PlacementOf(row)}, ""},
		{"delete without claim", &pb.DeleteTileRequest{TileId: "u1/7", ClaimVersion: 3}, "delete"},
		{"delete with claim", DeleteOf(row), ""},
		{"freeze without claim", &pb.SetTileRequest{TileId: "u1/7", UrlFrozen: &yes}, "claim_frozen"},
		{"freeze with claim", &pb.SetTileRequest{TileId: "u1/7", UrlFrozen: &yes, ClaimFrozen: &no}, ""},
		{"capture claims nothing", &pb.SetTileRequest{TileId: "u1/7", Preview: []byte{1}}, ""},
		{"create claims nothing", &pb.CreateTileRequest{GridId: "u1/2"}, ""},
		{"a select claims nothing", &pb.SetSelectionRequest{GridId: "u1/2", Add: []string{"u1/7"}}, ""},
	} {
		got := UnclaimedWrite(c.req)
		if (got == "") != (c.want == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: UnclaimedWrite = %q, want a reason naming %q", c.name, got, c.want)
		}
	}
}

// Every request with a claim field is one UnclaimedWrite answers for, so a
// verb that gains a claim cannot be sent without it by omission.
func TestUnclaimedWriteKnowsEveryClaim(t *testing.T) {
	owned := map[protoreflect.FullName]bool{
		"gridwell.v1.PlaceTileRequest":  true,
		"gridwell.v1.DeleteTileRequest": true,
		"gridwell.v1.SetTileRequest":    true,
	}
	msgs := pb.File_gridwell_v1_data_proto.Messages()
	for i := 0; i < msgs.Len(); i++ {
		m := msgs.Get(i)
		claims := false
		fields := m.Fields()
		for j := 0; j < fields.Len(); j++ {
			if strings.HasPrefix(string(fields.Get(j).Name()), "claim") {
				claims = true
			}
		}
		if claims != owned[m.FullName()] {
			t.Errorf("%s: carries a claim field = %v, UnclaimedWrite answers for it = %v", m.FullName(), claims, owned[m.FullName()])
		}
	}
}
