package rpc

// Beacon bodies for the UNLOAD framing flush (framing-audit decision
// 2026-08-13): a quit or reload inside the settle window used to lose the
// last pan — the flush's RPC was fire-and-forget and the page died first.
// navigator.sendBeacon survives the page, but it needs a raw (path, body)
// pair; these helpers produce the EXACT Connect-unary wire form the
// ordinary client call would send (same *ToProto converters — one request
// builder, two transports), as proto-JSON, which the Connect handler
// accepts on its unary POSTs. Framing writes only: they are unary and
// idempotent-at-rest; content writes stream and stay with the save queue.
// Known tradeoff: a beacon cannot retry a version conflict (the page is
// gone) — a version bump racing the final instant of a session can still
// cost that one write; the settle persister covers every earlier one.

import (
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
)

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
