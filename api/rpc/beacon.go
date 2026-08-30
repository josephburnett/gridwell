package rpc

// Beacon bodies for the unload flush. An ordinary RPC is fire-and-forget
// and the page dies first, so a quit or reload inside the settle window
// loses the last write. navigator.sendBeacon survives the page but needs a
// raw (path, body) pair; these helpers produce the exact Connect-unary wire
// form the ordinary client call would send, from the same *ToProto
// converters, as proto-JSON, which the Connect handler accepts on its
// unary POSTs.
//
// The WriteContent beacon hand-builds the one Connect client-streaming
// envelope (a single enveloped message; the request stream ends with the
// body) so unsaved text survives a tab close. That envelope is pinned to
// the real Connect handler by a seam test in internal/server, so a shift in
// the protocol framing fails the pin rather than the user's last paragraph.
//
// The framing and url-state beacons carry no version claim, so nothing they
// send can be refused for losing a race the page is no longer around to
// re-run. The WriteContent beacon claims the save basis, as every content
// write must; a conflict there is a genuine concurrent edit that the beacon
// cannot resolve, so that one write is lost visibly on the next load rather
// than silently overwriting the other edit.

import (
	"encoding/binary"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
)

// BeaconJSONType is the content type every unary beacon body carries.
const BeaconJSONType = "application/json"

// BeaconStreamType is the content type of the WriteContent beacon's
// enveloped body (the Connect streaming protocol over proto-JSON).
const BeaconStreamType = "application/connect+json"

func beacon(procedure string, m proto.Message) (path string, body []byte) {
	b, err := protojson.Marshal(m)
	if err != nil {
		return "", nil
	}
	return procedure, b
}

// SetTextViewBeacon is the beacon form of Client.SetTextView.
func SetTextViewBeacon(req *SetTextViewRequest) (path string, body []byte) {
	return beacon(gridwellv1connect.GridwellSetTileProcedure, SetTextViewToProto(req))
}

// SetFramingBeacon is the beacon form of Client.SetFraming: the one framing
// beacon, doorway tile and root grid alike.
func SetFramingBeacon(req *SetFramingRequest) (path string, body []byte) {
	return beacon(gridwellv1connect.GridwellSetFramingProcedure, SetFramingToProto(req))
}

// SetURLStateBeacon is the beacon form of Client.SetURLState without the
// preview jpeg, so the address, title, and history a live page navigated to
// survive a tab close. The jpeg stays empty: the store skips empty fields,
// so the tile keeps its previous frozen face rather than losing the trail.
// The beacon queue budget is about 64 KB, which a jpeg would exhaust.
func SetURLStateBeacon(req *SetURLStateRequest) (path string, body []byte) {
	r := *req
	r.JPEG = nil
	return beacon(gridwellv1connect.GridwellSetTileProcedure, SetURLStateToProto(&r))
}

// WriteContentBeacon is the beacon form of Client.WriteContent, and the one
// streaming beacon. The Connect client-streaming request body is a sequence
// of enveloped messages (1 flags byte, 4-byte big-endian length, payload),
// and a complete WriteContent is one message (tile_id, version, data — the
// same single-message shape writeAllContent sends), so the whole request is
// exactly one envelope. Returns a nil body when data will not fit the
// roughly 64 KB beacon queue budget, so the caller falls back to the
// ordinary async post rather than beaconing something the browser will
// refuse or truncate.
func WriteContentBeacon(tileID string, version int64, data []byte) (path string, body []byte) {
	const beaconBudget = 60 * 1024
	m, err := protojson.Marshal(&pb.WriteContentRequest{TileId: tileID, Version: version, Data: data})
	if err != nil || len(m) > beaconBudget {
		return "", nil
	}
	env := make([]byte, 5+len(m))
	binary.BigEndian.PutUint32(env[1:5], uint32(len(m)))
	copy(env[5:], m)
	return gridwellv1connect.GridwellWriteContentProcedure, env
}
