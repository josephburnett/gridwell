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

// GridToProto converts a Grid to its wire form.
func GridToProto(g *Grid) *pb.Grid {
	if g == nil {
		return nil
	}
	return &pb.Grid{
		Id:            g.ID,
		Version:       g.Version,
		SourceKind:    g.SourceKind,
		SourceId:      g.SourceID,
		Writable:      g.Writable,
		ScratchGridId: g.ScratchGridID,
		NodeNs:        g.NodeNS,
		Stale:         g.Stale,
		MenuEntries:   MenuEntriesToProto(g.MenuEntries),
	}
}

// GridFromProto converts a wire Grid back.
func GridFromProto(g *pb.Grid) *Grid {
	if g == nil {
		return nil
	}
	return &Grid{
		ID:            g.Id,
		Version:       g.Version,
		SourceKind:    g.SourceKind,
		SourceID:      g.SourceId,
		Writable:      g.Writable,
		ScratchGridID: g.ScratchGridId,
		NodeNS:        g.NodeNs,
		Stale:         g.Stale,
		MenuEntries:   MenuEntriesFromProto(g.MenuEntries),
	}
}

// TileToProto converts a Tile to its wire form.
func TileToProto(t *Tile) *pb.Tile {
	if t == nil {
		return nil
	}
	return &pb.Tile{
		Id:               t.ID,
		Version:          t.Version,
		GridId:           t.GridID,
		Kind:             t.Kind,
		X:                t.X,
		Y:                t.Y,
		W:                t.W,
		H:                t.H,
		ViewCx:           t.ViewCx,
		ViewCy:           t.ViewCy,
		ViewZoom:         t.ViewZoom,
		ChildGridId:      t.ChildGridID,
		TextX:            t.TextX,
		TextY:            t.TextY,
		TextW:            t.TextW,
		TextH:            t.TextH,
		TextMode:         t.TextMode,
		BlobId:           t.BlobID,
		UrlString:        t.URLString,
		PreviewBlobId:    t.PreviewBlobID,
		AltText:          t.AltText,
		Reference:        t.Reference,
		ContentZoom:      t.ContentZoom,
		UrlHistory:       t.URLHistory,
		LinkTargetId:     t.LinkTargetID,
		UrlFrozen:        t.URLFrozen,
		ServesPage:       t.ServesPage,
		TextPresentation: t.TextPresentation,
		StatusDetail:     t.StatusDetail,
	}
}

// TileFromProto converts a wire Tile back.
func TileFromProto(t *pb.Tile) *Tile {
	if t == nil {
		return nil
	}
	return &Tile{
		ID:               t.Id,
		Version:          t.Version,
		GridID:           t.GridId,
		Kind:             t.Kind,
		X:                t.X,
		Y:                t.Y,
		W:                t.W,
		H:                t.H,
		ViewCx:           t.ViewCx,
		ViewCy:           t.ViewCy,
		ViewZoom:         t.ViewZoom,
		ChildGridID:      t.ChildGridId,
		TextX:            t.TextX,
		TextY:            t.TextY,
		TextW:            t.TextW,
		TextH:            t.TextH,
		TextMode:         t.TextMode,
		BlobID:           t.BlobId,
		URLString:        t.UrlString,
		PreviewBlobID:    t.PreviewBlobId,
		AltText:          t.AltText,
		Reference:        t.Reference,
		ContentZoom:      t.ContentZoom,
		URLHistory:       t.UrlHistory,
		LinkTargetID:     t.LinkTargetId,
		URLFrozen:        t.UrlFrozen,
		ServesPage:       t.ServesPage,
		TextPresentation: t.TextPresentation,
		StatusDetail:     t.StatusDetail,
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

// Mutation request converters. None of these carries a version: layout is
// last-writer-wins (docs/simplify-plan.md S5), and the reserved proto field
// numbers say so on the wire.

func CloneTileFromProto(r *pb.CloneTileRequest) *CloneTileRequest {
	return &CloneTileRequest{TileID: r.TileId, DestGridID: r.DestGridId, X: r.X, Y: r.Y}
}
func CloneTileToProto(r *CloneTileRequest) *pb.CloneTileRequest {
	return &pb.CloneTileRequest{TileId: r.TileID, DestGridId: r.DestGridID, X: r.X, Y: r.Y}
}

func PlaceTileFromProto(r *pb.PlaceTileRequest) *PlaceTileRequest {
	return &PlaceTileRequest{TileID: r.TileId, GridID: r.GridId, X: r.X, Y: r.Y, W: r.W, H: r.H}
}

func PlaceTileToProto(r *PlaceTileRequest) *pb.PlaceTileRequest {
	return &pb.PlaceTileRequest{TileId: r.TileID, GridId: r.GridID, X: r.X, Y: r.Y, W: r.W, H: r.H}
}

func ShellSessionAliveToProto(r *ShellSessionAliveRequest) *pb.ShellSessionAliveRequest {
	return &pb.ShellSessionAliveRequest{TileId: r.TileID}
}
func ShellSessionAliveResponseFromProto(r *pb.ShellSessionAliveResponse) *ShellSessionAliveResponse {
	return &ShellSessionAliveResponse{Alive: r.Alive}
}

func DeleteTileFromProto(r *pb.DeleteTileRequest) *DeleteTileRequest {
	return &DeleteTileRequest{TileID: r.TileId}
}
func DeleteTileToProto(r *DeleteTileRequest) *pb.DeleteTileRequest {
	return &pb.DeleteTileRequest{TileId: r.TileID}
}

// MenuEntriesToProto / FromProto convert the plugin menu-entry lists
// (issue #258) — one pair, shared by the grid and plugin-info paths.
func MenuEntriesToProto(in []MenuEntry) []*pb.MenuEntry {
	if len(in) == 0 {
		return nil
	}
	out := make([]*pb.MenuEntry, len(in))
	for i, e := range in {
		out[i] = &pb.MenuEntry{Id: e.ID, Label: e.Label, Glyph: e.Glyph,
			Color: e.Color, GridId: e.GridID}
	}
	return out
}

func MenuEntriesFromProto(in []*pb.MenuEntry) []MenuEntry {
	if len(in) == 0 {
		return nil
	}
	out := make([]MenuEntry, len(in))
	for i, e := range in {
		out[i] = MenuEntry{ID: e.Id, Label: e.Label, Glyph: e.Glyph,
			Color: e.Color, GridID: e.GridId}
	}
	return out
}
