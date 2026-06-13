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
		Id:         g.ID,
		ObjectId:   g.ObjectID,
		Version:    g.Version,
		SourceKind: g.SourceKind,
		SourceId:   g.SourceID,
	}
}

// GridFromProto converts a wire Grid back.
func GridFromProto(g *pb.Grid) *Grid {
	if g == nil {
		return nil
	}
	return &Grid{
		ID:         g.Id,
		ObjectID:   g.ObjectId,
		Version:    g.Version,
		SourceKind: g.SourceKind,
		SourceID:   g.SourceId,
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
		FsPath:        t.FSPath,
		Pid:           t.PID,
		SourceKey:     t.SourceKey,
		AltText:       t.AltText,
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
		FSPath:        t.FsPath,
		PID:           t.Pid,
		SourceKey:     t.SourceKey,
		AltText:       t.AltText,
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
	case EventGridForked:
		return &pb.Event{Payload: &pb.Event_GridForked{GridForked: &pb.GridForked{
			WellId:    e.GridForked.WellID,
			OldGridId: e.GridForked.OldGridID,
			NewGridId: e.GridForked.NewGridID,
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
	case *pb.Event_GridForked:
		return Event{Kind: EventGridForked, GridForked: &GridForked{
			WellID:    p.GridForked.WellId,
			OldGridID: p.GridForked.OldGridId,
			NewGridID: p.GridForked.NewGridId,
		}}
	}
	return Event{}
}

// Create request converters.

func CreateWellFromProto(r *pb.CreateWellRequest) *CreateWellRequest {
	return &CreateWellRequest{Path: PathFromProto(r.Path), GridID: r.GridId, X: r.X, Y: r.Y, W: r.W, H: r.H}
}
func CreateWellToProto(r *CreateWellRequest) *pb.CreateWellRequest {
	return &pb.CreateWellRequest{Path: PathToProto(r.Path), GridId: r.GridID, X: r.X, Y: r.Y, W: r.W, H: r.H}
}

func CreateTextFromProto(r *pb.CreateTextRequest) *CreateTextRequest {
	return &CreateTextRequest{Path: PathFromProto(r.Path), GridID: r.GridId, X: r.X, Y: r.Y, W: r.W, H: r.H, Data: r.Data}
}
func CreateTextToProto(r *CreateTextRequest) *pb.CreateTextRequest {
	return &pb.CreateTextRequest{Path: PathToProto(r.Path), GridId: r.GridID, X: r.X, Y: r.Y, W: r.W, H: r.H, Data: r.Data}
}

func CreateURLFromProto(r *pb.CreateURLRequest) *CreateURLRequest {
	return &CreateURLRequest{Path: PathFromProto(r.Path), GridID: r.GridId, X: r.X, Y: r.Y, W: r.W, H: r.H, URL: r.Url}
}
func CreateURLToProto(r *CreateURLRequest) *pb.CreateURLRequest {
	return &pb.CreateURLRequest{Path: PathToProto(r.Path), GridId: r.GridID, X: r.X, Y: r.Y, W: r.W, H: r.H, Url: r.URL}
}

func CreateBlackHoleFromProto(r *pb.CreateBlackHoleRequest) *CreateBlackHoleRequest {
	return &CreateBlackHoleRequest{Path: PathFromProto(r.Path), GridID: r.GridId, X: r.X, Y: r.Y, W: r.W, H: r.H}
}
func CreateBlackHoleToProto(r *CreateBlackHoleRequest) *pb.CreateBlackHoleRequest {
	return &pb.CreateBlackHoleRequest{Path: PathToProto(r.Path), GridId: r.GridID, X: r.X, Y: r.Y, W: r.W, H: r.H}
}

func CreateFileWellFromProto(r *pb.CreateFileWellRequest) *CreateFileWellRequest {
	return &CreateFileWellRequest{Path: PathFromProto(r.Path), GridID: r.GridId, X: r.X, Y: r.Y, W: r.W, H: r.H, FSPath: r.FsPath}
}
func CreateFileWellToProto(r *CreateFileWellRequest) *pb.CreateFileWellRequest {
	return &pb.CreateFileWellRequest{Path: PathToProto(r.Path), GridId: r.GridID, X: r.X, Y: r.Y, W: r.W, H: r.H, FsPath: r.FSPath}
}

func CreateProcessWellFromProto(r *pb.CreateProcessWellRequest) *CreateProcessWellRequest {
	return &CreateProcessWellRequest{Path: PathFromProto(r.Path), GridID: r.GridId, X: r.X, Y: r.Y, W: r.W, H: r.H, PID: r.Pid}
}
func CreateProcessWellToProto(r *CreateProcessWellRequest) *pb.CreateProcessWellRequest {
	return &pb.CreateProcessWellRequest{Path: PathToProto(r.Path), GridId: r.GridID, X: r.X, Y: r.Y, W: r.W, H: r.H, Pid: r.PID}
}

func CreateShellFromProto(r *pb.CreateShellRequest) *CreateShellRequest {
	return &CreateShellRequest{Path: PathFromProto(r.Path), GridID: r.GridId, X: r.X, Y: r.Y, W: r.W, H: r.H}
}
func CreateShellToProto(r *CreateShellRequest) *pb.CreateShellRequest {
	return &pb.CreateShellRequest{Path: PathToProto(r.Path), GridId: r.GridID, X: r.X, Y: r.Y, W: r.W, H: r.H}
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

func SetWellViewFromProto(r *pb.SetWellViewRequest) *SetWellViewRequest {
	return &SetWellViewRequest{Path: PathFromProto(r.Path), TileID: r.TileId, Version: r.Version, ViewX: r.ViewX, ViewY: r.ViewY, ViewZoom: r.ViewZoom}
}
func SetWellViewToProto(r *SetWellViewRequest) *pb.SetWellViewRequest {
	return &pb.SetWellViewRequest{Path: PathToProto(r.Path), TileId: r.TileID, Version: r.Version, ViewX: r.ViewX, ViewY: r.ViewY, ViewZoom: r.ViewZoom}
}

func SetTextViewFromProto(r *pb.SetTextViewRequest) *SetTextViewRequest {
	return &SetTextViewRequest{Path: PathFromProto(r.Path), TileID: r.TileId, Version: r.Version, TextX: r.TextX, TextY: r.TextY, TextW: r.TextW, TextH: r.TextH, TextMode: r.TextMode}
}
func SetTextViewToProto(r *SetTextViewRequest) *pb.SetTextViewRequest {
	return &pb.SetTextViewRequest{Path: PathToProto(r.Path), TileId: r.TileID, Version: r.Version, TextX: r.TextX, TextY: r.TextY, TextW: r.TextW, TextH: r.TextH, TextMode: r.TextMode}
}

func SetShellPreviewFromProto(r *pb.SetShellPreviewRequest) *SetShellPreviewRequest {
	return &SetShellPreviewRequest{Path: PathFromProto(r.Path), TileID: r.TileId, Version: r.Version, JPEG: r.Jpeg}
}
func SetShellPreviewToProto(r *SetShellPreviewRequest) *pb.SetShellPreviewRequest {
	return &pb.SetShellPreviewRequest{Path: PathToProto(r.Path), TileId: r.TileID, Version: r.Version, Jpeg: r.JPEG}
}

func SetURLStateFromProto(r *pb.SetURLStateRequest) *SetURLStateRequest {
	return &SetURLStateRequest{Path: PathFromProto(r.Path), TileID: r.TileId, Version: r.Version, JPEG: r.Jpeg, URL: r.Url, Title: r.Title}
}
func SetURLStateToProto(r *SetURLStateRequest) *pb.SetURLStateRequest {
	return &pb.SetURLStateRequest{Path: PathToProto(r.Path), TileId: r.TileID, Version: r.Version, Jpeg: r.JPEG, Url: r.URL, Title: r.Title}
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

func SetRootViewFromProto(r *pb.SetRootViewRequest) *SetRootViewRequest {
	return &SetRootViewRequest{Cx: r.Cx, Cy: r.Cy, Zoom: r.Zoom}
}
func SetRootViewToProto(r *SetRootViewRequest) *pb.SetRootViewRequest {
	return &pb.SetRootViewRequest{Cx: r.Cx, Cy: r.Cy, Zoom: r.Zoom}
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
