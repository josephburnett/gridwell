// Package rpc declares the wire-format types for the Hangar RPC service.
//
// These types mirror the messages defined in proto/hangar.proto. The server
// transports them as JSON over HTTP under /rpc/<MethodName>; the client uses
// the same encoding. All field names are snake_case via JSON tags so they
// match the proto field names exactly.
package rpc

// ViewRect is the framed region of the originating pane in the affected grid's
// own coordinates. Servers reject mutations whose target footprint is not
// entirely inside this rectangle. See spec §6.
type ViewRect struct {
	X int64 `json:"x"`
	Y int64 `json:"y"`
	W int64 `json:"w"`
	H int64 `json:"h"`
}

// Contains reports whether the rectangle (x,y,w,h) is entirely inside r.
func (r ViewRect) Contains(x, y, w, h int64) bool {
	return x >= r.X && y >= r.Y && x+w <= r.X+r.W && y+h <= r.Y+r.H
}

// Path is the descent path from the user's root grid to the grid the caller is
// looking at. WellIDs is the sequence of well row ids walked through. A path
// of length 0 means the caller is at the user's root grid.
type Path struct {
	WellIDs []int64 `json:"well_ids"`
}

// Grid mirrors the grids table.
type Grid struct {
	ID            int64   `json:"id"`
	ObjectID      string  `json:"object_id"`
	OwnerID       int64   `json:"owner_id"`
	GroupID       int64   `json:"group_id"`
	Mode          int32   `json:"mode"`
	DefaultViewCx float64 `json:"default_view_cx"`
	DefaultViewCy float64 `json:"default_view_cy"`
	DefaultZoom   float64 `json:"default_zoom"`
}

// Node mirrors the nodes table. Type-specific fields are populated only for
// the matching kind (well or file).
type Node struct {
	ID          int64  `json:"id"`
	ObjectID    string `json:"object_id"`
	GridID      int64  `json:"grid_id"`
	Type        string `json:"type"`
	X           int64  `json:"x"`
	Y           int64  `json:"y"`
	W           int64  `json:"w"`
	H           int64  `json:"h"`
	ViewX       int64   `json:"view_x"`
	ViewY       int64   `json:"view_y"`
	ViewZoom    float64 `json:"view_zoom"`
	ChildGridID int64   `json:"child_grid_id,omitempty"`
	Capped      bool   `json:"capped,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
	BlobID      int64  `json:"blob_id,omitempty"`
	OwnerID     int64  `json:"owner_id"`
	GroupID     int64  `json:"group_id"`
	Mode        int32  `json:"mode"`
}

// Auth requests/responses.

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type LoginResponse struct {
	UserID     int64  `json:"user_id"`
	Username   string `json:"username"`
	RootGridID int64  `json:"root_grid_id"`
}
type LogoutRequest struct{}
type LogoutResponse struct{}
type WhoamiRequest struct{}
type WhoamiResponse struct {
	UserID     int64  `json:"user_id"`
	Username   string `json:"username"`
	RootGridID int64  `json:"root_grid_id"`
}

// Reads.

type GetGridRequest struct {
	GridID int64 `json:"grid_id"`
}
type GetGridResponse struct {
	Grid     Grid   `json:"grid"`
	Nodes    []Node `json:"nodes"`
	Readable bool   `json:"readable"`
	Writable bool   `json:"writable"`
}

type GetBlobRequest struct {
	BlobID int64 `json:"blob_id"`
}
type GetBlobResponse struct {
	Data     []byte `json:"data"`
	MimeType string `json:"mime_type"`
}

type GetURLTitleRequest struct {
	URL string `json:"url"`
}
type GetURLTitleResponse struct {
	Title string `json:"title"`
}

// Mutations on a grid. Every mutating request carries the originating pane's
// descent path and framed view rectangle so the server can enforce locality
// of action and walk the CoW fork up the path.

type CreateWellRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	GridID   int64    `json:"grid_id"`
	X        int64    `json:"x"`
	Y        int64    `json:"y"`
	W        int64    `json:"w"`
	H        int64    `json:"h"`
}

type CreateFileRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	GridID   int64    `json:"grid_id"`
	X        int64    `json:"x"`
	Y        int64    `json:"y"`
	W        int64    `json:"w"`
	H        int64    `json:"h"`
	MimeType string   `json:"mime_type"`
	Data     []byte   `json:"data"`
}

type NodeResponse struct {
	Node Node `json:"node"`
}

type MoveNodeRequest struct {
	Path         Path     `json:"path"`
	ViewRect     ViewRect `json:"view_rect"`
	NodeID       int64    `json:"node_id"`
	DestGridID   int64    `json:"dest_grid_id"`
	DestPath     Path     `json:"dest_path"`
	DestViewRect ViewRect `json:"dest_view_rect"`
	X            int64    `json:"x"`
	Y            int64    `json:"y"`
}
type MoveNodeResponse struct {
	Node Node `json:"node"`
}

type CloneNodeRequest struct {
	Path         Path     `json:"path"`
	ViewRect     ViewRect `json:"view_rect"`
	NodeID       int64    `json:"node_id"`
	DestGridID   int64    `json:"dest_grid_id"`
	DestPath     Path     `json:"dest_path"`
	DestViewRect ViewRect `json:"dest_view_rect"`
	X            int64    `json:"x"`
	Y            int64    `json:"y"`
}

type ResizeNodeRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	NodeID   int64    `json:"node_id"`
	// X, Y, W, H specify the new footprint. The whole footprint is
	// updated, so callers that only want to change W/H must still send
	// the existing X, Y. Used by corner-drag resize where any corner
	// can move with the cursor.
	X int64 `json:"x"`
	Y int64 `json:"y"`
	W int64 `json:"w"`
	H int64 `json:"h"`
}

type SetNodeViewportRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	NodeID   int64    `json:"node_id"`
	ViewX    int64    `json:"view_x"`
	ViewY    int64    `json:"view_y"`
	// ViewZoom is the zoom the node should be shown at when re-entered
	// (e.g., re-descending into a well). Persisted alongside ViewX/Y.
	// Wells use this; files ignore it (their zoom is computed from
	// the pane size and natural content size on each entry).
	ViewZoom float64 `json:"view_zoom"`
}

type CapWellRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	NodeID   int64    `json:"node_id"`
}
type RedigWellRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	NodeID   int64    `json:"node_id"`
}
type FillWellRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	NodeID   int64    `json:"node_id"`
}
type FillWellResponse struct{}

type UpdateFileContentRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	NodeID   int64    `json:"node_id"`
	Data     []byte   `json:"data"`
}

type AscendAtRootRequest struct{}
type AscendAtRootResponse struct {
	NewRootGridID int64 `json:"new_root_grid_id"`
	WellID        int64 `json:"well_id"`
}

// Real-time subscription. Each event is one JSON object on an SSE stream.

type SubscribeRequest struct{}

// EventKind identifies which payload field on Event is populated.
type EventKind string

const (
	EventGridChanged EventKind = "grid_changed"
	EventNodeChanged EventKind = "node_changed"
	EventNodeRemoved EventKind = "node_removed"
	EventGridForked  EventKind = "grid_forked"
)

type Event struct {
	Kind        EventKind    `json:"kind"`
	GridChanged *GridChanged `json:"grid_changed,omitempty"`
	NodeChanged *NodeChanged `json:"node_changed,omitempty"`
	NodeRemoved *NodeRemoved `json:"node_removed,omitempty"`
	GridForked  *GridForked  `json:"grid_forked,omitempty"`
}

type GridChanged struct {
	GridID int64 `json:"grid_id"`
}
type NodeChanged struct {
	Node Node `json:"node"`
}
type NodeRemoved struct {
	GridID int64 `json:"grid_id"`
	NodeID int64 `json:"node_id"`
}
type GridForked struct {
	WellID     int64 `json:"well_id"`
	OldGridID  int64 `json:"old_grid_id"`
	NewGridID  int64 `json:"new_grid_id"`
}
