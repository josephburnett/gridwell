// This file holds the conversions that are NOT a mechanical message
// mirror. The mirrors — every record and every request/response whose Go
// shape is the proto message field for field — are GENERATED into
// wire_gen.go from api/gridwell/v1/data.proto, which is the one
// description of them (docs/simplify-plan.md S6). What is left here is
// where the Go shape deliberately differs from the wire shape and a
// human has to say how:
//
//   - Event: the proto's oneof payload becomes a discriminator string
//     plus optional pointers on the Go side.
//   - The per-kind CREATE helpers: the wire has one CreateTile carrying a
//     Tile whose kind selects the meaningful fields; these map each
//     primitive's typed create onto it.
//   - The per-kind SET helpers: likewise for the single SetTile verb.
package rpc

import (
	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// EventToProto converts an Event to its wire form. The wire Event uses
// a oneof payload; the Go Event uses a discriminator string + optional
// pointer fields — only one is non-nil at a time, matching oneof's
// semantics on the wire.
func EventToProto(e Event) *pb.Event {
	switch e.Kind {
	case EventGridChanged:
		return &pb.Event{Payload: &pb.Event_GridChanged{GridChanged: GridChangedToProto(e.GridChanged)}}
	case EventTileChanged:
		return &pb.Event{Payload: &pb.Event_TileChanged{TileChanged: TileChangedToProto(e.TileChanged)}}
	case EventTileRemoved:
		return &pb.Event{Payload: &pb.Event_TileRemoved{TileRemoved: TileRemovedToProto(e.TileRemoved)}}
	case EventPluginHealth:
		return &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: PluginHealthToProto(e.PluginHealth)}}
	}
	return &pb.Event{}
}

// EventFromProto converts a wire Event back.
func EventFromProto(e *pb.Event) Event {
	if e == nil {
		return Event{}
	}
	switch p := e.Payload.(type) {
	case *pb.Event_GridChanged:
		return Event{Kind: EventGridChanged, GridChanged: GridChangedFromProto(p.GridChanged)}
	case *pb.Event_TileChanged:
		return Event{Kind: EventTileChanged, TileChanged: TileChangedFromProto(p.TileChanged)}
	case *pb.Event_TileRemoved:
		return Event{Kind: EventTileRemoved, TileRemoved: TileRemovedFromProto(p.TileRemoved)}
	case *pb.Event_PluginHealth:
		return Event{Kind: EventPluginHealth, PluginHealth: PluginHealthFromProto(p.PluginHealth)}
	}
	return Event{}
}

// Create converters. The wire has a single CreateTile carrying a Tile whose
// Kind selects the meaningful fields; these per-kind helpers are the one place
// each primitive's create maps onto that unified shape. The Client exposes
// typed sugar over them; the plugin fans CreateTile back out by Kind.

func CreateWellToProto(r *CreateWellRequest) *pb.CreateTileRequest {
	return &pb.CreateTileRequest{GridId: r.GridID,
		Tile: &pb.Tile{Kind: KindWell, X: r.X, Y: r.Y, W: r.W, H: r.H, ChildGridId: r.ChildGridID, AltText: r.Label,
			ViewCx: r.Cx, ViewCy: r.Cy, ViewZoom: r.Zoom}}
}

func CreateTextToProto(r *CreateTextRequest) *pb.CreateTileRequest {
	return &pb.CreateTileRequest{GridId: r.GridID,
		Tile: &pb.Tile{Kind: KindText, X: r.X, Y: r.Y, W: r.W, H: r.H}}
}
func CreateURLToProto(r *CreateURLRequest) *pb.CreateTileRequest {
	return &pb.CreateTileRequest{GridId: r.GridID,
		Tile: &pb.Tile{Kind: KindURL, X: r.X, Y: r.Y, W: r.W, H: r.H, UrlString: r.URL}}
}
func CreateShellToProto(r *CreateShellRequest) *pb.CreateTileRequest {
	return &pb.CreateTileRequest{GridId: r.GridID,
		Tile: &pb.Tile{Kind: KindShell, X: r.X, Y: r.Y, W: r.W, H: r.H}}
}
func CreatePaneToProto(r *CreatePaneRequest) *pb.CreateTileRequest {
	return &pb.CreateTileRequest{GridId: r.GridID,
		Tile: &pb.Tile{Kind: KindPane, X: r.X, Y: r.Y, W: r.W, H: r.H, AltText: r.Label}}
}

// CreateLeafLinkToProto builds the CreateTile for a LEAF LINK: any leaf kind
// plus a qualified link_target_id. The one create for all four linkable leaf
// kinds — the destination plugin stores the reference verbatim and no content
// rides along (bytes live in the target).
func CreateLeafLinkToProto(r *CreateLeafLinkRequest) *pb.CreateTileRequest {
	return &pb.CreateTileRequest{GridId: r.GridID,
		Tile: &pb.Tile{Kind: r.Kind, X: r.X, Y: r.Y, W: r.W, H: r.H,
			LinkTargetId: r.LinkTargetID, AltText: r.Label}}
}

// SetTile converters. The wire has a single SetTile dispatched on the target
// tile's Kind; these helpers map each kind's framing/preview writeback onto it.

func SetTextViewToProto(r *SetTextViewRequest) *pb.SetTileRequest {
	return &pb.SetTileRequest{TileId: r.TileID,
		Tile: &pb.Tile{Kind: KindText, TextX: r.TextX, TextY: r.TextY, TextW: r.TextW, TextH: r.TextH, TextMode: r.TextMode}}
}
func SetShellPreviewToProto(r *SetShellPreviewRequest) *pb.SetTileRequest {
	return &pb.SetTileRequest{TileId: r.TileID,
		Tile: &pb.Tile{Kind: KindShell}, Preview: r.JPEG}
}
func SetURLStateToProto(r *SetURLStateRequest) *pb.SetTileRequest {
	return &pb.SetTileRequest{TileId: r.TileID,
		Tile: &pb.Tile{Kind: KindURL, UrlString: r.URL, AltText: r.Title, UrlHistory: r.History}, Preview: r.JPEG}
}
