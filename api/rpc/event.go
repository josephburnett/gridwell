package rpc

import (
	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// HealthEvent is the one shape a namespace's reachability takes on the wire.
// An empty uuid means "the namespace this rode in from", which the fan-in
// fills (QualifyEventIDs), so a layer that does not know the uuid the registry
// gave it still reports.
func HealthEvent(uuid string, healthy bool, detail string) *pb.Event {
	return &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{
		PluginUuid: uuid, Healthy: healthy, Detail: detail,
	}}}
}

// DisabledDetail is the reason a disabled source gives wherever a down
// source gives one.
const DisabledDetail = "disabled until the node restarts"

// DisabledEvent is the health a source the user switched off reports for the
// rest of the node's life.
func DisabledEvent(uuid string) *pb.Event {
	ev := HealthEvent(uuid, false, DisabledDetail)
	ev.GetPluginHealth().Disabled = true
	return ev
}

// FramingEvent is the one shape a root grid's framing write takes on the
// wire, from the store and the plugin adapter alike.
func FramingEvent(gridID string, f Framing) *pb.Event {
	return &pb.Event{Payload: &pb.Event_GridFramingChanged{GridFramingChanged: &pb.GridFramingChanged{
		GridId: gridID, ViewCx: f.cx, ViewCy: f.cy, ViewZoom: f.zoom,
	}}}
}

// EventKey names the entity a wire event is about, so internal/eventhub can
// replace an older undelivered event for the same entity and drop no distinct
// one. "" is unkeyable and never coalesces. It is one arm set for every hub:
// a publisher that emits no health event is unaffected by the health arm,
// while two hubs disagreeing would coalesce the same wire event two ways.
func EventKey(ev *pb.Event) string {
	switch p := ev.GetPayload().(type) {
	case *pb.Event_GridChanged:
		return "g/" + p.GridChanged.GetGridId()
	case *pb.Event_GridFramingChanged:
		return "f/" + p.GridFramingChanged.GetGridId()
	case *pb.Event_TileChanged:
		// A content change is keyed apart, so a framing event on the same row
		// behind it cannot replace the news that its bytes moved.
		if p.TileChanged.GetContentChanged() {
			return "c/" + p.TileChanged.GetTile().GetId()
		}
		return "t/" + p.TileChanged.GetTile().GetId()
	case *pb.Event_TileRemoved:
		// Keyed apart from TileChanged, and by grid: a cross-grid move emits
		// TileRemoved for the source then TileChanged for the destination,
		// for the same tile id, and both must reach the consumer.
		return "r/" + p.TileRemoved.GetGridId() + "/" + p.TileRemoved.GetTileId()
	case *pb.Event_PluginHealth:
		return "h/" + p.PluginHealth.GetPluginUuid()
	}
	return ""
}
