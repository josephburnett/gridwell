package rpc

// Beacon bodies for the unload flush. An ordinary RPC dies with the page, so a
// quit inside the settle window loses the last write; these hand
// navigator.sendBeacon the raw (path, body) pair of the Connect request the
// ordinary call would have sent, pinned to the real handlers by a seam test in
// internal/server.
//
// The framing and url-state beacons carry no version claim, so nothing they
// send is refused for losing a race the page cannot re-run. The WriteContent
// beacon claims the save basis, as every content write must, so a genuine
// concurrent edit is lost visibly on the next load rather than silently
// overwriting the other.

import (
	"encoding/binary"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
)

// BeaconJSONType is the content type every unary beacon body carries.
const BeaconJSONType = "application/json"

// BeaconStreamType is the content type of the WriteContent beacon's enveloped
// body.
const BeaconStreamType = "application/connect+json"

func beacon(procedure string, m proto.Message) (path string, body []byte) {
	b, err := protojson.Marshal(m)
	if err != nil {
		return "", nil
	}
	return procedure, b
}

// SetTileBeacon drops the preview jpeg, which would exhaust the roughly 64 KB
// beacon budget; the store skips an empty preview, so the tile keeps its old
// face and still gets the address and title the live page navigated to.
func SetTileBeacon(req *pb.SetTileRequest) (path string, body []byte) {
	r := proto.Clone(req).(*pb.SetTileRequest)
	r.Preview = nil
	return beacon(gridwellv1connect.GridwellSetTileProcedure, r)
}

// SetFramingBeacon is the beacon form of Client.SetFraming: the one framing
// beacon, doorway tile and root grid alike.
func SetFramingBeacon(req *pb.SetFramingRequest) (path string, body []byte) {
	return beacon(gridwellv1connect.GridwellSetFramingProcedure, req)
}

// DeleteTileBeacon: the only delete that parks and so reaches the unload drain
// is the ephemeral visit's off-grid cleanup.
func DeleteTileBeacon(req *pb.DeleteTileRequest) (path string, body []byte) {
	return beacon(gridwellv1connect.GridwellDeleteTileProcedure, req)
}

// WriteContentBeacon is the one streaming beacon: a complete WriteContent is a
// single Connect envelope (1 flags byte, 4-byte big-endian length, payload).
// Returns a nil body when the data will not fit the beacon budget, so the
// caller falls back to the ordinary async post rather than beaconing something
// the browser truncates.
func WriteContentBeacon(tileID string, claim ContentBasis, data []byte) (path string, body []byte) {
	const beaconBudget = 60 * 1024
	m, err := protojson.Marshal(&pb.WriteContentRequest{TileId: tileID, Version: claim.Version, ContentStamp: claim.Stamp, Data: data})
	if err != nil || len(m) > beaconBudget {
		return "", nil
	}
	env := make([]byte, 5+len(m))
	binary.BigEndian.PutUint32(env[1:5], uint32(len(m)))
	copy(env[5:], m)
	return gridwellv1connect.GridwellWriteContentProcedure, env
}
