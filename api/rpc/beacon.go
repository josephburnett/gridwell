package rpc

// Beacon bodies for the UNLOAD framing flush (framing-audit decision
// 2026-08-13): a quit or reload inside the settle window used to lose the
// last pan — the flush's RPC was fire-and-forget and the page died first.
// navigator.sendBeacon survives the page, but it needs a raw (path, body)
// pair; these helpers produce the EXACT Connect-unary wire form the
// ordinary client call would send (same *ToProto converters — one request
// builder, two transports), as proto-JSON, which the Connect handler
// accepts on its unary POSTs. Two content-shaped additions (2026-08-14,
// the transport-loss class): the url-state beacon (unary, minus the jpeg
// — the beacon budget is ~64 KB and the store skips empty fields, so the
// previous frozen face survives) and the WriteContent beacon, which
// hand-builds the ONE Connect client-streaming envelope (a single
// enveloped message; the request stream ends with the body) so unsaved
// text survives a tab close. That envelope is pinned to the real Connect
// handler by a seam test in internal/server — if the protocol framing
// ever shifts, the pin fails, not the user's last paragraph.
// Known tradeoff: a beacon cannot retry a version conflict (the page is
// gone) — a version bump racing the final instant of a session can still
// cost that one write; the settle persister covers every earlier one.

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

// SetWellViewBeacon is the beacon form of Client.SetWellView.
func SetWellViewBeacon(req *SetWellViewRequest) (path string, body []byte) {
	return beacon(gridwellv1connect.GridwellSetTileProcedure, SetWellViewToProto(req))
}

// SetTextViewBeacon is the beacon form of Client.SetTextView.
func SetTextViewBeacon(req *SetTextViewRequest) (path string, body []byte) {
	return beacon(gridwellv1connect.GridwellSetTileProcedure, SetTextViewToProto(req))
}

// SetRootViewBeacon is the beacon form of Client.SetRootView.
func SetRootViewBeacon(req *SetRootViewRequest) (path string, body []byte) {
	return beacon(gridwellv1connect.GridwellSetRootViewProcedure, SetRootViewToProto(req))
}

// SetURLStateBeacon is the beacon form of Client.SetURLState WITHOUT the
// preview jpeg: the address, title, and history a live page navigated to
// must survive a tab close (audit #2, 2026-08-14 — they used to persist
// exactly once, at a teardown whose IPC reply never arrives during
// unload). The jpeg stays empty — the store skips empty fields, so the
// tile keeps its previous frozen face rather than losing the trail.
func SetURLStateBeacon(req *SetURLStateRequest) (path string, body []byte) {
	r := *req
	r.JPEG = nil
	return beacon(gridwellv1connect.GridwellSetTileProcedure, SetURLStateToProto(&r))
}

// WriteContentBeacon is the beacon form of Client.WriteContent — the one
// STREAMING beacon. The Connect client-streaming request body is a
// sequence of enveloped messages (1 flags byte, 4-byte big-endian length,
// payload), and a complete WriteContent is ONE message (tile_id + version
// + data — the same single-message shape writeAllContent sends), so the
// whole request is exactly one envelope. Returns nil body when data won't
// fit a beacon (the ~64 KB queue budget) — the caller falls back to the
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
