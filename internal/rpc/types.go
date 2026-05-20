// Package rpc declares the wire-format types for the Gridwell RPC service.
//
// The server transports them as JSON over HTTP under /rpc/<MethodName>; the
// client uses the same encoding. All field names are snake_case via JSON
// tags.
package rpc

// ViewRect is the framed region of the originating pane in the affected grid's
// own coordinates. Servers reject mutations whose target footprint does not
// intersect this rectangle — a locality guard so a pane can't move tiles in
// regions it isn't currently looking at.
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

// Intersects reports whether the rectangle (x,y,w,h) overlaps r at all
// — i.e., at least one cell of the rectangle is inside r. Used by the
// locality check on cross-grid moves so a tile larger than the framed
// view can still be acted on, as long as the user can see *some* part
// of it.
func (r ViewRect) Intersects(x, y, w, h int64) bool {
	if w <= 0 || h <= 0 || r.W <= 0 || r.H <= 0 {
		return false
	}
	return x < r.X+r.W && x+w > r.X && y < r.Y+r.H && y+h > r.Y
}

// Path is the sequence of well-tile IDs walked from the user's root grid to
// the pane the request originates from. Used both for permission scoping and
// to identify the affected grid stack for CoW forks.
type Path struct {
	WellIDs []int64 `json:"well_ids"`
}

// Grid is the persistent unit of canvas. Tiles live in grids; wells point at
// child grids. The user's root grid has no parent.
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

// Tile is the persistent unit of content in a grid: a movable, resizable,
// non-overlapping element. Two kinds are distinguished by Type:
//   - "well": points at a child grid (ChildGridID, Capped).
//   - "file": holds a blob (MimeType, BlobID) — except URL tiles
//     (MimeType=="text/uri-list") which store the URL directly in
//     URLString instead, see §8.3.
//
// Mirrors the tiles table. Kind-specific fields are populated only for the
// matching kind.
type Tile struct {
	ID          int64   `json:"id"`
	ObjectID    string  `json:"object_id"`
	GridID      int64   `json:"grid_id"`
	Type        string  `json:"type"`
	X           int64   `json:"x"`
	Y           int64   `json:"y"`
	W           int64   `json:"w"`
	H           int64   `json:"h"`
	ViewX       int64   `json:"view_x"`
	ViewY       int64   `json:"view_y"`
	ViewZoom    float64 `json:"view_zoom"`
	ChildGridID int64   `json:"child_grid_id,omitempty"`
	Capped      bool    `json:"capped,omitempty"`
	MimeType    string  `json:"mime_type,omitempty"`
	BlobID      int64   `json:"blob_id,omitempty"`
	URLString   string  `json:"url_string,omitempty"`
	OwnerID     int64   `json:"owner_id"`
	GroupID     int64   `json:"group_id"`
	Mode        int32   `json:"mode"`
}

// IsURL reports whether the tile is a URL tile (text/uri-list file).
func (t *Tile) IsURL() bool { return t.Type == "file" && t.MimeType == MimeURIList }

// IsBlackHole reports whether the tile is a black-hole (trashcan) tile.
// Dropping any other tile onto a black hole deletes the dropped tile.
func (t *Tile) IsBlackHole() bool { return t.Type == "file" && t.MimeType == MimeBlackHole }

// MimeURIList is the canonical MIME type for URL tiles.
const MimeURIList = "text/uri-list"

// MimeBlackHole is the canonical MIME type for black-hole tiles —
// "trashcan" tiles that delete anything dropped on them. Not a real
// IANA type; the application/x-gridwell-* prefix keeps it from
// colliding with real upload types.
const MimeBlackHole = "application/x-gridwell-blackhole"

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
	Tiles    []Tile `json:"tiles"`
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

// GetTilePreview returns the latest captured JPEG preview bytes for a URL
// tile. Empty bytes means the tile has no preview yet (e.g. brand-new
// tile or Chromium currently unavailable). See spec §8.3.
type GetTilePreviewRequest struct {
	TileID int64 `json:"tile_id"`
}
type GetTilePreviewResponse struct {
	JPEG []byte `json:"jpeg"`
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

type TileResponse struct {
	Tile Tile `json:"tile"`
}

type MoveTileRequest struct {
	Path         Path     `json:"path"`
	ViewRect     ViewRect `json:"view_rect"`
	TileID       int64    `json:"tile_id"`
	DestGridID   int64    `json:"dest_grid_id"`
	DestPath     Path     `json:"dest_path"`
	DestViewRect ViewRect `json:"dest_view_rect"`
	X            int64    `json:"x"`
	Y            int64    `json:"y"`
}
type MoveTileResponse struct {
	Tile Tile `json:"tile"`
}

type CloneTileRequest struct {
	Path         Path     `json:"path"`
	ViewRect     ViewRect `json:"view_rect"`
	TileID       int64    `json:"tile_id"`
	DestGridID   int64    `json:"dest_grid_id"`
	DestPath     Path     `json:"dest_path"`
	DestViewRect ViewRect `json:"dest_view_rect"`
	X            int64    `json:"x"`
	Y            int64    `json:"y"`
}

type ResizeTileRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	TileID   int64    `json:"tile_id"`
	// X, Y, W, H specify the new footprint. The whole footprint is
	// updated, so callers that only want to change W/H must still send
	// the existing X, Y. Used by corner-drag resize where any corner
	// can move with the cursor.
	X int64 `json:"x"`
	Y int64 `json:"y"`
	W int64 `json:"w"`
	H int64 `json:"h"`
}

// SetGridDefaultViewRequest sets a grid's default viewport. The user
// must have write permission on the grid. Used to remember the user's
// preferred camera position for the user's root grid (which has no
// parent tile to hang ViewX/Y/Zoom off of).
type SetGridDefaultViewRequest struct {
	GridID int64   `json:"grid_id"`
	Cx     float64 `json:"cx"`
	Cy     float64 `json:"cy"`
	Zoom   float64 `json:"zoom"`
}
type SetGridDefaultViewResponse struct {
	Grid Grid `json:"grid"`
}

type SetTileViewportRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	TileID   int64    `json:"tile_id"`
	ViewX    int64    `json:"view_x"`
	ViewY    int64    `json:"view_y"`
	// ViewZoom is the zoom the tile should be shown at when re-entered
	// (e.g., re-descending into a well-tile). Persisted alongside
	// ViewX/Y. Wells use this; files ignore it (their zoom is computed
	// from the pane size and natural content size on each entry).
	ViewZoom float64 `json:"view_zoom"`
}

type UpdateFileContentRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	TileID   int64    `json:"tile_id"`
	Data     []byte   `json:"data"`
}

// DeleteTileRequest removes a single tile from its grid. If the tile
// is a well, its child grid is dereferenced (refcount-- and cascade-
// delete when the count hits zero). Used by the black-hole drop
// gesture — there is no other delete affordance in the UI.
type DeleteTileRequest struct {
	Path     Path     `json:"path"`
	ViewRect ViewRect `json:"view_rect"`
	TileID   int64    `json:"tile_id"`
}
type DeleteTileResponse struct{}

type AscendAtRootRequest struct{}
type AscendAtRootResponse struct {
	NewRootGridID int64 `json:"new_root_grid_id"`
	WellID        int64 `json:"well_id"`
}

// URL tile mutations. See spec §8.3 for semantics. ForkURL duplicates
// the tile as a dormant sibling capturing the current URL + preview.
// (Liveness is no longer modelled as tile state: a URL tile is "live"
// for the duration of a /rpc/URLStream WebSocket — see url_stream.go.)

type ForkURLRequest struct {
	Path         Path     `json:"path"`
	ViewRect     ViewRect `json:"view_rect"`
	TileID       int64    `json:"tile_id"`
	DestGridID   int64    `json:"dest_grid_id"`
	DestPath     Path     `json:"dest_path"`
	DestViewRect ViewRect `json:"dest_view_rect"`
	X            int64    `json:"x"`
	Y            int64    `json:"y"`
}
type ForkURLResponse struct {
	Tile Tile `json:"tile"`
}

// Real-time subscription. Each event is one JSON object on an SSE stream.

type SubscribeRequest struct{}

// EventKind identifies which payload field on Event is populated.
type EventKind string

const (
	EventGridChanged EventKind = "grid_changed"
	EventTileChanged EventKind = "tile_changed"
	EventTileRemoved EventKind = "tile_removed"
	EventGridForked  EventKind = "grid_forked"
)

type Event struct {
	Kind        EventKind    `json:"kind"`
	GridChanged *GridChanged `json:"grid_changed,omitempty"`
	TileChanged *TileChanged `json:"tile_changed,omitempty"`
	TileRemoved *TileRemoved `json:"tile_removed,omitempty"`
	GridForked  *GridForked  `json:"grid_forked,omitempty"`
}

type GridChanged struct {
	GridID int64 `json:"grid_id"`
}
type TileChanged struct {
	Tile Tile `json:"tile"`
}
type TileRemoved struct {
	GridID int64 `json:"grid_id"`
	TileID int64 `json:"tile_id"`
}
type GridForked struct {
	WellID    int64 `json:"well_id"`
	OldGridID int64 `json:"old_grid_id"`
	NewGridID int64 `json:"new_grid_id"`
}
