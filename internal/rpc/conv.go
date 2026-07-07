// Package rpc owns the Go-side wire types for Gridwell. The persistent
// records and the RPC payloads are defined here as plain Go structs;
// the protobuf messages in api/gridwell/v1/data.proto mirror them for
// the on-the-wire encoding.
//
// This file is the bridge: ToProto/FromProto functions translate every
// Go rpc.* value to and from its generated protobuf counterpart. The
// rule is mechanical — field for field, same semantics, different
// casing. A drift-lint test asserts the proto and Go shapes stay in
// step.
package rpc

import (
	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// PathToProto converts a Path to its wire form.
func PathToProto(p Path) *pb.Path {
	return &pb.Path{WellIds: p.WellIDs}
}

// PathFromProto converts a wire Path back into the Go type. nil maps to
// an empty Path; the server treats it as the root pane.
func PathFromProto(p *pb.Path) Path {
	if p == nil {
		return Path{}
	}
	return Path{WellIDs: p.WellIds}
}

// GridToProto converts a Grid to its wire form.
func GridToProto(g *Grid) *pb.Grid {
	if g == nil {
		return nil
	}
	return &pb.Grid{
		Id:            g.ID,
		ObjectId:      g.ObjectID,
		Version:       g.Version,
		SourceKind:    g.SourceKind,
		SourceId:      g.SourceID,
		Writable:      g.Writable,
		ScratchGridId: g.ScratchGridID,
		ProxyEndpoint: g.ProxyEndpoint,
	}
}

// GridFromProto converts a wire Grid back.
func GridFromProto(g *pb.Grid) *Grid {
	if g == nil {
		return nil
	}
	return &Grid{
		ID:            g.Id,
		ObjectID:      g.ObjectId,
		Version:       g.Version,
		SourceKind:    g.SourceKind,
		SourceID:      g.SourceId,
		Writable:      g.Writable,
		ScratchGridID: g.ScratchGridId,
		ProxyEndpoint: g.ProxyEndpoint,
	}
}

// TileToProto converts a Tile to its wire form.
func TileToProto(t *Tile) *pb.Tile {
	if t == nil {
		return nil
	}
	return &pb.Tile{
		Id:            t.ID,
		ObjectId:      t.ObjectID,
		Version:       t.Version,
		GridId:        t.GridID,
		Kind:          t.Kind,
		X:             t.X,
		Y:             t.Y,
		W:             t.W,
		H:             t.H,
		ViewX:         t.ViewX,
		ViewY:         t.ViewY,
		ViewZoom:      t.ViewZoom,
		ChildGridId:   t.ChildGridID,
		TextX:         t.TextX,
		TextY:         t.TextY,
		TextW:         t.TextW,
		TextH:         t.TextH,
		TextMode:      t.TextMode,
		BlobId:        t.BlobID,
		UrlString:     t.URLString,
		PreviewBlobId: t.PreviewBlobID,
		AltText:       t.AltText,
		Reference:     t.Reference,
		ContentZoom:   t.ContentZoom,
		UrlHistory:    t.URLHistory,
	}
}

// TileFromProto converts a wire Tile back.
func TileFromProto(t *pb.Tile) *Tile {
	if t == nil {
		return nil
	}
	return &Tile{
		ID:            t.Id,
		ObjectID:      t.ObjectId,
		Version:       t.Version,
		GridID:        t.GridId,
		Kind:          t.Kind,
		X:             t.X,
		Y:             t.Y,
		W:             t.W,
		H:             t.H,
		ViewX:         t.ViewX,
		ViewY:         t.ViewY,
		ViewZoom:      t.ViewZoom,
		ChildGridID:   t.ChildGridId,
		TextX:         t.TextX,
		TextY:         t.TextY,
		TextW:         t.TextW,
		TextH:         t.TextH,
		TextMode:      t.TextMode,
		BlobID:        t.BlobId,
		URLString:     t.UrlString,
		PreviewBlobID: t.PreviewBlobId,
		AltText:       t.AltText,
		Reference:     t.Reference,
		ContentZoom:   t.ContentZoom,
		URLHistory:    t.UrlHistory,
	}
}

// TilesToProto converts a slice of Tiles to wire form.
func TilesToProto(ts []Tile) []*pb.Tile {
	if ts == nil {
		return nil
	}
	out := make([]*pb.Tile, len(ts))
	for i := range ts {
		out[i] = TileToProto(&ts[i])
	}
	return out
}

// TilesFromProto converts a slice of wire Tiles back.
func TilesFromProto(ts []*pb.Tile) []Tile {
	if ts == nil {
		return nil
	}
	out := make([]Tile, len(ts))
	for i, t := range ts {
		out[i] = *TileFromProto(t)
	}
	return out
}

// EventToProto converts an Event to its wire form. The wire Event uses
// a oneof payload; the Go Event uses a discriminator string + optional
// pointer fields — only one is non-nil at a time, matching oneof's
// semantics on the wire.
func EventToProto(e Event) *pb.Event {
	switch e.Kind {
	case EventGridChanged:
		return &pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: e.GridChanged.GridID}}}
	case EventTileChanged:
		return &pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: TileToProto(&e.TileChanged.Tile)}}}
	case EventTileRemoved:
		return &pb.Event{Payload: &pb.Event_TileRemoved{TileRemoved: &pb.TileRemoved{GridId: e.TileRemoved.GridID, TileId: e.TileRemoved.TileID}}}
	case EventPluginHealth:
		return &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{
			PluginUuid: e.PluginHealth.PluginUUID,
			Healthy:    e.PluginHealth.Healthy,
			Detail:     e.PluginHealth.Detail,
		}}}
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
		return Event{Kind: EventGridChanged, GridChanged: &GridChanged{GridID: p.GridChanged.GridId}}
	case *pb.Event_TileChanged:
		tile := TileFromProto(p.TileChanged.Tile)
		if tile == nil {
			return Event{Kind: EventTileChanged, TileChanged: &TileChanged{}}
		}
		return Event{Kind: EventTileChanged, TileChanged: &TileChanged{Tile: *tile}}
	case *pb.Event_TileRemoved:
		return Event{Kind: EventTileRemoved, TileRemoved: &TileRemoved{GridID: p.TileRemoved.GridId, TileID: p.TileRemoved.TileId}}
	case *pb.Event_PluginHealth:
		return Event{Kind: EventPluginHealth, PluginHealth: &PluginHealth{
			PluginUUID: p.PluginHealth.PluginUuid,
			Healthy:    p.PluginHealth.Healthy,
			Detail:     p.PluginHealth.Detail,
		}}
	}
	return Event{}
}

// Create converters. The wire has a single CreateTile carrying a Tile whose
// Kind selects the meaningful fields; these per-kind helpers are the one place
// each primitive's create maps onto that unified shape. The Client exposes
// typed sugar over them; the plugin fans CreateTile back out by Kind.

func CreateWellToProto(r *CreateWellRequest) *pb.CreateTileRequest {
	return &pb.CreateTileRequest{Path: PathToProto(r.Path), GridId: r.GridID,
		Tile: &pb.Tile{Kind: KindWell, X: r.X, Y: r.Y, W: r.W, H: r.H, ChildGridId: r.ChildGridID, AltText: r.Label}}
}
func CreateTextToProto(r *CreateTextRequest) *pb.CreateTileRequest {
	return &pb.CreateTileRequest{Path: PathToProto(r.Path), GridId: r.GridID,
		Tile: &pb.Tile{Kind: KindText, X: r.X, Y: r.Y, W: r.W, H: r.H}, Data: r.Data}
}
func CreateURLToProto(r *CreateURLRequest) *pb.CreateTileRequest {
	return &pb.CreateTileRequest{Path: PathToProto(r.Path), GridId: r.GridID,
		Tile: &pb.Tile{Kind: KindURL, X: r.X, Y: r.Y, W: r.W, H: r.H, UrlString: r.URL}}
}
func CreateShellToProto(r *CreateShellRequest) *pb.CreateTileRequest {
	return &pb.CreateTileRequest{Path: PathToProto(r.Path), GridId: r.GridID,
		Tile: &pb.Tile{Kind: KindShell, X: r.X, Y: r.Y, W: r.W, H: r.H}}
}

// SetTile converters. The wire has a single SetTile dispatched on the target
// tile's Kind; these helpers map each kind's framing/preview writeback onto it.

func SetWellViewToProto(r *SetWellViewRequest) *pb.SetTileRequest {
	return &pb.SetTileRequest{Path: PathToProto(r.Path), TileId: r.TileID, Version: r.Version,
		Tile: &pb.Tile{Kind: KindWell, ViewX: r.ViewX, ViewY: r.ViewY, ViewZoom: r.ViewZoom}}
}
func SetTextViewToProto(r *SetTextViewRequest) *pb.SetTileRequest {
	return &pb.SetTileRequest{Path: PathToProto(r.Path), TileId: r.TileID, Version: r.Version,
		Tile: &pb.Tile{Kind: KindText, TextX: r.TextX, TextY: r.TextY, TextW: r.TextW, TextH: r.TextH, TextMode: r.TextMode}}
}
func SetShellPreviewToProto(r *SetShellPreviewRequest) *pb.SetTileRequest {
	return &pb.SetTileRequest{Path: PathToProto(r.Path), TileId: r.TileID, Version: r.Version,
		Tile: &pb.Tile{Kind: KindShell}, Preview: r.JPEG}
}
func SetURLStateToProto(r *SetURLStateRequest) *pb.SetTileRequest {
	return &pb.SetTileRequest{Path: PathToProto(r.Path), TileId: r.TileID, Version: r.Version,
		Tile: &pb.Tile{Kind: KindURL, UrlString: r.URL, AltText: r.Title, UrlHistory: r.History}, Preview: r.JPEG}
}

// Mutation request converters.

func MoveTileFromProto(r *pb.MoveTileRequest) *MoveTileRequest {
	return &MoveTileRequest{Path: PathFromProto(r.Path), TileID: r.TileId, Version: r.Version, DestGridID: r.DestGridId, DestPath: PathFromProto(r.DestPath), X: r.X, Y: r.Y}
}
func MoveTileToProto(r *MoveTileRequest) *pb.MoveTileRequest {
	return &pb.MoveTileRequest{Path: PathToProto(r.Path), TileId: r.TileID, Version: r.Version, DestGridId: r.DestGridID, DestPath: PathToProto(r.DestPath), X: r.X, Y: r.Y}
}

func CloneTileFromProto(r *pb.CloneTileRequest) *CloneTileRequest {
	return &CloneTileRequest{Path: PathFromProto(r.Path), TileID: r.TileId, Version: r.Version, DestGridID: r.DestGridId, DestPath: PathFromProto(r.DestPath), X: r.X, Y: r.Y}
}
func CloneTileToProto(r *CloneTileRequest) *pb.CloneTileRequest {
	return &pb.CloneTileRequest{Path: PathToProto(r.Path), TileId: r.TileID, Version: r.Version, DestGridId: r.DestGridID, DestPath: PathToProto(r.DestPath), X: r.X, Y: r.Y}
}

func ResizeTileFromProto(r *pb.ResizeTileRequest) *ResizeTileRequest {
	return &ResizeTileRequest{Path: PathFromProto(r.Path), TileID: r.TileId, Version: r.Version, X: r.X, Y: r.Y, W: r.W, H: r.H}
}
func ResizeTileToProto(r *ResizeTileRequest) *pb.ResizeTileRequest {
	return &pb.ResizeTileRequest{Path: PathToProto(r.Path), TileId: r.TileID, Version: r.Version, X: r.X, Y: r.Y, W: r.W, H: r.H}
}

func ShellSessionAliveFromProto(r *pb.ShellSessionAliveRequest) *ShellSessionAliveRequest {
	return &ShellSessionAliveRequest{TileID: r.TileId}
}
func ShellSessionAliveToProto(r *ShellSessionAliveRequest) *pb.ShellSessionAliveRequest {
	return &pb.ShellSessionAliveRequest{TileId: r.TileID}
}
func ShellSessionAliveResponseFromProto(r *pb.ShellSessionAliveResponse) *ShellSessionAliveResponse {
	return &ShellSessionAliveResponse{Alive: r.Alive}
}
func ShellSessionAliveResponseToProto(r *ShellSessionAliveResponse) *pb.ShellSessionAliveResponse {
	return &pb.ShellSessionAliveResponse{Alive: r.Alive}
}

func UpdateTextFromProto(r *pb.UpdateTextRequest) *UpdateTextRequest {
	return &UpdateTextRequest{Path: PathFromProto(r.Path), TileID: r.TileId, Version: r.Version, Data: r.Data}
}
func UpdateTextToProto(r *UpdateTextRequest) *pb.UpdateTextRequest {
	return &pb.UpdateTextRequest{Path: PathToProto(r.Path), TileId: r.TileID, Version: r.Version, Data: r.Data}
}

func DeleteTileFromProto(r *pb.DeleteTileRequest) *DeleteTileRequest {
	return &DeleteTileRequest{Path: PathFromProto(r.Path), TileID: r.TileId, Version: r.Version}
}
func DeleteTileToProto(r *DeleteTileRequest) *pb.DeleteTileRequest {
	return &pb.DeleteTileRequest{Path: PathToProto(r.Path), TileId: r.TileID, Version: r.Version}
}
